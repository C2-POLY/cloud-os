-- +goose Up
-- The task queue shared by the core (Go) and the workers (Python). The SQL in
-- core/internal/tasks and worker/src/worker/tasks.py is the contract.
CREATE TABLE tasks (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind text NOT NULL,
    payload jsonb NOT NULL DEFAULT '{}',
    priority integer NOT NULL DEFAULT 0, -- higher first
    state text NOT NULL DEFAULT 'queued' CHECK (state IN ('queued', 'running', 'done', 'failed')),
    attempts integer NOT NULL DEFAULT 0,
    max_attempts integer NOT NULL DEFAULT 5 CHECK (max_attempts > 0),
    run_after timestamptz NOT NULL DEFAULT now(),
    locked_by text,
    locked_until timestamptz,
    last_error text,
    dedupe_key text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz
);

-- The ready tasks, in the order Claim takes them.
CREATE INDEX tasks_ready ON tasks (priority DESC, run_after, id) WHERE state = 'queued';
-- Leases to reap.
CREATE INDEX tasks_running ON tasks (locked_until) WHERE state = 'running';
-- One pending task per dedupe key.
CREATE UNIQUE INDEX tasks_dedupe ON tasks (dedupe_key) WHERE state IN ('queued', 'running') AND dedupe_key IS NOT NULL;

-- +goose Down
DROP TABLE tasks;
