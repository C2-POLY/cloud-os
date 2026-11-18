"""Text of documents (PDF, Office, OpenDocument, plain text), for
core/internal/doctext.

The worker reads a content from the data root, read-only, checks it against
its hash, and hands its bytes to a child process (`python -m worker.doctext`)
that reads them on stdin and answers JSON on stdout, within TIMEOUT seconds
and MEMORY bytes: a booby-trapped document fails for good instead of holding
the worker and its task. The text goes to document_text, keyed by the
content's SHA-256: nothing is ever written to a file. A text with words
queues its vectors (worker.textemb) in the same transaction.

The format is recognised by the content, not by the name: a PDF by its
header, an Office (OOXML) or OpenDocument file by the parts of its zip, any
other content as plain text. PDF is read with pypdf (BSD-3-Clause), with
cryptography (Apache-2.0 OR BSD-3-Clause) for the PDFs encrypted with AES and
an empty user password; the zip formats with the standard library, streamed
part by part (expat refuses entity expansion bombs). Pages, slides and sheets
are separated by a form feed (\\f).
"""

from __future__ import annotations

import codecs
import contextlib
import io
import json
import logging
import posixpath
import re
import resource
import subprocess
import sys
import time
import xml.etree.ElementTree as ET
import zipfile
from collections.abc import Iterator
from dataclasses import asdict, dataclass
from typing import IO, TYPE_CHECKING

import psycopg

from worker.content import read_content, sha_of
from worker.errors import Permanent

if TYPE_CHECKING:
    from worker.tasks import Task

KIND = "text"
MAX_CHARS = 1_000_000  # characters kept, as the CHECK of document_text.chars
MIN_PAGE_CHARS = 32  # visible characters per page below which a PDF has no text: a scan
TIMEOUT = 60  # seconds for one child process
SOFT_DEADLINE = 45  # seconds after which the child stops reading and keeps what it has
MEMORY = 2 * 1024**3  # address space of one child process, in bytes
COMMAND = [sys.executable, "-I", "-m", "worker.doctext"]
UNREADABLE = 3  # exit status of the child for a document it cannot read
PAGE = "\n\f\n"  # between pages, slides, sheets

SOURCES = ("pdf", "docx", "xlsx", "pptx", "odt", "ods", "odp", "text")
ODF = {
    "application/vnd.oasis.opendocument.text": "odt",
    "application/vnd.oasis.opendocument.spreadsheet": "ods",
    "application/vnd.oasis.opendocument.presentation": "odp",
}


@dataclass
class Extracted:
    source: str  # one of SOURCES
    text: str
    truncated: bool = False
    pages: int | None = None  # pages of a PDF, slides, sheets
    has_text: bool = False


class _Full(Exception):
    """The text reached MAX_CHARS, or the reading its deadline: stop reading."""


class _Text:
    """The text being read, bounded in size and in time."""

    def __init__(self, deadline: float | None) -> None:
        self.parts: list[str] = []
        self.size = 0
        self.deadline = deadline
        self.truncated = False

    def add(self, s: str | None) -> None:
        if not s:
            return
        self.parts.append(s)
        self.size += len(s)
        if self.size > MAX_CHARS or (
            self.deadline is not None and time.monotonic() > self.deadline
        ):
            self.truncated = True
            raise _Full

    def value(self) -> str:
        return "".join(self.parts)


def clean(text: str) -> str:
    """Text fit for Postgres and for reading: no NUL (Postgres refuses it), no
    lone surrogate, Unix line ends, no trailing spaces, page breaks on a line
    of their own, at most one blank line in a row, cut to MAX_CHARS."""
    text = text.replace("\x00", "").encode("utf-8", "replace").decode("utf-8")
    text = text.replace("\r\n", "\n").replace("\r", "\n")
    text = re.sub(r"\s*\f\s*", PAGE, text)
    lines = [line.rstrip(" \t") for line in text.split("\n")]
    text = re.sub(r"\n{3,}", "\n\n", "\n".join(lines)).strip()
    return text[:MAX_CHARS]


