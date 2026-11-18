"""Reading a stored content by its SHA-256, for the tasks keyed by content.

The worker reads the data root read-only and checks each copy against its
hash before using it: it never writes a file.
"""

from __future__ import annotations

import hashlib
import os
from pathlib import Path

import psycopg

from worker.errors import Permanent

MAX_BYTES = 200 * 1024 * 1024


def data_root() -> Path:
    return Path(os.environ.get("CLOUDOS_DATA_DIR", "/data")).resolve()


def read_content(conn: psycopg.Connection, sha: bytes, what: str = "file") -> bytes | None:
    """The bytes of a live file with this content, checked against its hash.
    None when no live file has it any more (deleted or trashed: nothing to do).
    Raises Permanent when it is larger than MAX_BYTES ("<what> too large")."""
    rows = conn.execute(
        """SELECT owner, path, size FROM files
           WHERE kind = 'file' AND sha256 = %s AND trash_id IS NULL ORDER BY id""",
        (sha,),
    ).fetchall()
    if not rows:
        return None
    root = data_root()
    for owner, path, size in rows:
        if size > MAX_BYTES:
            raise Permanent(f"{what} too large: {size} bytes")
        disk = (root / owner / path).resolve()
        if not disk.is_relative_to(root):
            continue
        try:
            data = disk.read_bytes()
        except OSError:
            continue  # moved or replaced while we read: try the next copy
        if hashlib.sha256(data).digest() == sha:
            return data
    # Every copy moved or changed under us: retry later.
    raise RuntimeError("no copy of the content could be read")


def content_path(conn: psycopg.Connection, sha: bytes) -> Path | None:
    """The path of a live file with this content, checked against its hash by
    reading it in chunks, whatever its size: for videos, which ffmpeg reads
    from the disk. None when no live file has it any more."""
    rows = conn.execute(
        """SELECT owner, path FROM files
           WHERE kind = 'file' AND sha256 = %s AND trash_id IS NULL ORDER BY id""",
        (sha,),
    ).fetchall()
    if not rows:
        return None
    root = data_root()
    for owner, path in rows:
        disk = (root / owner / path).resolve()
        if not disk.is_relative_to(root):
            continue
        h = hashlib.sha256()
        try:
            with disk.open("rb") as f:
                while chunk := f.read(1024 * 1024):
                    h.update(chunk)
        except OSError:
            continue  # moved or replaced while we read: try the next copy
        if h.digest() == sha:
            return disk
    raise RuntimeError("no copy of the content could be read")


def head_of(path: Path, size: int = 32) -> bytes:
    with path.open("rb") as f:
        return f.read(size)


def sha_of(payload: dict) -> bytes:
    """The content a task keyed by content is about: payload {"sha256": hex}."""
    try:
        sha = bytes.fromhex(payload["sha256"])
    except (KeyError, TypeError, ValueError) as err:
        raise Permanent(f"bad payload: {payload!r}") from err
    if len(sha) != 32:
        raise Permanent(f"bad payload: {payload!r}")
    return sha
