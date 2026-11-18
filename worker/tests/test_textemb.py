"""Vectors of the text of documents (#70): the cutting into windows, the
textemb task, the model the worker declares and serves for queries, and the
search end to end on the benchmark corpus, against a throwaway database.

The tests that run the model need it (`make models`); without it they are
skipped, except when CLOUDOS_REQUIRE_MODELS is set (CI)."""

import hashlib
import json
import os
import threading
import tomllib
import urllib.request
from collections.abc import Iterator
from pathlib import Path

import numpy as np
import psycopg
import pytest
from tokenizers import Tokenizer

from worker import embedserver, models, tasks, textemb, vectors

BENCH = Path(__file__).parent / "fixtures" / "bench"
RECEIPT = (BENCH / "docs" / "quittance-loyer-avril.txt").read_text()  # 160 tokens
ZERO = "[" + ",".join(["0"] * 768) + "]"

needs_models = pytest.mark.skipif(
    not (models.models_dir() / "SHA256SUMS").exists()
    and not os.environ.get("CLOUDOS_REQUIRE_MODELS"),
    reason="no models: run `make models`",
)


@pytest.fixture
def tok() -> Tokenizer:
    return models.text_encoder().tok


def full_offsets(text: str) -> list[tuple[int, int]]:
    """Every token of text, from a tokenizer of its own that never truncates."""
    whole = Tokenizer.from_file(str(models.models_dir() / models.TEXT_MODEL / "tokenizer.json"))
    whole.no_truncation()
    return whole.encode(text, add_special_tokens=False).offsets


def long_text() -> str:
    """About 6,700 tokens of French: the benchmark's documents, end to end."""
    return "\n\n".join(p.read_text() for p in sorted((BENCH / "docs").glob("*.txt")))


@needs_models
def test_a_short_document_is_one_window_whole(tok: Tokenizer) -> None:
    assert textemb.chunks(RECEIPT, tok) == [textemb.Chunk(0, len(RECEIPT.rstrip()))]
    assert textemb.chunks("Contrat de location", tok) == [textemb.Chunk(0, 19)]
    assert textemb.chunks("", tok) == []
    assert textemb.chunks("  \n\n \f ", tok) == []


@needs_models
def test_a_long_document_is_cut_into_overlapping_windows(tok: Tokenizer) -> None:
    text = long_text()
    tokens = full_offsets(text)
    starts = [s for s, _ in tokens]
    ends = [e for _, e in tokens]
    windows = textemb.chunks(text, tok)
    assert len(tokens) > 5 * textemb.WINDOW
    # Window i holds tokens [first, last]: at most WINDOW of them, OVERLAP
    # shared with the next window, from the first token to the last.
    spans = [(starts.index(w.start), ends.index(w.end)) for w in windows]
    assert spans[0][0] == 0 and spans[-1][1] == len(tokens) - 1
    for first, last in spans:
        assert last - first + 1 <= textemb.WINDOW
    for (_, last), (first, _) in zip(spans, spans[1:], strict=False):
        assert last - first + 1 == textemb.OVERLAP
    # Each window fits the model whole: 512 tokens at most, special ones included.
    enc = models.text_encoder()
    for w in windows:
        assert len(enc.tok.encode(text[w.start : w.end]).ids) <= models.TEXT_SPEC_MAX_TOKENS


