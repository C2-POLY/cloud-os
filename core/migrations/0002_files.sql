-- +goose Up
-- One row per file stored under <data root>/<owner>/<path>.
-- Paths are NFC, relative, '/'-separated; two paths that differ only by case
-- cannot coexist for one owner, as on the Finder and the Explorer.
CREATE TABLE files (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    owner text NOT NULL,
    path text NOT NULL,
    size bigint NOT NULL CHECK (size >= 0),
    mime text NOT NULL,
    sha256 bytea NOT NULL CHECK (length(sha256) = 32),
    created_at timestamptz NOT NULL DEFAULT now(),
    modified_at timestamptz NOT NULL,
    indexed_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX files_owner_path_ci ON files (owner, lower(path));

-- +goose Down
DROP TABLE files;
