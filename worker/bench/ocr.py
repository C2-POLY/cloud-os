"""OCR benchmark: draw each document of the corpus as a scanned page and as a
phone photo of a page, read them back with each candidate, and measure the
character error rate (CER) against the text drawn, the time per page and the
memory. Then search the text read: does « contrat de l'appart » still find
the lease once it went through the OCR?

    uv run --group bench python -m bench.ocr [--font DejaVuSans.ttf] [--json out.json]

Runs best in the Linux container of worker/bench/Dockerfile, which has
Tesseract and its French model from Debian and the DejaVu fonts.
"""

from __future__ import annotations

import argparse
import io
import json
import os
import random
import resource
import subprocess
import sys
import tempfile
import time
import tomllib
import unicodedata
from pathlib import Path

import numpy as np
from PIL import Image, ImageDraw, ImageFilter, ImageFont

from bench.__main__ import FIXTURES, MODELS, TEXT, peak_rss_mb, score
from worker.models import TextEncoder

FONTS = ["/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf", "/Library/Fonts/DejaVuSans.ttf"]
PAGE = (1240, 1754)  # A4 at 150 dpi
MARGIN = 110
FONT_SIZE = 22  # 10.5 pt at 150 dpi
LINE = 31
PPOCR = MODELS / "ppocr"

ENGINES = {
    "tesseract-5-fra": None,
    # Sauvola: a local threshold, for pages lit unevenly (a phone photo).
    "tesseract-5-fra-sauvola": None,
    "pp-ocrv6-small": ("PP-OCRv6_det_small.onnx", "PP-OCRv6_rec_small.onnx"),
    "pp-ocrv6-tiny": ("PP-OCRv6_det_tiny.onnx", "PP-OCRv6_rec_tiny.onnx"),
    "pp-ocrv5-latin-mobile": ("ch_PP-OCRv5_det_mobile.onnx", "latin_PP-OCRv5_rec_mobile.onnx"),
}


def wrap(text: str, font: ImageFont.FreeTypeFont, width: int) -> list[str]:
    lines = []
    for para in text.splitlines():
        words, cur = para.split(), ""
        for w in words:
            trial = f"{cur} {w}".strip()
            if font.getlength(trial) <= width:
                cur = trial
            else:
                lines.append(cur)
                cur = w
        lines.append(cur)
    return lines


def render(text: str, font_path: str) -> tuple[Image.Image, str]:
    """The text drawn on white A4 pages stacked into one tall page, and the
    text exactly as drawn (line breaks where they fell)."""
    font = ImageFont.truetype(font_path, FONT_SIZE)
    lines = wrap(text, font, PAGE[0] - 2 * MARGIN)
    height = max(PAGE[1], 2 * MARGIN + LINE * len(lines))
    page = Image.new("L", (PAGE[0], height), 255)
    draw = ImageDraw.Draw(page)
    for i, line in enumerate(lines):
        draw.text((MARGIN, MARGIN + i * LINE), line, font=font, fill=20)
    return page, "\n".join(lines)


def as_scan(page: Image.Image, rng: random.Random) -> Image.Image:
    """A flatbed scan: a slight tilt, a little blur and grain, JPEG."""
    im = page.rotate(rng.uniform(-1.0, 1.0), resample=Image.Resampling.BICUBIC, fillcolor=255)
    im = im.filter(ImageFilter.GaussianBlur(0.6))
    arr = np.asarray(im, dtype=np.float32)
    arr += np.random.default_rng(rng.randrange(1 << 30)).normal(0, 9, arr.shape)
    return jpeg(Image.fromarray(arr.clip(0, 255).astype(np.uint8)), 70)


def as_photo(page: Image.Image, rng: random.Random) -> Image.Image:
    """A phone photo of the page: seen a little from below, uneven light,
    lower resolution, in colour, JPEG."""
    w, h = page.size
    d = w * rng.uniform(0.03, 0.06)
    # Keystone: the top edge narrower than the bottom one.
    quad = (-d, 0, -d * 0.2, h, w + d * 0.2, h, w + d, 0)
    im = page.transform(
        page.size, Image.Transform.QUAD, quad, Image.Resampling.BICUBIC, fillcolor=235
    )
    light = (
        np.linspace(0.62, 1.0, w, dtype=np.float32)[None, :]
        * np.linspace(0.85, 1.0, h, dtype=np.float32)[:, None]
    )
    arr = np.asarray(im, dtype=np.float32) * light
    rgb = np.stack([arr * 1.0, arr * 0.97, arr * 0.9], axis=-1)
    im = Image.fromarray(rgb.clip(0, 255).astype(np.uint8)).filter(ImageFilter.GaussianBlur(1.0))
    im = im.resize((int(w * 0.8), int(h * 0.8)), Image.Resampling.BILINEAR)
    return jpeg(im, 80)


def jpeg(im: Image.Image, quality: int) -> Image.Image:
    buf = io.BytesIO()
    im.save(buf, "JPEG", quality=quality)
    buf.seek(0)
    return Image.open(buf).copy()


