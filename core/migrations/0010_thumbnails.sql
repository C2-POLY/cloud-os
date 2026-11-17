-- +goose Up
-- Thumbnails of image files, written by the workers (they never write to the
-- data root). Keyed by content: identical files share one, a new version gets
-- its own. Rows whose content no file has any more are pruned by the core.
CREATE TABLE thumbnails (
    sha256 bytea NOT NULL CHECK (length(sha256) = 32),
    size int NOT NULL CHECK (size > 0),
    mime text NOT NULL,
    width int NOT NULL,
    height int NOT NULL,
    bytes bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (sha256, size)
);

CREATE INDEX files_sha256 ON files (sha256) WHERE kind = 'file';

-- +goose Down
DROP INDEX files_sha256;
DROP TABLE thumbnails;
