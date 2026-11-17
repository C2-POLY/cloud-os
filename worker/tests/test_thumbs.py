"""Thumbnails: rendering, and the thumb task against a throwaway database and
data root."""

import hashlib
import io
from pathlib import Path

import psycopg
import pytest
from PIL import Image

from worker import tasks, thumbs


def image(
    fmt: str, size: tuple[int, int] = (1200, 800), mode: str = "RGB", color=(255, 0, 0), **save
) -> bytes:
    out = io.BytesIO()
    Image.new(mode, size, color).save(out, fmt, **save)
    return out.getvalue()


def opened(webp: bytes) -> Image.Image:
    im = Image.open(io.BytesIO(webp))
    assert im.format == "WEBP"
    return im


@pytest.mark.parametrize("fmt", ["JPEG", "PNG", "WEBP"])
def test_render_fits_the_longest_side(fmt: str) -> None:
    webp, w, h = thumbs.render(image(fmt))
    assert (w, h) == (256, 171)
    assert opened(webp).size == (256, 171)


def test_render_applies_the_exif_orientation() -> None:
    exif = Image.Exif()
    exif[0x0112] = 6  # rotated 90° clockwise
    _, w, h = thumbs.render(image("JPEG", exif=exif.tobytes()))
    assert (w, h) == (171, 256)


def test_render_keeps_transparency() -> None:
    webp, _, _ = thumbs.render(image("PNG", mode="RGBA", color=(255, 0, 0, 128)))
    assert opened(webp).mode == "RGBA"


@pytest.mark.parametrize(
    "data",
    [b"not an image", image("JPEG")[:200], image("GIF")],
    ids=["text", "truncated", "unsupported format"],
)
def test_unreadable_images_are_permanent_errors(data: bytes) -> None:
    with pytest.raises(tasks.Permanent):
        thumbs.render(data)


def test_decompression_bombs_are_refused(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(Image, "MAX_IMAGE_PIXELS", 1000)
    with pytest.raises(tasks.Permanent):
        thumbs.render(image("PNG", size=(40, 40)))


@pytest.fixture
def root(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
    monkeypatch.setenv("CLOUDOS_DATA_DIR", str(tmp_path))
    return tmp_path


def store(conn: psycopg.Connection, root: Path, owner: str, path: str, data: bytes) -> bytes:
    """Put a file on disk and in the index, as core/internal/storage does."""
    disk = root / owner / path
    disk.parent.mkdir(parents=True, exist_ok=True)
    disk.write_bytes(data)
    sha = hashlib.sha256(data).digest()
    conn.execute(
        """INSERT INTO files (owner, path, size, mime, sha256, modified_at)
           VALUES (%s, %s, %s, 'image/jpeg', %s, now())""",
        (owner, path, len(data), sha),
    )
    return sha


def run(conn: psycopg.Connection, sha: bytes) -> tuple[str, str | None]:
    task_id = tasks.enqueue(conn, thumbs.KIND, {"sha256": sha.hex()})
    assert tasks.run_one(conn, "w", tasks.HANDLERS)
    row = conn.execute("SELECT state, last_error FROM tasks WHERE id = %s", (task_id,)).fetchone()
    assert row
    return row[0], row[1]


def test_task_writes_the_thumbnail_and_never_the_file(conn: psycopg.Connection, root: Path) -> None:
    data = image("JPEG")
    sha = store(conn, root, "enzo", "Photos/a.jpg", data)
    assert run(conn, sha) == ("done", None)
    row = conn.execute(
        "SELECT mime, width, height, bytes FROM thumbnails WHERE sha256 = %s", (sha,)
    ).fetchone()
    assert row and row[:3] == ("image/webp", 256, 171)
    assert opened(row[3]).size == (256, 171)
    assert (root / "enzo" / "Photos" / "a.jpg").read_bytes() == data
    assert sorted(p.name for p in root.rglob("*")) == ["Photos", "a.jpg", "enzo"]
    # Running it again is a no-op.
    assert run(conn, sha) == ("done", None)


def test_unreadable_image_fails_without_blocking_the_queue(
    conn: psycopg.Connection, root: Path
) -> None:
    bad = store(conn, root, "enzo", "bad.jpg", b"not a jpeg at all")
    good = store(conn, root, "enzo", "good.jpg", image("JPEG"))
    state, error = run(conn, bad)
    assert state == "failed" and error and "unreadable image" in error
    assert run(conn, good) == ("done", None)


def test_content_changed_on_disk_is_retried(conn: psycopg.Connection, root: Path) -> None:
    sha = store(conn, root, "enzo", "a.jpg", image("JPEG"))
    (root / "enzo" / "a.jpg").write_bytes(image("PNG"))
    state, error = run(conn, sha)
    assert state == "queued" and error and "no copy" in error


def test_content_no_longer_stored_is_done(conn: psycopg.Connection, root: Path) -> None:
    sha = hashlib.sha256(b"gone").digest()
    assert run(conn, sha) == ("done", None)
    assert conn.execute("SELECT count(*) FROM thumbnails").fetchone() == (0,)
