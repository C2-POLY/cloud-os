"""Local AI models: text embeddings, image–text embeddings and OCR, run on
the CPU with ONNX Runtime (docs/decisions/005-modeles-locaux.md).

The model files are baked into the worker image at build time by
worker/models/fetch.sh, which checks each one against its SHA-256, into the
folder CLOUDOS_MODELS_DIR names (/opt/models in the image, .tools/models for
the tests). Nothing is downloaded at run time: a missing file is an error,
never a fetch. No PyTorch: only ONNX Runtime, tokenizers and numpy.
ONNX Runtime's telemetry is off (ORT_DISABLE_TELEMETRY, set by the worker
package before this module imports it).

This module only loads the models and turns texts and images into unit
vectors, and pages into text; indexing them is the job of worker.tasks
(#68, #70, #71). A worker loads each model once, whichever of its threads
(the task loop, the query encoder of worker.encoder) asks first.
"""

from __future__ import annotations

import os
import threading
from collections.abc import Callable, Sequence
from dataclasses import dataclass
from functools import cache, wraps
from pathlib import Path
from typing import Literal

import numpy as np
import onnxruntime as ort
from PIL import Image
from tokenizers import Tokenizer

# The second lock on the telemetry, should something have imported ONNX
# Runtime before the worker package set ORT_DISABLE_TELEMETRY.
ort.disable_telemetry_events()

DEFAULT_DIR = "/opt/models"
# ONNX Runtime threads per model. A Raspberry Pi 5 has four cores, shared
# with Postgres and the core: CLOUDOS_MODEL_THREADS lowers it.
THREADS = 4

# The models kept by docs/decisions/005-modeles-locaux.md, each in its folder
# of CLOUDOS_MODELS_DIR (worker/models/fetch.sh). The name goes with every
# vector stored, so that a new model never mixes with the old one's vectors.
TEXT_MODEL = "arctic-embed-m-v2.0"
IMAGE_MODEL = "siglip2-base-patch16-224"
OCR_MODEL = "pp-ocrv5-latin"
# The table of each embedding model's vectors (core/migrations): another
# model is another table, so that vectors of two models never mix.
TEXT_TABLE = "text_vectors_arctic_m_v2"
# Arctic reads up to 8192 tokens, but 2048 at once already take the worker
# from 1.2 to 2.6 GB of RAM, and a long window blurs the passage that answers
# a precise question: documents are cut into windows of 512 tokens (about
# 1,800 characters of French), worker.textemb.
TEXT_SPEC_MAX_TOKENS = 512


def models_dir() -> Path:
    return Path(os.environ.get("CLOUDOS_MODELS_DIR", DEFAULT_DIR))


def threads() -> int:
    return int(os.environ.get("CLOUDOS_MODEL_THREADS", THREADS))


@cache
def file_sha256(name: str) -> str:
    """The SHA-256 of a model file ("arctic-embed-m-v2.0/model.onnx"), as
    worker/models/fetch.sh checked and listed it in SHA256SUMS: it names the
    version of the vectors the file makes."""
    sums = models_dir() / "SHA256SUMS"
    if not sums.is_file():
        raise FileNotFoundError(f"model list missing: {sums} (worker/models/fetch.sh)")
    for line in sums.read_text().splitlines():
        digest, _, path = line.partition("  ")
        if path == name:
            return digest
    raise FileNotFoundError(f"{name} is not in {sums} (worker/models/fetch.sh)")


_loading = threading.Lock()


def _load_once[T](load: Callable[[], T]) -> Callable[[], T]:
    """load, run once per process: a second thread asking while the first
    loads waits for that model instead of loading another copy."""
    loaded = cache(load)

    @wraps(load)
    def get() -> T:
        with _loading:
            return loaded()

    return get


def session(path: Path, n_threads: int | None = None) -> ort.InferenceSession:
    """An ONNX Runtime session on the CPU for the model file at path."""
    if not path.is_file():
        raise FileNotFoundError(f"model file missing: {path} (worker/models/fetch.sh)")
    opts = ort.SessionOptions()
    opts.intra_op_num_threads = n_threads or threads()
    opts.inter_op_num_threads = 1
    opts.log_severity_level = 3  # errors only
    return ort.InferenceSession(str(path), opts, providers=["CPUExecutionProvider"])


def unit(x: np.ndarray) -> np.ndarray:
    """Rows scaled to length 1, so a dot product is the cosine similarity."""
    norm = np.linalg.norm(x, axis=-1, keepdims=True)
    return (x / np.maximum(norm, 1e-12)).astype(np.float32)


@dataclass(frozen=True)
class TextSpec:
    """How a text embedding model is fed and how its token vectors become one
    vector: the mean of the tokens, the first one (CLS) or the last one (a
    decoder such as Qwen3), or the model's own pooled output."""

    onnx: str
    pooling: Literal["mean", "cls", "last", "output"]
    query_prefix: str = ""
    passage_prefix: str = ""
    max_tokens: int = 512
    eos: str | None = None  # appended when the tokenizer does not add it
    lowercase: bool = False


