"""The vector of a search query, for the core (#70; #71 adds the photos').

The core has no ONNX Runtime: to search by meaning, it asks the worker, which
already holds the models in memory, to encode the query. A small HTTP server
of the standard library, in a thread of the worker, on the Compose network
only (`expose`, no port published on the host): nothing leaves the box.

    POST /encode {"model": "<name>", "text": "<query>"}
    200 {"model": "<name>", "version": "<version>", "vector": [<unit vector>]}
    400 a body that is not that, an empty text or one over MAX_TEXT characters
    404 a model this worker does not serve, or another path

ENCODERS maps a model name to the way it encodes a query, which is not the
way it encodes what it indexes (Arctic adds "query: " in front of a query).
The text of a query is never written to the logs.
"""

from __future__ import annotations

import json
import logging
import os
import threading
from collections.abc import Callable
from dataclasses import dataclass
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import numpy as np

from worker import models, textemb

log = logging.getLogger("worker.encoder")

ADDR = "0.0.0.0:8090"  # CLOUDOS_ENCODER_ADDR; the core's CLOUDOS_ENCODER_URL points at it
MAX_BODY = 16 * 1024  # bytes of a request
MAX_TEXT = 1000  # characters of a query, as core/internal/textemb.MaxQueryChars


@dataclass(frozen=True)
class QueryEncoder:
    """How one model encodes a query, and the version of its vectors."""

    encode: Callable[[str], np.ndarray]
    version: Callable[[], str]


def _text_query(text: str) -> np.ndarray:
    return models.text_encoder().embed([text], query=True)[0]


ENCODERS: dict[str, QueryEncoder] = {
    models.TEXT_MODEL: QueryEncoder(_text_query, textemb.version),
}


class _Server(ThreadingHTTPServer):
    daemon_threads = True

    def __init__(self, addr: tuple[str, int], encoders: dict[str, QueryEncoder]) -> None:
        super().__init__(addr, _Handler)
        self.encoders = encoders


class _Handler(BaseHTTPRequestHandler):
    server: _Server
    protocol_version = "HTTP/1.1"  # keep-alive: the core reuses its connection
    server_version = "cloudos-encoder"
    sys_version = ""

    def log_message(self, format: str, *args: object) -> None:
        pass  # the request line is harmless, but nothing here logs a query

    def _reply(self, status: int, body: dict) -> None:
        data = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self) -> None:
        self._reply(404, {"error": "no such route"})

    def do_POST(self) -> None:
        try:
            length = int(self.headers.get("Content-Length", ""))
        except ValueError:
            length = -1
        if not 0 < length <= MAX_BODY:
            self.close_connection = True  # the body, if any, is left unread
            self._reply(400, {"error": f"a JSON body of 1 to {MAX_BODY} bytes is needed"})
            return
        data = self.rfile.read(length)  # read whatever the path: the connection stays usable
        if self.path != "/encode":
            self._reply(404, {"error": "no such route"})
            return
        try:
            body = json.loads(data)
        except ValueError:
            body = None
        if not isinstance(body, dict):
            self._reply(400, {"error": "the body is not a JSON object"})
            return
        model, text = body.get("model"), body.get("text")
        if not isinstance(text, str) or not text.strip() or len(text) > MAX_TEXT:
            self._reply(400, {"error": f"text must have 1 to {MAX_TEXT} characters"})
            return
        enc = self.server.encoders.get(model) if isinstance(model, str) else None
        if enc is None:
            self._reply(404, {"error": f"model not served: {model!r}"})
            return
        try:
            vector = enc.encode(text)
            version = enc.version()
        except Exception as err:  # a broken model answers 500, never kills the server
            log.error("encoding a query with %s failed: %s", model, type(err).__name__)
            self._reply(500, {"error": "the query could not be encoded"})
            return
        self._reply(200, {"model": model, "version": version, "vector": vector.tolist()})


def parse_addr(addr: str) -> tuple[str, int]:
    host, _, port = addr.rpartition(":")
    return host or "0.0.0.0", int(port)


def serve(
    addr: str, encoders: dict[str, QueryEncoder] | None = None
) -> tuple[ThreadingHTTPServer, threading.Thread]:
    """Start the server in a thread; server.shutdown() stops it."""
    server = _Server(parse_addr(addr), encoders if encoders is not None else ENCODERS)
    thread = threading.Thread(target=server.serve_forever, name="encoder", daemon=True)
    thread.start()
    return server, thread


def run(stop: threading.Event) -> None:
    """The worker's encoder until stopped: loads the text model first, so that
    the first search does not wait for it, then answers."""
    addr = os.environ.get("CLOUDOS_ENCODER_ADDR", ADDR)
    try:
        server, _ = serve(addr)
    except OSError as err:
        log.error("query encoder not started on %s: %s", addr, err)
        return
    log.info("query encoder on %s: %s", addr, ", ".join(ENCODERS))
    try:
        models.text_encoder()
    except Exception as err:  # no model files: every query answers 500, said once here
        log.error("text model not loaded: %s", err)
    stop.wait()
    server.shutdown()
