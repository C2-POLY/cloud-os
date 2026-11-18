-- +goose Up
-- Full-text search in the text of documents (#69): French stemming, accents
-- and case ignored. The text search configuration, unaccent and the GIN index
-- ship with PostgreSQL (PostgreSQL licence).

-- French, with accents removed before stemming: "Électricité" and
-- "electricite" both give 'electricit'. Only the token types that can hold a
-- non-ASCII letter need unaccent; the others keep french's own mapping.
CREATE TEXT SEARCH CONFIGURATION cloudos_fr (COPY = pg_catalog.french);
ALTER TEXT SEARCH CONFIGURATION cloudos_fr
    ALTER MAPPING FOR hword, hword_part, word WITH public.unaccent, french_stem;

-- The tsvector of a document's text. A tsvector holds at most 1 MB of
-- lexemes and positions: a long sheet of distinct numbers can exceed it, and
-- the error would fail the worker's INSERT, losing the text as well. Then only
-- the start is indexed, which always fits. The slice is taken from a full copy
-- (doc || ''): left() on a stored, compressed text can fail on PostgreSQL 17.8
-- when the slice cuts an accented character (fixed in 17.9).
-- +goose StatementBegin
CREATE FUNCTION cloudos_doc_tsv(doc text) RETURNS tsvector
LANGUAGE plpgsql IMMUTABLE STRICT
AS $$
BEGIN
    RETURN to_tsvector('public.cloudos_fr', doc);
EXCEPTION WHEN program_limit_exceeded THEN
    RETURN to_tsvector('public.cloudos_fr', left(doc || '', 100000));
END
$$;
-- +goose StatementEnd

-- Computed when the worker writes the row, and here for the rows already there.
ALTER TABLE document_text ADD COLUMN tsv tsvector GENERATED ALWAYS AS (cloudos_doc_tsv(text)) STORED;
CREATE INDEX document_text_tsv ON document_text USING gin (tsv);

-- +goose Down
DROP INDEX document_text_tsv;
ALTER TABLE document_text DROP COLUMN tsv;
DROP FUNCTION cloudos_doc_tsv(text);
DROP TEXT SEARCH CONFIGURATION cloudos_fr;
