"""The worker's query endpoint: turns the text of a search into a vector, for
the core (core/internal/embed, docs/architecture/embeddings.md).

    POST /embed  {"model": "siglip2-base-patch16-224", "texts": ["chien à la plage"]}
              →  {"model": "…", "version": "…", "vectors": [[0.01, …]]}
    GET /health  →  {"models": {"siglip2-base-patch16-224": {"version": "…", "dims": 768}}}

Indexing goes through the task queue; only queries come here, because the
core waits for them, and a queued query would wait behind an OCR page or a
PDF. The server runs on its own thread in the worker's process, with the
standard library's http.server. It listens on the Compose network only (no
published port), reads no file and no database, and never logs a query.
"""

from __future__ import annotations

import json
import logging
import os
import threading
from http import HTTPStatus
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from worker import models

log = logging.getLogger("worker.embedserver")

DEFAULT_ADDR = "0.0.0.0:8090"
MAX_BODY = 64 * 1024  # bytes of a request
MAX_TEXTS = 16
MAX_CHARS = 1000  # per text; SigLIP 2 reads 64 tokens anyway


class BadRequest(Exception):
    def __init__(self, status: HTTPStatus, message: str) -> None:
        super().__init__(message)
        self.status = status


def parse(body: bytes) -> tuple[str, list[str]]:
    """The model and texts of a POST /embed body. Raises BadRequest."""
    try:
        req = json.loads(body)
    except ValueError as err:
        raise BadRequest(HTTPStatus.BAD_REQUEST, "the body is not JSON") from err
    if not isinstance(req, dict):
        raise BadRequest(HTTPStatus.BAD_REQUEST, "the body is not an object")
    model, texts = req.get("model"), req.get("texts")
    if not isinstance(model, str) or not model:
        raise BadRequest(HTTPStatus.BAD_REQUEST, "model is missing")
    if (
        not isinstance(texts, list)
        or not 1 <= len(texts) <= MAX_TEXTS
        or not all(isinstance(t, str) and 0 < len(t) <= MAX_CHARS for t in texts)
    ):
        raise BadRequest(
            HTTPStatus.BAD_REQUEST,
            f"texts must be 1 to {MAX_TEXTS} non-empty strings of at most {MAX_CHARS} characters",
        )
    return model, texts


class Handler(BaseHTTPRequestHandler):
    server_version = "cloudos-worker"
    sys_version = ""
    protocol_version = "HTTP/1.1"

    def reply(self, status: HTTPStatus, body: dict) -> None:
        data = json.dumps(body, separators=(",", ":")).encode()
        try:
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)
        except (BrokenPipeError, ConnectionResetError):
            # The core stopped waiting (core/internal/embed Timeout): one line, no traceback.
            self.close_connection = True
            log.warning("the core stopped waiting for a query vector (over its timeout)")

    def do_GET(self) -> None:
        if self.path != "/health":
            self.reply(HTTPStatus.NOT_FOUND, {"error": "no such route"})
            return
        served = models.embedding_models()
        self.reply(
            HTTPStatus.OK,
            {"models": {n: {"version": s.version, "dims": s.dims} for n, s in served.items()}},
        )

    def do_POST(self) -> None:
        try:
            if self.path != "/embed":
                raise BadRequest(HTTPStatus.NOT_FOUND, "no such route")
            try:
                length = int(self.headers.get("Content-Length", ""))
            except ValueError as err:
                raise BadRequest(HTTPStatus.LENGTH_REQUIRED, "Content-Length is missing") from err
            if not 0 <= length <= MAX_BODY:
                self.close_connection = True  # the body is left unread
                raise BadRequest(HTTPStatus.REQUEST_ENTITY_TOO_LARGE, "the body is too large")
            name, texts = parse(self.rfile.read(length))
            served = models.embedding_models().get(name)
            if served is None:
                raise BadRequest(HTTPStatus.NOT_FOUND, f"no model {name!r} here")
            try:
                vectors = served.query(texts)
            except FileNotFoundError as err:  # the image was built without its models
                log.error("cannot encode a query: %s", err)
                raise BadRequest(HTTPStatus.SERVICE_UNAVAILABLE, "model files missing") from err
        except BadRequest as err:
            self.reply(err.status, {"error": str(err)})
            return
        self.reply(
            HTTPStatus.OK,
            {"model": name, "version": served.version, "vectors": vectors.tolist()},
        )

    def log_message(self, format: str, *args: object) -> None:
        # The request line only, never a body: a query stays private.
        log.debug(format, *args)


def address() -> tuple[str, int]:
    host, _, port = os.environ.get("CLOUDOS_EMBED_ADDR", DEFAULT_ADDR).rpartition(":")
    return host or "0.0.0.0", int(port)


def warm() -> None:
    """Load every query encoder now, so that the first search is fast."""
    for name, served in models.embedding_models().items():
        try:
            served.query(["warm up"])
        except FileNotFoundError as err:
            log.warning("model %s cannot encode queries: %s", name, err)


def start(stop: threading.Event, addr: tuple[str, int] | None = None) -> ThreadingHTTPServer:
    """Serve POST /embed on its own thread until stop is set. The socket is
    bound before this returns; the models load on the server's thread, while
    the first requests wait for them."""
    server = ThreadingHTTPServer(addr or address(), Handler)
    server.daemon_threads = True

    def run() -> None:
        warm()
        log.info("query endpoint on %s:%s", *server.server_address[:2])
        server.serve_forever(poll_interval=0.5)

    threading.Thread(target=run, name="embedserver", daemon=True).start()
    threading.Thread(target=lambda: (stop.wait(), server.shutdown()), daemon=True).start()
    return server
