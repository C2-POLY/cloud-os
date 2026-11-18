"""OCR of photographed and scanned documents (worker.ocr), for
core/internal/ocr.

invoice-scan.jpg and invoice-photo.jpg are the benchmark's electricity invoice
(bench/docs/facture-electricite-mars.txt) drawn as a scanned page and as a
phone photo of the page (fixtures/README.md). The tests that run the models
are skipped without them (`make models`), except when CLOUDOS_REQUIRE_MODELS
is set (CI)."""

from __future__ import annotations

import difflib
import hashlib
import io
import math
import os
import stat
from collections.abc import Iterator
from pathlib import Path

import numpy as np
import psycopg
import pytest
from PIL import Image, ImageDraw
from pypdf import PdfReader, PdfWriter
from pypdf.generic import DictionaryObject, NameObject, NumberObject, StreamObject

from worker import doctext, models, ocr, tasks, textemb

FIXTURES = Path(__file__).parent / "fixtures"
BENCH = FIXTURES / "bench"
INVOICE = (BENCH / "docs" / "facture-electricite-mars.txt").read_text()

needs_models = pytest.mark.skipif(
    not (models.models_dir() / "SHA256SUMS").exists()
    and not os.environ.get("CLOUDOS_REQUIRE_MODELS"),
    reason="no models: run `make models`",
)


def fixture(name: str) -> bytes:
    return (FIXTURES / name).read_bytes()


def same_text(read: str, truth: str) -> float:
    """How close two texts are, whitespace aside: 1 when equal."""
    return difflib.SequenceMatcher(None, " ".join(read.split()), " ".join(truth.split())).ratio()


def pdf_of(*pages: Image.Image) -> bytes:
    """A PDF whose pages are these images (JPEG inside), as a scanner makes it."""
    out = io.BytesIO()
    pages[0].save(out, "PDF", resolution=150, save_all=True, append_images=list(pages[1:]))
    return out.getvalue()


def blank_page() -> Image.Image:
    """A white A4 page at 150 dpi, with a scanner's grain."""
    grain = np.random.default_rng(7).normal(245, 6, (1754, 1240)).clip(0, 255)
    return Image.fromarray(grain.astype(np.uint8)).convert("RGB")


# Lines on the detector's map, without the models.


def bar_map(*bars: tuple[float, float, float, float, float]) -> np.ndarray:
    """A detector map with a solid bar of probability 0.9 for each (cx, cy,
    length, thickness, degrees)."""
    im = Image.new("F", (640, 480), 0)
    draw = ImageDraw.Draw(im)
    for cx, cy, length, thick, deg in bars:
        a = math.radians(deg)
        u, v = (math.cos(a), math.sin(a)), (-math.sin(a), math.cos(a))
        quad = [
            (cx + du * length / 2 * u[0] + dv * thick / 2 * v[0],
             cy + du * length / 2 * u[1] + dv * thick / 2 * v[1])
            for du, dv in ((-1, -1), (-1, 1), (1, 1), (1, -1))
        ]  # fmt: skip
        draw.polygon(quad, fill=0.9)
    return np.array(im, dtype=np.float32)


def test_a_blank_map_has_no_lines() -> None:
    assert ocr.line_boxes(np.zeros((480, 640), dtype=np.float32)) == []


def test_each_line_gets_its_own_box_along_its_slant() -> None:
    boxes = ocr.line_boxes(bar_map((200, 100, 300, 12, 0), (320, 300, 400, 16, -4)))
    assert len(boxes) == 2
    by_y = sorted(boxes, key=lambda b: b.cy)
    flat, tilted = by_y
    assert abs(flat.cx - 200) < 2 and abs(flat.cy - 100) < 2 and abs(flat.angle) < 0.01
    assert abs(tilted.cx - 320) < 2 and abs(tilted.cy - 300) < 2
    assert abs(math.degrees(tilted.angle) + 4) < 0.5
    # Grown as PaddleOCR grows its boxes: by area * 1.5 / perimeter on each side.
    assert flat.w > 300 and flat.h > 12 * 2
    assert flat.w / flat.h > ocr.LINE_RATIO


