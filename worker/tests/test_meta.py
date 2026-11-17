"""Photo metadata: EXIF reading, and the meta task against a throwaway database
and data root."""

import hashlib
import io
from datetime import datetime
from pathlib import Path

import psycopg
import pytest
from PIL import Image
from PIL.TiffImagePlugin import IFDRational

from worker import meta, tasks

HEIC = (Path(__file__).parent / "fixtures" / "photo.heic").read_bytes()


def rationals(*values: float) -> tuple[IFDRational, ...]:
    return tuple(IFDRational(v) for v in values)


def photo(
    exif: Image.Exif | None = None, fmt: str = "JPEG", size: tuple[int, int] = (1200, 800)
) -> bytes:
    out = io.BytesIO()
    extra = {"exif": exif.tobytes()} if exif is not None else {}
    Image.new("RGB", size, (0, 128, 255)).save(out, fmt, **extra)
    return out.getvalue()


def iphone_exif() -> Image.Exif:
    """What an iPhone writes, give or take: date with offset, place, camera."""
    exif = Image.Exif()
    exif[meta.MAKE] = "Apple"
    exif[meta.MODEL] = "iPhone 15\x00"
    exif[meta.ORIENTATION] = 6  # held upright: rotated 90° clockwise
    exif[meta.DATETIME] = "2024:07:15 09:00:00"  # last edit, not the shot
    exif[meta.IFD_EXIF] = {
        meta.DATETIME_ORIGINAL: "2024:07:14 18:03:22",
        meta.OFFSET_ORIGINAL: "+02:00",
        meta.SUBSEC_ORIGINAL: "45",
        meta.LENS_MODEL: "iPhone 15 back camera 6.86mm f/1.78",
    }
    exif[meta.IFD_GPS] = {
        meta.GPS_LAT_REF: "N",
        meta.GPS_LAT: rationals(48, 51, 30.24),
        meta.GPS_LON_REF: "E",
        meta.GPS_LON: rationals(2, 17, 40.2),
    }
    return exif


@pytest.mark.parametrize("fmt", ["JPEG", "PNG", "WEBP"])
def test_extract_reads_date_place_camera_and_upright_size(fmt: str) -> None:
    m = meta.extract(photo(iphone_exif(), fmt))
    assert m.taken_at == datetime(2024, 7, 14, 18, 3, 22, 450000)
    assert m.taken_offset == 120
    assert m.latitude == pytest.approx(48.8584)
    assert m.longitude == pytest.approx(2.2945)
    assert (m.make, m.model) == ("Apple", "iPhone 15")
    assert m.lens == "iPhone 15 back camera 6.86mm f/1.78"
    assert (m.width, m.height, m.orientation) == (800, 1200, 6)


def test_extract_reads_a_heic() -> None:
    m = meta.extract(HEIC)
    assert m.taken_at == datetime(2024, 7, 14, 18, 3, 22, 123000)
    assert m.taken_offset == 120
    assert m.latitude == pytest.approx(48.85822, abs=1e-4)
    assert m.longitude == pytest.approx(2.29450, abs=1e-4)
    assert (m.make, m.model) == ("Apple", "iPhone 15")
    # libheif turns the pixels upright: the size is as displayed, orientation 1.
    assert (m.width, m.height, m.orientation) == (64, 48, 1)


def test_extract_without_exif_keeps_the_size() -> None:
    m = meta.extract(photo(size=(640, 480)))
    assert (m.width, m.height) == (640, 480)
    assert m.taken_at is m.taken_offset is m.latitude is m.make is m.orientation is None


def test_south_west_and_date_fallbacks() -> None:
    exif = Image.Exif()
    exif[meta.DATETIME] = "2019:01:02 03:04:05"
    exif[meta.IFD_EXIF] = {meta.OFFSET: "-05:30"}
    exif[meta.IFD_GPS] = {
        meta.GPS_LAT_REF: "S",
        meta.GPS_LAT: rationals(33, 52, 4),
        meta.GPS_LON_REF: "W",
        meta.GPS_LON: rationals(151, 12, 36),
    }
    m = meta.extract(photo(exif))
    assert (m.taken_at, m.taken_offset) == (datetime(2019, 1, 2, 3, 4, 5), -330)
    assert m.latitude == pytest.approx(-33.8678, abs=1e-4)
    assert m.longitude == pytest.approx(-151.21, abs=1e-4)


def test_absurd_values_are_dropped_not_fatal() -> None:
    exif = Image.Exif()
    exif[meta.ORIENTATION] = 42
    exif[meta.IFD_EXIF] = {
        meta.DATETIME_ORIGINAL: "0000:00:00 00:00:00",
        meta.DATETIME_DIGITIZED: "2024:02:30 10:00:00",  # no 30 February
        meta.OFFSET_ORIGINAL: "+25:00",
    }
    exif[meta.IFD_GPS] = {meta.GPS_LAT: rationals(0, 0, 0), meta.GPS_LON: rationals(0, 0, 0)}
    m = meta.extract(photo(exif, size=(40, 30)))
    assert m.taken_at is m.taken_offset is m.latitude is m.longitude is m.orientation is None
    assert (m.width, m.height) == (40, 30)


def test_gps_out_of_range_is_dropped() -> None:
    exif = Image.Exif()
    exif[meta.IFD_GPS] = {meta.GPS_LAT: rationals(91, 0, 0), meta.GPS_LON: rationals(2, 0, 0)}
    m = meta.extract(photo(exif))
    assert m.latitude is m.longitude is None


