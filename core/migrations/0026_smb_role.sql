-- +goose Up
-- The smb service's own database role (#97, docs/architecture/smb.md).
--
-- The smb service (`core smb`) reads accounts, SMB passwords and the index,
-- and notes when an SMB password was last used: nothing else. It connects as
-- cloudos_smb, which can do exactly that, so a fault in SMB cannot write the
-- index, the accounts, the web sessions or the task queue, nor read the
-- accounts' password hashes. The core sets the role's password at each start,
-- from CLOUDOS_SMB_DB_PASSWORD (db.SetLoginPassword): a migration cannot read
-- the environment, and the role cannot log in until then.
--
-- A role belongs to the whole Postgres server, not to one database: it may
-- already exist (another database of the server, such as a test's).
-- +goose StatementBegin
DO $$
BEGIN
    CREATE ROLE cloudos_smb NOLOGIN NOINHERIT CONNECTION LIMIT 8;
EXCEPTION
    -- unique_violation: created at the same moment by another database's migration
    WHEN duplicate_object OR unique_violation THEN NULL;
END
$$;
-- +goose StatementEnd

-- auth.SMBHash and Sweep: whether the account exists and is active, never its password hash.
GRANT SELECT (name, disabled_at) ON accounts TO cloudos_smb;
-- auth.SMBHash and Sweep read the NT hash; SMBCredential.Succeeded notes its last use.
GRANT SELECT (account, nt_hash), UPDATE (last_used_at) ON smb_passwords TO cloudos_smb;
-- smbfs.View, through storage.Lookup and storage.List.
GRANT SELECT ON files TO cloudos_smb;
-- migrate.HasPending: core smb refuses to start before the core has migrated.
-- goose always has its table here; the worker's tests apply these files
-- without goose, hence the check.
-- +goose StatementBegin
DO $$
BEGIN
    IF to_regclass('goose_db_version') IS NOT NULL THEN
        GRANT SELECT ON goose_db_version TO cloudos_smb;
    END IF;
END
$$;
-- +goose StatementEnd
-- LISTEN smb_revoke needs no grant. Tables added later get nothing by default:
-- a migration that gives the smb service more says so (#92 for its writes).

-- +goose Down
-- Only rights go: no row, no file is touched. The role itself goes when no
-- other database of the server grants it anything.
REVOKE ALL ON accounts, smb_passwords, files, goose_db_version FROM cloudos_smb;
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_shdepend d JOIN pg_roles r ON r.oid = d.refobjid
         WHERE d.refclassid = 'pg_authid'::regclass AND r.rolname = 'cloudos_smb'
    ) THEN
        DROP ROLE IF EXISTS cloudos_smb;
    END IF;
EXCEPTION
    -- Another database granted it something in the meantime: it stays
    WHEN dependent_objects_still_exist THEN NULL;
END
$$;
-- +goose StatementEnd
