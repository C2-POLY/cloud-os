-- +goose Up
-- Where an account's files stand in the analysis (#82,
-- docs/architecture/tasks.md): the tasks of one content are found by its hash
-- in any state (the dedupe key only covers the queued and running ones), to
-- tell a failed step from a retried one. Only an index: no row changes.
CREATE INDEX tasks_kind_sha ON tasks (kind, (payload ->> 'sha256'));

-- +goose Down
DROP INDEX tasks_kind_sha;