def levenshtein(a: str, b: str) -> int:
    """Edit distance, one row at a time with numpy (insertions through a
    running minimum), fast enough for pages of a few thousand characters."""
    if len(a) < len(b):
        a, b = b, a
    if not b:
        return len(a)
    bb = np.array([ord(c) for c in b], dtype=np.int64)
    idx = np.arange(len(b) + 1, dtype=np.int64)
    prev = idx.copy()
    for i, ch in enumerate(a, 1):
        cur = np.empty_like(prev)
        cur[0] = i
        cur[1:] = np.minimum(prev[1:] + 1, prev[:-1] + (bb != ord(ch)))
        prev = np.minimum.accumulate(cur - idx) + idx
    return int(prev[-1])


def flat(text: str, loose: bool = False) -> str:
    """Whitespace collapsed; loose also drops accents and case."""
    text = " ".join(unicodedata.normalize("NFC", text).split())
    if loose:
        text = "".join(
            c for c in unicodedata.normalize("NFD", text.lower()) if not unicodedata.combining(c)
        )
    return text


def cer(truth: str, read: str, loose: bool = False) -> float:
    t = flat(truth, loose)
    return levenshtein(t, flat(read, loose)) / max(len(t), 1)


def tesseract(path: Path, n_threads: int, sauvola: bool = False) -> str:
    env = {**os.environ, "OMP_THREAD_LIMIT": str(n_threads)}
    cmd = ["tesseract", str(path), "-", "-l", "fra", "--psm", "3"]
    if sauvola:
        cmd += ["-c", "thresholding_method=2"]
    return subprocess.run(cmd, check=True, capture_output=True, text=True, env=env).stdout


def ppocr_engine(det: str, rec: str, n_threads: int):
    from rapidocr import RapidOCR

    return RapidOCR(
        params={
            "Global.log_level": "error",
            "Global.use_cls": False,
            "Det.model_path": str(PPOCR / det),
            "Rec.model_path": str(PPOCR / rec),
            "EngineConfig.onnxruntime.intra_op_num_threads": n_threads,
            "EngineConfig.onnxruntime.inter_op_num_threads": 1,
        }
    )


def ppocr_text(engine, path: Path) -> str:
    """The boxes read, put back in reading order: rows by height, then left to right."""
    out = engine(str(path))
    if out.txts is None:
        return ""
    boxes = [
        (b[:, 1].min(), b[:, 1].max(), b[:, 0].min(), t)
        for b, t in zip(out.boxes, out.txts, strict=True)
    ]
    boxes.sort()
    rows: list[list] = []
    for top, bottom, left, text in boxes:
        mid = (top + bottom) / 2
        if rows and abs(mid - rows[-1][0]) < (bottom - top) * 0.5:
            rows[-1][1].append((left, text))
        else:
            rows.append([mid, [(left, text)]])
    return "\n".join(" ".join(t for _, t in sorted(r[1])) for r in rows)


def pages(font: str, folder: Path) -> list[dict]:
    """Draw every document twice (scan, photo) into folder; their ground truth."""
    out = []
    for doc in sorted((FIXTURES / "docs").glob("*.txt")):
        page, truth = render(doc.read_text(), font)
        rng = random.Random(doc.stem)
        for kind, fn in (("scan", as_scan), ("photo", as_photo)):
            path = folder / f"{doc.stem}.{kind}.jpg"
            fn(page, rng).save(path, quality=95)
            out.append({"doc": doc.stem, "kind": kind, "path": path, "truth": truth})
    return out


def run_engine(name: str, items: list[dict], n_threads: int) -> dict:
    spec = ENGINES[name]
    if spec is None:
        sauvola = name.endswith("sauvola")
        read = lambda p: tesseract(p, n_threads, sauvola)  # noqa: E731
        version = subprocess.run(["tesseract", "--version"], capture_output=True, text=True)
        label = version.stdout.splitlines()[0] if version.stdout else "tesseract"
        label += " fra, Sauvola" if sauvola else " fra"
    else:
        engine = ppocr_engine(*spec, n_threads)
        read = lambda p: ppocr_text(engine, p)  # noqa: E731
        label = name
    read(items[0]["path"])  # warm-up
    texts, seconds = {}, {"scan": [], "photo": []}
    for it in items:
        start = time.perf_counter()
        texts[(it["doc"], it["kind"])] = read(it["path"])
        seconds[it["kind"]].append(time.perf_counter() - start)
    result: dict = {"name": name, "label": label}
    for kind in ("scan", "photo"):
        subset = [it for it in items if it["kind"] == kind]
        result[f"cer_{kind}"] = float(
            np.mean([cer(it["truth"], texts[(it["doc"], kind)]) for it in subset])
        )
        result[f"cer_loose_{kind}"] = float(
            np.mean([cer(it["truth"], texts[(it["doc"], kind)], loose=True) for it in subset])
        )
        result[f"s_per_page_{kind}"] = float(np.mean(seconds[kind]))
    if spec is None:  # the peak of the tesseract processes themselves
        child = resource.getrusage(resource.RUSAGE_CHILDREN).ru_maxrss
        result["rss_mb"] = child / 2**20 if sys.platform == "darwin" else child / 2**10
        result["size_mb"] = tessdata_size()
    else:
        result["rss_mb"] = peak_rss_mb()
        result["size_mb"] = sum((PPOCR / f).stat().st_size for f in spec) / 1e6
    result["texts"] = {f"{d}.{k}": t for (d, k), t in texts.items()}
    return result