def test_dots_and_faint_marks_are_not_lines() -> None:
    prob = bar_map((200, 100, 300, 12, 0))
    prob[300:302, 300:302] = 0.9  # under MIN_BOX
    prob[400:420, 100:500] = 0.35  # over THRESHOLD, under BOX_THRESHOLD
    assert len(ocr.line_boxes(prob)) == 1


def test_a_page_keeps_its_largest_boxes(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(ocr, "MAX_LINES", 2)
    boxes = ocr.line_boxes(
        bar_map((200, 60, 100, 10, 0), (200, 160, 300, 10, 0), (200, 260, 200, 10, 0))
    )
    assert sorted(round(b.cy) for b in boxes) == [160, 260]


# Images.


@needs_models
@pytest.mark.parametrize("name", ["invoice-scan.jpg", "invoice-photo.jpg"])
def test_a_scanned_or_photographed_invoice_is_read(name: str) -> None:
    found = ocr.read_image(fixture(name))
    assert found.is_document and found.has_text and found.pages == 1
    assert same_text(found.text, INVOICE) > 0.99
    for words in ("VOLTA ÉNERGIE", "Facture d'électricité n° FE-2024-03-118842", "119,69 €"):
        assert words in found.text, words
    # Lines in reading order, one per line.
    assert found.text.splitlines()[:2] == [
        "VOLTA ÉNERGIE",
        "Facture d'électricité n° FE-2024-03-118842",
    ]


@needs_models
def test_an_invoice_in_english_is_read() -> None:
    found = ocr.read_image(fixture("invoice-en-photo.jpg"))
    assert found.is_document and found.has_text
    assert same_text(found.text, fixture("invoice-en.txt").decode()) > 0.99
    for words in ("Invoice No. NW-2024-00417", "VAT (20%): £89.80", "Total due: £538.80"):
        assert words in found.text, words


@needs_models
def test_holiday_photos_are_not_documents() -> None:
    photos = sorted((BENCH / "photos").glob("*.jpg"))
    assert len(photos) == 17
    for p in photos:
        found = ocr.read_image(p.read_bytes())
        assert (found.is_document, found.text, found.has_text) == (False, "", False), p.name


@needs_models
def test_a_blank_page_and_a_tiny_heic_are_not_documents() -> None:
    out = io.BytesIO()
    blank_page().save(out, "JPEG")
    for data in (out.getvalue(), fixture("photo.heic")):
        found = ocr.read_image(data)
        assert (found.is_document, found.text, found.has_text) == (False, "", False)


@needs_models
def test_a_screenshot_with_a_transparent_background_is_read() -> None:
    """Black text on a transparent background: laid on white, not on black."""
    with Image.open(FIXTURES / "ocr-line.png") as im:
        line = im.convert("L")
    shot = Image.new("RGBA", (line.width + 80, 6 * (line.height + 30)), (0, 0, 0, 0))
    ink = Image.new("RGBA", line.size, (0, 0, 0, 255))
    for i in range(6):
        shot.paste(ink, (40, 20 + i * (line.height + 30)), mask=line.point(lambda p: 255 - p))
    out = io.BytesIO()
    shot.save(out, "PNG")
    found = ocr.read_image(out.getvalue())
    assert found.is_document and found.has_text
    assert found.text.splitlines() == ["Facture n° 2024-118 : total 119,69 € (TVA à 20 %)"] * 6


@pytest.mark.parametrize(
    "data", [b"\xff\xd8\xff\xe0 not a jpeg", b"", fixture("invoice-photo.jpg")[:5000]]
)
def test_an_unreadable_image_fails_for_good(data: bytes) -> None:
    with pytest.raises(tasks.Permanent, match="unreadable image"):
        ocr.read_image(data)


# Scanned PDFs.


@needs_models
def test_a_scanned_pdf_is_read() -> None:
    found = ocr.read_pdf(fixture("scan.pdf"))
    assert found.pages == 1 and not found.truncated and found.has_text
    # The scan's font has no euro sign: the page shows a box there, not "€".
    assert found.text.splitlines() == [
        "Facture n° 2024-117",
        "Plomberie Dupuis, Nantes",
        "Remplacement du chauffe-eau",
        "Montant : 128,40",
    ]


@needs_models
def test_pages_are_read_upright_and_apart() -> None:
    """Page 1 is the invoice stored on its side, displayed upright by its
    /Rotate; page 2 is blank; page 3 is the invoice again, upright."""
    with Image.open(FIXTURES / "invoice-scan.jpg") as im:
        invoice = im.convert("RGB")
    reader = PdfReader(io.BytesIO(pdf_of(invoice.rotate(90, expand=True), blank_page(), invoice)))
    writer = PdfWriter(clone_from=reader)
    writer.pages[0].rotate(90)
    out = io.BytesIO()
    writer.write(out)
    found = ocr.read_pdf(out.getvalue())
    assert found.pages == 3 and found.has_text
    # A blank page leaves no line: its breaks merge into one, as in worker.doctext.
    first, last = found.text.split(doctext.PAGE)
    assert same_text(first, INVOICE) > 0.99 and same_text(last, INVOICE) > 0.99


@needs_models
def test_a_page_scanned_in_colour_at_600_dpi_is_read() -> None:
    """139 MB of pixels: over pypdf's own 75 MB limit, under MAX_IMAGE_BYTES."""
    with Image.open(FIXTURES / "invoice-scan.jpg") as im:
        page = im.convert("RGB").resize((4960, 7016), Image.Resampling.BICUBIC)
    found = ocr.read_pdf(pdf_of(page))
    assert found.pages == 1 and same_text(found.text, INVOICE) > 0.99


@needs_models
def test_a_blank_scan_has_no_text() -> None:
    found = ocr.read_pdf(pdf_of(blank_page()))
    assert (found.text, found.pages, found.has_text) == ("", 1, False)


def jbig2_pdf() -> bytes:
    """A one-page PDF whose only image is JBIG2, which Pillow does not read."""
    writer = PdfWriter()
    page = writer.add_blank_page(595, 842)
    image = StreamObject()
    image._data = b"\x97JB2\r\n\x1a\n not really"
    image.update(
        {
            NameObject("/Type"): NameObject("/XObject"),
            NameObject("/Subtype"): NameObject("/Image"),
            NameObject("/Width"): NumberObject(1240),
            NameObject("/Height"): NumberObject(1754),
            NameObject("/BitsPerComponent"): NumberObject(1),
            NameObject("/ColorSpace"): NameObject("/DeviceGray"),
            NameObject("/Filter"): NameObject("/JBIG2Decode"),
        }
    )
    page[NameObject("/Resources")] = DictionaryObject(
        {NameObject("/XObject"): DictionaryObject({NameObject("/Im0"): writer._add_object(image)})}
    )
    out = io.BytesIO()
    writer.write(out)
    return out.getvalue()


@needs_models
def test_a_page_whose_image_does_not_decode_reads_as_empty() -> None:
    found = ocr.read_pdf(jbig2_pdf())
    assert (found.text, found.pages, found.has_text) == ("", 1, False)


@needs_models
def test_past_max_pages_or_the_deadline_what_was_read_is_kept(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    data = pdf_of(blank_page(), blank_page(), blank_page())
    monkeypatch.setattr(ocr, "MAX_PAGES", 2)
    found = ocr.read_pdf(data)
    assert (found.pages, found.truncated) == (2, True)
    found = ocr.read_pdf(fixture("scan.pdf"), deadline=0)
    assert (found.text, found.pages, found.truncated, found.has_text) == ("", 0, True, False)


@pytest.mark.parametrize(
    ("data", "why"),
    [
        (b"%PDF-1.7\n not a pdf", "unreadable pdf"),
        (fixture("scan.pdf")[:3000], "unreadable pdf"),
    ],
)
def test_a_broken_pdf_fails_for_good_in_its_child_process(data: bytes, why: str) -> None:
    with pytest.raises(tasks.Permanent, match=why):
        ocr.read_pdf_text(data)


def test_a_pdf_that_needs_a_password_fails_for_good() -> None:
    writer = PdfWriter(clone_from=PdfReader(io.BytesIO(fixture("scan.pdf"))))
    writer.encrypt("secret", algorithm="AES-128")
    out = io.BytesIO()
    writer.write(out)
    with pytest.raises(tasks.Permanent, match="password"):
        ocr.read_pdf(out.getvalue())


# The task, against a throwaway database and a read-only data root.


@pytest.fixture
def root(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Iterator[Path]:
    monkeypatch.setenv("CLOUDOS_DATA_DIR", str(tmp_path))
    yield tmp_path
    for p in [tmp_path, *tmp_path.rglob("*")]:  # writable again, for the cleanup
        p.chmod(p.stat().st_mode | stat.S_IWUSR)


def store(conn: psycopg.Connection, root: Path, path: str, data: bytes, mime: str) -> bytes:
    """Put a file on disk and in the index, as core/internal/storage does."""
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


def read_only(root: Path) -> dict[Path, tuple[bytes, int]]:
    """Make the data root read-only, as the worker's mount; what it holds."""
    seen = {}
    for p in sorted(root.rglob("*"), reverse=True):
        if p.is_file():
            seen[p] = (hashlib.sha256(p.read_bytes()).digest(), p.stat().st_mtime_ns)
        p.chmod(p.stat().st_mode & ~(stat.S_IWUSR | stat.S_IWGRP | stat.S_IWOTH))
    root.chmod(root.stat().st_mode & ~stat.S_IWUSR)
    return seen


def run(
    conn: psycopg.Connection, kind: str, sha: bytes, priority: int = 0
) -> tuple[str, str | None]:
    """Queue a task of kind for the content and run the next ready task of
    that kind: the vectors an OCR queues (test_textemb.py) are left queued."""
    task_id = tasks.enqueue(conn, kind, {"sha256": sha.hex()}, priority=priority)
    assert tasks.run_one(conn, "w", {kind: tasks.HANDLERS[kind]})
    row = conn.execute("SELECT state, last_error FROM tasks WHERE id = %s", (task_id,)).fetchone()
    assert row
    return row[0], row[1]


def vector_tasks(conn: psycopg.Connection) -> list[tuple[str, int]]:
    """The textemb tasks queued: content and priority."""
    return conn.execute(
        "SELECT payload->>'sha256', priority FROM tasks WHERE kind = %s ORDER BY id",
        (textemb.KIND,),
    ).fetchall()


def text_row(conn: psycopg.Connection, sha: bytes) -> tuple | None:
    return conn.execute(
        """SELECT source, text, chars, truncated, pages, has_text, ocr_model
           FROM document_text WHERE sha256 = %s""",
        (sha,),
    ).fetchone()


@needs_models
def test_the_task_reads_images_and_never_writes_the_files(
    conn: psycopg.Connection, root: Path
) -> None:
    invoice = store(conn, root, "Factures/edf.jpg", fixture("invoice-photo.jpg"), "image/jpeg")
    beach = store(
        conn,
        root,
        "Vacances/plage.jpg",
        (BENCH / "photos" / "plage.jpg").read_bytes(),
        "image/jpeg",
    )
    before = read_only(root)
    assert run(conn, ocr.KIND, invoice, priority=10) == ("done", None)
    row = text_row(conn, invoice)
    assert row is not None
    source, text, chars, truncated, pages, has_text, model = row
    assert (source, chars, truncated, pages, has_text, model) == (
        "image",
        len(text),
        False,
        None,
        True,
        models.OCR_MODEL,
    )
    assert "Montant total TTC : 119,69 €" in text
    # A plain photo gets its row, without text, and is not looked at again.
    assert run(conn, ocr.KIND, beach) == ("done", None)
    assert text_row(conn, beach) == ("image", "", 0, False, None, False, models.OCR_MODEL)
    assert run(conn, ocr.KIND, beach) == ("done", None)
    assert conn.execute("SELECT count(*) FROM document_text").fetchone() == (2,)
    # The text read queued its vectors, at the priority of its OCR; the photo none.
    assert vector_tasks(conn) == [(invoice.hex(), 10)]
    # The originals are untouched, and nothing was written next to them.
    after = {p: (hashlib.sha256(p.read_bytes()).digest(), p.stat().st_mtime_ns) for p in before}
    assert after == before
    assert sorted(p for p in root.rglob("*") if p.is_file()) == sorted(before)


@needs_models
def test_a_scan_is_read_after_its_text_task(conn: psycopg.Connection, root: Path) -> None:
    scan = store(conn, root, "Scans/facture.pdf", fixture("scan.pdf"), "application/pdf")
    letter = store(conn, root, "Contrats/bail.pdf", fixture("letter.pdf"), "application/pdf")
    read_only(root)
    tasks.enqueue(conn, doctext.KIND, {"sha256": scan.hex()}, priority=10)
    tasks.enqueue(conn, doctext.KIND, {"sha256": letter.hex()}, priority=10)
    for _ in range(2):
        assert tasks.run_one(conn, "w", {doctext.KIND: doctext.make})
    assert text_row(conn, scan) == ("pdf", "", 0, False, 1, False, None)
    # The scan, and only it, queued its OCR at the priority of its text task.
    queued = conn.execute(
        "SELECT payload->>'sha256', priority, dedupe_key FROM tasks WHERE kind = 'ocr'"
    ).fetchall()
    assert queued == [(scan.hex(), 10, f"ocr:{scan.hex()}")]
    # The letter's text queued its vectors; the scan, without text yet, none.
    assert vector_tasks(conn) == [(letter.hex(), 10)]
    assert tasks.run_one(conn, "w", {ocr.KIND: ocr.make})
    row = text_row(conn, scan)
    assert row is not None
    assert row[0] == "pdf" and row[4:] == (1, True, models.OCR_MODEL)
    assert row[1].startswith("Facture n° 2024-117\n") and row[2] == len(row[1])
    # The text read in place queued the scan's vectors, at the priority of its OCR.
    assert vector_tasks(conn) == [(letter.hex(), 10), (scan.hex(), 10)]
    # Read once: another task changes nothing.
    assert run(conn, ocr.KIND, scan) == ("done", None)
    assert text_row(conn, scan) == row
    # A document with its own text is not the OCR's.
    assert run(conn, ocr.KIND, letter) == ("done", None)
    letter_row = text_row(conn, letter)
    assert letter_row is not None and letter_row[6] is None


def test_unreadable_image_fails_without_blocking_the_queue(
    conn: psycopg.Connection, root: Path
) -> None:
    bad = store(conn, root, "cassee.jpg", b"\xff\xd8\xff\xe0 broken", "image/jpeg")
    read_only(root)
    state, error = run(conn, ocr.KIND, bad)
    assert state == "failed" and error and "unreadable image" in error
    assert conn.execute("SELECT attempts FROM tasks WHERE state = 'failed'").fetchone() == (1,)
    assert conn.execute("SELECT count(*) FROM document_text").fetchone() == (0,)
    # A content no live file has any more: nothing to do.
    assert run(conn, ocr.KIND, hashlib.sha256(b"gone").digest()) == ("done", None)
