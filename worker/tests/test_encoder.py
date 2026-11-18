"""The query encoder the core calls (#70): POST /encode on the Compose
network. With fake encoders for the protocol, and with the text model (when
`make models` fetched it) for the vector itself."""

import http.client
import json
import logging
import os
from collections.abc import Iterator

import numpy as np
import pytest

from worker import encoder, models

needs_models = pytest.mark.skipif(
    not (models.models_dir() / "SHA256SUMS").exists()
    and not os.environ.get("CLOUDOS_REQUIRE_MODELS"),
    reason="no models: run `make models`",
)


def broken(text: str) -> np.ndarray:
    raise RuntimeError(f"model gone while reading {text}")


FAKES = {
    "fake": encoder.QueryEncoder(lambda t: np.array([len(t), 0.5], np.float32), lambda: "v1"),
    "broken": encoder.QueryEncoder(broken, lambda: "v1"),
}


@pytest.fixture
def port() -> Iterator[int]:
    server, thread = encoder.serve("127.0.0.1:0", FAKES)
    yield server.server_address[1]
    server.shutdown()
    thread.join()


def post(
    conn: http.client.HTTPConnection, body: bytes | None, path: str = "/encode"
) -> tuple[int, dict]:
    headers = {"Content-Type": "application/json"}
    conn.request("POST", path, body=body, headers=headers)
    r = conn.getresponse()
    return r.status, json.loads(r.read())


def encode(conn: http.client.HTTPConnection, model: object, text: object) -> tuple[int, dict]:
    return post(conn, json.dumps({"model": model, "text": text}).encode())


def test_a_query_comes_back_as_a_vector_on_one_connection(port: int) -> None:
    conn = http.client.HTTPConnection("127.0.0.1", port, timeout=5)
    assert encode(conn, "fake", "contrat") == (
        200,
        {"model": "fake", "version": "v1", "vector": [7.0, 0.5]},
    )
    # Kept alive: the core reuses its connection.
    assert encode(conn, "fake", "bail")[1]["vector"] == [4.0, 0.5]
    conn.close()


@pytest.mark.parametrize(
    ("body", "status"),
    [
        (b"", 400),
        (b"not json", 400),
        (b"[1, 2]", 400),
        (json.dumps({"model": "fake"}).encode(), 400),
        (json.dumps({"model": "fake", "text": 3}).encode(), 400),
        (json.dumps({"model": "fake", "text": "  "}).encode(), 400),
        (json.dumps({"model": "fake", "text": "x" * (encoder.MAX_TEXT + 1)}).encode(), 400),
        (b"{" + b" " * encoder.MAX_BODY + b"}", 400),
        (json.dumps({"model": "other", "text": "contrat"}).encode(), 404),
        (json.dumps({"model": None, "text": "contrat"}).encode(), 404),
    ],
)
def test_a_bad_request_is_refused(port: int, body: bytes, status: int) -> None:
    conn = http.client.HTTPConnection("127.0.0.1", port, timeout=5)
    got, answer = post(conn, body)
    assert got == status and answer["error"]
    conn.close()


def test_other_paths_do_not_exist(port: int) -> None:
    conn = http.client.HTTPConnection("127.0.0.1", port, timeout=5)
    assert post(conn, b"{}", "/other")[0] == 404
    conn.request("GET", "/encode")
    r = conn.getresponse()
    assert (r.status, json.loads(r.read())) == (404, {"error": "no such route"})
    conn.close()


def test_a_failing_model_answers_500_and_never_logs_the_query(
    port: int, caplog: pytest.LogCaptureFixture
) -> None:
    conn = http.client.HTTPConnection("127.0.0.1", port, timeout=5)
    with caplog.at_level(logging.DEBUG):
        status, answer = encode(conn, "broken", "facture du docteur Martin")
    assert status == 500 and "Martin" not in answer["error"]
    assert "Martin" not in caplog.text and "RuntimeError" in caplog.text
    # The server still answers.
    assert encode(conn, "fake", "x")[0] == 200
    conn.close()


@needs_models
def test_the_text_model_encodes_a_query_with_its_prefix() -> None:
    server, thread = encoder.serve("127.0.0.1:0")
    try:
        conn = http.client.HTTPConnection("127.0.0.1", server.server_address[1], timeout=30)
        status, answer = encode(conn, models.TEXT_MODEL, "contrat de l'appart")
        conn.close()
    finally:
        server.shutdown()
        thread.join()
    assert status == 200
    assert answer["model"] == models.TEXT_MODEL
    assert answer["version"] == encoder.ENCODERS[models.TEXT_MODEL].version()
    got = np.array(answer["vector"], dtype=np.float32)
    enc = models.text_encoder()
    assert got.shape == (768,)
    assert np.allclose(got, enc.embed(["contrat de l'appart"], query=True)[0], atol=1e-6)
    # A query is not encoded as a document is: Arctic's "query: " prefix.
    assert not np.allclose(got, enc.embed(["contrat de l'appart"])[0], atol=1e-3)
