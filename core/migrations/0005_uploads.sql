-- +goose Up
-- Resumable uploads (tus). The bytes received so far live in
-- <data>/.cloudos/uploads/<id>; their size is the resume offset.
CREATE TABLE uploads (
    id text PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    owner text NOT NULL,
    path text NOT NULL,
    length bigint NOT NULL CHECK (length >= 0),
    sha256 bytea CHECK (sha256 IS NULL OR length(sha256) = 32),
    modified_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);

CREATE INDEX uploads_expires_at ON uploads (expires_at);

-- +goose Down
DROP TABLE uploads;
