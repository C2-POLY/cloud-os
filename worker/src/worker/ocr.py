"""Text of photographed and scanned documents, read by OCR, for
core/internal/ocr.

Two kinds of content come here, by the task "ocr" {sha256}:

- an image (JPEG, PNG, WebP, HEIC, HEIF), queued by the core when it is
  stored. The detector alone, on the image scaled down to TRIAGE_SIDE pixels,
  tells a document (MIN_LINES lines of text or more) from a plain photo, which
  gets a row without text. A document is read in the worker, as thumbnails
  are made: a child process would cost more than the triage itself.
- a PDF that worker.doctext found without text (a scan), queued by its text
  task. pypdf hands out the largest image of each page, MAX_PAGES at most,
  read in a child process (`python -I -m worker.ocr`) bounded by TIMEOUT and
  MEMORY, like the one of worker.doctext.

A page is read with PP-OCRv5 (worker.models.OcrModel), numpy and Pillow, no
OpenCV: the detector's map, its connected components, an oriented box around
each line, the line cut out upright and read, then the lines in reading order.

The worker reads the data root read-only and checks each content against its
hash; the text goes to document_text, keyed by the content's SHA-256, with
ocr_model naming the model: nothing is ever written to a file.
"""

from __future__ import annotations

import contextlib
import io
import json
import logging
import math
import resource
import subprocess
import sys
import time
from dataclasses import asdict, dataclass
from typing import TYPE_CHECKING

import numpy as np
import psycopg
from PIL import Image
from pypdf import PasswordType, PdfReader, apply_configuration

from worker import doctext, models, thumbs
from worker.content import read_content, sha_of
from worker.errors import Permanent

if TYPE_CHECKING:
    from worker.tasks import Task

KIND = "ocr"
MODEL = models.OCR_MODEL

# Telling a document from a photo: the detector on the image scaled down to
# TRIAGE_SIDE pixels on its long side (35 ms on an M4, about 0.2 s on a Pi 5),
# and a document has at least MIN_LINES lines of text, boxes at least LINE_RATIO
# times as wide as tall. Measured on worker/tests/fixtures/bench: every page,
# scanned or photographed, has 11 to 32 of them, every photo 0 or 1.
TRIAGE_SIDE = 960
MIN_LINES = 5
LINE_RATIO = 3

# Reading a page, as PaddleOCR's detector post-processing: pixels over
# THRESHOLD are text, a box whose mean probability is below BOX_THRESHOLD is
# dropped, and each box is grown by UNCLIP (the detector maps a shrunk line).
# A line read with a mean probability below MIN_SCORE is noise (a stamp, a
# logo); a page keeps its MAX_LINES largest boxes, so that noise read as
# thousands of lines cannot hold the worker.
THRESHOLD = 0.3
BOX_THRESHOLD = 0.6
UNCLIP = 1.5
MIN_BOX = 3  # pixels of the map, on the short side
MIN_SCORE = 0.5
MAX_LINES = 400
# An image is decoded at least this large on its long side: the detector's input.
DECODE_SIDE = models.OcrModel.MAX_SIDE

# Scanned PDFs, in a child process.
MAX_PAGES = 50  # pages read; the rest is left out, the text marked truncated
TIMEOUT = 600  # seconds for one child process
SOFT_DEADLINE = 540  # seconds after which the child stops and keeps what it read
# Address space of one child process, in bytes. Measured in the worker image:
# 2.5 GiB at the peak of a three-page scan, ONNX Runtime's threads and arenas
# included (2 GiB fails), 1.9 GiB for an A4 page in colour at 600 dpi.
MEMORY = 3 * 1024**3
# What pypdf may decode for one image: 100 million pixels in RGB, as
# thumbs.MAX_PIXELS. An A4 page scanned in colour at 600 dpi is 139 MB, over
# the 75 MB pypdf allows on its own.
MAX_IMAGE_BYTES = 3 * thumbs.MAX_PIXELS
COMMAND = [sys.executable, "-I", "-m", "worker.ocr"]
UNREADABLE = 3  # exit status of the child for a PDF it cannot read