def visible(text: str) -> int:
    return len(re.sub(r"\s+", "", text))


# Plain text.

TEXT_BYTES = 4 * MAX_CHARS + 4  # enough bytes for MAX_CHARS characters of UTF-8


def read_plain(data: bytes, out: _Text) -> None:
    """UTF-8 (with or without BOM), UTF-16 with BOM, else Windows-1252.
    Raises Permanent for a binary content (NUL bytes)."""
    cut = len(data) > TEXT_BYTES
    head = data[:TEXT_BYTES]
    if head.startswith((codecs.BOM_UTF16_LE, codecs.BOM_UTF16_BE)):
        text = codecs.getincrementaldecoder("utf-16")("replace").decode(head, final=not cut)
    else:
        if b"\x00" in head[:8192]:
            raise Permanent("not a text document: binary content")
        try:
            text = codecs.getincrementaldecoder("utf-8-sig")().decode(head, final=not cut)
        except UnicodeDecodeError:
            text = head.decode("cp1252", "replace")
    out.truncated = cut
    with contextlib.suppress(_Full):
        out.add(text)


# PDF.


def read_pdf(data: bytes, out: _Text) -> tuple[int, int]:
    """The text of each page, and (pages, pages read). Raises Permanent for a
    PDF that needs a password."""
    from pypdf import PasswordType, PdfReader

    logging.getLogger("pypdf").setLevel(logging.ERROR)
    reader = PdfReader(io.BytesIO(data))
    if reader.is_encrypted and reader.decrypt("") == PasswordType.NOT_DECRYPTED:
        raise Permanent("unreadable pdf: a password is needed to open it")
    pages = len(reader.pages)
    done = 0
    try:
        for i, page in enumerate(reader.pages):
            if i:
                out.add(PAGE)
            out.add(page.extract_text())
            done += 1
    except _Full:
        pass
    return pages, done


# Office (OOXML) and OpenDocument: zip archives of XML parts.

W = "{http://schemas.openxmlformats.org/wordprocessingml/2006/main}"
A = "{http://schemas.openxmlformats.org/drawingml/2006/main}"
S = "{http://schemas.openxmlformats.org/spreadsheetml/2006/main}"
P = "{http://schemas.openxmlformats.org/presentationml/2006/main}"
R = "{http://schemas.openxmlformats.org/officeDocument/2006/relationships}"
REL = "{http://schemas.openxmlformats.org/package/2006/relationships}Relationship"
CT = "{http://schemas.openxmlformats.org/package/2006/content-types}"
# Markup compatibility: a Fallback repeats its Choice (text boxes), read once.
FALLBACK = "{http://schemas.openxmlformats.org/markup-compatibility/2006}Fallback"

T = "{urn:oasis:names:tc:opendocument:xmlns:text:1.0}"
TB = "{urn:oasis:names:tc:opendocument:xmlns:table:1.0}"
D = "{urn:oasis:names:tc:opendocument:xmlns:drawing:1.0}"
OF = "{urn:oasis:names:tc:opendocument:xmlns:office:1.0}"
BLANKS = re.compile(r"[ \t\r\n]+")


def _events(fp: IO[bytes]) -> Iterator[tuple[str, ET.Element, ET.Element | None]]:
    """Stream an XML part: ("start", element, None) and ("end", element,
    parent). The consumer drops what it has read (_drop), so memory stays
    bounded whatever the size of the part."""
    stack: list[ET.Element] = []
    for event, elem in ET.iterparse(fp, events=("start", "end")):
        if event == "start":
            stack.append(elem)
            yield "start", elem, None
        else:
            stack.pop()
            yield "end", elem, stack[-1] if stack else None


def _drop(elem: ET.Element, parent: ET.Element | None) -> None:
    elem.clear()
    if parent is not None:
        parent.remove(elem)