def tessdata_size() -> float:
    out = subprocess.run(["tesseract", "--list-langs"], capture_output=True, text=True)
    folder = (
        out.stdout.split('"')[1] if '"' in out.stdout else "/usr/share/tesseract-ocr/5/tessdata"
    )
    return sum(f.stat().st_size for f in Path(folder).glob("*.traineddata")) / 1e6


def search_after_ocr(results: list[dict], text_model: str) -> None:
    """recall@1 of the text queries over the documents as each engine read them."""
    queries = tomllib.loads((FIXTURES / "queries.toml").read_text())["text"]
    folder, spec = TEXT[text_model]
    enc = TextEncoder(MODELS / folder, spec, 4)
    qv = enc.embed([q["q"] for q in queries], query=True)
    names = sorted(d.stem for d in (FIXTURES / "docs").glob("*.txt"))
    for r in results:
        for kind in ("scan", "photo"):
            docs = enc.embed([r["texts"][f"{n}.{kind}"] for n in names])
            s = score(qv @ docs.T, names, queries)
            r[f"recall@1_{kind}"] = s["recall@1"]
            r[f"misses_{kind}"] = s["misses"]


def table(results: list[dict]) -> str:
    lines = [
        "| OCR | CER scan | CER photo | CER sans accents ni casse (scan / photo) "
        "| s/page scan | s/page photo | Modèles | RAM (pic) "
        "| recall@1 après OCR (scan / photo) |",
        "|---|---|---|---|---|---|---|---|---|",
    ]
    for r in results:
        lines.append(
            f"| {r['label']} | {r['cer_scan']:.1%} | {r['cer_photo']:.1%} "
            f"| {r['cer_loose_scan']:.1%} / {r['cer_loose_photo']:.1%} "
            f"| {r['s_per_page_scan']:.2f} | {r['s_per_page_photo']:.2f} | {r['size_mb']:.0f} Mo "
            f"| {r['rss_mb']:.0f} Mo "
            f"| {r.get('recall@1_scan', 0):.2f} / {r.get('recall@1_photo', 0):.2f} |"
        )
    return "\n".join(lines)


def main() -> None:
    parser = argparse.ArgumentParser(prog="python -m bench.ocr")
    parser.add_argument("engines", nargs="*", default=list(ENGINES))
    parser.add_argument("--font", default=next((f for f in FONTS if Path(f).exists()), None))
    parser.add_argument("--threads", type=int, default=4)
    parser.add_argument("--text-model", default="arctic-embed-m-v2.0 int8")
    parser.add_argument("--only", help="run one engine in this process, print JSON")
    parser.add_argument("--pages", type=Path, help="folder of pages already drawn (with --only)")
    parser.add_argument("--keep", type=Path, help="keep the pages drawn in this folder")
    parser.add_argument("--json", type=Path)
    args = parser.parse_args()
    if not args.font:
        raise SystemExit("no DejaVu Sans: pass --font, or run in worker/bench/Dockerfile")
    if args.only:
        items = json.loads((args.pages / "pages.json").read_text())
        for it in items:
            it["path"] = Path(it["path"])
        print(json.dumps(run_engine(args.only, items, args.threads), ensure_ascii=False))
        return
    with tempfile.TemporaryDirectory() as tmp:
        folder = args.keep or Path(tmp)
        folder.mkdir(parents=True, exist_ok=True)
        items = pages(args.font, folder)
        (folder / "pages.json").write_text(
            json.dumps([{**it, "path": str(it["path"])} for it in items], ensure_ascii=False)
        )
        results = []
        for name in args.engines:
            cmd = [sys.executable, "-m", "bench.ocr", "--only", name, "--pages", str(folder)]
            cmd += ["--font", args.font, "--threads", str(args.threads)]
            out = subprocess.run(cmd, check=True, capture_output=True, text=True).stdout
            r = json.loads(out.strip().splitlines()[-1])
            results.append(r)
            print(
                f"{name}: CER scan {r['cer_scan']:.1%}, photo {r['cer_photo']:.1%}", file=sys.stderr
            )
    search_after_ocr(results, args.text_model)
    for r in results:
        for kind in ("scan", "photo"):
            for m in r[f"misses_{kind}"]:
                print(f"    {r['name']} {kind}: {m}", file=sys.stderr)
    print(table(results))
    if args.json:
        args.json.write_text(json.dumps(results, ensure_ascii=False, indent=1))


if __name__ == "__main__":
    main()
