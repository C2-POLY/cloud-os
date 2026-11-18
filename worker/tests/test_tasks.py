"""The worker's side of the task queue, against a throwaway database built from
the core's own migrations, with the same cases as core/internal/tasks."""

import threading
import time
from pathlib import Path

import psycopg
import pytest
from psycopg.conninfo import conninfo_to_dict

from worker import tasks

# The core's Reap (core/internal/tasks/tasks.go), the same statement.
REAP = """UPDATE tasks SET locked_by = NULL, locked_until = NULL, updated_at = now(),
              last_error = coalesce(last_error, 'the worker stopped answering'),
              state = CASE WHEN attempts >= max_attempts THEN 'failed' ELSE 'queued' END,
              finished_at = CASE WHEN attempts >= max_attempts THEN now() END
          WHERE state = 'running' AND locked_until < now()"""


def state(conn: psycopg.Connection, task_id: int) -> str:
    row = conn.execute("SELECT state FROM tasks WHERE id = %s", (task_id,)).fetchone()
    assert row
    return row[0]


def test_lifecycle(conn: psycopg.Connection) -> None:
    task_id = tasks.enqueue(conn, "thumb", {"file": 42})
    assert tasks.claim(conn, "w1", ["ocr"]) is None
    task = tasks.claim(conn, "w1", ["thumb"])
    assert task and task.id == task_id and task.attempts == 1 and task.payload == {"file": 42}
    assert tasks.claim(conn, "w2", ["thumb"]) is None
    with pytest.raises(tasks.Lost):
        tasks.extend(conn, task_id, "w2")
    tasks.extend(conn, task_id, "w1")
    tasks.complete(conn, task_id, "w1")
    assert state(conn, task_id) == "done"
    with pytest.raises(tasks.Lost):
        tasks.complete(conn, task_id, "w1")


def test_fail_retries_with_backoff_then_gives_up(conn: psycopg.Connection) -> None:
    task_id = tasks.enqueue(conn, "ocr", max_attempts=3)
    delays = []
    for attempt in (1, 2, 3):
        conn.execute("UPDATE tasks SET run_after = now() WHERE id = %s", (task_id,))
        task = tasks.claim(conn, "w", ["ocr"])
        assert task and task.attempts == attempt
        tasks.fail(conn, task_id, "w", f"boom {attempt}")
        row = conn.execute(
            "SELECT state, extract(epoch FROM run_after - updated_at) FROM tasks WHERE id = %s",
            (task_id,),
        ).fetchone()
        assert row
        if attempt < 3:
            assert row[0] == "queued"
            delays.append(round(float(row[1])))
        else:
            assert row[0] == "failed"
    assert delays == [10, 20]


def test_reap_gives_back_the_tasks_of_a_dead_worker(conn: psycopg.Connection) -> None:
    alive = tasks.enqueue(conn, "x")
    dead = tasks.enqueue(conn, "x")
    for _ in range(2):
        assert tasks.claim(conn, "w", ["x"])
    conn.execute(
        "UPDATE tasks SET locked_until = now() - interval '1 second' WHERE id = %s", (dead,)
    )
    # Reaping is the core's job; the worker sees its lease taken away.
    conn.execute(
        """UPDATE tasks SET locked_by = NULL, locked_until = NULL, state = 'queued'
           WHERE state = 'running' AND locked_until < now()"""
    )
    assert state(conn, alive) == "running" and state(conn, dead) == "queued"
    with pytest.raises(tasks.Lost):
        tasks.complete(conn, dead, "w")


def test_dedupe_and_priority(conn: psycopg.Connection) -> None:
    assert tasks.enqueue(conn, "thumb", dedupe_key="thumb:42")
    assert tasks.enqueue(conn, "thumb", dedupe_key="thumb:42") is None
    old1 = tasks.enqueue(conn, "p", priority=0)
    new = tasks.enqueue(conn, "p", priority=10)
    old2 = tasks.enqueue(conn, "p", priority=0)
    order = [tasks.claim(conn, "w", ["p"]).id for _ in range(3)]  # type: ignore[union-attr]
    assert order == [new, old1, old2]