def _rels(zf: zipfile.ZipFile, part: str) -> dict[str, tuple[str, str]]:
    """The relationships of a part: id -> (type, part name)."""
    folder, name = posixpath.split(part)
    rels = posixpath.join(folder, "_rels", name + ".rels")
    if rels not in zf.NameToInfo:
        return {}
    found = {}
    for rel in ET.fromstring(zf.read(rels)).iter(REL):
        target = rel.get("Target", "")
        if rel.get("TargetMode") == "External" or not target:
            continue
        path = target.lstrip("/") if target.startswith("/") else posixpath.join(folder, target)
        found[rel.get("Id", "")] = (rel.get("Type", ""), posixpath.normpath(path))
    return found


def _ooxml(zf: zipfile.ZipFile) -> tuple[str, str] | None:
    """(source, main part) of an Office document, from its relationships and
    content types; None when the zip is not one."""
    main = next((p for t, p in _rels(zf, "").values() if t.endswith("/officeDocument")), None)
    if main is None or "[Content_Types].xml" not in zf.NameToInfo:
        return None
    types = {
        o.get("PartName", "").lstrip("/"): o.get("ContentType", "")
        for o in ET.fromstring(zf.read("[Content_Types].xml")).iter(CT + "Override")
    }
    kind = types.get(main, "")
    for marks, source in (
        (("wordprocessingml", "ms-word"), "docx"),
        (("spreadsheetml", "ms-excel"), "xlsx"),
        (("presentationml", "ms-powerpoint"), "pptx"),
    ):
        if any(m in kind for m in marks):
            return source, main
    return None


def read_docx(zf: zipfile.ZipFile, main: str, out: _Text) -> None:
    """Paragraphs of the body one per line, text boxes included; table cells
    separated by tabs, rows by lines. Deleted text (tracked changes) and field
    codes are not text."""
    run = skip = cell = 0
    started = False  # a paragraph of the current table cell was read
    with zf.open(main) as fp, contextlib.suppress(_Full):
        for event, elem, parent in _events(fp):
            tag = elem.tag
            if event == "start":
                if tag == W + "r":
                    run += 1
                elif tag == FALLBACK:
                    skip += 1
                elif tag == W + "tc":
                    cell += 1
                    started = False
                elif tag == W + "p" and cell and not skip:
                    if started:
                        out.add(" ")
                    started = True
                continue
            if tag == W + "r":
                run -= 1
            elif tag == FALLBACK:
                skip -= 1
                _drop(elem, parent)
            elif tag == W + "tc":
                cell -= 1
                if not skip:
                    out.add("\t")
                _drop(elem, parent)
            elif tag == W + "tr":
                if not skip:
                    out.add("\n")
                _drop(elem, parent)
            elif skip:
                continue
            elif tag == W + "t":
                out.add(elem.text)
            elif run and tag == W + "tab":  # not a tab stop of the paragraph
                out.add("\t")
            elif run and tag in (W + "br", W + "cr"):
                out.add("\n")
            elif tag == W + "noBreakHyphen":
                out.add("-")
            elif tag == W + "p":
                if not cell:
                    out.add("\n")
                _drop(elem, parent)


def read_pptx(zf: zipfile.ZipFile, main: str, out: _Text) -> int:
    """The text of each slide, in the presentation's order; the number of slides."""
    rels = _rels(zf, main)
    order = ET.fromstring(zf.read(main)).iter(P + "sldId")
    slides = [rels[s.get(R + "id", "")][1] for s in order if s.get(R + "id", "") in rels]
    with contextlib.suppress(_Full):
        _slides(zf, slides, out)
    return len(slides)


