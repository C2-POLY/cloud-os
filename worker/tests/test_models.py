"""The local AI models (docs/decisions/005-modeles-locaux.md): each one loads
from CLOUDOS_MODELS_DIR with ONNX Runtime and does its job on the benchmark
corpus (tests/fixtures/bench).

The models are about 740 MB: `make models` fetches them into .tools/models
and `make test` points CLOUDOS_MODELS_DIR there. Without them these tests are
skipped, except when CLOUDOS_REQUIRE_MODELS is set (CI). ocr-line.png is a
line drawn with DejaVu Sans."""

import os
import subprocess
import sys
import tomllib
from pathlib import Path

import numpy as np
import pytest
from PIL import Image

from worker import models

FIXTURES = Path(__file__).parent / "fixtures"
BENCH = FIXTURES / "bench"

needs_models = pytest.mark.skipif(
    not (models.models_dir() / "SHA256SUMS").exists()
    and not os.environ.get("CLOUDOS_REQUIRE_MODELS"),
    reason="no models: run `make models`",
)


def queries(kind: str) -> dict[str, list[str]]:
    rows = tomllib.loads((BENCH / "queries.toml").read_text())[kind]
    return {q["q"]: q["expect"] for q in rows}


def test_onnx_runtime_sends_no_telemetry(tmp_path: Path) -> None:
    """ONNX Runtime keeps an id and a queue of usage events for Microsoft in
    the user's cache (Linux) or Application Support (macOS) unless its
    telemetry is off: importing the worker's models must leave none."""
    env = {**os.environ, "HOME": str(tmp_path), "XDG_CACHE_HOME": str(tmp_path / ".cache")}
    env.pop("ORT_DISABLE_TELEMETRY", None)
    code = "import os, worker.models; print(os.environ['ORT_DISABLE_TELEMETRY'])"
    out = subprocess.run([sys.executable, "-c", code], env=env, capture_output=True, text=True)
    assert out.returncode == 0, out.stderr
    assert out.stdout.strip() == "1"
    assert "telemetry" not in out.stderr.lower()
    assert not [p for p in tmp_path.rglob("*") if "onnxruntime" in p.name.lower()]


def test_a_missing_model_file_is_an_error_not_a_download(tmp_path: Path) -> None:
    with pytest.raises(FileNotFoundError, match="fetch.sh"):
        models.session(tmp_path / "absent.onnx")


@needs_models
def test_the_files_match_their_sha256() -> None:
    folder = models.models_dir()
    listed = (folder / "SHA256SUMS").read_text().split("\n")
    names = {line.split("  ")[1] for line in listed if line}
    assert names == {
        f"{models.TEXT_MODEL}/model.onnx",
        f"{models.TEXT_MODEL}/tokenizer.json",
        f"{models.IMAGE_MODEL}/vision_model.onnx",
        f"{models.IMAGE_MODEL}/text_model.onnx",
        f"{models.IMAGE_MODEL}/tokenizer.json",
        f"{models.OCR_MODEL}/det.onnx",
        f"{models.OCR_MODEL}/rec.onnx",
    }
    assert (folder / "NOTICE.md").exists()
    assert (folder / "LICENSE-Apache-2.0.txt").exists()


@needs_models
def test_the_text_model_finds_the_lease_from_contrat_de_l_appart() -> None:
    enc = models.text_encoder()
    docs = sorted((BENCH / "docs").glob("*.txt"))
    vectors = enc.embed([d.read_text() for d in docs])
    assert vectors.shape == (len(docs), 768)
    assert np.allclose(np.linalg.norm(vectors, axis=1), 1, atol=1e-4)
    assert enc.embed([]).shape == (0, 768)

    expected = queries("text")
    asked = ["contrat de l'appart", "fiche de paie", "carte grise de la voiture"]
    found = enc.embed(asked, query=True) @ vectors.T
    for q, row in zip(asked, found, strict=True):
        assert docs[int(row.argmax())].stem in expected[q], q


@needs_models
def test_the_image_model_finds_the_dog_on_the_beach() -> None:
    photos = sorted((BENCH / "photos").glob("*.jpg"))
    images = []
    for p in photos:
        with Image.open(p) as im:
            images.append(im.convert("RGB"))
    vectors = models.image_encoder().embed(images)
    assert vectors.shape == (len(photos), 768)
    assert models.image_encoder().embed([]).shape == (0, 768)
    assert models.image_query_encoder().embed([]).shape == (0, 768)

    expected = queries("image")
    asked = ["chien à la plage", "un chat qui dort", "la tour Eiffel"]
    found = models.image_query_encoder().embed(asked) @ vectors.T
    for q, row in zip(asked, found, strict=True):
        top = [photos[i].stem for i in np.argsort(-row)[: len(expected[q])]]
        assert sorted(top) == sorted(expected[q]), q


@needs_models
def test_the_ocr_reads_a_line_with_accents_and_the_euro_sign() -> None:
    ocr = models.ocr_model()
    with Image.open(FIXTURES / "ocr-line.png") as im:
        line = im.convert("RGB")
    assert ocr.read_line(line) == "Facture n° 2024-118 : total 119,69 € (TVA à 20 %)"

    # The detector finds the line on a page, and nothing on a blank one.
    page = Image.new("RGB", (900, 400), "white")
    page.paste(line, (37, 172))
    text = ocr.text_map(page) > 0.3
    rows = np.flatnonzero(text.any(axis=1)) / text.shape[0]
    assert 0.02 < text.mean() < 0.3
    assert rows.min() > 160 / 400 and rows.max() < 240 / 400
    blank = ocr.text_map(Image.new("RGB", page.size, "white"))
    assert (blank > 0.3).mean() < 0.001
