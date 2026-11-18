"""The models the worker declares to the core, and vectors as pgvector text."""

import numpy as np
import psycopg
import pytest

from worker import models, vectors


def row(conn: psycopg.Connection) -> tuple:
    found = conn.execute(
        "SELECT version, dims, requeued_at IS NULL FROM embedding_models WHERE name = %s",
        (models.IMAGE_MODEL,),
    ).fetchone()
    assert found
    return found


def test_declare_sets_a_new_version_due_once(
    conn: psycopg.Connection, monkeypatch: pytest.MonkeyPatch
) -> None:
    vectors.declare(conn)
    assert row(conn) == (models.IMAGE_MODEL_VERSION, 768, True)
    # The core queues every photo for it (core/internal/embed MarkRequeued).
    conn.execute("UPDATE embedding_models SET requeued_at = now()")
    vectors.declare(conn)  # a restart, same version: nothing to queue again
    assert row(conn) == (models.IMAGE_MODEL_VERSION, 768, False)

    newer = models.Served("next-int8", 768, models.image_queries)
    monkeypatch.setattr(models, "embedding_models", lambda: {models.IMAGE_MODEL: newer})
    vectors.declare(conn)
    assert row(conn) == ("next-int8", 768, True)


def test_literal_gives_back_the_same_float32_values(conn: psycopg.Connection) -> None:
    v = np.array([0.1, -1 / 3, 1e-8, 0.0, 12345.678], dtype=np.float32)
    text = vectors.literal(v)
    assert text.startswith("[0.100000001,") and text.endswith("]")
    back = conn.execute("SELECT %s::vector::real[]", (text,)).fetchone()
    assert back and np.array_equal(np.array(back[0], dtype=np.float32), v)