def test_run_one_records_success_and_failure(conn: psycopg.Connection) -> None:
    ok = tasks.enqueue(conn, "selftest", {})
    assert tasks.run_one(conn, "w", tasks.HANDLERS)
    assert state(conn, ok) == "done"
    bad = tasks.enqueue(conn, "selftest", {"fail_times": 1}, max_attempts=2)
    assert tasks.run_one(conn, "w", tasks.HANDLERS)
    row = conn.execute("SELECT state, last_error FROM tasks WHERE id = %s", (bad,)).fetchone()
    assert row and row[0] == "queued" and "failing on purpose" in row[1]
    assert not tasks.run_one(conn, "w", tasks.HANDLERS)  # waiting for its backoff


def test_permanent_error_fails_at_once(conn: psycopg.Connection) -> None:
    def broken(task: tasks.Task, c: psycopg.Connection) -> None:
        raise tasks.Permanent("unreadable")

    task_id = tasks.enqueue(conn, "broken", max_attempts=5)
    assert tasks.run_one(conn, "w", {"broken": broken})
    row = conn.execute("SELECT state, attempts FROM tasks WHERE id = %s", (task_id,)).fetchone()
    assert row == ("failed", 1)


def test_lease_is_extended_while_a_slow_handler_runs(
    db_url: str, conn: psycopg.Connection, monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    # The password only comes from the URL, as in the worker's container.
    monkeypatch.delenv("PGPASSWORD", raising=False)
    monkeypatch.setenv("PGPASSFILE", str(tmp_path / "no-pgpass"))
    params = conninfo_to_dict(db_url)
    params.pop("password", None)
    try:
        psycopg.connect(**params).close()
    except psycopg.OperationalError:
        pass  # the server asks for the password, as in Compose and CI
    else:
        pytest.skip("the test database does not ask for a password")

    task_id = tasks.enqueue(conn, "slow")
    leases: list[object] = []

    def slow(task: tasks.Task, c: psycopg.Connection) -> None:
        def locked_until() -> object:
            row = c.execute("SELECT locked_until FROM tasks WHERE id = %s", (task.id,)).fetchone()
            return row[0] if row else None

        leases.append(locked_until())
        time.sleep(3.0)  # two leases
        leases.append(locked_until())

    stop = threading.Event()

    def reaper() -> None:
        with psycopg.connect(db_url, autocommit=True) as c:
            while not stop.wait(0.1):
                c.execute(REAP)

    reaping = threading.Thread(target=reaper)
    reaping.start()
    try:
        assert tasks.run_one(conn, "w", {"slow": slow}, lease=1.5, extend_every=0.25)
    finally:
        stop.set()
        reaping.join()

    first, last = leases
    assert last is not None, "the task was reaped while its handler ran"
    assert last > first  # type: ignore[operator]
    row = conn.execute("SELECT state, attempts FROM tasks WHERE id = %s", (task_id,)).fetchone()
    assert row == ("done", 1)


def test_a_short_task_opens_no_lease_connection(
    conn: psycopg.Connection, monkeypatch: pytest.MonkeyPatch
) -> None:
    opened: list[str] = []
    connect = psycopg.connect

    def counting(*args: object, **kwargs: object) -> psycopg.Connection:
        opened.append(threading.current_thread().name)
        return connect(*args, **kwargs)  # type: ignore[arg-type]

    monkeypatch.setattr(psycopg, "connect", counting)
    task_id = tasks.enqueue(conn, "selftest", {})
    assert tasks.run_one(conn, "w", tasks.HANDLERS)
    assert state(conn, task_id) == "done"
    assert opened == []


def test_concurrent_workers_never_share_a_task(db_url: str, conn: psycopg.Connection) -> None:
    for _ in range(200):
        tasks.enqueue(conn, "c")
    seen: dict[int, str] = {}
    lock = threading.Lock()
    errors: list[str] = []

    def worker(name: str) -> None:
        with psycopg.connect(db_url, autocommit=True) as c:
            while True:
                task = tasks.claim(c, name, ["c"])
                if task is None:
                    return
                with lock:
                    if task.id in seen:
                        errors.append(f"task {task.id} taken by {seen[task.id]} and {name}")
                    seen[task.id] = name
                tasks.complete(c, task.id, name)

    threads = [threading.Thread(target=worker, args=(f"w{i}",)) for i in range(8)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    assert errors == []
    assert len(seen) == 200
