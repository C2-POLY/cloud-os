-- +goose Up
-- Replacing a file keeps its previous content as a version, under
-- <data>/.cloudos/versions/<owner>/<file id>/<version id>.
CREATE TABLE file_versions (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    file_id bigint NOT NULL REFERENCES files (id) ON DELETE CASCADE,
    owner text NOT NULL,
    size bigint NOT NULL CHECK (size >= 0),
    mime text NOT NULL,
    sha256 bytea NOT NULL CHECK (length(sha256) = 32),
    modified_at timestamptz NOT NULL, -- when this content was last modified
    created_at timestamptz NOT NULL DEFAULT now() -- when it was replaced
);

CREATE INDEX file_versions_file ON file_versions (file_id, created_at DESC);

ALTER TABLE moves DROP CONSTRAINT moves_op_check;
ALTER TABLE moves ADD CONSTRAINT moves_op_check CHECK (op IN ('move', 'trash', 'restore', 'version'));
ALTER TABLE moves ADD COLUMN version_id bigint;

-- A resumable upload may replace an existing file.
ALTER TABLE uploads ADD COLUMN replace_existing boolean NOT NULL DEFAULT false;

-- +goose Down
-- Refuse to drop versions that still exist: their bytes would be orphaned.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM file_versions) OR EXISTS (SELECT 1 FROM moves WHERE op = 'version') THEN
        RAISE EXCEPTION 'purge the file versions before undoing migration 7';
    END IF;
END
$$;
-- +goose StatementEnd

ALTER TABLE uploads DROP COLUMN replace_existing;
ALTER TABLE moves DROP COLUMN version_id;
ALTER TABLE moves DROP CONSTRAINT moves_op_check;
ALTER TABLE moves ADD CONSTRAINT moves_op_check CHECK (op IN ('move', 'trash', 'restore'));

DROP TABLE file_versions;
