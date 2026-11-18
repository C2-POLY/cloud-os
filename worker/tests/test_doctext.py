"""Document text: extraction of each format, and the text task against a
throwaway database and data root."""

import hashlib
import io
import re
import stat
import sys
import time
import zipfile
from collections.abc import Iterator
from pathlib import Path

import psycopg
import pytest
from pypdf import PdfReader, PdfWriter

from worker import doctext, tasks

FIXTURES = Path(__file__).parent / "fixtures"


def fixture(name: str) -> bytes:
    return (FIXTURES / name).read_bytes()


LEASE = [
    "Contrat de location",
    "Appartement de trois pièces, deuxième étage, rue des Tilleuls à Nantes.",
    "Le loyer mensuel est de 850 € charges comprises",
    "Fait à Nantes, le 14 juillet 2024.",
    "État des lieux",
    "fenêtres à double vitrage.",
]
BUDGET = ["Budget 2024\nPoste\tMontant\nLoyer\t850\n", "Assurance habitation\t18", "Notes\n"]
SLIDES = (
    "Déménagement\nCartons : 40\nCamion réservé le 31 juillet\n\f\n"
    "Calendrier\nRemise des clés le 1er août"
)


@pytest.mark.parametrize(
    ("name", "source", "pages", "parts"),
    [
        ("letter.pdf", "pdf", 2, [*LEASE, "2024.\n\f\nÉtat des lieux"]),
        ("letter.docx", "docx", None, LEASE),
        ("letter.odt", "odt", None, [*LEASE, "Dépôt de garantie\t850 €\nDurée du bail\t3 ans"]),
        ("budget.xlsx", "xlsx", 2, [*BUDGET, "Électricité\t62.5", "18\n\f\nNotes"]),
        ("budget.ods", "ods", 2, [*BUDGET, "Électricité\t62,5", "18\n\f\nNotes"]),
        ("slides.pptx", "pptx", 2, [SLIDES]),
        ("slides.odp", "odp", 2, [SLIDES]),
    ],
)
def test_extract_reads_each_format(name: str, source: str, pages: int | None, parts: list[str]):
    found = doctext.extract(fixture(name))
    assert (found.source, found.pages, found.has_text, found.truncated) == (
        source,
        pages,
        True,
        False,
    )
    for part in parts:
        assert part in found.text
    assert found.text == found.text.strip()


@pytest.mark.parametrize(
    ("data", "text"),
    [
        (b"# Courses\r\n\r\n- lait\r\n- pain   \r\n", "# Courses\n\n- lait\n- pain"),
        ("date;libellé;montant\n14/07;Électricité;62,5\n".encode(), "Électricité"),
        ("﻿notes en UTF-8 avec BOM : été".encode(), "notes en UTF-8 avec BOM : été"),
        ("notes en UTF-16 : été".encode("utf-16"), "notes en UTF-16 : été"),
        ("notes Windows : été, 5 €".encode("cp1252"), "notes Windows : été, 5 €"),
        (b"", ""),
        # Quoting a PDF header does not make a text a PDF.
        (b"Un PDF commence par %PDF-1.7 puis ses objets.", "Un PDF commence par %PDF-1.7"),
    ],
    ids=["markdown crlf", "csv", "utf-8 bom", "utf-16", "cp1252", "empty", "quotes a pdf header"],
)
def test_extract_reads_plain_text(data: bytes, text: str) -> None:
    found = doctext.extract(data)
    assert found.source == "text" and found.pages is None
    assert text in found.text
    assert found.has_text == bool(text)


def test_a_scanned_pdf_is_recorded_without_text() -> None:
    found = doctext.extract(fixture("scan.pdf"))
    assert (found.source, found.text, found.pages, found.has_text) == ("pdf", "", 1, False)


def test_a_pdf_with_too_little_text_per_page_has_no_text(monkeypatch: pytest.MonkeyPatch) -> None:
    # As a scan with a scanner's watermark: some text, far less than a page.
    monkeypatch.setattr(doctext, "MIN_PAGE_CHARS", 1000)
    found = doctext.extract(fixture("letter.pdf"))
    assert "Contrat de location" in found.text and not found.has_text


def encrypted(user_password: str) -> bytes:
    writer = PdfWriter(clone_from=PdfReader(io.BytesIO(fixture("letter.pdf"))))
    writer.encrypt(user_password=user_password, owner_password="owner", algorithm="AES-256")
    out = io.BytesIO()
    writer.write(out)
    return out.getvalue()