def _slides(zf: zipfile.ZipFile, slides: list[str], out: _Text) -> None:
    for n, slide in enumerate(slides):
        if n:
            out.add(PAGE)
        skip = 0
        with zf.open(slide) as fp:
            for event, elem, parent in _events(fp):
                if elem.tag == FALLBACK:
                    skip += 1 if event == "start" else -1
                elif event == "end" and not skip:
                    if elem.tag == A + "t":
                        out.add(elem.text)
                    elif elem.tag == A + "br":
                        out.add("\n")
                    elif elem.tag == A + "p":
                        out.add("\n")
                        _drop(elem, parent)


def _shared_strings(zf: zipfile.ZipFile, part: str | None) -> list[str]:
    """The shared strings of a workbook, without phonetic guides; past
    MAX_CHARS of them, the rest are empty."""
    strings: list[str] = []
    if part is None or part not in zf.NameToInfo:
        return strings
    size = 0
    with zf.open(part) as fp:
        for event, elem, parent in _events(fp):
            if event == "end" and elem.tag == S + "si":
                text = "".join(
                    t.text or ""
                    for child in elem
                    if child.tag in (S + "t", S + "r")
                    for t in child.iter(S + "t")
                )
                strings.append(text if size <= MAX_CHARS else "")
                size += len(text)
                _drop(elem, parent)
    return strings


def _cell(c: ET.Element, shared: list[str]) -> str:
    kind = c.get("t")
    if kind == "inlineStr":
        return "".join(t.text or "" for t in c.iter(S + "t"))
    v = c.find(S + "v")
    value = v.text if v is not None and v.text else ""
    if kind == "s":
        i = int(value) if value.isdigit() else -1
        return shared[i] if 0 <= i < len(shared) else ""
    if kind == "b":
        return "TRUE" if value == "1" else "FALSE"
    if kind == "e":  # #DIV/0! and the like
        return ""
    return value  # a number or a formula's text, as stored


def read_xlsx(zf: zipfile.ZipFile, main: str, out: _Text) -> int:
    """Each sheet, in the workbook's order: its name, then one line per row,
    cells separated by tabs; the number of sheets."""
    rels = _rels(zf, main)
    shared = next((p for t, p in rels.values() if t.endswith("/sharedStrings")), None)
    strings = _shared_strings(zf, shared)
    sheets = [
        (s.get("name", ""), rels[s.get(R + "id", "")][1])
        for s in ET.fromstring(zf.read(main)).iter(S + "sheet")
        if s.get(R + "id", "") in rels
    ]
    with contextlib.suppress(_Full):
        _sheets(zf, sheets, strings, out)
    return len(sheets)


def _sheets(
    zf: zipfile.ZipFile, sheets: list[tuple[str, str]], strings: list[str], out: _Text
) -> None:
    for n, (name, part) in enumerate(sheets):
        if n:
            out.add(PAGE)
        out.add(name + "\n")
        with zf.open(part) as fp:
            for event, elem, parent in _events(fp):
                if event == "end" and elem.tag == S + "row":
                    cells = [_cell(c, strings) for c in elem.iter(S + "c")]
                    out.add("\t".join(cells).rstrip("\t") + "\n")
                    _drop(elem, parent)


def _odf_text(elem: ET.Element, parts: list[str]) -> None:
    """The text of an OpenDocument paragraph: spans, links, spaces, tabs,
    line breaks; annotations (comments) left out. Blanks in the XML collapse
    to one space: only text:s, text:tab and text:line-break are kept as they
    are (ODF 1.3, 6.1.2)."""
    if elem.tag == OF + "annotation":
        return
    if elem.tag == T + "s":
        count = elem.get(T + "c", "1")
        parts.append(" " * min(int(count), 1000) if count.isdigit() else " ")
    elif elem.tag == T + "tab":
        parts.append("\t")
    elif elem.tag == T + "line-break":
        parts.append("\n")
    else:
        parts.append(BLANKS.sub(" ", elem.text or ""))
        for child in elem:
            _odf_text(child, parts)
            parts.append(BLANKS.sub(" ", child.tail or ""))


