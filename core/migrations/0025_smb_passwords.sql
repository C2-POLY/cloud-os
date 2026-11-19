-- +goose Up
-- The SMB password of an account (#91, docs/architecture/auth.md).
--
-- SMB signs in with NTLMv2, which needs the server to hold the NT hash of the
-- password: MD4 of its UTF-16LE, unsalted. The account's own password is kept
-- only as argon2id and never gets one; each account may instead draw a random
-- password used by SMB alone. Only its NT hash is stored, never the password:
-- a copy of this table opens SMB, not the web interface nor WebDAV, and a
-- member revokes or replaces it from Réglages.
CREATE TABLE smb_passwords (
    account text PRIMARY KEY REFERENCES accounts (name) ON DELETE CASCADE,
    nt_hash bytea NOT NULL CHECK (length(nt_hash) = 16),
    created_at timestamptz NOT NULL DEFAULT now(),
    -- The last time an SMB client signed in with it.
    last_used_at timestamptz
);

-- +goose Down
-- Every SMB password goes: members draw a new one. No file is touched.
DROP TABLE smb_passwords;
