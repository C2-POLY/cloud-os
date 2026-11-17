"""Heartbeat tests against a throwaway database next to CLOUDOS_TEST_DATABASE_URL.

The schema comes from the core's own migration file, so the worker is tested
against the table the core really creates.
"""

import os
import threading
import time
import uuid
from collections.abc import Iterator
from pathlib import Path
from urllib.parse import urlsplit, urlunsplit

import psycopg
import pytest

from worker import beat, wait_for_table

MIGRATION = Path(__file__).resolve().parents[2] / "core" / "migrations" / "0001_init.sql"


def migration_up() -> str:
    """The `-- +goose Up` part of the core's first migration."""
    sql = MIGRATION.read_text()
    return sql.split("-- +goose Up", 1)[1].split("-- +goose Down", 1)[0]


@pytest.fixture
def db_url() -> Iterator[str]:
    admin = os.environ.get("CLOUDOS_TEST_DATABASE_URL")
    if not admin:
        pytest.skip(
            "CLOUDOS_TEST_DATABASE_URL is not set: run `make test`, which starts the database"
        )
    name = f"cloudos_test_{uuid.uuid4().hex[:12]}"
    with psycopg.connect(admin, autocommit=True) as root:
        root.execute(f"CREATE DATABASE {name}")
        try:
            url = urlunsplit(urlsplit(admin)._replace(path=f"/{name}"))
            yield url
        finally:
            root.execute(f"DROP DATABASE {name} WITH (FORCE)")


@pytest.fixture
def conn(db_url: str) -> Iterator[psycopg.Connection]:
    with psycopg.connect(db_url, autocommit=True) as c:
        yield c


def test_waits_for_the_table_then_returns(db_url: str, conn: psycopg.Connection) -> None:
    stop = threading.Event()
    result: list[bool] = []
    waiter = threading.Thread(
        target=lambda: result.append(wait_for_table(conn, "worker_heartbeat", stop))
    )
    waiter.start()
    time.sleep(0.5)
    assert waiter.is_alive(), "returned before the table existed"

    with psycopg.connect(db_url, autocommit=True) as other:
        other.execute(migration_up())
    waiter.join(timeout=10)

    assert result == [True]


def test_stop_ends_the_wait(conn: psycopg.Connection) -> None:
    stop = threading.Event()
    stop.set()
    assert wait_for_table(conn, "worker_heartbeat", stop) is False


def test_beat_inserts_then_updates_one_row(conn: psycopg.Connection) -> None:
    conn.execute(migration_up())

    beat(conn, "worker-a")
    first = conn.execute(
        "SELECT seen_at FROM worker_heartbeat WHERE worker_id = 'worker-a'"
    ).fetchone()
    time.sleep(0.05)
    beat(conn, "worker-a")
    rows = conn.execute("SELECT worker_id, seen_at FROM worker_heartbeat").fetchall()

    assert first is not None
    assert len(rows) == 1
    assert rows[0][0] == "worker-a"
    assert rows[0][1] > first[0]