@dataclass
class Read:
    text: str
    truncated: bool = False
    pages: int = 0  # pages read: an image is one
    is_document: bool = True

    @property
    def has_text(self) -> bool:
        """As worker.doctext: MIN_PAGE_CHARS visible characters per page read."""
        return (
            self.is_document
            and self.pages > 0
            and (doctext.visible(self.text) >= doctext.MIN_PAGE_CHARS * self.pages)
        )


@dataclass
class Box:
    """A line of text on the detector's map: its centre, its size along and
    across the line, and the angle of the line."""

    cx: float
    cy: float
    w: float
    h: float
    angle: float

    def corners(self, sx: float, sy: float, width: int, height: int) -> list[tuple[float, float]]:
        """Its corners on a width × height image, the map scaled by (sx, sy):
        upper left, lower left, lower right, upper right, as Image.transform's
        QUAD. Kept inside the image, as PaddleOCR keeps them: what lies beyond
        would be cut out black."""
        c, s = math.cos(self.angle), math.sin(self.angle)
        out = []
        for du, dv in ((-1, -1), (-1, 1), (1, 1), (1, -1)):
            u, v = du * self.w / 2, dv * self.h / 2
            x, y = (self.cx + u * c - v * s) * sx, (self.cy + u * s + v * c) * sy
            out.append((min(max(x, 0.0), width), min(max(y, 0.0), height)))
        return out


# The detector's map into boxes.


def _runs(mask: np.ndarray) -> tuple[np.ndarray, np.ndarray, np.ndarray]:
    """The runs of True of each row: their row, start and end (excluded), in
    row-major order."""
    edges = np.diff(np.pad(mask.astype(np.int8), ((0, 0), (1, 1))), axis=1)
    rows, starts = np.nonzero(edges == 1)
    _, ends = np.nonzero(edges == -1)
    return rows, starts, ends


def _label(rows: np.ndarray, starts: np.ndarray, ends: np.ndarray, height: int) -> np.ndarray:
    """The connected component of each run (8-connectivity): runs of two
    consecutive rows that touch, even by a corner, are joined (union-find)."""
    parent = list(range(len(rows)))

    def find(x: int) -> int:
        while parent[x] != x:
            parent[x] = parent[parent[x]]
            x = parent[x]
        return x

    first = np.searchsorted(rows, np.arange(height + 1)).tolist()
    s, e = starts.tolist(), ends.tolist()
    for row in range(height - 1):
        i, i_end, j_end = first[row], first[row + 1], first[row + 2]
        j = i_end
        while i < i_end and j < j_end:
            if s[j] <= e[i] and s[i] <= e[j]:
                a, b = find(i), find(j)
                if a != b:
                    parent[b] = a
            if e[i] < e[j]:
                i += 1
            else:
                j += 1
    roots = np.array([find(x) for x in range(len(rows))], dtype=np.int64)
    return np.unique(roots, return_inverse=True)[1]


