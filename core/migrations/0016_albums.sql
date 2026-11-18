-- +goose Up
-- Albums (#39): an album only refers to rows of files, by id, and never holds
-- a copy. A file keeps its id when it is moved, put in the trash or restored:
-- the queries of an album read live files only (trash_id IS NULL), so a photo
-- in the trash leaves its albums and comes back with its restore, and the
-- purge of the trash removes it from them (ON DELETE CASCADE).
CREATE TABLE albums (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    owner text NOT NULL REFERENCES accounts (name) ON DELETE CASCADE,
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX albums_owner_name_ci ON albums (owner, lower(name));

CREATE TABLE album_items (
    album_id bigint NOT NULL REFERENCES albums (id) ON DELETE CASCADE,
    file_id bigint NOT NULL REFERENCES files (id) ON DELETE CASCADE,
    added_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (album_id, file_id)
);

CREATE INDEX album_items_file ON album_items (file_id);

-- +goose Down
-- Only albums go: no file is touched.
DROP TABLE album_items;

DROP TABLE albums;
