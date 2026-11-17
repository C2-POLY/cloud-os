"""Fixtures shared by the database tests: a throwaway database built from the
core's own migrations."""

import os
import uuid
from collections.abc import Iterator
from pathlib import Path
from urllib.parse import urlsplit, urlunsplit

import psycopg
import pytest

MIGRATIONS = Path(__file__).resolve().parents[2] / "core" / "migrations"


def migrations_up() -> str:
    """The Up part of every core migration, in order."""
    return "\n".join(
        f.read_text().split("-- +goose Up", 1)[1].split("-- +goose Down", 1)[0]
        for f in sorted(MIGRATIONS.glob("*.sql"))
    )


@pytest.fixture
def db_url() -> Iterator[str]:
    admin = os.environ.get("CLOUDOS_TEST_DATABASE_URL")
    if not admin:
        pytest.skip("CLOUDOS_TEST_DATABASE_URL is not set: run `make test`")
    name = f"cloudos_test_{uuid.uuid4().hex[:12]}"
    with psycopg.connect(admin, autocommit=True) as root:
        root.execute(f"CREATE DATABASE {name}")
        try:
            url = urlunsplit(urlsplit(admin)._replace(path=f"/{name}"))
            with psycopg.connect(url, autocommit=True) as c:
                c.execute(migrations_up())
            yield url
        finally:
            root.execute(f"DROP DATABASE {name} WITH (FORCE)")


@pytest.fixture
def conn(db_url: str) -> Iterator[psycopg.Connection]:
    with psycopg.connect(db_url, autocommit=True) as c:
        yield c