def line_boxes(prob: np.ndarray) -> list[Box]:
    """The lines of text on the detector's map: one oriented box around each
    connected component, from its moments (its long axis is the line's), grown
    by UNCLIP as PaddleOCR does; the MAX_LINES largest ones."""
    rows, starts, ends = _runs(prob > THRESHOLD)
    if not len(rows):
        return []
    label = _label(rows, starts, ends, prob.shape[0])
    k = int(label.max()) + 1
    # Sums over the pixels of each component, run by run: count, x, y, x², y², xy, probability.
    n = (ends - starts).astype(np.float64)
    first, last = starts.astype(np.float64), ends.astype(np.float64) - 1
    sum_x = (first + last) * n / 2
    sum_xx = (last * (last + 1) * (2 * last + 1) - (first - 1) * first * (2 * first - 1)) / 6
    y = rows.astype(np.float64)
    cum = np.pad(np.cumsum(prob, axis=1, dtype=np.float64), ((0, 0), (1, 0)))
    count = np.bincount(label, n, k)
    mx = np.bincount(label, sum_x, k) / count
    my = np.bincount(label, y * n, k) / count
    cxx = np.bincount(label, sum_xx, k) / count - mx * mx
    cyy = np.bincount(label, y * y * n, k) / count - my * my
    cxy = np.bincount(label, y * sum_x, k) / count - mx * my
    score = np.bincount(label, cum[rows, ends] - cum[rows, starts], k) / count
    angle = 0.5 * np.arctan2(2 * cxy, cxx - cyy)
    cos, sin = np.cos(angle), np.sin(angle)
    # Extent along (u) and across (v) the line: the ends of the runs, projected.
    lab = np.concatenate([label, label])
    px = np.concatenate([first, last]) - mx[lab]
    py = np.concatenate([y, y]) - my[lab]
    u = px * cos[lab] + py * sin[lab]
    v = py * cos[lab] - px * sin[lab]
    u0, v0 = np.full(k, np.inf), np.full(k, np.inf)
    u1, v1 = np.full(k, -np.inf), np.full(k, -np.inf)
    np.minimum.at(u0, lab, u)
    np.maximum.at(u1, lab, u)
    np.minimum.at(v0, lab, v)
    np.maximum.at(v1, lab, v)
    w, h = u1 - u0 + 1, v1 - v0 + 1
    keep = np.flatnonzero((np.minimum(w, h) >= MIN_BOX) & (score >= BOX_THRESHOLD))
    keep = keep[np.argsort(-(w * h)[keep], kind="stable")[:MAX_LINES]]
    boxes = []
    for i in keep.tolist():
        grow = w[i] * h[i] * UNCLIP / (2 * (w[i] + h[i]))
        mu, mv = (u0[i] + u1[i]) / 2, (v0[i] + v1[i]) / 2
        boxes.append(
            Box(
                cx=mx[i] + mu * cos[i] - mv * sin[i],
                cy=my[i] + mu * sin[i] + mv * cos[i],
                w=w[i] + 2 * grow,
                h=h[i] + 2 * grow,
                angle=angle[i],
            )
        )
    return boxes


# Images into text.


def rgb(image: Image.Image) -> Image.Image:
    """The image in RGB, a transparent one laid on white (a screenshot's text
    on a transparent background would turn black on black)."""
    if image.mode == "RGB":
        return image
    if "A" in image.getbands() or "transparency" in image.info:
        rgba = image.convert("RGBA")
        page = Image.new("RGB", rgba.size, "white")
        page.paste(rgba, mask=rgba.getchannel("A"))
        return page
    return image.convert("RGB")


def is_document(image: Image.Image) -> bool:
    """Whether the image shows a document rather than a scene: MIN_LINES lines
    of text or more, seen by the detector at TRIAGE_SIDE pixels."""
    prob = models.ocr_model().text_map(image, min_side=32, max_side=TRIAGE_SIDE)
    lines = [b for b in line_boxes(prob) if b.w >= LINE_RATIO * b.h]
    return len(lines) >= MIN_LINES


