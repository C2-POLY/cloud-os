"""The embedding models the worker declares to the core, and vectors in the
form pgvector reads (docs/architecture/embeddings.md).

The worker is the one that knows which model and version it runs: at
startup it writes them to embedding_models. When a version changes,
requeued_at goes back to NULL, and the core queues every content again for
it (core/internal/photovec Sync). The classifier of documents, which reads
Arctic's vectors (worker.doccat, #73), is declared the same way: a new
version of its sentences classifies every document again.
"""

from __future__ import annotations

from collections.abc import Iterable

import psycopg

from worker import models

DECLARE = """
    INSERT INTO embedding_models (name, version, dims) VALUES (%s, %s, %s)
    ON CONFLICT (name) DO UPDATE
       SET version = EXCLUDED.version, dims = EXCLUDED.dims,
           declared_at = now(), requeued_at = NULL
     WHERE embedding_models.version <> EXCLUDED.version
        OR embedding_models.dims <> EXCLUDED.dims
"""


def declare(conn: psycopg.Connection) -> None:
    """Record the name, version and dimension of every embedding model this
    worker serves, and of the classifier of documents (the dimension of the
    vectors it reads). One already declared at the same version is left as
    it is, so restarting a worker queues nothing again."""
    from worker import doccat

    for name, served in models.embedding_models().items():
        conn.execute(DECLARE, (name, served.version, served.dims))
    conn.execute(DECLARE, (doccat.NAME, doccat.version(), doccat.DIMS))


def literal(vector: Iterable[float]) -> str:
    """A vector as pgvector's text input, "[0.1,-0.2,…]", precise enough to
    give back the same float32 values; cast it with ::vector."""
    return "[" + ",".join(format(float(x), ".9g") for x in vector) + "]"