class TextEncoder:
    """A text embedding model: texts in, unit vectors out."""

    def __init__(self, folder: Path, spec: TextSpec, n_threads: int | None = None) -> None:
        self.spec = spec
        self.tok = Tokenizer.from_file(str(folder / "tokenizer.json"))
        self.tok.no_padding()
        self.tok.enable_truncation(spec.max_tokens - (1 if spec.eos else 0))
        self.eos_id = self.tok.token_to_id(spec.eos) if spec.eos else None
        self.sess = session(folder / spec.onnx, n_threads)
        self.inputs = {i.name: i for i in self.sess.get_inputs()}
        self.outputs = [o.name for o in self.sess.get_outputs()]
        self.dim = int(self.sess.get_outputs()[0].shape[-1])

    def ids(self, text: str) -> list[int]:
        ids = self.tok.encode(text.lower() if self.spec.lowercase else text).ids
        if self.eos_id is not None and (not ids or ids[-1] != self.eos_id):
            ids.append(self.eos_id)
        return ids

    def embed(self, texts: Sequence[str], *, query: bool = False) -> np.ndarray:
        """One unit vector per text, shape (len(texts), dim). A query gets the
        model's query prefix (e5: "query: "), a stored document the passage one."""
        if not texts:
            return np.zeros((0, self.dim), dtype=np.float32)
        prefix = self.spec.query_prefix if query else self.spec.passage_prefix
        rows = [self.ids(prefix + t) for t in texts]
        n = max(len(r) for r in rows)
        ids = np.zeros((len(rows), n), dtype=np.int64)
        mask = np.zeros((len(rows), n), dtype=np.int64)
        for i, r in enumerate(rows):
            ids[i, : len(r)] = r
            mask[i, : len(r)] = 1
        feed: dict[str, np.ndarray] = {"input_ids": ids}
        if "attention_mask" in self.inputs:
            feed["attention_mask"] = mask
        if "token_type_ids" in self.inputs:
            feed["token_type_ids"] = np.zeros_like(ids)
        if "position_ids" in self.inputs:
            feed["position_ids"] = np.broadcast_to(np.arange(n, dtype=np.int64), ids.shape).copy()
        for name, arg in self.inputs.items():
            if name.startswith("past_key_values"):  # a decoder exported with its cache: empty
                _, heads, _, size = arg.shape
                feed[name] = np.zeros((len(rows), heads, 0, size), dtype=np.float32)
        wanted = "sentence_embedding" if self.spec.pooling == "output" else self.outputs[0]
        out = self.sess.run([wanted], feed)[0]
        if self.spec.pooling == "output":
            return unit(out)
        if self.spec.pooling == "cls":
            return unit(out[:, 0])
        if self.spec.pooling == "last":
            return unit(out[np.arange(len(rows)), mask.sum(axis=1) - 1])
        m = mask[..., None].astype(np.float32)
        return unit((out * m).sum(axis=1) / m.sum(axis=1))


@dataclass(frozen=True)
class ImageSpec:
    """How an image is turned into the pixel tensor of a vision model:
    squashed to size × size (SigLIP), or its short side resized to size then
    cropped in the middle (CLIP); then scaled to 0–1 and normalised."""

    onnx: str
    size: int
    mean: tuple[float, float, float]
    std: tuple[float, float, float]
    crop: bool = False
    resample: Image.Resampling = Image.Resampling.BILINEAR


class ImageEncoder:
    """The vision half of an image–text model: images in, unit vectors out."""

    def __init__(self, folder: Path, spec: ImageSpec, n_threads: int | None = None) -> None:
        self.spec = spec
        self.sess = session(folder / spec.onnx, n_threads)
        outputs = {o.name: o for o in self.sess.get_outputs()}
        self.output = next(
            (n for n in ("image_embeds", "pooler_output") if n in outputs), next(iter(outputs))
        )
        self.dim = int(outputs[self.output].shape[-1])

    def pixels(self, image: Image.Image) -> np.ndarray:
        s = self.spec
        im = image.convert("RGB")
        if s.crop:
            scale = s.size / min(im.size)
            w, h = max(s.size, round(im.width * scale)), max(s.size, round(im.height * scale))
            im = im.resize((w, h), s.resample)
            left, top = (w - s.size) // 2, (h - s.size) // 2
            im = im.crop((left, top, left + s.size, top + s.size))
        else:
            im = im.resize((s.size, s.size), s.resample)
        x = np.asarray(im, dtype=np.float32) / 255.0
        x = (x - np.array(s.mean, dtype=np.float32)) / np.array(s.std, dtype=np.float32)
        return x.transpose(2, 0, 1)

    def embed(self, images: Sequence[Image.Image]) -> np.ndarray:
        if not images:
            return np.zeros((0, self.dim), dtype=np.float32)
        batch = np.stack([self.pixels(im) for im in images])
        return unit(self.sess.run([self.output], {"pixel_values": batch})[0])


