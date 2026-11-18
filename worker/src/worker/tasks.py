"""The worker's side of the task queue shared with the core.

The SQL here is the contract: core/internal/tasks/tasks.go runs the same
statements, and both sides have the same tests.
"""

from __future__ import annotations

import json
import logging
import threading
import time
from collections.abc import Callable
from dataclasses import dataclass
from typing import Any

import psycopg
from psycopg.conninfo import make_conninfo

from worker.errors import Permanent as Permanent  # re-exported: tasks.Permanent

log = logging.getLogger("worker.tasks")

# Defaults of the contract, the same as in Go.
LEASE = 60.0
EXTEND_EVERY = 20.0
BASE_BACKOFF = 10.0
MAX_BACKOFF = 3600.0
IDLE_WAIT = 2.0

COLUMNS = (
    "id, kind, payload, priority, state, attempts, max_attempts, "
    "run_after, locked_by, locked_until, last_error, dedupe_key"
)


class Lost(Exception):
    """The task is no longer held by this worker (its lease expired)."""


@dataclass
class Task:
    id: int
    kind: str
    payload: dict[str, Any]
    priority: int
    state: str
    attempts: int
    max_attempts: int


def _task(row: tuple) -> Task:
    return Task(
        id=row[0],
        kind=row[1],
        payload=row[2] or {},
        priority=row[3],
        state=row[4],
        attempts=row[5],
        max_attempts=row[6],
    )


def enqueue(
    conn: psycopg.Connection,
    kind: str,
    payload: dict[str, Any] | None = None,
    priority: int = 0,
    max_attempts: int = 5,
    dedupe_key: str | None = None,
) -> int | None:
    """Add a task; None when a task with the same dedupe key is pending."""
    row = conn.execute(
        """INSERT INTO tasks (kind, payload, priority, max_attempts, dedupe_key)
           VALUES (%s, %s, %s, %s, %s)
           ON CONFLICT (dedupe_key) WHERE state IN ('queued', 'running') AND dedupe_key IS NOT NULL
           DO NOTHING RETURNING id""",
        (kind, json.dumps(payload or {}), priority, max_attempts, dedupe_key),
    ).fetchone()
    return row[0] if row else None


def claim(
    conn: psycopg.Connection, worker: str, kinds: list[str], lease: float = LEASE
) -> Task | None:
    """Take the next ready task of one of kinds, or None. SKIP LOCKED makes
    two workers never take the same task."""
    row = conn.execute(
        f"""UPDATE tasks SET state = 'running', attempts = attempts + 1, locked_by = %s,
                locked_until = now() + make_interval(secs => %s), updated_at = now()
            WHERE id = (
                SELECT id FROM tasks
                WHERE state = 'queued' AND run_after <= now() AND kind = ANY(%s)
                ORDER BY priority DESC, run_after, id
                FOR UPDATE SKIP LOCKED
                LIMIT 1)
            RETURNING {COLUMNS}""",
        (worker, lease, kinds),
    ).fetchone()
    return _task(row) if row else None


def _held(cur: psycopg.Cursor) -> None:
    if cur.rowcount == 0:
        raise Lost


def extend(conn: psycopg.Connection, task_id: int, worker: str, lease: float = LEASE) -> None:
    """Push back the lease of a task this worker still holds."""
    _held(
        conn.execute(
            """UPDATE tasks SET locked_until = now() + make_interval(secs => %s), updated_at = now()
               WHERE id = %s AND state = 'running' AND locked_by = %s""",
            (lease, task_id, worker),
        )
    )


def complete(conn: psycopg.Connection, task_id: int, worker: str) -> None:
    """Mark a held task done."""
    _held(
        conn.execute(
            """UPDATE tasks SET state = 'done', locked_by = NULL, locked_until = NULL,
                   last_error = NULL, finished_at = now(), updated_at = now()
               WHERE id = %s AND state = 'running' AND locked_by = %s""",
            (task_id, worker),
        )
    )


