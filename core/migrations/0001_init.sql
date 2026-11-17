-- +goose Up
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE worker_heartbeat (
    worker_id text PRIMARY KEY,
    seen_at timestamptz NOT NULL
);

-- +goose Down
DROP TABLE worker_heartbeat;

DROP EXTENSION IF EXISTS vector;
