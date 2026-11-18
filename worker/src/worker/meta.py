"""Metadata of photos and videos (date taken, GPS, camera, dimensions, a
video's length), for core/internal/meta.

The worker reads the file's EXIF from the data root, read-only, without
decoding the pixels, and writes one row to media_meta, keyed by the content's
SHA-256: it never writes a file. A value the file does not carry, or carries
broken, is stored as NULL; only an unreadable image fails the task.
"""

from __future__ import annotations

import io
import math
import re
import warnings
from dataclasses import astuple, dataclass
from datetime import datetime
from typing import TYPE_CHECKING

import psycopg
from PIL import Image, UnidentifiedImageError

from worker import heif, thumbs, video
from worker.content import content_path, head_of, read_content, sha_of
from worker.errors import Permanent

if TYPE_CHECKING:
    from worker.tasks import Task

KIND = "meta"
FORMATS = thumbs.FORMATS  # with HEIF and HEIC, read by worker.heif; videos by worker.video
MAX_TEXT = 200

# EXIF tags (CIPA DC-008).
IFD_EXIF, IFD_GPS = 0x8769, 0x8825
MAKE, MODEL, ORIENTATION, DATETIME = 0x010F, 0x0110, 0x0112, 0x0132
DATETIME_ORIGINAL, DATETIME_DIGITIZED = 0x9003, 0x9004
OFFSET, OFFSET_ORIGINAL, OFFSET_DIGITIZED = 0x9010, 0x9011, 0x9012
SUBSEC, SUBSEC_ORIGINAL, SUBSEC_DIGITIZED = 0x9290, 0x9291, 0x9292
LENS_MODEL = 0xA434
GPS_LAT_REF, GPS_LAT, GPS_LON_REF, GPS_LON = 1, 2, 3, 4

DATE = re.compile(r"\s*(\d{4})[:-](\d{2})[:-](\d{2})[ T](\d{2}):(\d{2}):(\d{2})")
OFFSET_RE = re.compile(r"\s*([+-])(\d{2}):(\d{2})\s*$")


@dataclass
class Meta:
    taken_at: datetime | None = None  # wall clock as shot, no zone
    taken_offset: int | None = None  # minutes east of UTC
    latitude: float | None = None
    longitude: float | None = None
    make: str | None = None
    model: str | None = None
    lens: str | None = None
    width: int | None = None  # as displayed: orientation applied
    height: int | None = None
    orientation: int | None = None
    duration_ms: int | None = None  # a video's length


def text(value: object) -> str | None:
    """An EXIF string, cleaned: no NUL (Postgres refuses it), trimmed, bounded."""
    if isinstance(value, bytes):
        value = value.decode("utf-8", "replace")
    if not isinstance(value, str):
        return None
    value = value.replace("\x00", "").strip()
    return value[:MAX_TEXT] or None


def taken(value: object, subsec: object) -> datetime | None:
    """An EXIF date ("2024:07:14 18:03:22"), None when absent or impossible
    ("0000:00:00 00:00:00")."""
    m = DATE.match(text(value) or "")
    if not m:
        return None
    try:
        at = datetime(*(int(g) for g in m.groups()))
    except ValueError:
        return None
    if not 1900 <= at.year <= 2200:
        return None
    digits = re.match(r"\d+", text(subsec) or "")
    if digits:
        at = at.replace(microsecond=int(digits.group()[:6].ljust(6, "0")))
    return at


def offset(value: object) -> int | None:
    """An EXIF offset ("+02:00") in minutes, None when absent or out of range."""
    m = OFFSET_RE.match(text(value) or "")
    if not m:
        return None
    minutes = int(m.group(2)) * 60 + int(m.group(3))
    if minutes > 18 * 60:
        return None
    return -minutes if m.group(1) == "-" else minutes


def coordinate(value: object, ref: object, negative: str, limit: float) -> float | None:
    """Degrees from an EXIF GPS value (degrees, minutes, seconds), signed by its
    reference (S or W is negative). None when absent or broken."""
    try:
        parts = [float(v) for v in value] if isinstance(value, tuple | list) else [float(value)]
        deg = sum(p / 60**i for i, p in enumerate(parts[:3]))
    except (TypeError, ValueError, ZeroDivisionError):
        return None
    if not math.isfinite(deg) or not 0 <= deg <= limit:
        return None
    return -deg if (text(ref) or "").upper().startswith(negative) else deg