def read_page(image: Image.Image) -> str:
    """The text of a page, its lines in reading order: rows from top to
    bottom (lines whose middles are within half a line's height), and left to
    right within a row."""
    ocr = models.ocr_model()
    prob = ocr.text_map(image)
    sx, sy = image.width / prob.shape[1], image.height / prob.shape[0]
    crops, places = [], []
    for box in line_boxes(prob):
        quad = box.corners(sx, sy, image.width, image.height)
        width, height = math.dist(quad[0], quad[3]), math.dist(quad[0], quad[1])
        if width < 2 or height < 2:
            continue
        crop = image.transform(
            (round(width), round(height)),
            Image.Transform.QUAD,
            [c for corner in quad for c in corner],
            Image.Resampling.BICUBIC,
        )
        if crop.height > 1.5 * crop.width:  # a vertical line, as PaddleOCR turns it
            crop = crop.transpose(Image.Transpose.ROTATE_90)
        ys = [p[1] for p in quad]
        crops.append(crop)
        places.append((min(ys), max(ys), min(p[0] for p in quad)))
    lines = sorted(
        (top, bottom, left, text)
        for (top, bottom, left), (text, score) in zip(places, ocr.read_lines(crops), strict=True)
        if text.strip() and score >= MIN_SCORE
    )
    rows: list[tuple[float, list[tuple[float, str]]]] = []
    for top, bottom, left, text in lines:
        middle = (top + bottom) / 2
        if rows and abs(middle - rows[-1][0]) < (bottom - top) / 2:
            rows[-1][1].append((left, text))
        else:
            rows.append((middle, [(left, text)]))
    return "\n".join(" ".join(t for _, t in sorted(row)) for _, row in rows)


def read_image(data: bytes) -> Read:
    """The text of a photographed or scanned document, or no text for a plain
    photo. Raises Permanent for an unreadable image."""
    image = rgb(thumbs.open_upright(data, DECODE_SIDE))
    if not is_document(image):
        return Read("", pages=1, is_document=False)
    text = doctext.clean(read_page(image))
    return Read(text, len(text) >= doctext.MAX_CHARS, pages=1)


# Scanned PDFs.


def page_image(page) -> Image.Image | None:  # noqa: ANN001 (pypdf's PageObject)
    """The largest image of a PDF page that decodes, turned as the page is
    displayed (/Rotate); None when it has none (JBIG2, which Pillow does not
    read, or a page of vector drawings)."""
    found: Image.Image | None = None
    with contextlib.suppress(Exception):  # no images, or a broken resource dictionary
        names = list(page.images.keys())
        for name in names:
            try:
                image = page.images[name].image
            except Exception:  # an image Pillow cannot decode: the next one
                continue
            if image is not None and (found is None or _area(image) > _area(found)):
                found = image
    if found is None:
        return None
    turn = page.rotation % 360
    return rgb(found).rotate(-turn, expand=True) if turn else rgb(found)


def _area(image: Image.Image) -> int:
    return image.width * image.height


# pypdf's limits while reading a scan: its images may be as large as
# MAX_IMAGE_BYTES, and it never runs a jbig2dec found on the machine (the
# worker image has none: a JBIG2 page reads as empty wherever it runs).
PDF_LIMITS = {
    "image_maximum_buffer_size": MAX_IMAGE_BYTES,
    "zlib_maximum_output_length": MAX_IMAGE_BYTES,
    "lzw_maximum_output_length": MAX_IMAGE_BYTES,
    "run_length_maximum_output_length": MAX_IMAGE_BYTES,
    "array_based_stream_maximum_output_length": MAX_IMAGE_BYTES,
    "jbig2dec_binary": None,
}


def read_pdf(data: bytes, deadline: float | None = None) -> Read:
    """The text of a scanned PDF, MAX_PAGES at most, its pages separated as
    worker.doctext separates them. Past the deadline (time.monotonic()), what
    was read is kept, marked truncated. Raises Permanent for a PDF that cannot
    be read."""
    logging.getLogger("pypdf").setLevel(logging.ERROR)
    with apply_configuration(**PDF_LIMITS):
        try:
            reader = PdfReader(io.BytesIO(data))
            if reader.is_encrypted and reader.decrypt("") == PasswordType.NOT_DECRYPTED:
                raise Permanent("unreadable pdf: a password is needed to open it")
            total = len(reader.pages)
        except Permanent:
            raise
        except Exception as err:
            raise Permanent(f"unreadable pdf: {type(err).__name__}: {err}"[:500]) from err
        parts: list[str] = []
        done = 0
        truncated = total > MAX_PAGES
        for i in range(min(total, MAX_PAGES)):
            if deadline is not None and time.monotonic() > deadline:
                truncated = True
                break
            try:
                image = page_image(reader.pages[i])
            except Exception:  # a broken page: read as empty
                image = None
            parts.append(read_page(image) if image is not None else "")
            done += 1
    text = doctext.clean(doctext.PAGE.join(parts))
    return Read(text, truncated or len(text) >= doctext.MAX_CHARS, pages=done)


