"""The query endpoint the core asks for the vector of a search
(worker/src/worker/embedserver.py), on a free local port."""

import json
import logging
import os
import socket
import struct
import threading
import time
import urllib.error
import urllib.request
from collections.abc import Iterator, Sequence

import numpy as np
import pytest

from worker import embedserver, models

FAKE = "fake-model"

needs_models = pytest.mark.skipif(
    not (models.models_dir() / "SHA256SUMS").exists()
    and not os.environ.get("CLOUDOS_REQUIRE_MODELS"),
    reason="no models: run `make models`",
)


def fake_query(texts: Sequence[str]) -> np.ndarray:
    """One unit vector per text, along the axis of its length."""
    out = np.zeros((len(texts), 4), dtype=np.float32)
    for i, t in enumerate(texts):
        out[i, len(t) % 4] = 1
    return out


def missing(texts: Sequence[str]) -> np.ndarray:
    raise FileNotFoundError("model file missing: /opt/models/x.onnx (worker/models/fetch.sh)")


@pytest.fixture
def url(monkeypatch: pytest.MonkeyPatch) -> Iterator[str]:
    served = {
        FAKE: models.Served("v7", 4, fake_query),
        "absent-model": models.Served("v1", 4, missing),
    }
    monkeypatch.setattr(models, "embedding_models", lambda: served)
    stop = threading.Event()
    server = embedserver.start(stop, ("127.0.0.1", 0))
    yield f"http://127.0.0.1:{server.server_address[1]}"
    stop.set()


def call(url: str, path: str, body: bytes | None = None) -> tuple[int, dict]:
    req = urllib.request.Request(
        url + path, data=body, method="POST" if body is not None else "GET"
    )
    req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=30) as res:
            return res.status, json.loads(res.read())
    except urllib.error.HTTPError as err:
        return err.code, json.loads(err.read())


def embed(url: str, payload: object) -> tuple[int, dict]:
    return call(url, "/embed", json.dumps(payload).encode())


def test_embed_returns_the_vectors_and_the_version(url: str) -> None:
    status, body = embed(url, {"model": FAKE, "texts": ["plage", "chien à la plage"]})
    assert status == 200
    assert body == {"model": FAKE, "version": "v7", "vectors": [[0, 1, 0, 0], [1, 0, 0, 0]]}


def test_health_lists_the_models(url: str) -> None:
    status, body = call(url, "/health")
    assert status == 200
    assert body["models"][FAKE] == {"version": "v7", "dims": 4}


@pytest.mark.parametrize(
    "payload",
    [
        "not an object",
        {"texts": ["plage"]},
        {"model": FAKE},
        {"model": FAKE, "texts": []},
        {"model": FAKE, "texts": [""]},
        {"model": FAKE, "texts": [42]},
        {"model": FAKE, "texts": "plage"},
        {"model": FAKE, "texts": ["plage"] * (embedserver.MAX_TEXTS + 1)},
        {"model": FAKE, "texts": ["a" * (embedserver.MAX_CHARS + 1)]},
    ],
)
def test_bad_requests_are_400(url: str, payload: object) -> None:
    status, body = embed(url, payload)
    assert status == 400 and body["error"]


def test_not_json_is_400(url: str) -> None:
    assert call(url, "/embed", b"{not json")[0] == 400


def test_unknown_model_and_route_are_404(url: str) -> None:
    assert embed(url, {"model": "clip", "texts": ["plage"]})[0] == 404
    assert call(url, "/other", b"{}")[0] == 404
    assert call(url, "/other")[0] == 404


def test_a_body_too_large_is_413(url: str) -> None:
    big = json.dumps({"model": FAKE, "texts": ["x" * (embedserver.MAX_BODY + 1)]}).encode()
    try:
        status, _ = call(url, "/embed", big)
    except (ConnectionError, urllib.error.URLError):
        status = 413  # the server may close before the client has sent it all
    assert status == 413


def test_missing_model_files_are_503(url: str) -> None:
    status, body = embed(url, {"model": "absent-model", "texts": ["plage"]})
    assert status == 503 and body == {"error": "model files missing"}


def test_a_client_that_gives_up_leaves_one_warning_and_no_traceback(
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    caplog: pytest.LogCaptureFixture,
) -> None:
    released = threading.Event()

    def slow(texts: Sequence[str]) -> np.ndarray:
        released.wait(5)
        return fake_query(texts)

    monkeypatch.setattr(models, "embedding_models", lambda: {FAKE: models.Served("v1", 4, slow)})
    stop = threading.Event()
    server = embedserver.start(stop, ("127.0.0.1", 0))
    try:
        body = json.dumps({"model": FAKE, "texts": ["plage"]}).encode()
        with socket.create_connection(server.server_address[:2], timeout=5) as s:
            s.sendall(
                b"POST /embed HTTP/1.1\r\nHost: worker\r\nContent-Type: application/json\r\n"
                + f"Content-Length: {len(body)}\r\n\r\n".encode()
                + body
            )
            time.sleep(0.2)
            # Gone before the answer, with a reset rather than a clean close.
            s.setsockopt(socket.SOL_SOCKET, socket.SO_LINGER, struct.pack("ii", 1, 0))
        released.set()
        for _ in range(50):
            if "stopped waiting" in caplog.text:
                break
            time.sleep(0.05)
    finally:
        stop.set()
    assert "stopped waiting" in caplog.text
    assert "Traceback" not in capsys.readouterr().err


def test_a_query_is_never_logged(url: str, caplog: pytest.LogCaptureFixture) -> None:
    caplog.set_level(logging.DEBUG, logger="worker")
    embed(url, {"model": FAKE, "texts": ["rendez-vous chez le médecin"]})
    assert "médecin" not in caplog.text


@needs_models
def test_embed_encodes_a_search_with_siglip(monkeypatch: pytest.MonkeyPatch) -> None:
    stop = threading.Event()
    server = embedserver.start(stop, ("127.0.0.1", 0))
    try:
        url = f"http://127.0.0.1:{server.server_address[1]}"
        status, body = embed(url, {"model": models.IMAGE_MODEL, "texts": ["chien à la plage"]})
    finally:
        stop.set()
    assert status == 200
    assert body["model"] == models.IMAGE_MODEL and body["version"] == models.IMAGE_MODEL_VERSION
    got = np.array(body["vectors"], dtype=np.float32)
    assert got.shape == (1, 768)
    assert np.allclose(got, models.image_queries(["chien à la plage"]), atol=1e-6)