@needs_models
def test_a_very_long_document_keeps_its_first_windows(
    tok: Tokenizer, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setattr(textemb, "MAX_CHUNKS", 3)
    text = long_text()
    windows = textemb.chunks(text, tok)
    assert len(windows) == 3
    assert windows[0].start == 0 and windows[-1].end < len(text) // 2


def test_the_version_names_the_model_and_the_cutting(monkeypatch: pytest.MonkeyPatch) -> None:
    # The revision of Arctic that worker/models/fetch.sh pins, and its fp16
    # export: the int8 one gives other vectors on other CPUs (models.py).
    fetch = (Path(__file__).parents[1] / "models" / "fetch.sh").read_text()
    revision = fetch.split("snowflake-arctic-embed-m-v2.0/resolve/", 1)[1][:7]
    assert f"{revision}-fp16" == models.TEXT_MODEL_VERSION
    model = next(ln for ln in fetch.splitlines() if " arctic-embed-m-v2.0/model.onnx " in ln)
    assert model.endswith("$ARCTIC/onnx/model_fp16.onnx")
    assert textemb.version() == f"{models.TEXT_MODEL_VERSION}-w512-o64-n64"
    monkeypatch.setattr(textemb, "OVERLAP", 32)
    assert textemb.version() == f"{models.TEXT_MODEL_VERSION}-w512-o32-n64"


def test_the_worker_declares_arctic_with_the_version_of_its_vectors(
    conn: psycopg.Connection,
) -> None:
    served = models.embedding_models()[models.TEXT_MODEL]
    assert (served.version, served.dims) == (textemb.version(), 768)
    # The core queues every document again when it changes (core/internal/textemb Sync).
    vectors.declare(conn)
    row = conn.execute(
        "SELECT version, dims, requeued_at IS NULL FROM embedding_models WHERE name = %s",
        (models.TEXT_MODEL,),
    ).fetchone()
    assert row == (textemb.version(), 768, True)


@needs_models
def test_a_search_is_encoded_with_the_query_prefix() -> None:
    served = models.embedding_models()[models.TEXT_MODEL]
    got = served.query(["contrat de l'appart"])
    want = models.text_encoder().embed(["query: contrat de l'appart"])
    assert got.shape == (1, 768) and np.allclose(got, want, atol=1e-6)
    assert not np.allclose(got, models.text_encoder().embed(["contrat de l'appart"]), atol=1e-3)


# The task, against a throwaway database.


def add(
    conn: psycopg.Connection,
    path: str,
    text: str,
    *,
    owner: str = "enzo",
    has_text: bool = True,
    trashed: bool = False,
    file: bool = True,
) -> bytes:
    """A document as the core and the text task leave it: its row in files
    (none when not file), and its text."""
    sha = hashlib.sha256(f"{path}\n{text}".encode()).digest()
    if file:
        trash = None
        if trashed:
            trash = conn.execute(
                """INSERT INTO trash (owner, path, kind, size, expires_at)
                   VALUES (%s, %s, 'file', 1, now() + interval '30 days') RETURNING id""",
                (owner, path),
            ).fetchone()[0]  # type: ignore[index]
        conn.execute(
            """INSERT INTO files (owner, path, size, mime, sha256, modified_at, trash_id)
               VALUES (%s, %s, %s, 'text/plain', %s, now(), %s)""",
            (owner, path, len(text), sha, trash),
        )
    conn.execute(
        """INSERT INTO document_text (sha256, source, text, chars, truncated, has_text)
           VALUES (%s, 'text', %s, %s, false, %s) ON CONFLICT DO NOTHING""",
        (sha, text, len(text), has_text),
    )
    return sha


def run(conn: psycopg.Connection, sha: bytes) -> tuple[str, str | None]:
    task_id = textemb.enqueue(conn, sha, 10)
    assert task_id is not None
    assert tasks.run_one(conn, "w", {textemb.KIND: textemb.make})
    row = conn.execute("SELECT state, last_error FROM tasks WHERE id = %s", (task_id,)).fetchone()
    assert row
    return row[0], row[1]


def stored(conn: psycopg.Connection, sha: bytes) -> list[tuple]:
    return conn.execute(
        f"""SELECT chunk, start_char, end_char, model, version, vector_dims(embedding),
                   round(l2_norm(embedding)::numeric, 2), created_at
            FROM {textemb.TABLE} WHERE sha256 = %s ORDER BY chunk""",
        (sha,),
    ).fetchall()


def put_vectors(conn: psycopg.Connection, sha: bytes, version: str) -> None:
    conn.execute(
        f"""INSERT INTO {textemb.TABLE}
                (sha256, chunk, start_char, end_char, model, version, embedding)
            VALUES (%s, 0, 0, 1, %s, %s, %s::halfvec)""",
        (sha, textemb.MODEL, version, ZERO),
    )


@needs_models
def test_the_task_stores_one_unit_vector_per_window(
    conn: psycopg.Connection, monkeypatch: pytest.MonkeyPatch
) -> None:
    text = long_text()
    sha = add(conn, "Dossier/tout.txt", text)
    assert run(conn, sha) == ("done", None)
    rows = stored(conn, sha)
    windows = textemb.chunks(text, models.text_encoder().tok)
    assert [(r[0], r[1], r[2]) for r in rows] == [
        (i, w.start, w.end) for i, w in enumerate(windows)
    ]
    current = textemb.version()
    assert {(r[3], r[4], r[5], float(r[6])) for r in rows} == {
        (models.TEXT_MODEL, current, 768, 1.0)
    }

    # Up to date: running it again changes nothing.
    assert run(conn, sha) == ("done", None)
    assert stored(conn, sha) == rows

    # Another version replaces every row of the content, in one go.
    monkeypatch.setattr(textemb, "version", lambda: "other")
    assert run(conn, sha) == ("done", None)
    again = stored(conn, sha)
    assert [(r[0], r[1], r[2], r[4]) for r in again] == [(r[0], r[1], r[2], "other") for r in rows]


def test_nothing_to_do_without_text(conn: psycopg.Connection) -> None:
    scan = add(conn, "Scans/facture.pdf", "", has_text=False)
    gone = add(conn, "parti.txt", "texte", file=False)
    conn.execute("DELETE FROM document_text WHERE sha256 = %s", (gone,))
    for sha in (scan, gone):
        assert run(conn, sha) == ("done", None)
    assert conn.execute(f"SELECT count(*) FROM {textemb.TABLE}").fetchone() == (0,)


def test_a_bad_payload_fails_for_good(conn: psycopg.Connection) -> None:
    task_id = tasks.enqueue(conn, textemb.KIND, {"sha256": "nope"})
    assert tasks.run_one(conn, "w", {textemb.KIND: textemb.make})
    row = conn.execute("SELECT state, attempts FROM tasks WHERE id = %s", (task_id,)).fetchone()
    assert row == ("failed", 1)


def test_vectors_go_with_the_text(conn: psycopg.Connection) -> None:
    sha = add(conn, "a.txt", "un texte")
    put_vectors(conn, sha, "v1")
    conn.execute("DELETE FROM document_text WHERE sha256 = %s", (sha,))
    assert conn.execute(f"SELECT count(*) FROM {textemb.TABLE}").fetchone() == (0,)


# End to end: the benchmark's documents indexed by the task, the query encoded
# by the worker's query endpoint (POST /embed), the nearest content found in
# the database.


@pytest.fixture
def query_url() -> Iterator[str]:
    stop = threading.Event()
    server = embedserver.start(stop, ("127.0.0.1", 0))
    yield f"http://127.0.0.1:{server.server_address[1]}/embed"
    stop.set()


def nearest(conn: psycopg.Connection, url: str, query: str) -> list[str]:
    """The paths of the contents nearest to the query, best window first."""
    body = json.dumps({"model": models.TEXT_MODEL, "texts": [query]}).encode()
    req = urllib.request.Request(url, body, headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=60) as r:
        answer = json.load(r)
    assert answer["version"] == textemb.version()
    rows = conn.execute(
        f"""SELECT f.path, min(v.embedding <=> %s::halfvec) AS d
            FROM {textemb.TABLE} v JOIN files f ON f.sha256 = v.sha256 AND f.trash_id IS NULL
            GROUP BY f.path ORDER BY d""",
        (vectors.literal(answer["vectors"][0]),),
    ).fetchall()
    return [r[0] for r in rows]


@needs_models
def test_contrat_de_l_appart_finds_the_lease_first(
    conn: psycopg.Connection, query_url: str
) -> None:
    for p in sorted((BENCH / "docs").glob("*.txt")):
        assert run(conn, add(conn, p.name, p.read_text())) == ("done", None), p.name
    # The lease is cut in two windows (520 tokens): its best one counts.
    assert conn.execute(f"SELECT max(chunk) FROM {textemb.TABLE}").fetchone() == (1,)
    assert Path(nearest(conn, query_url, "contrat de l'appart")[0]).stem == "bail-appartement"

    # The benchmark of #66 as a whole: the model found 32 of its 33 queries
    # with each document whole; cut into windows and stored in half precision,
    # it still does (docs/decisions/005-modeles-locaux.md).
    queries = tomllib.loads((BENCH / "queries.toml").read_text())["text"]
    missed = [
        q["q"] for q in queries if Path(nearest(conn, query_url, q["q"])[0]).stem not in q["expect"]
    ]
    assert len(missed) <= 0.1 * len(queries), missed
