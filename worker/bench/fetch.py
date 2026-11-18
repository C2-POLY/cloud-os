"""Download the candidate models of the benchmark into a folder (default
.tools/bench-models at the repository root), each at a pinned Hugging Face
revision, and print the SHA-256 of every file:

    cd worker && uv run --group bench python -m bench.fetch [name …]

Only the benchmark uses these files; the models the worker keeps are fetched
by worker/models/fetch.sh. PP-OCR comes with the rapidocr package and
Tesseract from the system (worker/bench/ocr.py).
"""

from __future__ import annotations

import hashlib
import sys
import urllib.request
from pathlib import Path

OUT = Path(__file__).resolve().parents[2] / ".tools" / "bench-models"
UA = {"User-Agent": "cloud-os-bench/0.1"}

# name: (repository, revision, files). Licences: docs/decisions/005-modeles-locaux.md.
CANDIDATES: dict[str, tuple[str, str, list[str]]] = {
    "e5-small": (
        "Xenova/multilingual-e5-small",
        "761b726dd34fb83930e26aab4e9ac3899aa1fa78",
        ["onnx/model_quantized.onnx", "onnx/model.onnx", "tokenizer.json", "config.json"],
    ),
    "e5-base": (
        "Xenova/multilingual-e5-base",
        "1ec9243030a27d1a115d5c340572074c125b58b2",
        ["onnx/model_quantized.onnx", "tokenizer.json", "config.json"],
    ),
    "bge-m3": (
        "Xenova/bge-m3",
        "4de13258303883538bd53b696b452bf8099f0858",
        ["onnx/model_quantized.onnx", "tokenizer.json", "config.json"],
    ),
    "qwen3-emb": (
        "onnx-community/Qwen3-Embedding-0.6B-ONNX",
        "c25a394dd583836952667c12f008335071b3f43d",
        ["onnx/model_quantized.onnx", "tokenizer.json", "config.json"],
    ),
    "arctic-m-v2": (
        "Snowflake/snowflake-arctic-embed-m-v2.0",
        "95c2741480856aa9666782eb4afe11959938017f",
        ["onnx/model_quantized.onnx", "tokenizer.json", "config.json"],
    ),
    "siglip2-224": (
        "onnx-community/siglip2-base-patch16-224-ONNX",
        "ba1f3b0843f24bc5417d38e19c37b287d719b2f4",
        [
            "onnx/vision_model_quantized.onnx",
            "onnx/text_model_quantized.onnx",
            "onnx/vision_model.onnx",
            "onnx/text_model.onnx",
            "tokenizer.json",
            "tokenizer_config.json",
            "preprocessor_config.json",
            "config.json",
        ],
    ),
    "mclip": (
        "sentence-transformers/clip-ViT-B-32-multilingual-v1",
        "58edf8cada9e398793dca955574a48cbb7f18be2",
        [
            "onnx/model_qint8_arm64.onnx",
            "onnx/model.onnx",
            "2_Dense/model.safetensors",
            "2_Dense/config.json",
            "1_Pooling/config.json",
            "tokenizer.json",
        ],
    ),
    "clip-b32": (
        "Xenova/clip-vit-base-patch32",
        "6b7c523703ae26e39be4b03774966f8571c7f523",
        ["onnx/vision_model_quantized.onnx", "onnx/vision_model.onnx", "preprocessor_config.json"],
    ),
}


# PP-OCR in ONNX, as converted and published by RapidAI (the rapidocr
# package) for its release 3.10.0, with the SHA-256 its default_models.yaml
# gives. The character list of each recogniser is in the ONNX metadata.
PPOCR_URL = "https://www.modelscope.cn/models/RapidAI/RapidOCR/resolve/v3.10.0/onnx"
PPOCR: dict[str, tuple[str, str]] = {
    "PP-OCRv6_det_small.onnx": (
        "PP-OCRv6/det/PP-OCRv6_det_small.onnx",
        "090f04abcd9d9a7498bc4ebf677e4cb9bdce1fe4197ddb7e529f1ef44e1ff94f",
    ),
    "PP-OCRv6_rec_small.onnx": (
        "PP-OCRv6/rec/PP-OCRv6_rec_small.onnx",
        "6f327246b50388f3c176ae304bd95767ea6dc0c9ae92153ef8cbe210b3c14884",
    ),
    "PP-OCRv6_det_tiny.onnx": (
        "PP-OCRv6/det/PP-OCRv6_det_tiny.onnx",
        "f42c0fbd294d95eac1a550e131b277dac97462c8025fa4b6c3cec1b7894bd3d5",
    ),
    "PP-OCRv6_rec_tiny.onnx": (
        "PP-OCRv6/rec/PP-OCRv6_rec_tiny.onnx",
        "e16e242de5937ad92609223f19bc2aff3727ee40b095f996907c24749bad251b",
    ),
    "ch_PP-OCRv5_det_mobile.onnx": (
        "PP-OCRv5/det/ch_PP-OCRv5_det_mobile.onnx",
        "4d97c44a20d30a81aad087d6a396b08f786c4635742afc391f6621f5c6ae78ae",
    ),
    "latin_PP-OCRv5_rec_mobile.onnx": (
        "PP-OCRv5/rec/latin_PP-OCRv5_rec_mobile.onnx",
        "b20bd37c168a570f583afbc8cd7925603890efbcdc000a59e22c269d160b5f5a",
    ),
}


def download(url: str, dest: Path) -> str:
    """Download url to dest unless it is there; the file's SHA-256."""
    dest.parent.mkdir(parents=True, exist_ok=True)
    if not dest.exists():
        part = dest.with_name(dest.name + ".part")
        req = urllib.request.Request(url, headers=UA)
        with urllib.request.urlopen(req, timeout=60) as r, part.open("wb") as f:
            while chunk := r.read(1 << 20):
                f.write(chunk)
        part.replace(dest)
    h = hashlib.sha256()
    with dest.open("rb") as f:
        while chunk := f.read(1 << 20):
            h.update(chunk)
    return h.hexdigest()


def fetch(name: str, repo: str, rev: str, path: str) -> None:
    dest = OUT / name / path
    digest = download(f"https://huggingface.co/{repo}/resolve/{rev}/{path}", dest)
    print(f"{digest}  {dest.stat().st_size:>11}  {name}/{path}", flush=True)


def fetch_ppocr() -> None:
    for name, (path, sha256) in PPOCR.items():
        dest = OUT / "ppocr" / name
        digest = download(f"{PPOCR_URL}/{path}", dest)
        if digest != sha256:
            dest.unlink()
            raise SystemExit(f"{name}: SHA-256 {digest}, expected {sha256}")
        print(f"{digest}  {dest.stat().st_size:>11}  ppocr/{name}", flush=True)


def main() -> None:
    for name in sys.argv[1:] or [*CANDIDATES, "ppocr"]:
        if name == "ppocr":
            fetch_ppocr()
            continue
        repo, rev, paths = CANDIDATES[name]
        for path in paths:
            fetch(name, repo, rev, path)


if __name__ == "__main__":
    main()
