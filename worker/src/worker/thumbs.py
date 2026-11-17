"""Thumbnails of image files, for core/internal/thumbs.

The worker reads the data root read-only and writes the thumbnail to the
database, keyed by the content's SHA-256: it never writes a file.
"""

from __future__ import annotations

import io
import warnings
from typing import TYPE_CHECKING

import psycopg
from PIL import Image, ImageOps, UnidentifiedImageError

from worker import heif
from worker.content import read_content, sha_of
from worker.errors import Permanent

if TYPE_CHECKING:
    from worker.tasks import Task

KIND = "thumb"
SIZE = 256  # longest side, as thumbs.Size in Go
FORMATS = ["JPEG", "PNG", "WEBP"]  # read by Pillow; HEIF and HEIC by worker.heif
MAX_PIXELS = 100_000_000
QUALITY = 80

Image.MAX_IMAGE_PIXELS = MAX_PIXELS


def render(data: bytes) -> tuple[bytes, int, int]:
    """Return the WebP thumbnail of an image, upright, at most SIZE pixels on
    its longest side, and its size. Raises Permanent for an unreadable image."""
    try:
        with warnings.catch_warnings():
            # Between MAX_PIXELS and twice that, Pillow only warns.
            warnings.simplefilter("error", Image.DecompressionBombWarning)
            if heif.is_heif(data):
                img = heif.open_image(data)  # already upright
            else:
                with Image.open(io.BytesIO(data), formats=FORMATS) as im:
                    im.draft("RGB", (SIZE * 2, SIZE * 2))  # JPEG: decode at a smaller scale
                    img = ImageOps.exif_transpose(im)
                    img.load()
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


def make(task: Task, conn: psycopg.Connection) -> None:
    """The thumb task: payload {"sha256": hex}."""
    sha = sha_of(task.payload)
    done = conn.execute(
        "SELECT 1 FROM thumbnails WHERE sha256 = %s AND size = %s", (sha, SIZE)
    ).fetchone()
    if done:
        return
    data = read_content(conn, sha, "image")
    if data is None:
        return
    webp, width, height = render(data)
    conn.execute(
        """INSERT INTO thumbnails (sha256, size, mime, width, height, bytes)
           VALUES (%s, %s, 'image/webp', %s, %s, %s) ON CONFLICT DO NOTHING""",
        (sha, SIZE, width, height, webp),
    )
