-- +goose Up
-- A share link gives a guest read access to one file or folder, by the id of
-- its row in files: it follows the item when it is renamed or moved, gives
-- nothing while the item is in the trash, and goes when the item is purged.
-- Only the SHA-256 of a link's token is stored: a copy of the database opens no link.
CREATE TABLE shares (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    token_hash bytea NOT NULL UNIQUE CHECK (length(token_hash) = 32),
    owner text NOT NULL REFERENCES accounts (name) ON DELETE CASCADE,
    file_id bigint NOT NULL REFERENCES files (id) ON DELETE CASCADE,
    password_hash text, -- argon2id; NULL when the link has no password
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX shares_owner ON shares (owner, created_at DESC);
CREATE INDEX shares_file ON shares (file_id);
CREATE INDEX shares_expires_at ON shares (expires_at);

-- An access opened by the password of a link, held by the guest in a cookie.
CREATE TABLE share_grants (
    token_hash bytea PRIMARY KEY CHECK (length(token_hash) = 32),
    share_id bigint NOT NULL REFERENCES shares (id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL
);

CREATE INDEX share_grants_share ON share_grants (share_id);

-- +goose Down
-- Only links go: no file is touched.
DROP TABLE share_grants;

DROP TABLE shares;
