"""Cloud OS worker: background tasks read from the Postgres task queue."""

import logging
import os
import signal
import socket
import threading

import psycopg

# ONNX Runtime (worker.models) sends usage telemetry to Microsoft unless told
# not to, and reads this switch when it is first imported: set it here, before
# any of the worker's modules can import it. Nothing leaves the box.
os.environ["ORT_DISABLE_TELEMETRY"] = "1"

log = logging.getLogger("worker")

HEARTBEAT_EVERY = 10.0
RETRY_EVERY = 2.0
NICE = 10  # CLOUDOS_WORKER_NICE: how much lower than the core the worker runs

UPSERT_HEARTBEAT = """
    INSERT INTO worker_heartbeat (worker_id, seen_at) VALUES (%s, now())
    ON CONFLICT (worker_id) DO UPDATE SET seen_at = EXCLUDED.seen_at
"""


def table_exists(conn: psycopg.Connection, name: str) -> bool:
    """Whether the table exists yet; the core creates it with its migrations."""
    row = conn.execute("SELECT to_regclass(%s) IS NOT NULL", (name,)).fetchone()
    return bool(row and row[0])


def wait_for_table(conn: psycopg.Connection, name: str, stop: threading.Event) -> bool:
    """Wait until the core's migrations have created the table. False if stopped first."""
    while not stop.is_set():
        if table_exists(conn, name):
            return True
        log.info("waiting for table %s (core migrations)", name)
        stop.wait(RETRY_EVERY)
    return False


def beat(conn: psycopg.Connection, worker_id: str) -> None:
    """Record that this worker is alive now."""
    conn.execute(UPSERT_HEARTBEAT, (worker_id,))


def run(url: str, worker_id: str, stop: threading.Event) -> None:
    """Send heartbeats until stopped, reconnecting whenever the database goes away."""
    while not stop.is_set():
        try:
            with psycopg.connect(url, autocommit=True) as conn:
                if not wait_for_table(conn, "worker_heartbeat", stop):
                    return
                log.info("worker %s ready", worker_id)
                while not stop.is_set():
                    beat(conn, worker_id)
                    stop.wait(HEARTBEAT_EVERY)
        except psycopg.OperationalError as err:
            log.warning("database unreachable, retrying in %ss: %s", RETRY_EVERY, err)
            stop.wait(RETRY_EVERY)


def run_tasks(url: str, worker_id: str, stop: threading.Event) -> None:
    """Run queued tasks until stopped, reconnecting whenever the database goes away."""
    from worker import tasks, vectors

    while not stop.is_set():
        try:
            with psycopg.connect(url, autocommit=True) as conn:
                if not wait_for_table(conn, "tasks", stop):
                    return
                # The core encodes every content again when a model's version changes.
                if not wait_for_table(conn, "embedding_models", stop):
                    return
                vectors.declare(conn)
                log.info("worker %s takes tasks: %s", worker_id, ", ".join(tasks.HANDLERS))
                tasks.work(conn, worker_id, stop)
        except psycopg.OperationalError as err:
            log.warning("database unreachable for tasks, retrying in %ss: %s", RETRY_EVERY, err)
            stop.wait(RETRY_EVERY)


def main() -> None:
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s %(message)s")
    url = os.environ.get("DATABASE_URL")
    if not url:
        raise SystemExit("DATABASE_URL is not set")
    # Unique per process: the queue tells workers apart by this id, so two
    # workers sharing one would complete each other's tasks.
    worker_id = f"{os.environ.get('WORKER_ID', 'worker')}@{socket.gethostname()}:{os.getpid()}"

    # Background work gives way to the core and Postgres, which answer the
    # user: the threads started below inherit this niceness.
    if niceness := int(os.environ.get("CLOUDOS_WORKER_NICE", NICE)):
        os.nice(niceness)

    stop = threading.Event()
    for sig in (signal.SIGINT, signal.SIGTERM):
        signal.signal(sig, lambda *_: stop.set())
    heartbeat = threading.Thread(
        target=run, args=(url, worker_id, stop), name="heartbeat", daemon=True
    )
    heartbeat.start()
    from worker import embedserver

    try:
        embedserver.start(stop)
    except OSError as err:  # the port is taken: tasks still run, searches answer 503
        log.error("query endpoint not started: %s", err)
    run_tasks(url, worker_id, stop)
    heartbeat.join(timeout=5)
    log.info("worker %s stopped", worker_id)
