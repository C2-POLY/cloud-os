"""Videos (MP4, QuickTime): one frame for the thumbnail, and the metadata
(length, date, GPS, camera, size as displayed), for worker.thumbs and
worker.meta.

ffmpeg and ffprobe are separate, unmodified LGPL binaries (built by
worker/ffmpeg/build.sh, docs/decisions/002-lgpl.md), run as subprocesses on
the stored file, which the worker opens read-only: they write to a pipe,
never to a file. CLOUDOS_FFMPEG_DIR names the folder holding them; without
it they are looked up on PATH.
"""

from __future__ import annotations

import contextlib
import json
import os
import re
import resource
import subprocess
from datetime import UTC, datetime, timedelta
from pathlib import Path

from worker import heif
from worker.errors import Permanent

TIMEOUT = 30  # seconds per ffmpeg or ffprobe run
MEMORY = 2 * 1024**3  # address space of one run, in bytes
FRAME_AT = 1.0  # seconds into the video: past a black first frame

ISO6709 = re.compile(r"^\s*([+-]\d+(?:\.\d+)?)([+-]\d+(?:\.\d+)?)")
APPLE_DATE = re.compile(r"^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})([+-])(\d{2}):?(\d{2})$")


def binary(name: str) -> str:
    folder = os.environ.get("CLOUDOS_FFMPEG_DIR")
    return str(Path(folder) / name) if folder else name


def is_video(head: bytes) -> bool:
    """Whether a content starting with head is an MP4 or QuickTime video:
    an ISO base media file (`ftyp` box) that is not a HEIF still image."""
    return len(head) >= 12 and head[4:8] == b"ftyp" and not heif.is_heif(head)


def _limit() -> None:  # pragma: no cover - runs in the child process
    # macOS refuses RLIMIT_AS: there, the timeout alone bounds the run.
    with contextlib.suppress(ValueError, OSError):
        resource.setrlimit(resource.RLIMIT_AS, (MEMORY, MEMORY))


def run(args: list[str]) -> bytes:
    """Run ffmpeg or ffprobe and return its standard output. Raises Permanent
    when the video cannot be read, RuntimeError when the binary is missing
    (another attempt may find it)."""
    try:
        done = subprocess.run(
            args,
            stdin=subprocess.DEVNULL,
            capture_output=True,
            timeout=TIMEOUT,
            preexec_fn=_limit if os.name == "posix" else None,
            check=False,
        )
    except FileNotFoundError as err:
        raise RuntimeError(f"{args[0]} not found: set CLOUDOS_FFMPEG_DIR") from err
    except subprocess.TimeoutExpired as err:
        raise Permanent(f"unreadable video: {Path(args[0]).name} took over {TIMEOUT} s") from err
    if done.returncode != 0:
        why = done.stderr.decode("utf-8", "replace").strip().splitlines()[-1:] or ["no output"]
        raise Permanent(f"unreadable video: {why[0][:200]}")
    return done.stdout


def frame(path: Path) -> bytes:
    """One frame of the video, upright, as PNG: at FRAME_AT seconds, or the
    first one when the video is shorter."""
    for at in (FRAME_AT, 0.0):
        png = run(
            [binary("ffmpeg"), "-nostdin", "-v", "error", "-ss", str(at), "-i", str(path)]
            + ["-frames:v", "1", "-f", "image2pipe", "-c:v", "png", "-"]
        )
        if png:
            return png
    raise Permanent("unreadable video: no frame")


def probe(path: Path) -> dict:
    """ffprobe's view of the video: its container and its streams."""
    out = run(
        [binary("ffprobe"), "-v", "error", "-print_format", "json"]
        + ["-show_format", "-show_streams", str(path)]
    )
    try:
        info = json.loads(out)
    except ValueError as err:
        raise Permanent("unreadable video: ffprobe gave no JSON") from err
    if not any(s.get("codec_type") == "video" for s in info.get("streams", [])):
        raise Permanent("unreadable video: no video stream")
    return info


def location(value: str | None) -> tuple[float, float] | None:
    """Latitude and longitude from an ISO 6709 string ("+48.8584+002.2945+035.000/")."""
    m = ISO6709.match(value or "")
    if not m:
        return None
    lat, lon = float(m.group(1)), float(m.group(2))
    if not (-90 <= lat <= 90 and -180 <= lon <= 180) or (lat, lon) == (0.0, 0.0):
        return None
    return lat, lon


def taken(tags: dict) -> tuple[datetime | None, int | None]:
    """When the video was shot, as wall clock and offset from UTC in minutes.
    iPhones write it with its zone; other phones write only UTC."""
    m = APPLE_DATE.match(tags.get("com.apple.quicktime.creationdate", "").strip())
    if m:
        try:
            at = datetime(*(int(g) for g in m.groups()[:6]))
        except ValueError:
            at = None
        minutes = int(m.group(8)) * 60 + int(m.group(9))
        if at and 1900 <= at.year <= 2200 and minutes <= 18 * 60:
            return at, -minutes if m.group(7) == "-" else minutes
    raw = tags.get("creation_time", "").strip()
    try:
        utc = datetime.fromisoformat(raw.replace("Z", "+00:00"))
    except ValueError:
        return None, None
    if utc.tzinfo is not None:
        utc = utc.astimezone(UTC).replace(tzinfo=None)
    # 1904-01-01, the QuickTime epoch, is what cameras write without a clock.
    if not 1971 <= utc.year <= 2200:
        return None, None
    return utc, 0


def rotation(stream: dict) -> int:
    """The display rotation of a video stream, in degrees."""
    for side in stream.get("side_data_list", []):
        if "rotation" in side:
            try:
                return int(float(side["rotation"]))
            except (TypeError, ValueError):
                return 0
    try:
        return int(stream.get("tags", {}).get("rotate", 0))
    except (TypeError, ValueError):
        return 0


def text(value: object) -> str | None:
    if not isinstance(value, str):
        return None
    value = value.replace("\x00", "").strip()
    return value[:200] or None


def metadata(path: Path) -> dict:
    """The fields of worker.meta.Meta a video has: taken_at, taken_offset,
    latitude, longitude, make, model, width, height, duration_ms."""
    info = probe(path)
    fmt = info.get("format", {})
    tags = {k.lower(): v for k, v in fmt.get("tags", {}).items()}
    video = next(s for s in info["streams"] if s.get("codec_type") == "video")
    out: dict = {}
    out["taken_at"], out["taken_offset"] = taken(tags)
    where = location(tags.get("com.apple.quicktime.location.iso6709") or tags.get("location"))
    if where:
        out["latitude"], out["longitude"] = where
    out["make"] = text(tags.get("com.apple.quicktime.make"))
    out["model"] = text(tags.get("com.apple.quicktime.model"))
    width, height = video.get("width"), video.get("height")
    if isinstance(width, int) and isinstance(height, int) and width > 0 and height > 0:
        if abs(rotation(video)) % 180 == 90:
            width, height = height, width
        out["width"], out["height"] = width, height
    try:
        seconds = float(fmt.get("duration") or video.get("duration"))
    except (TypeError, ValueError):
        seconds = -1.0
    if 0 <= seconds < timedelta(days=7).total_seconds():
        out["duration_ms"] = round(seconds * 1000)
    return out
