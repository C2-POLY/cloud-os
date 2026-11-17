-- +goose Up
-- Folders get their own rows, so an empty folder exists and a listing is a
-- lookup of direct children. Every ancestor of a row has a 'dir' row.
ALTER TABLE files ADD COLUMN kind text NOT NULL DEFAULT 'file';
ALTER TABLE files ALTER COLUMN sha256 DROP NOT NULL;
ALTER TABLE files DROP CONSTRAINT files_sha256_check;
ALTER TABLE files ADD CONSTRAINT files_kind_check CHECK (
    (kind = 'file' AND sha256 IS NOT NULL AND length(sha256) = 32)
    OR (kind = 'dir' AND sha256 IS NULL AND size = 0)
);

INSERT INTO files (owner, path, size, mime, sha256, modified_at, kind)
SELECT DISTINCT f.owner, array_to_string(parts[1:i], '/'), 0, 'inode/directory', NULL::bytea, now(), 'dir'
FROM files f
CROSS JOIN LATERAL string_to_array(f.path, '/') AS parts
CROSS JOIN LATERAL generate_series(1, array_length(parts, 1) - 1) AS i
ON CONFLICT (owner, lower(path)) DO NOTHING;

-- The journal of moves: an intention is committed before the disk is touched,
-- and finished or dropped at startup if the core stopped in between.
CREATE TABLE moves (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    owner text NOT NULL,
    from_path text NOT NULL,
    to_path text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
-- Only folder metadata goes away; no file row and no file on disk is touched.
DROP TABLE moves;

DELETE FROM files WHERE kind = 'dir';

ALTER TABLE files DROP CONSTRAINT files_kind_check;
ALTER TABLE files ALTER COLUMN sha256 SET NOT NULL;
ALTER TABLE files ADD CONSTRAINT files_sha256_check CHECK (length(sha256) = 32);
ALTER TABLE files DROP COLUMN kind;
