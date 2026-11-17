"""The worker's side of the task queue, against a throwaway database built from
the core's own migrations, with the same cases as core/internal/tasks."""

import threading

import psycopg
import pytest

from worker import tasks


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