@pytest.mark.parametrize(
    "data",
    [b"not an image", photo()[:200], photo(fmt="GIF"), HEIC[:300], HEIC[:40]],
    ids=["text", "truncated", "unsupported format", "truncated heic", "heic header only"],
)
def test_unreadable_images_are_permanent_errors(data: bytes) -> None:
    with pytest.raises(tasks.Permanent):
        meta.extract(data)


@pytest.fixture
def root(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
    monkeypatch.setenv("CLOUDOS_DATA_DIR", str(tmp_path))
    return tmp_path


def store(
    conn: psycopg.Connection,
    root: Path,
    owner: str,
    path: str,
    data: bytes,
    mime: str = "image/jpeg",
) -> bytes:
    """Put a file on disk and in the index, as core/internal/storage does."""
    disk = root / owner / path
    disk.parent.mkdir(parents=True, exist_ok=True)
    disk.write_bytes(data)
    sha = hashlib.sha256(data).digest()
    conn.execute(
        """INSERT INTO files (owner, path, size, mime, sha256, modified_at)
           VALUES (%s, %s, %s, %s, %s, now())""",
        (owner, path, len(data), mime, sha),
    )
    return sha


def run(conn: psycopg.Connection, sha: bytes) -> tuple[str, str | None]:
    task_id = tasks.enqueue(conn, meta.KIND, {"sha256": sha.hex()})
    assert tasks.run_one(conn, "w", tasks.HANDLERS)
    row = conn.execute("SELECT state, last_error FROM tasks WHERE id = %s", (task_id,)).fetchone()
    assert row
    return row[0], row[1]


def test_task_writes_the_row_and_never_the_file(conn: psycopg.Connection, root: Path) -> None:
    data = photo(iphone_exif())
    sha = store(conn, root, "enzo", "Photos/a.jpg", data)
    assert run(conn, sha) == ("done", None)
    row = conn.execute(
        """SELECT taken_at, taken_offset, round(latitude::numeric, 4), round(longitude::numeric, 4),
                  make, model, width, height, orientation
           FROM media_meta WHERE sha256 = %s""",
        (sha,),
    ).fetchone()
    assert row
    assert row[0] == datetime(2024, 7, 14, 18, 3, 22, 450000)
    assert (row[1], float(row[2]), float(row[3])) == (120, 48.8584, 2.2945)
    assert row[4:] == ("Apple", "iPhone 15", 800, 1200, 6)
    # The original is untouched, and nothing is written beside it.
    assert hashlib.sha256((root / "enzo" / "Photos" / "a.jpg").read_bytes()).digest() == sha
    assert sorted(p.name for p in root.rglob("*")) == ["Photos", "a.jpg", "enzo"]
    # Running it again is a no-op.
    assert run(conn, sha) == ("done", None)


def test_task_writes_the_row_of_a_heic(conn: psycopg.Connection, root: Path) -> None:
    sha = store(conn, root, "enzo", "iPhone/IMG_0001.HEIC", HEIC, "image/heic")
    assert run(conn, sha) == ("done", None)
    row = conn.execute(
        """SELECT taken_at, taken_offset, make, model, width, height, orientation
           FROM media_meta WHERE sha256 = %s""",
        (sha,),
    ).fetchone()
    assert row == (datetime(2024, 7, 14, 18, 3, 22, 123000), 120, "Apple", "iPhone 15", 64, 48, 1)
    assert (root / "enzo" / "iPhone" / "IMG_0001.HEIC").read_bytes() == HEIC


def test_broken_heic_fails_for_good(conn: psycopg.Connection, root: Path) -> None:
    sha = store(conn, root, "enzo", "broken.heic", HEIC[:300], "image/heic")
    state, error = run(conn, sha)
    assert state == "failed" and error and "unreadable image: HeifError" in error


def test_photo_without_exif_still_gets_its_row(conn: psycopg.Connection, root: Path) -> None:
    sha = store(conn, root, "enzo", "scan.jpg", photo(size=(640, 480)))
    assert run(conn, sha) == ("done", None)
    row = conn.execute(
        "SELECT taken_at, latitude, width, height FROM media_meta WHERE sha256 = %s", (sha,)
    ).fetchone()
    assert row == (None, None, 640, 480)


def test_unreadable_image_fails_without_blocking_the_queue(
    conn: psycopg.Connection, root: Path
) -> None:
    bad = store(conn, root, "enzo", "bad.jpg", b"not a jpeg at all")
    good = store(conn, root, "enzo", "good.jpg", photo())
    state, error = run(conn, bad)
    assert state == "failed" and error and "unreadable image" in error
    assert run(conn, good) == ("done", None)


def test_content_changed_on_disk_is_retried(conn: psycopg.Connection, root: Path) -> None:
    sha = store(conn, root, "enzo", "a.jpg", photo())
    (root / "enzo" / "a.jpg").write_bytes(photo(fmt="PNG"))
    state, error = run(conn, sha)
    assert state == "queued" and error and "no copy" in error


def test_content_no_longer_stored_is_done(conn: psycopg.Connection, root: Path) -> None:
    sha = hashlib.sha256(b"gone").digest()
    assert run(conn, sha) == ("done", None)
    assert conn.execute("SELECT count(*) FROM media_meta").fetchone() == (0,)
