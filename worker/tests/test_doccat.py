"""The category of documents (#73): the classifier's rule and version, the
category written with the vectors, the doccat task, and the documents of the
benchmark and of a second set end to end, against a throwaway database.

The tests that run the model need it (`make models`); without it they are
skipped, except when CLOUDOS_REQUIRE_MODELS is set (CI)."""

import hashlib
import os
import re
import tomllib
from collections.abc import Iterator
from pathlib import Path

import numpy as np
import psycopg
import pytest

from worker import doccat, models, tasks, textemb, vectors

FIXTURES = Path(__file__).parent / "fixtures"
MIGRATION = (
    Path(__file__).resolve().parents[2] / "core" / "migrations" / "0022_document_categories.sql"
)
INVOICE = (FIXTURES / "bench" / "docs" / "facture-electricite-mars.txt").read_text()

needs_models = pytest.mark.skipif(
    not (models.models_dir() / "SHA256SUMS").exists()
    and not os.environ.get("CLOUDOS_REQUIRE_MODELS"),
    reason="no models: run `make models`",
)


def test_the_categories_are_those_of_the_migration() -> None:
    checks = re.findall(r"category IN\s*\(([^)]*)\)", MIGRATION.read_text())
    assert len(checks) == 2  # document_categories and file_categories
    for check in checks:
        assert re.findall(r"'([a-z]+)'", check) == list(doccat.CATEGORIES)
    assert list(doccat.CATEGORIES)[-1] == doccat.OTHER
    for sentences in doccat.CATEGORIES.values():
        assert 3 <= len(sentences) <= 4


