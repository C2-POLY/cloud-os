"""What photos and videos show, as vectors, for core/internal/photovec and
the search by meaning (#71, docs/architecture/embeddings.md).

The worker reads the data root read-only, checks the content against its
SHA-256, encodes the photo (or a video's key frame, the one its thumbnail
shows) with SigLIP 2 and writes the vector to the database, keyed by
content: it never writes a file.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

import psycopg
from PIL import Image

from worker import models, thumbs, video
from worker.content import content_path, head_of, read_content, sha_of
from worker.vectors import literal

if TYPE_CHECKING:
    from worker.tasks import Task

KIND = "photovec"
# One table per model (core/migrations/0018_photo_vectors.sql).
TABLE = "photo_vectors_siglip2_b16_224"
MODEL = models.IMAGE_MODEL
VERSION = models.IMAGE_MODEL_VERSION
# SigLIP 2 reads 224 × 224 pixels: a JPEG needs decoding at no more than this.
AT_LEAST = 448


def upright_rgb(image: Image.Image) -> Image.Image:
    """The image in RGB, a transparent background turned white (as a viewer
    shows it) rather than black."""
    if image.mode in ("RGBA", "LA", "PA") or "transparency" in image.info:
        rgba = image.convert("RGBA")
        white = Image.new("RGBA", rgba.size, (255, 255, 255, 255))
        return Image.alpha_composite(white, rgba).convert("RGB")
    return image.convert("RGB")


def make(task: Task, conn: psycopg.Connection) -> None:
    """The photovec task: payload {"sha256": hex}."""
    sha = sha_of(task.payload)
    done = conn.execute(
        f"SELECT 1 FROM {TABLE} WHERE sha256 = %s AND version = %s", (sha, VERSION)
    ).fetchone()
    if done:
        return
    src = content_path(conn, sha)
    if src is None:
        return
    if video.is_video(head_of(src)):
        image, source = thumbs.decode(video.frame(src), AT_LEAST), "video"
    else:
        data = read_content(conn, sha, "image")
        if data is None:
            return
        image, source = thumbs.decode(data, AT_LEAST), "image"
    vector = models.image_encoder().embed([upright_rgb(image)])[0]
    conn.execute(
        f"""INSERT INTO {TABLE} (sha256, model, version, source, embedding)
            VALUES (%s, %s, %s, %s, %s::vector)
            ON CONFLICT (sha256) DO UPDATE
               SET model = EXCLUDED.model, version = EXCLUDED.version, source = EXCLUDED.source,
                   embedding = EXCLUDED.embedding, created_at = now()""",
        (sha, MODEL, VERSION, source, literal(vector)),
    )