def test_an_encrypted_pdf_without_password_is_read() -> None:
    found = doctext.extract(encrypted(""))
    assert found.source == "pdf" and "Contrat de location" in found.text


def test_a_pdf_that_needs_a_password_fails_for_good() -> None:
    with pytest.raises(tasks.Permanent, match="password"):
        doctext.extract(encrypted("secret"))


def zipped(base: str, part: str, xml: str) -> bytes:
    """A fixture whose part is replaced by xml."""
    out = io.BytesIO()
    with zipfile.ZipFile(io.BytesIO(fixture(base))) as src, zipfile.ZipFile(out, "w") as dst:
        for info in src.infolist():
            dst.writestr(info, xml.encode() if info.filename == part else src.read(info))
    return out.getvalue()


WORD = (
    'xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" '
    'xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006"'
)


def test_docx_tables_tracked_changes_and_text_boxes() -> None:
    body = f"""<w:document {WORD}><w:body>
      <w:p><w:pPr><w:tabs><w:tab w:val="left" w:pos="720"/></w:tabs></w:pPr>
        <w:r><w:t>Loyer</w:t><w:tab/><w:t xml:space="preserve">850 €</w:t></w:r>
        <w:del><w:r><w:delText>900 €</w:delText></w:r></w:del>
        <w:r><w:fldChar/><w:instrText>PAGE</w:instrText></w:r></w:p>
      <w:tbl>
        <w:tr><w:tc><w:p><w:r><w:t>Dépôt</w:t></w:r></w:p>
                    <w:p><w:r><w:t>de garantie</w:t></w:r></w:p></w:tc>
              <w:tc><w:p><w:r><w:t>850 €</w:t></w:r></w:p></w:tc></w:tr>
        <w:tr><w:tc><w:p><w:r><w:t>Durée</w:t></w:r></w:p></w:tc>
              <w:tc><w:p><w:r><w:t>3 ans</w:t></w:r></w:p></w:tc></w:tr>
      </w:tbl>
      <w:p><w:r><mc:AlternateContent>
        <mc:Choice><w:txbxContent><w:p><w:r><w:t>Encadré</w:t></w:r></w:p></w:txbxContent></mc:Choice>
        <mc:Fallback><w:txbxContent><w:p><w:r><w:t>Encadré</w:t></w:r></w:p></w:txbxContent></mc:Fallback>
      </mc:AlternateContent></w:r></w:p>
    </w:body></w:document>"""
    found = doctext.extract(zipped("letter.docx", "word/document.xml", body))
    assert found.text == "Loyer\t850 €\nDépôt de garantie\t850 €\nDurée\t3 ans\nEncadré"


ODF = (
    'xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" '
    'xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0" '
    'xmlns:dc="http://purl.org/dc/elements/1.1/"'
)


def test_odf_spaces_tracked_changes_and_comments() -> None:
    content = f"""<office:document-content {ODF}><office:body><office:text>
      <text:tracked-changes><text:changed-region><text:deletion>
        <text:p>Ancien loyer : 900 €</text:p>
      </text:deletion></text:changed-region></text:tracked-changes>
      <text:h>Bail</text:h>
      <text:p>Loyer :<text:s text:c="3"/>850 €<text:tab/>payable<text:line-break/>le 5
        <office:annotation><dc:creator>x</dc:creator><text:p>à vérifier</text:p>
        </office:annotation>du mois.</text:p>
    </office:text></office:body></office:document-content>"""
    found = doctext.extract(zipped("letter.odt", "content.xml", content))
    assert found.text == "Bail\nLoyer :   850 €\tpayable\nle 5 du mois."


