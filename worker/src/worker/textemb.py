"""Vectors of the text of documents (#70), for core/internal/textemb.

When a document's text is written (worker.doctext, and the OCR of #68), a
textemb task is queued for its content, at the priority of the text task. The
task cuts the text into windows of WINDOW tokens that overlap by OVERLAP, at
most MAX_CHUNKS of them, encodes each one with Arctic Embed
(worker.models.text_encoder) and replaces the content's rows in the model's
table, in one transaction. It reads document_text only: no file is read or
written.

The version of the vectors names the model file and the cutting. When the
worker starts, requeue_stale queues again, at backfill priority, every live
content without vectors of the current version: a new model file or another
cutting indexes everything again, without a command.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import TYPE_CHECKING

import numpy as np
import psycopg

from worker import models
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
PRIORITY_BACKFILL = 0  # core/internal/tasks.PriorityBackfill


@dataclass(frozen=True)
class Chunk:
    """A window of a text, in characters: text[start:end]."""

    start: int
    end: int


def version() -> str:
    """The version of the vectors: the model file and the cutting. Any change
    to either makes requeue_stale index every content again."""
    model = models.file_sha256(f"{MODEL}/model.onnx")[:12]
    return f"{model}-w{WINDOW + 2}-o{OVERLAP}-n{MAX_CHUNKS}"


def chunks(text: str, tok: Tokenizer) -> list[Chunk]:
    """The windows of text: WINDOW tokens each, OVERLAP of them shared with
    the next window, at most MAX_CHUNKS; [] for a text without a token.

    tok is the model's own tokenizer, shared with the query encoder, so it is
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


def literal(vector: np.ndarray) -> str:
    """A vector as pgvector reads it: "[0.1,-0.2,…]"."""
    return "[" + ",".join(f"{x:.6g}" for x in vector.tolist()) + "]"


def enqueue(conn: psycopg.Connection, sha: bytes, priority: int) -> int | None:
    """Queue the vectors of a content whose text was just written; None when
    its task is already pending. Call it in the transaction that writes the
    text, so that the text never stays without its task."""
    from worker import tasks

    return tasks.enqueue(
        conn, KIND, {"sha256": sha.hex()}, priority=priority, dedupe_key=f"{KIND}:{sha.hex()}"
    )


def requeue_stale(conn: psycopg.Connection, current: str | None = None) -> int:
    """Queue at backfill priority every live content with text and without
    vectors of the current version: after a new model file or another cutting,
    and the tasks lost on the way. Returns how many were queued."""
    cur = conn.execute(
        f"""INSERT INTO tasks (kind, payload, priority, dedupe_key)
            SELECT %(kind)s, jsonb_build_object('sha256', encode(d.sha256, 'hex')), %(priority)s,
                   %(kind)s || ':' || encode(d.sha256, 'hex')
            FROM document_text d
            WHERE d.has_text
              AND EXISTS (SELECT 1 FROM files f
                          WHERE f.kind = 'file' AND f.sha256 = d.sha256 AND f.trash_id IS NULL)
              AND NOT EXISTS (SELECT 1 FROM {TABLE} v
                              WHERE v.sha256 = d.sha256 AND v.version = %(version)s)
            ON CONFLICT (dedupe_key) WHERE state IN ('queued', 'running') AND dedupe_key IS NOT NULL
            DO NOTHING""",
        {"kind": KIND, "priority": PRIORITY_BACKFILL, "version": current or version()},
    )
    return cur.rowcount


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
    text = row[0]
    enc = models.text_encoder()
    rows = [
        (sha, i, c.start, c.end, MODEL, current, literal(enc.embed([text[c.start : c.end]])[0]))
        for i, c in enumerate(chunks(text, enc.tok))
    ]
    with conn.transaction(), conn.cursor() as cur:
        cur.execute(f"DELETE FROM {TABLE} WHERE sha256 = %s", (sha,))
        cur.executemany(
            f"""INSERT INTO {TABLE} (sha256, chunk, start_char, end_char, model, version, embedding)
                VALUES (%s, %s, %s, %s, %s, %s, %s::halfvec)""",
            rows,
        )
