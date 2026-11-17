"""Thumbnails of image files, for core/internal/thumbs.

The worker reads the data root read-only and writes the thumbnail to the
database, keyed by the content's SHA-256: it never writes a file.
"""

from __future__ import annotations

import hashlib
import io
import os
import warnings
from pathlib import Path

import psycopg
from PIL import Image, ImageOps, UnidentifiedImageError

from worker.tasks import Permanent, Task

KIND = "thumb"
SIZE = 256  # longest side, as thumbs.Size in Go
FORMATS = ["JPEG", "PNG", "WEBP"]  # HEIC: issue #30
MAX_PIXELS = 100_000_000
MAX_BYTES = 200 * 1024 * 1024
QUALITY = 80

Image.MAX_IMAGE_PIXELS = MAX_PIXELS


def data_root() -> Path:
    return Path(os.environ.get("CLOUDOS_DATA_DIR", "/data")).resolve()


def render(data: bytes) -> tuple[bytes, int, int]:
    """Return the WebP thumbnail of an image, upright, at most SIZE pixels on
    its longest side, and its size. Raises Permanent for an unreadable image."""
    try:
        with warnings.catch_warnings():
            # Between MAX_PIXELS and twice that, Pillow only warns.
            warnings.simplefilter("error", Image.DecompressionBombWarning)
            with Image.open(io.BytesIO(data), formats=FORMATS) as im:
                im.draft("RGB", (SIZE * 2, SIZE * 2))  # JPEG: decode at a smaller scale
                img = ImageOps.exif_transpose(im)
                img.thumbnail((SIZE, SIZE))
                if img.mode not in ("RGB", "RGBA"):
                    img = img.convert(
                        "RGBA" if "A" in img.getbands() or "transparency" in img.info else "RGB"
                    )
                out = io.BytesIO()
                img.save(out, "WEBP", quality=QUALITY)
                return out.getvalue(), img.width, img.height
    except (
        UnidentifiedImageError,
        Image.DecompressionBombError,
        Image.DecompressionBombWarning,
        OSError,
        SyntaxError,
        ValueError,
    ) as err:
        raise Permanent(f"unreadable image: {type(err).__name__}: {err}") from err


def read_content(conn: psycopg.Connection, sha: bytes) -> bytes | None:
    """The bytes of a live file with this content, checked against its hash.
    None when no live file has it any more (deleted or trashed: nothing to do)."""
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
            raise Permanent(f"image too large: {size} bytes")
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


def make(task: Task, conn: psycopg.Connection) -> None:
    """The thumb task: payload {"sha256": hex}."""
    try:
        sha = bytes.fromhex(task.payload["sha256"])
    except (KeyError, TypeError, ValueError) as err:
        raise Permanent(f"bad payload: {task.payload!r}") from err
    done = conn.execute(
        "SELECT 1 FROM thumbnails WHERE sha256 = %s AND size = %s", (sha, SIZE)
    ).fetchone()
    if done:
        return
    data = read_content(conn, sha)
    if data is None:
        return
    webp, width, height = render(data)
    conn.execute(
        """INSERT INTO thumbnails (sha256, size, mime, width, height, bytes)
           VALUES (%s, %s, 'image/webp', %s, %s, %s) ON CONFLICT DO NOTHING""",
        (sha, SIZE, width, height, webp),
    )
