"""The vectors of the benchmark corpus, for the core's test of Demander's one
list (#72, core/internal/search/bench_test.go): the Go test has no model, so
it reads what the worker's models make of the corpus from a file. Run it in
the worker's image, on Linux as the box runs it, from the repository root:

    docker compose run --rm --no-deps \
      -v "$PWD/worker/bench:/app/bench:ro" \
      -v "$PWD/worker/tests/fixtures:/app/tests/fixtures:ro" \
      -v "$PWD/core/internal/search/testdata:/core/internal/search/testdata" \
      worker /app/.venv/bin/python -m bench.golden

Not on macOS: SigLIP 2's int8 text encoder gives other vectors there, which
score unrelated photos up to 0.02 higher (docs/architecture/embeddings.md).

The vectors come from the code the tasks and the query endpoint run, one item
at a time as they do: textemb.chunks and the text encoder for each window of
a document, thumbs.open_upright and the image encoder for each photo, and the
encoders of models.embedding_models() for each query, under the names and
versions the worker declares. They are stored in half precision, as the core
stores the windows. Run it again whenever a model, the cutting or the corpus
changes: tests/test_golden.py fails until then.
"""

from __future__ import annotations

import base64
import gzip
import json
import tomllib
from pathlib import Path

import numpy as np

from worker import models, photovec, textemb, thumbs

BENCH = Path(__file__).resolve().parents[1] / "tests" / "fixtures" / "bench"
CORE = Path(__file__).resolve().parents[2] / "core"
OUT = CORE / "internal" / "search" / "testdata" / "bench.json.gz"


def half(vector: np.ndarray) -> str:
    """A vector as base64 of its float16 values, little-endian."""
    return base64.b64encode(np.asarray(vector, dtype="<f2").tobytes()).decode()


def main() -> None:
    served = models.embedding_models()
    enc = models.text_encoder()
    docs = []
    for p in sorted((BENCH / "docs").glob("*.txt")):
        text = p.read_text()
        windows = [
            {"start": c.start, "end": c.end, "vector": half(enc.embed([text[c.start : c.end]])[0])}
            for c in textemb.chunks(text, enc.tok)
        ]
        docs.append({"name": p.stem, "text": text, "windows": windows})

    photos = []
    for p in sorted((BENCH / "photos").glob("*.jpg")):
        image = photovec.upright_rgb(thumbs.open_upright(p.read_bytes(), photovec.DECODE_SIDE))
        photos.append({"name": p.stem, "vector": half(models.image_encoder().embed([image])[0])})

    queries = []
    toml = tomllib.loads((BENCH / "queries.toml").read_text())
    for kind in ("text", "image", "mixed"):
        for q in toml[kind]:
            vectors = {name: half(s.query([q["q"]])[0]) for name, s in sorted(served.items())}
            queries.append({"kind": kind, **q, "vectors": vectors})

    golden = {
        "models": {name: s.version for name, s in sorted(served.items())},
        "docs": docs,
        "photos": photos,
        "queries": queries,
    }
    OUT.parent.mkdir(parents=True, exist_ok=True)
    data = json.dumps(golden, ensure_ascii=False, sort_keys=True).encode()
    OUT.write_bytes(gzip.compress(data, mtime=0))
    print(f"{OUT}: {len(docs)} documents, {len(photos)} photos, {len(queries)} queries")


if __name__ == "__main__":
    main()