def read_exif(exif: Image.Exif, meta: Meta) -> None:
    """Fill meta from an image's EXIF. Values it lacks or carries broken stay None."""
    sub = exif.get_ifd(IFD_EXIF)
    for date, sec, off, ifd in (
        (DATETIME_ORIGINAL, SUBSEC_ORIGINAL, OFFSET_ORIGINAL, sub),
        (DATETIME_DIGITIZED, SUBSEC_DIGITIZED, OFFSET_DIGITIZED, sub),
        (DATETIME, SUBSEC, OFFSET, exif),
    ):
        at = taken(ifd.get(date), sub.get(sec))
        if at:
            meta.taken_at, meta.taken_offset = at, offset(sub.get(off))
            break
    meta.make, meta.model, meta.lens = (
        text(exif.get(MAKE)),
        text(exif.get(MODEL)),
        text(sub.get(LENS_MODEL)),
    )
    orientation = exif.get(ORIENTATION)
    if isinstance(orientation, int) and 1 <= orientation <= 8:
        meta.orientation = orientation
    gps = exif.get_ifd(IFD_GPS)
    lat = coordinate(gps.get(GPS_LAT), gps.get(GPS_LAT_REF), "S", 90)
    lon = coordinate(gps.get(GPS_LON), gps.get(GPS_LON_REF), "W", 180)
    # 0,0 is what some cameras write without a fix: in the sea off Africa.
    if lat is not None and lon is not None and (lat, lon) != (0.0, 0.0):
        meta.latitude, meta.longitude = lat, lon


def extract(data: bytes) -> Meta:
    """The metadata of an image. Raises Permanent for an unreadable image;
    a missing or broken EXIF only leaves its fields None."""
    meta = Meta()
    try:
        if heif.is_heif(data):
            width, height, tiff = heif.probe(data)  # size as displayed
            try:
                if tiff:
                    exif = Image.Exif()
                    exif.load(tiff)
                    read_exif(exif, meta)
            except Exception:  # a broken EXIF must not lose the photo's row
                meta = Meta()
            # libheif has turned the pixels upright already: the EXIF
            # orientation of a HEIC must not be applied again.
            meta.orientation = 1
        else:
            with warnings.catch_warnings():
                warnings.simplefilter("error", Image.DecompressionBombWarning)
                with Image.open(io.BytesIO(data), formats=FORMATS) as im:
                    width, height = im.size
                    try:
                        read_exif(im.getexif(), meta)
                    except Exception:  # a broken EXIF must not lose the photo's row
                        meta = Meta()
    except (
        UnidentifiedImageError,
        Image.DecompressionBombError,
        Image.DecompressionBombWarning,
        OSError,
        SyntaxError,
        ValueError,
    ) as err:
        raise Permanent(f"unreadable image: {type(err).__name__}: {err}") from err
    if meta.orientation in (5, 6, 7, 8):  # turned a quarter: displayed sideways
        width, height = height, width
    meta.width, meta.height = (width, height) if width > 0 and height > 0 else (None, None)
    return meta


def make(task: Task, conn: psycopg.Connection) -> None:
    """The meta task: payload {"sha256": hex}."""
    sha = sha_of(task.payload)
    if conn.execute("SELECT 1 FROM media_meta WHERE sha256 = %s", (sha,)).fetchone():
        return
    src = content_path(conn, sha)
    if src is None:
        return
    if video.is_video(head_of(src)):
        found = Meta(**video.metadata(src))
    else:
        data = read_content(conn, sha, "image")
        if data is None:
            return
        found = extract(data)
    conn.execute(
        """INSERT INTO media_meta (sha256, taken_at, taken_offset, latitude, longitude,
               make, model, lens, width, height, orientation, duration_ms)
           VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s) ON CONFLICT DO NOTHING""",
        (sha, *astuple(found)),
    )