def fail(
    conn: psycopg.Connection, task_id: int, worker: str, reason: str, final: bool = False
) -> None:
    """Record an error: queued again after a growing delay, or failed once
    max_attempts is used, or at once when final (Go: Fail and GiveUp)."""
    _held(
        conn.execute(
            """UPDATE tasks SET last_error = %(reason)s, locked_by = NULL, locked_until = NULL,
                   updated_at = now(),
                   state = CASE WHEN attempts >= max_attempts OR %(final)s
                           THEN 'failed' ELSE 'queued' END,
                   finished_at = CASE WHEN attempts >= max_attempts OR %(final)s THEN now() END,
                   run_after = now() + make_interval(
                       secs => least(%(base)s * power(2, attempts - 1), %(max)s))
               WHERE id = %(id)s AND state = 'running' AND locked_by = %(worker)s""",
            {
                "reason": reason[:2000],
                "final": final,
                "base": BASE_BACKOFF,
                "max": MAX_BACKOFF,
                "id": task_id,
                "worker": worker,
            },
        )
    )


Handler = Callable[[Task, psycopg.Connection], None]


def selftest(task: Task, conn: psycopg.Connection) -> None:
    """Proves the contract end to end: waits payload["sleep"] seconds, and fails
    while attempts <= payload["fail_times"]."""
    time.sleep(float(task.payload.get("sleep", 0)))
    if task.attempts <= int(task.payload.get("fail_times", 0)):
        raise RuntimeError(f"selftest failing on purpose (attempt {task.attempts})")


def _handlers() -> dict[str, Handler]:
    from worker import doctext, meta, ocr, photovec, thumbs

    return {
        "selftest": selftest,
        thumbs.KIND: thumbs.make,
        meta.KIND: meta.make,
        doctext.KIND: doctext.make,
        ocr.KIND: ocr.make,
        photovec.KIND: photovec.make,
    }


HANDLERS: dict[str, Handler] = _handlers()


def run_one(
    conn: psycopg.Connection,
    worker: str,
    handlers: dict[str, Handler],
    lease: float = LEASE,
    extend_every: float = EXTEND_EVERY,
) -> bool:
    """Claim and run one task; False when none is ready. While the handler
    works, a thread pushes back the lease every extend_every seconds, on its
    own connection, so the task is not taken away."""
    task = claim(conn, worker, list(handlers), lease)
    if task is None:
        return False
    done = threading.Event()
    # info.dsn leaves out the password, which the database asks for.
    conninfo = make_conninfo(conn.info.dsn, password=conn.info.password or None)

    def keep_lease() -> None:
        if done.wait(extend_every):
            return  # a short task needs no extension, nor a connection
        try:
            with psycopg.connect(conninfo, autocommit=True) as c:
                while True:
                    extend(c, task.id, worker, lease)
                    if done.wait(extend_every):
                        return
        except Lost:
            log.warning("task %s: lease lost", task.id)
        except psycopg.Error as err:
            log.warning("task %s: could not extend the lease: %s", task.id, err)

    keeper = threading.Thread(target=keep_lease, daemon=True)
    keeper.start()
    try:
        handlers[task.kind](task, conn)
    except Exception as err:  # a failing handler is recorded, never kills the worker
        done.set()
        keeper.join()  # its last extension lands before fail, not after
        log.warning("task %s (%s) failed: %s", task.id, task.kind, err)
        try:
            fail(
                conn,
                task.id,
                worker,
                f"{type(err).__name__}: {err}",
                final=isinstance(err, Permanent),
            )
        except Lost:
            log.warning("task %s: lost before recording the failure", task.id)
        return True
    done.set()
    keeper.join()
    try:
        complete(conn, task.id, worker)
    except Lost:
        log.warning("task %s: lost before completing", task.id)
    return True


def work(
    conn: psycopg.Connection,
    worker: str,
    stop: threading.Event,
    handlers: dict[str, Handler] | None = None,
) -> None:
    """Run tasks until stopped, waiting IDLE_WAIT when none is ready."""
    handlers = handlers or HANDLERS
    while not stop.is_set():
        if not run_one(conn, worker, handlers):
            stop.wait(IDLE_WAIT)