class OcrModel:
    """PP-OCR on ONNX Runtime: a detector that maps where the text is on a
    page, and a recogniser that reads one line of text. Both are fed BGR
    pixels scaled to -1…1, as PaddleOCR trained them.

    Turning the detector's map into line boxes and putting the lines in
    reading order is the page pipeline of #68; this class only runs the two
    models."""

    LINE_HEIGHT = 48  # pixels, the recogniser's input height
    MIN_SIDE = 736  # the detector's input: short side at least this, multiples of 32,
    MAX_SIDE = 2000  # and long side at most this (a 12 Mpx photo is scaled down)

    def __init__(self, folder: Path, n_threads: int | None = None) -> None:
        self.det = session(folder / "det.onnx", n_threads)
        self.rec = session(folder / "rec.onnx", n_threads)
        chars = self.rec.get_modelmeta().custom_metadata_map["character"].splitlines()
        self.chars = ["", *chars, " "]  # index 0 is the CTC blank, the space comes last

    @staticmethod
    def bgr(image: Image.Image) -> np.ndarray:
        x = np.asarray(image.convert("RGB"), dtype=np.float32)[..., ::-1]
        return (x / 255.0 - 0.5) / 0.5

    def text_map(self, image: Image.Image) -> np.ndarray:
        """The probability that each pixel is text, for the image resized so
        that its short side is at least MIN_SIDE, its long side at most
        MAX_SIDE (that one wins), and both sides are multiples of 32 (the map
        has that size)."""
        w, h = image.size
        ratio = min(max(1.0, self.MIN_SIDE / min(w, h)), self.MAX_SIDE / max(w, h))
        size = (max(32, round(w * ratio / 32) * 32), max(32, round(h * ratio / 32) * 32))
        x = self.bgr(image.resize(size, Image.Resampling.BILINEAR)).transpose(2, 0, 1)
        return self.det.run(None, {self.det.get_inputs()[0].name: x[None]})[0][0, 0]

    def read_line(self, image: Image.Image) -> str:
        """The text of an image holding one line of text."""
        w, h = image.size
        width = max(1, round(w * self.LINE_HEIGHT / h))
        x = self.bgr(image.resize((width, self.LINE_HEIGHT), Image.Resampling.BILINEAR))
        batch = np.zeros((1, 3, self.LINE_HEIGHT, max(width, 320)), dtype=np.float32)
        batch[0, :, :, :width] = x.transpose(2, 0, 1)
        best = self.rec.run(None, {self.rec.get_inputs()[0].name: batch})[0][0].argmax(axis=-1)
        keep = (best != 0) & np.concatenate([[True], best[1:] != best[:-1]])
        return "".join(self.chars[i] for i in best[keep] if i < len(self.chars))


class SiglipTextEncoder:
    """The text half of SigLIP: lowercased, padded to the 64 tokens it was
    trained with, the pooled output. Its vectors compare with ImageEncoder's."""

    def __init__(self, folder: Path, onnx: str, n_threads: int | None = None) -> None:
        self.tok = Tokenizer.from_file(str(folder / "tokenizer.json"))
        self.tok.no_padding()
        self.tok.enable_truncation(64)
        self.pad = self.tok.token_to_id("<pad>")
        self.eos = self.tok.token_to_id("<eos>")
        self.sess = session(folder / onnx, n_threads)

    def embed(self, texts: Sequence[str]) -> np.ndarray:
        if not texts:
            return np.zeros((0, int(self.sess.get_outputs()[-1].shape[-1])), dtype=np.float32)
        ids = np.full((len(texts), 64), self.pad, dtype=np.int64)
        for i, t in enumerate(texts):
            row = self.tok.encode(t.lower()).ids[:63]
            if not row or row[-1] != self.eos:
                row.append(self.eos)
            ids[i, : len(row)] = row
        return unit(self.sess.run(["pooler_output"], {"input_ids": ids})[0])


@_load_once
def text_encoder() -> TextEncoder:
    """Arctic Embed M v2.0: documents and queries into 768-dimension vectors;
    a query takes the prefix "query: ", a document none."""
    spec = TextSpec("model.onnx", "output", query_prefix="query: ", max_tokens=TEXT_SPEC_MAX_TOKENS)
    return TextEncoder(models_dir() / TEXT_MODEL, spec)


@_load_once
def image_encoder() -> ImageEncoder:
    """SigLIP 2 base/16 at 224 px: photos into 768-dimension vectors."""
    spec = ImageSpec("vision_model.onnx", 224, (0.5, 0.5, 0.5), (0.5, 0.5, 0.5))
    return ImageEncoder(models_dir() / IMAGE_MODEL, spec)


@_load_once
def image_query_encoder() -> SiglipTextEncoder:
    """SigLIP 2's text half: a query into a vector that compares with the photos'."""
    return SiglipTextEncoder(models_dir() / IMAGE_MODEL, "text_model.onnx")


@_load_once
def ocr_model() -> OcrModel:
    """PP-OCRv5: the mobile text detector and the Latin mobile recogniser."""
    return OcrModel(models_dir() / OCR_MODEL)
