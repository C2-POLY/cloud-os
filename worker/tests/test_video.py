"""Videos: metadata and the thumbnail frame with the LGPL ffmpeg, and the thumb
and meta tasks on a stored video, against a throwaway database and data root.

The ffmpeg tests need ffmpeg and ffprobe: `make ffmpeg` builds them into
.tools/ffmpeg and `make test` points CLOUDOS_FFMPEG_DIR there. Without them
the tests are skipped, except when CLOUDOS_REQUIRE_FFMPEG is set (CI)."""

import hashlib
import io
import os
import shutil
from datetime import datetime
from pathlib import Path

import psycopg
import pytest
from PIL import Image

from worker import meta, tasks, thumbs, video
from worker.errors import Permanent

FIXTURES = Path(__file__).parent / "fixtures"
CLIP = FIXTURES / "clip.mp4"  # 320×240, 2 s, UTC date, GPS
IPHONE = FIXTURES / "iphone.mov"  # 320×240 turned 90°, 0.5 s, Apple date, GPS, camera
BROKEN = b"\x00\x00\x00\x18ftypisom\x00\x00\x02\x00isomiso2" + b"\xde\xad" * 400

ffmpeg = pytest.mark.skipif(
    shutil.which(video.binary("ffprobe")) is None and not os.environ.get("CLOUDOS_REQUIRE_FFMPEG"),
    reason="no ffmpeg: run `make ffmpeg`",
)


def test_is_video_tells_videos_from_heif_and_others() -> None:
    assert video.is_video(CLIP.read_bytes()[:32])
    assert video.is_video(IPHONE.read_bytes()[:32])
    assert not video.is_video((FIXTURES / "photo.heic").read_bytes()[:32])
    assert not video.is_video(b"\xff\xd8\xff\xe0" + b"\x00" * 28)


@pytest.mark.parametrize(
    ("value", "want"),
    [
        ("+48.8584+002.2945+035.000/", (48.8584, 2.2945)),
        ("-33.8688+151.2093/", (-33.8688, 151.2093)),
        ("+00.0000+000.0000/", None),  # no fix
        ("+95.0+002.0/", None),
        ("", None),
        (None, None),
    ],
)
def test_location_reads_iso6709(value: str | None, want: tuple[float, float] | None) -> None:
    assert video.location(value) == want


def test_taken_prefers_the_apple_date_with_its_zone() -> None:
    at, off = video.taken(
        {
            "com.apple.quicktime.creationdate": "2024-07-14T18:03:22+0200",
            "creation_time": "2024-07-14T16:03:22.000000Z",
        }
    )
    assert (at, off) == (datetime(2024, 7, 14, 18, 3, 22), 120)


def test_taken_falls_back_to_utc_and_drops_the_quicktime_epoch() -> None:
    assert video.taken({"creation_time": "2024-07-14T16:03:22.000000Z"}) == (
        datetime(2024, 7, 14, 16, 3, 22),
        0,
    )
    assert video.taken({"creation_time": "1904-01-01T00:00:00.000000Z"}) == (None, None)
    assert video.taken({}) == (None, None)


def test_missing_binary_is_retried_not_permanent(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("CLOUDOS_FFMPEG_DIR", "/nonexistent")
    with pytest.raises(RuntimeError, match="not found"):
        video.probe(CLIP)


@ffmpeg
def test_metadata_of_an_mp4() -> None:
    m = video.metadata(CLIP)
    assert m["duration_ms"] == 2000
    assert (m["width"], m["height"]) == (320, 240)
    assert (m["taken_at"], m["taken_offset"]) == (datetime(2024, 7, 14, 16, 3, 22), 0)
    assert (m["latitude"], m["longitude"]) == (48.8584, 2.2945)


@ffmpeg
def test_metadata_of_an_iphone_mov_turned_upright() -> None:
    m = video.metadata(IPHONE)
    assert m["duration_ms"] == 500
    assert (m["width"], m["height"]) == (240, 320)  # shot upright
    assert (m["taken_at"], m["taken_offset"]) == (datetime(2024, 7, 14, 18, 3, 22), 120)
    assert (m["latitude"], m["longitude"]) == (48.8584, 2.2945)
    assert (m["make"], m["model"]) == ("Apple", "iPhone 15")


@ffmpeg
def test_frame_is_upright_and_short_videos_use_their_first_frame() -> None:
    assert Image.open(io.BytesIO(video.frame(CLIP))).size == (320, 240)
    assert Image.open(io.BytesIO(video.frame(IPHONE))).size == (240, 320)


@ffmpeg
def test_broken_video_is_permanent(tmp_path: Path) -> None:
    bad = tmp_path / "bad.mp4"
    bad.write_bytes(BROKEN)
    with pytest.raises(Permanent):
        video.metadata(bad)
    with pytest.raises(Permanent):
        video.frame(bad)


@pytest.fixture
def root(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
    monkeypatch.setenv("CLOUDOS_DATA_DIR", str(tmp_path))
    return tmp_path


def store(conn: psycopg.Connection, root: Path, path: str, data: bytes, mime: str) -> bytes:
    disk = root / "enzo" / path
    disk.parent.mkdir(parents=True, exist_ok=True)
    disk.write_bytes(data)
    sha = hashlib.sha256(data).digest()
    conn.execute(
        """INSERT INTO files (owner, path, size, mime, sha256, modified_at)
           VALUES ('enzo', %s, %s, %s, %s, now())""",
        (path, len(data), mime, sha),
    )
    return sha


def run(conn: psycopg.Connection, kind: str, sha: bytes) -> tuple[str, str | None]:
    task_id = tasks.enqueue(conn, kind, {"sha256": sha.hex()})
    assert tasks.run_one(conn, "w", tasks.HANDLERS)
    row = conn.execute("SELECT state, last_error FROM tasks WHERE id = %s", (task_id,)).fetchone()
    assert row
    return row[0], row[1]


@ffmpeg
def test_tasks_write_thumbnail_and_metadata_and_never_the_file(
    conn: psycopg.Connection, root: Path
) -> None:
    data = IPHONE.read_bytes()
    sha = store(conn, root, "Vidéos/IMG_0001.MOV", data, "video/quicktime")
    assert run(conn, thumbs.KIND, sha) == ("done", None)
    assert run(conn, meta.KIND, sha) == ("done", None)
    w, h, webp = conn.execute(
        "SELECT width, height, bytes FROM thumbnails WHERE sha256 = %s", (sha,)
    ).fetchone()
    assert (w, h) == (192, 256)
    assert Image.open(io.BytesIO(webp)).format == "WEBP"
    row = conn.execute(
        "SELECT taken_at, taken_offset, latitude, model, width, height, duration_ms"
        " FROM media_meta WHERE sha256 = %s",
        (sha,),
    ).fetchone()
    assert row == (datetime(2024, 7, 14, 18, 3, 22), 120, 48.8584, "iPhone 15", 240, 320, 500)
    assert (root / "enzo" / "Vidéos/IMG_0001.MOV").read_bytes() == data  # untouched


@ffmpeg
def test_broken_video_fails_for_good(conn: psycopg.Connection, root: Path) -> None:
    sha = store(conn, root, "bad.mp4", BROKEN, "video/mp4")
    state, err = run(conn, meta.KIND, sha)
    assert state == "failed"
    assert err and "unreadable video" in err
    state, _ = run(conn, thumbs.KIND, sha)
    assert state == "failed"