def read_odf(zf: zipfile.ZipFile, source: str, out: _Text) -> int | None:
    """Paragraphs and headings one per line; table cells separated by tabs,
    rows by lines; a spreadsheet's sheets and a presentation's slides
    separated by PAGE. Deleted text (tracked changes) is not text. The number
    of sheets or slides; None for a text document, or when the text was cut
    before the end (the rest is not counted)."""
    try:
        pages = _odf(zf, source, out)
    except _Full:
        return None
    return pages if source in ("ods", "odp") else None


def _odf(zf: zipfile.ZipFile, source: str, out: _Text) -> int:
    pages = 0
    skip = para = cell = table = 0
    started = False  # a paragraph of the current table cell was read
    with zf.open("content.xml") as fp:
        for event, elem, parent in _events(fp):
            tag = elem.tag
            if event == "start":
                if tag in (T + "tracked-changes", OF + "annotation"):
                    skip += 1
                elif tag in (T + "p", T + "h"):
                    para += 1
                elif tag in (TB + "table-cell", TB + "covered-table-cell"):
                    cell += 1
                    started = False
                elif tag == TB + "table":
                    table += 1
                    if source == "ods" and table == 1 and not skip:
                        if pages:
                            out.add(PAGE)
                        out.add(elem.get(TB + "name", "") + "\n")
                        pages += 1
                elif tag == D + "page" and source == "odp" and not skip:
                    if pages:
                        out.add(PAGE)
                    pages += 1
                continue
            if tag in (T + "tracked-changes", OF + "annotation"):
                skip -= 1
                if not para:  # inside a paragraph, its tail is the paragraph's text
                    _drop(elem, parent)
            elif tag in (T + "p", T + "h"):
                para -= 1
                if not para:
                    if not skip:
                        parts: list[str] = []
                        _odf_text(elem, parts)
                        text = "".join(parts).strip(" ")
                        out.add((" " if cell and started else "") + text + ("" if cell else "\n"))
                        started = True
                    _drop(elem, parent)
            elif tag in (TB + "table-cell", TB + "covered-table-cell"):
                cell -= 1
                if not skip:
                    out.add("\t")
                _drop(elem, parent)
            elif tag == TB + "table-row":
                if not skip:
                    out.add("\n")
                _drop(elem, parent)
            elif tag == TB + "table":
                table -= 1
            elif tag == D + "page":
                _drop(elem, parent)
    return pages


def read_zip(data: bytes, out: _Text) -> tuple[str, int | None]:
    """(source, pages) of an Office or OpenDocument file."""
    with zipfile.ZipFile(io.BytesIO(data)) as zf:
        if "mimetype" in zf.NameToInfo:
            mimetype = zf.read("mimetype")[:100].decode("ascii", "replace").strip()
            source = next((s for m, s in ODF.items() if mimetype.startswith(m)), None)
            if source is None:
                raise Permanent(f"not a document: {mimetype[:60]}")
            return source, read_odf(zf, source, out)
        found = _ooxml(zf)
        if found is None:
            raise Permanent("not a document: a zip archive")
        source, main = found
        if source == "docx":
            read_docx(zf, main, out)
            return source, None
        if source == "pptx":
            return source, read_pptx(zf, main, out)
        return source, read_xlsx(zf, main, out)


def source_of(data: bytes) -> str:
    """The family of a content: pdf, zip (Office or OpenDocument), or text. A
    PDF starts with its header, or has it in its first 1024 bytes and ends
    with %%EOF (a text quoting the header is still a text)."""
    at = data.find(b"%PDF-", 0, 1024)
    if at == 0 or (at > 0 and b"%%EOF" in data[-1024:]):
        return "pdf"
    if data[:4] == b"PK\x03\x04":
        return "zip"
    return "text"


