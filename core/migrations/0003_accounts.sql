-- +goose Up
-- An account's name is also its folder under the data root (see storage.CleanOwner).
CREATE TABLE accounts (
    name text PRIMARY KEY CHECK (name ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
    display_name text NOT NULL,
    password_hash text NOT NULL,
    role text NOT NULL CHECK (role IN ('admin', 'member')),
    disabled_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Only the SHA-256 of a session token is stored: a copy of the database opens no session.
CREATE TABLE sessions (
    token_hash bytea PRIMARY KEY CHECK (length(token_hash) = 32),
    account text NOT NULL REFERENCES accounts (name) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);

CREATE INDEX sessions_account ON sessions (account);

-- +goose Down
DROP TABLE sessions;

DROP TABLE accounts;
