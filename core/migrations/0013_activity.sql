-- +goose Up
-- What happened in each account, for the desktop's "Ce qui s'est passé":
-- lines only, never files; pruned after 90 days by the core.
CREATE TABLE activity (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    owner text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('added', 'replaced', 'trashed', 'restored', 'share_opened')),
    path text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX activity_owner_recent ON activity (owner, created_at DESC);

-- +goose Down
DROP TABLE activity;
