"""Retrieval benchmark of the candidate models: for each one, in a process of
its own (so that its peak memory is its own), embed the corpus and the French
queries, rank, and measure recall@1, MRR, latency and memory.

    uv run --group bench python -m bench [text] [image] [--threads 4] [--json out.json]

ONNX Runtime runs on the CPU with 4 threads, the cores of a Raspberry Pi 5.
"""

from __future__ import annotations

import argparse
import json
import resource
import subprocess
import sys
import time
import tomllib
from collections.abc import Callable
from pathlib import Path

import numpy as np
from PIL import Image

from bench.fetch import OUT as MODELS
from worker.models import ImageEncoder, ImageSpec, SiglipTextEncoder, TextEncoder, TextSpec, unit

FIXTURES = Path(__file__).resolve().parents[1] / "tests" / "fixtures" / "bench"
SIGLIP = ((0.5, 0.5, 0.5), (0.5, 0.5, 0.5))
CLIP = ((0.48145466, 0.4578275, 0.40821073), (0.26862954, 0.26130258, 0.27577711))
QWEN_TASK = (
    "Instruct: Given a web search query, retrieve relevant passages that answer the query\nQuery:"
)

# name: (folder under MODELS, spec). Long-context models read up to 1024 tokens.
TEXT: dict[str, tuple[str, TextSpec]] = {
    "multilingual-e5-small int8": (
        "e5-small",
        TextSpec("onnx/model_quantized.onnx", "mean", "query: ", "passage: ", 512),
    ),
    "multilingual-e5-small fp32": (
        "e5-small",
        TextSpec("onnx/model.onnx", "mean", "query: ", "passage: ", 512),
    ),
    "multilingual-e5-base int8": (
        "e5-base",
        TextSpec("onnx/model_quantized.onnx", "mean", "query: ", "passage: ", 512),
    ),
    "bge-m3 int8": ("bge-m3", TextSpec("onnx/model_quantized.onnx", "cls", max_tokens=1024)),
    "qwen3-embedding-0.6b int8": (
        "qwen3-emb",
        TextSpec(
            "onnx/model_quantized.onnx",
            "last",
            query_prefix=QWEN_TASK,
            max_tokens=1024,
            eos="<|endoftext|>",
        ),
    ),
    "arctic-embed-m-v2.0 int8": (
        "arctic-m-v2",
        TextSpec("onnx/model_quantized.onnx", "output", query_prefix="query: ", max_tokens=1024),
    ),
}

IMAGE = {
    "siglip2-base-224 int8": "siglip2 int8",
    "siglip2-base-224 fp32": "siglip2 fp32",
    "clip-b32-multilingual int8": "mclip int8",
    "clip-b32-multilingual fp32": "mclip fp32",
}


class MclipText:
    """sentence-transformers' multilingual CLIP text encoder: DistilBERT
    multilingual, mean of the tokens, then a 768→512 linear layer whose
    weights are read from its safetensors file (no PyTorch)."""

    def __init__(self, folder: Path, onnx: str, n_threads: int) -> None:
        self.enc = TextEncoder(folder, TextSpec(onnx, "mean", max_tokens=128), n_threads)
        self.dense = read_safetensors(folder / "2_Dense" / "model.safetensors")["linear.weight"]

    def embed(self, texts: list[str]) -> np.ndarray:
        # TextEncoder returns unit vectors; the projection is linear, so the
        # scale does not change the direction of its output.
        return unit(self.enc.embed(texts) @ self.dense.T)


def read_safetensors(path: Path) -> dict[str, np.ndarray]:
    data = path.read_bytes()
    n = int.from_bytes(data[:8], "little")
    header = json.loads(data[8 : 8 + n])
    dtypes = {"F32": np.float32, "F16": np.float16}
    out = {}
    for name, t in header.items():
        if name == "__metadata__":
            continue
        start, end = t["data_offsets"]
        raw = data[8 + n + start : 8 + n + end]
        out[name] = np.frombuffer(raw, dtype=dtypes[t["dtype"]]).reshape(t["shape"])
    return out


def image_models(kind: str, n_threads: int) -> tuple[Callable, Callable, list[Path]]:
    """embed_images, embed_texts and the files of an image–text candidate."""
    family, precision = kind.split()
    q = precision == "int8"
    if family == "siglip2":
        folder = MODELS / "siglip2-224"
        vision = "onnx/vision_model_quantized.onnx" if q else "onnx/vision_model.onnx"
        text = "onnx/text_model_quantized.onnx" if q else "onnx/text_model.onnx"
        img = ImageEncoder(folder, ImageSpec(vision, 224, *SIGLIP), n_threads)
        txt = SiglipTextEncoder(folder, text, n_threads)
        files = [folder / vision, folder / text, folder / "tokenizer.json"]
    else:
        vfolder, tfolder = MODELS / "clip-b32", MODELS / "mclip"
        vision = "onnx/vision_model_quantized.onnx" if q else "onnx/vision_model.onnx"
        text = "onnx/model_qint8_arm64.onnx" if q else "onnx/model.onnx"
        spec = ImageSpec(vision, 224, *CLIP, crop=True, resample=Image.Resampling.BICUBIC)
        img = ImageEncoder(vfolder, spec, n_threads)
        txt = MclipText(tfolder, text, n_threads)
        files = [
            vfolder / vision,
            tfolder / text,
            tfolder / "tokenizer.json",
            tfolder / "2_Dense" / "model.safetensors",
        ]
    return img.embed, txt.embed, files


def peak_rss_mb() -> float:
    rss = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss
    return rss / 2**20 if sys.platform == "darwin" else rss / 2**10