def test_the_version_names_the_vectors_the_sentences_and_the_threshold(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    first = doccat.version()
    assert re.fullmatch(re.escape(textemb.version()) + r"-c[0-9a-f]{8}", first)
    assert doccat.version() == first

    monkeypatch.setattr(doccat, "THRESHOLD", 0.2)
    assert doccat.version() != first
    monkeypatch.undo()

    edited = dict(doccat.CATEGORIES)
    edited["impots"] = (*edited["impots"], "Un avis de taxe sur les logements vacants.")
    monkeypatch.setattr(doccat, "CATEGORIES", edited)
    assert doccat.version() != first
    monkeypatch.undo()

    monkeypatch.setattr(textemb, "OVERLAP", 32)  # other vectors: classified again
    assert doccat.version() != first


def test_the_worker_declares_the_classifier(conn: psycopg.Connection) -> None:
    def row() -> tuple:
        found = conn.execute(
            "SELECT version, dims, requeued_at IS NULL FROM embedding_models WHERE name = %s",
            (doccat.NAME,),
        ).fetchone()
        assert found
        return found

    vectors.declare(conn)
    assert row() == (doccat.version(), 768, True)
    # The core queues every document for it (core/internal/doccat Sync).
    conn.execute("UPDATE embedding_models SET requeued_at = now()")
    vectors.declare(conn)  # a restart, same version: nothing to queue again
    assert row() == (doccat.version(), 768, False)


# The rule, with sentences of known vectors: no model needed.


def unit(*xs: float) -> np.ndarray:
    v = np.array(xs, dtype=np.float32)
    return v / np.linalg.norm(v)


@pytest.fixture
def sentences(monkeypatch: pytest.MonkeyPatch) -> None:
    """Two sentences for factures, one for contrats, one for autre, along four
    axes; the fifth axis is described by none."""
    labels = ("factures", "factures", "contrats", "autre")
    rows = np.eye(5, dtype=np.float32)[:4]
    monkeypatch.setattr(doccat, "prototypes", lambda: (labels, rows))


@pytest.mark.usefixtures("sentences")
def test_a_category_scores_its_nearest_sentence() -> None:
    got = doccat.classify(np.array([unit(0, 1, 0, 0, 0)]))
    assert got == doccat.Verdict("factures", pytest.approx(1.0), pytest.approx(1.0))


@pytest.mark.usefixtures("sentences")
def test_a_document_is_the_mean_of_its_windows() -> None:
    # The best window alone would be factures at 1.0; the mean is nearer to
    # factures (two windows) than to contrats (one).
    windows = np.array([unit(1, 0, 0, 0, 0), unit(1, 0, 0, 0, 0), unit(0, 0, 1, 0, 0)])
    got = doccat.classify(windows)
    assert got.category == "factures"
    assert got.score == pytest.approx(2 / 5**0.5)
    assert got.margin == pytest.approx(2 / 5**0.5 - 1 / 5**0.5)


@pytest.mark.usefixtures("sentences")
def test_under_the_threshold_a_document_is_autre() -> None:
    near_nothing = unit(0.1, 0, 0, 0, 1)
    got = doccat.classify(np.array([near_nothing]))
    assert got.category == "autre"
    assert got.score == pytest.approx(0.1 / 1.01**0.5) and got.score < doccat.THRESHOLD
    # autre has sentences of its own, and wins like any category.
    assert doccat.classify(np.array([unit(0, 0, 0, 1, 0)])).category == "autre"


# The task, against a throwaway database.


def add(conn: psycopg.Connection, path: str, text: str, *, owner: str = "enzo") -> bytes:
    """A document as the core and the text task leave it: its row in files and its text."""
    sha = hashlib.sha256(f"{path}\n{text}".encode()).digest()
    conn.execute(
        """INSERT INTO files (owner, path, size, mime, sha256, modified_at)
           VALUES (%s, %s, %s, 'text/plain', %s, now())""",
        (owner, path, len(text), sha),
    )
    conn.execute(
        """INSERT INTO document_text (sha256, source, text, chars, truncated, has_text)
           VALUES (%s, 'text', %s, %s, false, true)""",
        (sha, text, len(text)),
    )
    return sha


def run(conn: psycopg.Connection, kind: str, sha: bytes) -> tuple[str, str | None]:
    handlers = {textemb.KIND: textemb.make, doccat.KIND: doccat.make}
    task_id = tasks.enqueue(conn, kind, {"sha256": sha.hex()}, priority=10)
    assert tasks.run_one(conn, "w", {kind: handlers[kind]})
    row = conn.execute("SELECT state, last_error FROM tasks WHERE id = %s", (task_id,)).fetchone()
    assert row
    return row[0], row[1]


def category(conn: psycopg.Connection, sha: bytes) -> tuple | None:
    return conn.execute(
        "SELECT category, version, classified_at FROM document_categories WHERE sha256 = %s",
        (sha,),
    ).fetchone()


def vectors_of(conn: psycopg.Connection, sha: bytes) -> list[tuple]:
    return conn.execute(
        f"SELECT chunk, version, created_at FROM {textemb.TABLE} WHERE sha256 = %s ORDER BY chunk",
        (sha,),
    ).fetchall()


@needs_models
def test_the_vectors_come_with_the_category(conn: psycopg.Connection) -> None:
    sha = add(conn, "Factures/edf.txt", INVOICE)
    assert run(conn, textemb.KIND, sha) == ("done", None)
    got = conn.execute(
        "SELECT category, score, margin, version FROM document_categories WHERE sha256 = %s",
        (sha,),
    ).fetchone()
    assert got
    assert got[0] == "factures" and got[3] == doccat.version()
    assert doccat.THRESHOLD <= got[1] <= 1 and 0 <= got[2] <= got[1]


@needs_models
def test_the_task_classifies_again_without_encoding(conn: psycopg.Connection) -> None:
    sha = add(conn, "Factures/edf.txt", INVOICE)
    assert run(conn, textemb.KIND, sha) == ("done", None)
    encoded = vectors_of(conn, sha)
    # Another version of the classifier, which had said autre.
    conn.execute(
        "UPDATE document_categories SET category = 'autre', version = 'old' WHERE sha256 = %s",
        (sha,),
    )
    assert run(conn, doccat.KIND, sha) == ("done", None)
    again = category(conn, sha)
    assert again and again[:2] == ("factures", doccat.version())
    assert vectors_of(conn, sha) == encoded  # the vectors were read, not written

    # Up to date: nothing to do.
    assert run(conn, doccat.KIND, sha) == ("done", None)
    assert category(conn, sha) == again


@needs_models
def test_a_text_without_windows_has_no_category(conn: psycopg.Connection) -> None:
    sha = add(conn, "vide.txt", " \n\f\n ")
    conn.execute(
        """INSERT INTO document_categories (sha256, category, score, margin, version)
           VALUES (%s, 'autre', 0, 0, 'old')""",
        (sha,),
    )
    assert run(conn, textemb.KIND, sha) == ("done", None)
    assert category(conn, sha) is None


def test_the_task_waits_for_vectors_of_the_current_version(conn: psycopg.Connection) -> None:
    sha = add(conn, "Factures/edf.txt", INVOICE)
    assert run(conn, doccat.KIND, sha) == ("done", None)  # none yet
    conn.execute(
        f"""INSERT INTO {textemb.TABLE}
                (sha256, chunk, start_char, end_char, model, version, embedding)
            VALUES (%s, 0, 0, 1, %s, 'old', array_fill(0.036, ARRAY[768])::halfvec)""",
        (sha, textemb.MODEL),
    )
    assert run(conn, doccat.KIND, sha) == ("done", None)  # an older version only
    assert category(conn, sha) is None


def test_a_bad_payload_fails_for_good(conn: psycopg.Connection) -> None:
    task_id = tasks.enqueue(conn, doccat.KIND, {"sha256": "nope"})
    assert tasks.run_one(conn, "w", {doccat.KIND: doccat.make})
    row = conn.execute("SELECT state, attempts FROM tasks WHERE id = %s", (task_id,)).fetchone()
    assert row == ("failed", 1)


def test_the_category_goes_with_the_text(conn: psycopg.Connection) -> None:
    sha = add(conn, "a.txt", "un texte")
    conn.execute(
        """INSERT INTO document_categories (sha256, category, score, margin, version)
           VALUES (%s, 'autre', 0.1, 0, 'v')""",
        (sha,),
    )
    conn.execute("DELETE FROM document_text WHERE sha256 = %s", (sha,))
    assert conn.execute("SELECT count(*) FROM document_categories").fetchone() == (0,)


# End to end: each document indexed by the textemb task, its category read
# back from the database and compared with the expected one.


@pytest.fixture
def report(capsys: pytest.CaptureFixture[str]) -> Iterator[list[str]]:
    """Lines printed after the test, even when pytest captures the output:
    the accuracy is a result to read, not only to assert."""
    lines: list[str] = []
    yield lines
    with capsys.disabled():
        print("\n" + "\n".join(lines))


Result = tuple[str, str, list[str], float, float]  # stem, category, expected, score, margin


def classify_corpus(conn: psycopg.Connection, folder: Path) -> list[Result]:
    expect = tomllib.loads((folder / "categories.toml").read_text())["expect"]
    docs = sorted((folder / "docs").glob("*.txt"))
    assert sorted(p.stem for p in docs) == sorted(expect)
    for p in docs:
        assert run(conn, textemb.KIND, add(conn, p.name, p.read_text())) == ("done", None), p.name
    rows = conn.execute(
        """SELECT f.path, c.category, c.score, c.margin FROM files f
           JOIN document_categories c ON c.sha256 = f.sha256 ORDER BY f.path"""
    ).fetchall()
    assert len(rows) == len(docs)
    return [
        (Path(path).stem, got, expect[Path(path).stem], score, margin)
        for path, got, score, margin in rows
    ]


def describe(name: str, results: list[Result]) -> tuple[int, list[str]]:
    right = sum(got in want for _, got, want, _, _ in results)
    lines = [f"{name}: {right}/{len(results)} documents in their category (score, margin)"]
    lines += [
        f"  {'ok  ' if got in want else 'MISS'} {stem:34} {got:14} {score:.3f} {margin:.3f}"
        f"  expected {'/'.join(want)}"
        for stem, got, want, score, margin in results
    ]
    return right, lines


@needs_models
def test_the_benchmark_documents_land_in_their_category(
    conn: psycopg.Connection, report: list[str]
) -> None:
    right, lines = describe("benchmark", classify_corpus(conn, FIXTURES / "bench"))
    report.extend(lines)
    # 30 of 30 on arm64. fp16 moves a cosine by 0.0015 at most from one CPU to
    # another: the CAF certificate is 0.003 ahead of travail, the internet
    # invoice 0.011 ahead of contrats.
    assert right >= 28, "\n".join(lines)


@needs_models
def test_documents_the_sentences_were_not_tuned_on(
    conn: psycopg.Connection, report: list[str]
) -> None:
    right, lines = describe("second set", classify_corpus(conn, FIXTURES / "doccat"))
    report.extend(lines)
    assert right >= 9, "\n".join(lines)