def test_the_text_is_cut_at_max_chars(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(doctext, "MAX_CHARS", 40)
    for data in (fixture("letter.docx"), fixture("budget.xlsx"), "é" * 1000):
        found = doctext.extract(data if isinstance(data, bytes) else data.encode())
        assert found.truncated and len(found.text) == 40 and found.has_text


def test_past_the_deadline_what_was_read_is_kept() -> None:
    found = doctext.extract(fixture("letter.pdf"), deadline=time.monotonic() - 1)
    assert found.truncated and found.pages == 2
    assert found.text.startswith("Contrat de location") and "État des lieux" not in found.text


def a_zip(**parts: str) -> bytes:
    out = io.BytesIO()
    with zipfile.ZipFile(out, "w") as z:
        for name, text in parts.items():
            z.writestr(name, text)
    return out.getvalue()


@pytest.mark.parametrize(
    ("data", "why"),
    [
        (b"\x00\x01\x02 binary \x00\xff", "binary content"),
        (fixture("letter.docx")[:2000], "unreadable document"),
        (fixture("letter.pdf")[:3000], "unreadable pdf"),
        (b"%PDF-1.7\n" + b"\x00garbage\xff" * 50, "unreadable pdf"),
        (a_zip(**{"notes.txt": "hello"}), "not a document: a zip archive"),
        (a_zip(mimetype="application/epub+zip"), "not a document: application/epub+zip"),
        (zipped("letter.odt", "content.xml", "<office:document-content"), "unreadable"),
    ],
    ids=["binary", "truncated docx", "truncated pdf", "garbage pdf", "zip", "epub", "broken xml"],
)
def test_broken_documents_fail_for_good(data: bytes, why: str) -> None:
    with pytest.raises(tasks.Permanent, match=re.escape(why)):
        doctext.extract(data)


def test_entity_expansion_is_refused() -> None:
    # Each entity is ten of the previous one: &h; would be 100 million characters.
    chain = zip("abcdefg", "bcdefgh", strict=True)
    bomb = (
        '<?xml version="1.0"?><!DOCTYPE d [<!ENTITY a "aaaaaaaaaa">'
        + "".join(f'<!ENTITY {n} "{("&" + p + ";") * 10}">' for p, n in chain)
        + f"]><office:document-content {ODF}><text:p>&h;</text:p></office:document-content>"
    )
    with pytest.raises(tasks.Permanent, match="unreadable"):
        doctext.extract(zipped("letter.odt", "content.xml", bomb))


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


def run(conn: psycopg.Connection, sha: bytes, priority: int = 0) -> tuple[str, str | None]:
    """Run the text task of a content. The text tasks only: the vectors and
    the OCR it queues (test_textemb.py, test_ocr.py) are left queued."""
    task_id = tasks.enqueue(conn, doctext.KIND, {"sha256": sha.hex()}, priority=priority)
    assert tasks.run_one(conn, "w", {doctext.KIND: doctext.make})
    row = conn.execute("SELECT state, last_error FROM tasks WHERE id = %s", (task_id,)).fetchone()
    assert row
    return row[0], row[1]


OOXML = "application/vnd.openxmlformats-officedocument."
OD = "application/vnd.oasis.opendocument."
# path: (fixture, MIME as storage gives it, source, pages, has_text)
DOCUMENTS = {
    "Contrats/bail.pdf": ("letter.pdf", "application/pdf", "pdf", 2, True),
    "Contrats/bail.docx": ("letter.docx", OOXML + "wordprocessingml.document", "docx", None, True),
    "Contrats/bail.odt": ("letter.odt", OD + "text", "odt", None, True),
    "Comptes/budget.xlsx": ("budget.xlsx", OOXML + "spreadsheetml.sheet", "xlsx", 2, True),
    "Comptes/budget.ods": ("budget.ods", OD + "spreadsheet", "ods", 2, True),
    "Projets/slides.pptx": ("slides.pptx", OOXML + "presentationml.presentation", "pptx", 2, True),
    "Projets/slides.odp": ("slides.odp", OD + "presentation", "odp", 2, True),
    "Scans/facture.pdf": ("scan.pdf", "application/pdf", "pdf", 1, False),
}


def test_task_writes_the_text_and_never_the_file(conn: psycopg.Connection, root: Path) -> None:
    shas = {p: store(conn, root, p, fixture(f), mime) for p, (f, mime, *_) in DOCUMENTS.items()}
    notes = store(conn, root, "notes.md", b"# Courses\n- lait\n", "text/markdown")
    before = read_only(root)
    for path, sha in shas.items():
        assert run(conn, sha) == ("done", None), path
        _, _, source, pages, has_text = DOCUMENTS[path]
        row = conn.execute(
            """SELECT source, text, chars, truncated, pages, has_text FROM document_text
               WHERE sha256 = %s""",
            (sha,),
        ).fetchone()
        assert row is not None, path
        assert (row[0], row[2], row[3], row[4], row[5]) == (
            source,
            len(row[1]),
            False,
            pages,
            has_text,
        ), path
    assert run(conn, notes) == ("done", None)
    row = conn.execute("SELECT source, text FROM document_text WHERE sha256 = %s", (notes,))
    assert row.fetchone() == ("text", "# Courses\n- lait")
    # The originals are untouched, and nothing was written next to them.
    after = {p: (hashlib.sha256(p.read_bytes()).digest(), p.stat().st_mtime_ns) for p in before}
    assert after == before
    assert sorted(p for p in root.rglob("*") if p.is_file()) == sorted(before)
    # Running it again is a no-op.
    assert run(conn, shas["Contrats/bail.pdf"]) == ("done", None)


def test_a_text_with_words_queues_its_vectors_at_the_priority_of_its_task(
    conn: psycopg.Connection, root: Path
) -> None:
    new = store(conn, root, "notes.md", b"# Courses\n- lait\n", "text/markdown")
    old = store(conn, root, "Contrats/bail.pdf", fixture("letter.pdf"), "application/pdf")
    scan = store(conn, root, "Scans/facture.pdf", fixture("scan.pdf"), "application/pdf")
    read_only(root)
    assert run(conn, new, priority=10) == ("done", None)
    assert run(conn, old, priority=0) == ("done", None)
    assert run(conn, scan, priority=10) == ("done", None)
    queued = conn.execute(
        "SELECT payload->>'sha256', priority, dedupe_key, state FROM tasks WHERE kind = 'textemb'"
        " ORDER BY id"
    ).fetchall()
    # A new file stays new, a backfill stays backfill; a scan waits for its OCR.
    assert queued == [
        (new.hex(), 10, f"textemb:{new.hex()}", "queued"),
        (old.hex(), 0, f"textemb:{old.hex()}", "queued"),
    ]
    # A text already there queues nothing more.
    conn.execute("UPDATE tasks SET state = 'done' WHERE kind = 'textemb'")
    assert run(conn, new, priority=10) == ("done", None)
    assert conn.execute("SELECT count(*) FROM tasks WHERE kind = 'textemb'").fetchone() == (2,)


def test_unreadable_document_fails_without_blocking_the_queue(
    conn: psycopg.Connection, root: Path
) -> None:
    bad = store(conn, root, "bad.docx", fixture("letter.docx")[:2000], "application/x")
    good = store(conn, root, "good.pdf", fixture("letter.pdf"), "application/pdf")
    read_only(root)
    state, error = run(conn, bad)
    assert state == "failed" and error and "unreadable document" in error
    attempts = conn.execute("SELECT attempts FROM tasks WHERE state = 'failed'").fetchone()
    assert attempts == (1,)
    assert conn.execute("SELECT count(*) FROM document_text").fetchone() == (0,)
    assert run(conn, good) == ("done", None)


def test_a_reader_too_slow_fails_for_good(
    conn: psycopg.Connection, root: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setattr(doctext, "TIMEOUT", 0.5)
    monkeypatch.setattr(doctext, "COMMAND", [sys.executable, "-c", "import time; time.sleep(5)"])
    sha = store(conn, root, "slow.pdf", fixture("letter.pdf"), "application/pdf")
    state, error = run(conn, sha)
    assert state == "failed" and error and "took over 0.5 s" in error


def test_a_reader_that_cannot_start_is_retried(
    conn: psycopg.Connection, root: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    command = [sys.executable, "-c", "import sys; sys.exit('no module named pypdf')"]
    monkeypatch.setattr(doctext, "COMMAND", command)
    sha = store(conn, root, "a.pdf", fixture("letter.pdf"), "application/pdf")
    state, error = run(conn, sha)
    assert state == "queued" and error and "document reader failed: no module named pypdf" in error


def test_nothing_to_do_once_no_live_file_has_the_content(
    conn: psycopg.Connection, root: Path
) -> None:
    sha = hashlib.sha256(b"deleted").digest()
    assert run(conn, sha) == ("done", None)
    assert conn.execute("SELECT count(*) FROM document_text").fetchone() == (0,)


def test_running_out_of_memory_is_unreadable(
    monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    # In the child, past MEMORY, Python raises MemoryError: the document fails for good.
    def greedy(data: bytes, deadline: float | None = None) -> doctext.Extracted:
        raise MemoryError

    monkeypatch.setattr(doctext, "extract", greedy)
    monkeypatch.setattr(doctext.resource, "setrlimit", lambda *_: None)  # not on pytest itself
    monkeypatch.setattr(sys, "stdin", io.TextIOWrapper(io.BytesIO(b"%PDF-1.7")))
    assert doctext.main() == doctext.UNREADABLE
    assert "unreadable document: MemoryError()" in capsys.readouterr().err