def score(sims: np.ndarray, names: list[str], queries: list[dict]) -> dict:
    """recall@1, MRR and the misses, for sims[query, item]."""
    ranks, misses = [], []
    for i, q in enumerate(queries):
        order = [names[j] for j in np.argsort(-sims[i])]
        rank = min(order.index(e) for e in q["expect"]) + 1
        ranks.append(rank)
        if rank > 1:
            misses.append(f"{q['q']} → {order[0]} (attendu au rang {rank})")
    r = np.array(ranks)
    return {"recall@1": float((r == 1).mean()), "mrr": float((1 / r).mean()), "misses": misses}


def timed(fn: Callable, items: list) -> tuple[np.ndarray, float]:
    """Embed the items one at a time (as the worker will), after one warm-up
    run; the vectors and the mean milliseconds per item."""
    fn(items[:1])
    out, start = [], time.perf_counter()
    for it in items:
        out.append(fn([it]))
    return np.concatenate(out), (time.perf_counter() - start) * 1000 / len(items)


def run_one(kind: str, name: str, n_threads: int) -> dict:
    queries = tomllib.loads((FIXTURES / "queries.toml").read_text())[kind]
    base = peak_rss_mb()
    start = time.perf_counter()
    if kind == "text":
        folder, spec = TEXT[name]
        enc = TextEncoder(MODELS / folder, spec, n_threads)
        files = [MODELS / folder / spec.onnx, MODELS / folder / "tokenizer.json"]
        load = time.perf_counter() - start
        docs = sorted((FIXTURES / "docs").glob("*.txt"))
        names = [d.stem for d in docs]
        tokens = [len(enc.ids(spec.passage_prefix + d.read_text())) for d in docs]
        items, per_item = timed(lambda t: enc.embed(t), [d.read_text() for d in docs])
        qv, per_query = timed(lambda t: enc.embed(t, query=True), [q["q"] for q in queries])
        extra = {"tokens_mean": float(np.mean(tokens)), "dim": enc.dim}
    else:
        embed_images, embed_texts, files = image_models(IMAGE[name], n_threads)
        load = time.perf_counter() - start
        photos = sorted((FIXTURES / "photos").glob("*.jpg"))
        names = [p.stem for p in photos]

        def from_file(paths: list[Path]) -> np.ndarray:
            images = []
            for p in paths:
                with Image.open(p) as im:
                    im.load()
                    images.append(im)
            return embed_images(images)

        items, per_item = timed(from_file, photos)
        qv, per_query = timed(embed_texts, [q["q"] for q in queries])
        extra = {"dim": int(items.shape[1])}
    result = score(qv @ items.T, names, queries)
    return {
        "name": name,
        "kind": kind,
        "size_mb": sum(f.stat().st_size for f in files) / 1e6,
        "load_s": load,
        "ms_per_item": per_item,
        "ms_per_query": per_query,
        "rss_mb": peak_rss_mb(),
        "rss_base_mb": base,
        **extra,
        **result,
    }


# A Raspberry Pi 5 (4 × Cortex-A76 at 2.4 GHz) against this benchmark run in
# Linux on an Apple M4 Pro (worker/bench/Dockerfile), 4 threads each: about 6
# times slower (about 2 for the clock, 2 for the width of the vector units,
# 1.5 for the int8 matrix instructions the A76 lacks). An estimate, explained
# in docs/decisions/005-modeles-locaux.md, to check on the box itself.
PI5_FACTOR = 6


def table(results: list[dict]) -> str:
    unit_name = {"text": "doc", "image": "photo"}
    lines = [
        "| Modèle | recall@1 | MRR | Taille | RAM (pic) | ms/élément | ms/requête "
        "| Pi 5 estimé (élément) |",
        "|---|---|---|---|---|---|---|---|",
    ]
    for r in results:
        lines.append(
            f"| {r['name']} | {r['recall@1']:.2f} | {r['mrr']:.2f} | {r['size_mb']:.0f} Mo "
            f"| {r['rss_mb']:.0f} Mo | {r['ms_per_item']:.0f} ({unit_name[r['kind']]}) "
            f"| {r['ms_per_query']:.0f} | {r['ms_per_item'] * PI5_FACTOR / 1000:.1f} s |"
        )
    return "\n".join(lines)


def main() -> None:
    parser = argparse.ArgumentParser(prog="python -m bench")
    parser.add_argument("kinds", nargs="*", default=["text", "image"])
    parser.add_argument("--threads", type=int, default=4)
    parser.add_argument("--only", help="run one candidate, in this process")
    parser.add_argument("--json", type=Path, help="also write the results there")
    args = parser.parse_args()
    if args.only:
        print(json.dumps(run_one(args.kinds[0], args.only, args.threads)))
        return
    results = []
    for kind in args.kinds:
        for name in TEXT if kind == "text" else IMAGE:
            cmd = [
                sys.executable,
                "-m",
                "bench",
                kind,
                "--only",
                name,
                "--threads",
                str(args.threads),
            ]
            out = subprocess.run(cmd, check=True, capture_output=True, text=True).stdout
            r = json.loads(out.strip().splitlines()[-1])
            results.append(r)
            print(f"{name}: recall@1 {r['recall@1']:.2f}, MRR {r['mrr']:.2f}", file=sys.stderr)
            for m in r["misses"]:
                print(f"    {m}", file=sys.stderr)
    print(table(results))
    if args.json:
        args.json.write_text(json.dumps(results, ensure_ascii=False, indent=1))


if __name__ == "__main__":
    main()
