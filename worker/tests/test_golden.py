"""The vectors of the benchmark corpus that the core's test of Demander's list
reads (#72, core/internal/search/testdata/bench.json.gz) are those of the
models, the cutting and the corpus of today: otherwise run worker/bench/golden.py
again, in the worker's image as its docstring says. Needs no model."""

import gzip
import json
import tomllib
from pathlib import Path

from worker import models

BENCH = Path(__file__).parent / "fixtures" / "bench"
GOLDEN_PATH = "core/internal/search/testdata/bench.json.gz"
GOLDEN = Path(__file__).resolve().parents[2] / GOLDEN_PATH
AGAIN = f"{GOLDEN_PATH} is stale: run worker/bench/golden.py again, as its docstring says"


def golden() -> dict:
    return json.loads(gzip.decompress(GOLDEN.read_bytes()))


def test_the_vectors_are_of_the_versions_the_worker_serves() -> None:
    served = {name: s.version for name, s in models.embedding_models().items()}
    assert golden()["models"] == served, AGAIN


def test_the_vectors_are_of_the_corpus_and_queries_of_today() -> None:
    g = golden()
    docs = {p.stem: p.read_text() for p in (BENCH / "docs").glob("*.txt")}
    assert {d["name"]: d["text"] for d in g["docs"]} == docs, AGAIN
    photos = {p.stem for p in (BENCH / "photos").glob("*.jpg")}
    assert {p["name"] for p in g["photos"]} == photos, AGAIN
    toml = tomllib.loads((BENCH / "queries.toml").read_text())
    want = [{"kind": kind, **q} for kind in ("text", "image", "mixed") for q in toml[kind]]
    got = [{k: v for k, v in q.items() if k != "vectors"} for q in g["queries"]]
    assert got == want, AGAIN
    for q in g["queries"]:
        assert set(q["vectors"]) == set(g["models"]), AGAIN
