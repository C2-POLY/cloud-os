-- +goose Up
-- Deleted files and folders go to the trash: their rows stay in files, marked
-- with trash_id, and their bytes move under <data>/.cloudos/trash/<owner>/<id>/.
CREATE TABLE trash (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    owner text NOT NULL,
    path text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('file', 'dir')),
    size bigint NOT NULL CHECK (size >= 0),
    deleted_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);

CREATE INDEX trash_owner ON trash (owner, deleted_at DESC);
CREATE INDEX trash_expires_at ON trash (expires_at);

ALTER TABLE files ADD COLUMN trash_id bigint REFERENCES trash (id);
CREATE INDEX files_trash_id ON files (trash_id) WHERE trash_id IS NOT NULL;

-- A trashed row frees its path: only live rows must be unique.
DROP INDEX files_owner_path_ci;
CREATE UNIQUE INDEX files_owner_path_ci ON files (owner, lower(path)) WHERE trash_id IS NULL;

-- The journal now also records moves to and from the trash.
ALTER TABLE moves ADD COLUMN op text NOT NULL DEFAULT 'move' CHECK (op IN ('move', 'trash', 'restore'));
ALTER TABLE moves ADD COLUMN trash_id bigint;

-- +goose Down
-- Refuse to drop what the trash holds: its rows would claim paths already reused.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM trash) OR EXISTS (SELECT 1 FROM moves WHERE op <> 'move') THEN
        RAISE EXCEPTION 'empty or restore the trash before undoing migration 6';
    END IF;
END
$$;
-- +goose StatementEnd

ALTER TABLE moves DROP COLUMN trash_id;
ALTER TABLE moves DROP COLUMN op;

DROP INDEX files_owner_path_ci;
CREATE UNIQUE INDEX files_owner_path_ci ON files (owner, lower(path));

DROP INDEX files_trash_id;
ALTER TABLE files DROP COLUMN trash_id;

DROP TABLE trash;
