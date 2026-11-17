-- +goose Up
-- Search by name and path, ignoring accents and case: "ete" finds "Été 2024".
-- unaccent and pg_trgm ship with PostgreSQL (PostgreSQL licence).
CREATE EXTENSION IF NOT EXISTS unaccent;
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- unaccent() is only STABLE (its dictionary could change); naming the
-- dictionary makes this wrapper safe to declare IMMUTABLE, so it can be indexed.
-- +goose StatementBegin
CREATE FUNCTION cloudos_fold(text) RETURNS text
LANGUAGE sql IMMUTABLE PARALLEL SAFE STRICT
AS $$ SELECT lower(public.unaccent('public.unaccent'::regdictionary, $1)) $$;
-- +goose StatementEnd

CREATE INDEX files_path_fold ON files USING gin (cloudos_fold(path) gin_trgm_ops) WHERE trash_id IS NULL;

-- +goose Down
DROP INDEX files_path_fold;
DROP FUNCTION cloudos_fold(text);
DROP EXTENSION IF EXISTS pg_trgm;
DROP EXTENSION IF EXISTS unaccent;
