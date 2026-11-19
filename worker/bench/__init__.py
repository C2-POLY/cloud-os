"""The benchmark that chose the worker's local models (#66,
docs/decisions/005-modeles-locaux.md). Not part of the worker image.

    cd worker
    uv run --group bench python -m bench.fetch      # candidate models, pinned
    uv run --group bench python -m bench text image # retrieval: recall@1, MRR, speed, RAM
    uv run --group bench python -m bench.ocr        # OCR: character error rate, speed

bench.golden writes the vectors of the corpus for the core's test of
Demander's list (#72); it runs in the worker's image (see its docstring).

The corpus is in worker/tests/fixtures/bench: fictitious French documents,
CC0 photos (PROVENANCE.md) and the queries with their expected answers.
"""