def extract(data: bytes, deadline: float | None = None) -> Extracted:
    """The text of a document, cleaned and cut to MAX_CHARS. Past the deadline
    (time.monotonic()), what was read is kept, marked truncated. Raises
    Permanent for a document that cannot be read."""
    out = _Text(deadline)
    family = source_of(data)
    try:
        if family == "pdf":
            pages, done = read_pdf(data, out)
            text = clean(out.value())
            enough = visible(text) >= MIN_PAGE_CHARS * done if done else False
            return Extracted("pdf", text, out.truncated, pages, enough or out.size > MAX_CHARS)
        if family == "zip":
            source, pages = read_zip(data, out)
        else:
            source, pages = "text", None
            read_plain(data, out)
    except Permanent:
        raise
    except Exception as err:  # a broken document: its parser's error, whatever it is
        name = "pdf" if family == "pdf" else "document"
        raise Permanent(f"unreadable {name}: {type(err).__name__}: {err}"[:500]) from err
    text = clean(out.value())
    return Extracted(source, text, out.truncated, pages, visible(text) > 0)


def read_text(data: bytes) -> Extracted:
    """extract(), run in a child process bounded by TIMEOUT and MEMORY.
    Raises Permanent when the document cannot be read in time, RuntimeError
    when the child cannot run (another attempt may)."""
    try:
        done = subprocess.run(
            COMMAND, input=data, capture_output=True, timeout=TIMEOUT, check=False
        )
    except subprocess.TimeoutExpired as err:
        raise Permanent(f"unreadable document: took over {TIMEOUT} s") from err
    why = (done.stderr.decode("utf-8", "replace").strip().splitlines() or ["no output"])[-1]
    if done.returncode == UNREADABLE:
        raise Permanent(why[:500])
    if done.returncode < 0:  # killed: out of memory, or worse
        raise Permanent(f"unreadable document: reader killed by signal {-done.returncode}")
    if done.returncode != 0:
        raise RuntimeError(f"document reader failed: {why[:500]}")
    found = Extracted(**json.loads(done.stdout))
    if found.source not in SOURCES:
        raise RuntimeError(f"document reader answered {found.source!r}")
    return found


def make(task: Task, conn: psycopg.Connection) -> None:
    """The text task: payload {"sha256": hex}."""
    sha = sha_of(task.payload)
    if conn.execute("SELECT 1 FROM document_text WHERE sha256 = %s", (sha,)).fetchone():
        return
    data = read_content(conn, sha, "document")
    if data is None:
        return
    found = read_text(data)
    # Imported here: the child process that reads documents (main) never
    # loads ONNX Runtime.
    from worker import ocr, textemb

    with conn.transaction():
        written = conn.execute(
            """INSERT INTO document_text (sha256, source, text, chars, truncated, pages, has_text)
               VALUES (%s, %s, %s, %s, %s, %s, %s) ON CONFLICT DO NOTHING RETURNING has_text""",
            (
                sha,
                found.source,
                found.text,
                len(found.text),
                found.truncated,
                found.pages,
                found.has_text,
            ),
        ).fetchone()
        if written and written[0]:
            # Its vectors, at the priority of this task: a new file stays new.
            textemb.enqueue(conn, sha, task.priority)
        if found.source == "pdf" and not found.has_text:  # a scan: the OCR reads it
            ocr.queue(conn, sha, task.priority)


def main() -> int:
    """The child process: a document's bytes on stdin, its text as JSON on stdout."""
    # macOS refuses RLIMIT_AS: there, the timeout alone bounds the run.
    with contextlib.suppress(ValueError, OSError):
        resource.setrlimit(resource.RLIMIT_AS, (MEMORY, MEMORY))
    data = sys.stdin.buffer.read()
    try:
        found = extract(data, time.monotonic() + SOFT_DEADLINE)
    except Exception as err:  # Permanent, or MemoryError past the limit
        why = str(err) if isinstance(err, Permanent) else f"unreadable document: {err!r}"
        print(why[:500], file=sys.stderr)
        return UNREADABLE
    json.dump(asdict(found), sys.stdout)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
