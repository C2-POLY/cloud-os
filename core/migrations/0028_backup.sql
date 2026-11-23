-- +goose Up
-- The nightly backup (#101, docs/architecture/backup.md).
--
-- backup_runs: one row per run of the backup service, whatever its outcome,
-- read by the status panel (#102). A row is written when the run starts
-- (finished_at and ok empty) and completed when it ends: a row left
-- unfinished is a run that was cut off.
CREATE TABLE backup_runs (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind        text NOT NULL CHECK (kind IN ('backup', 'copy', 'check', 'restore_test')),
    started_at  timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    ok          boolean,
    -- Snapshots, files, bytes added, sample, differences: what the run saw.
    detail      jsonb NOT NULL DEFAULT '{}',
    error       text,
    CHECK ((finished_at IS NULL) = (ok IS NULL)),
    CHECK (ok IS NOT FALSE OR error IS NOT NULL)
);
CREATE INDEX backup_runs_kind_started ON backup_runs (kind, started_at DESC);

-- The backup service's own role. pg_dump reads the whole database, so the
-- role is a member of pg_read_all_data (password hashes included, as any
-- database backup); it writes backup_runs and nothing else. The core sets
-- its password at each start from CLOUDOS_BACKUP_DB_PASSWORD
-- (db.SetLoginPassword), as for cloudos_smb: it cannot log in until then.
-- A role belongs to the whole server: it may already exist.
-- +goose StatementBegin
DO $$
BEGIN
    CREATE ROLE cloudos_backup NOLOGIN INHERIT CONNECTION LIMIT 8;
EXCEPTION
    WHEN duplicate_object OR unique_violation THEN NULL;
END
$$;
-- +goose StatementEnd
GRANT pg_read_all_data TO cloudos_backup;
GRANT INSERT (kind, detail), UPDATE (finished_at, ok, detail, error) ON backup_runs TO cloudos_backup;
-- The core reads backup_runs for the status panel: it owns the table.

-- +goose Down
-- Only the run history goes: no file, no backup on the disk is touched.
DROP TABLE backup_runs;
-- The role goes when no other database of the server grants it anything.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_shdepend d JOIN pg_roles r ON r.oid = d.refobjid
         WHERE d.refclassid = 'pg_authid'::regclass AND r.rolname = 'cloudos_backup'
    ) THEN
        REVOKE pg_read_all_data FROM cloudos_backup;
        DROP ROLE IF EXISTS cloudos_backup;
    END IF;
EXCEPTION
    WHEN dependent_objects_still_exist THEN NULL;
END
$$;
-- +goose StatementEnd