def read_pdf_text(data: bytes) -> Read:
    """read_pdf(), run in a child process bounded by TIMEOUT and MEMORY.
    Raises Permanent when the PDF cannot be read in time, RuntimeError when
    the child cannot run (another attempt may)."""
    try:
        done = subprocess.run(
            COMMAND, input=data, capture_output=True, timeout=TIMEOUT, check=False
        )
    except subprocess.TimeoutExpired as err:
        raise Permanent(f"unreadable pdf: OCR took over {TIMEOUT} s") from err
    why = (done.stderr.decode("utf-8", "replace").strip().splitlines() or ["no output"])[-1]
    if done.returncode == UNREADABLE:
        raise Permanent(why[:500])
    if done.returncode < 0:  # killed: out of memory, or worse
        raise Permanent(f"unreadable pdf: OCR killed by signal {-done.returncode}")
    if done.returncode != 0:
        raise RuntimeError(f"OCR of a pdf failed: {why[:500]}")
    return Read(**json.loads(done.stdout))


# The task.


def queue(conn: psycopg.Connection, sha: bytes, priority: int) -> None:
    """Queue the OCR of a content (a PDF found without text), once."""
    from worker.tasks import enqueue

    enqueue(conn, KIND, {"sha256": sha.hex()}, priority=priority, dedupe_key=f"{KIND}:{sha.hex()}")


def make(task: Task, conn: psycopg.Connection) -> None:
    """The ocr task: payload {"sha256": hex}. An image without a row in
    document_text, or a PDF found without text that no OCR has read; anything
    else has nothing to do."""
    sha = sha_of(task.payload)
    row = conn.execute(
        "SELECT source, has_text, ocr_model FROM document_text WHERE sha256 = %s", (sha,)
    ).fetchone()
    if row is not None and not (row[0] == "pdf" and not row[1] and row[2] is None):
        return
    data = read_content(conn, sha, "image" if row is None else "document")
    if data is None:
        return
    if row is None:
        found = read_image(data)
        conn.execute(
            """INSERT INTO document_text
                   (sha256, source, text, chars, truncated, pages, has_text, ocr_model)
               VALUES (%s, 'image', %s, %s, %s, NULL, %s, %s) ON CONFLICT DO NOTHING""",
            (sha, found.text, len(found.text), found.truncated, found.has_text, MODEL),
        )
        return
    found = read_pdf_text(data)
    conn.execute(
        """UPDATE document_text SET text = %s, chars = %s, truncated = %s, has_text = %s,
               ocr_model = %s
           WHERE sha256 = %s AND source = 'pdf' AND NOT has_text AND ocr_model IS NULL""",
        (found.text, len(found.text), found.truncated, found.has_text, MODEL, sha),
    )


def main() -> int:
    """The child process: a scanned PDF's bytes on stdin, its text as JSON on stdout."""
    # macOS refuses RLIMIT_AS: there, the timeout alone bounds the run.
    with contextlib.suppress(ValueError, OSError):
        resource.setrlimit(resource.RLIMIT_AS, (MEMORY, MEMORY))
    data = sys.stdin.buffer.read()
    try:
        found = read_pdf(data, time.monotonic() + SOFT_DEADLINE)
    except Exception as err:  # Permanent, or MemoryError past the limit
        why = str(err) if isinstance(err, Permanent) else f"unreadable pdf: {err!r}"
        print(why[:500], file=sys.stderr)
        return UNREADABLE
    json.dump(asdict(found), sys.stdout)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
