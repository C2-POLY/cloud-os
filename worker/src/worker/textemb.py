"""Vectors of the text of documents (#70), for core/internal/textemb.

When a document's text is written (worker.doctext, or worker.ocr for an
image or a scan), a textemb task is queued for its content, at the priority
of the task that wrote it. The
task cuts the text into windows of WINDOW tokens that overlap by OVERLAP, at
most MAX_CHUNKS of them, encodes each one with Arctic Embed
(worker.models.text_encoder) and replaces the content's rows in the model's
table, in one transaction. It reads document_text only: no file is read or
written.

The version of the vectors names the model (its revision and export) and the
cutting. The worker declares it in embedding_models (worker.vectors) with the
model it serves for queries (worker.embedserver); when it changes, the core
queues every content again at backfill priority (core/internal/textemb Sync):
another model or another cutting indexes everything again, without a command.

The same transaction writes the document's category (worker.doccat, #73),
computed from the vectors just encoded.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import TYPE_CHECKING

import numpy as np
import psycopg

from worker import models, vectors
from worker.content import sha_of

if TYPE_CHECKING:
    from tokenizers import Tokenizer

    from worker.tasks import Task

KIND = "textemb"
MODEL = models.TEXT_MODEL
TABLE = models.TEXT_TABLE
WINDOW = 510  # tokens of text per window; with the model's two special ones, 512
OVERLAP = 64  # tokens a window shares with the next, so a sentence cut at the edge is whole in one
MAX_CHUNKS = 64  # windows per document: about 100,000 characters, thirty pages


@dataclass(frozen=True)
class Chunk:
    """A window of a text, in characters: text[start:end]."""

    start: int
    end: int


def version() -> str:
    """The version of the vectors: the model and the cutting. A change to
    either indexes every content again."""
    return f"{models.TEXT_MODEL_VERSION}-w{WINDOW + 2}-o{OVERLAP}-n{MAX_CHUNKS}"


def chunks(text: str, tok: Tokenizer) -> list[Chunk]:
    """The windows of text: WINDOW tokens each, OVERLAP of them shared with
    the next window, at most MAX_CHUNKS; [] for a text without a token.

    tok is the model's own tokenizer, shared with the query endpoint, so it is
    never reconfigured: it cuts a long text into pieces (its truncation), and
    the pieces put end to end give every token with its offsets in text."""
    enc = tok.encode(text, add_special_tokens=False)
    offsets = [o for piece in (enc, *enc.overflowing) for o in piece.offsets]
    out: list[Chunk] = []
    start = 0
    while start < len(offsets) and len(out) < MAX_CHUNKS:
        end = min(start + WINDOW, len(offsets))
        out.append(Chunk(offsets[start][0], offsets[end - 1][1]))
        if end == len(offsets):
            break
        start = end - OVERLAP
    return out


def enqueue(conn: psycopg.Connection, sha: bytes, priority: int) -> int | None:
    """Queue the vectors of a content whose text was just written; None when
    its task is already pending. Call it in the transaction that writes the
    text, so that the text never stays without its task."""
    from worker import tasks

    return tasks.enqueue(
        conn, KIND, {"sha256": sha.hex()}, priority=priority, dedupe_key=f"{KIND}:{sha.hex()}"
    )


def make(task: Task, conn: psycopg.Connection) -> None:
    """The textemb task: payload {"sha256": hex}."""
    sha = sha_of(task.payload)
    row = conn.execute(
        "SELECT text FROM document_text WHERE sha256 = %s AND has_text", (sha,)
    ).fetchone()
    if row is None:
        return  # no file has the content any more, or no text yet (a scan before its OCR)
    current = version()
    done = conn.execute(
        f"SELECT 1 FROM {TABLE} WHERE sha256 = %s AND version = %s LIMIT 1", (sha, current)
    ).fetchone()
    if done:
        return
    from worker import doccat

    text = row[0]
    enc = models.text_encoder()
    windows = chunks(text, enc.tok)
    vecs = [enc.embed([text[c.start : c.end]])[0] for c in windows]
    rows = [
        (sha, i, c.start, c.end, MODEL, current, vectors.literal(v))
        for i, (c, v) in enumerate(zip(windows, vecs, strict=True))
    ]
    with conn.transaction(), conn.cursor() as cur:
        cur.execute(f"DELETE FROM {TABLE} WHERE sha256 = %s", (sha,))
        cur.executemany(
            f"""INSERT INTO {TABLE} (sha256, chunk, start_char, end_char, model, version, embedding)
                VALUES (%s, %s, %s, %s, %s, %s, %s::halfvec)""",
            rows,
        )
        # The category, from the vectors just computed: the document is never
        # listed with vectors and no category (worker.doccat).
        if vecs:
            doccat.store(cur, sha, doccat.classify(np.stack(vecs)), doccat.version())
        else:
            cur.execute(f"DELETE FROM {doccat.TABLE} WHERE sha256 = %s", (sha,))
