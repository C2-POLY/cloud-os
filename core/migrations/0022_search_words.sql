-- +goose Up
-- The vocabulary of the documents' text, to find a word typed in part or
-- with a typo (#85, docs/architecture/search.md): one row per lexeme of
-- document_text.tsv (cloudos_fr: French stem, no accents), with the number of
-- texts that have it. pg_trgm ships with PostgreSQL (PostgreSQL licence).

-- The consonant skeleton of a lexeme: its first letter, then its consonants
-- without h or w, doubled letters merged. "warhallow" and "warolo" both give
-- "wrl": a word spelled as it sounds finds the word it sounds like.
-- +goose StatementBegin
CREATE FUNCTION cloudos_sound(word text) RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT left(word, 1) || regexp_replace(regexp_replace(substr(word, 2), '[aeiouyhw]', '', 'g'), '(.)\1+', '\1', 'g')
$$;
-- +goose StatementEnd

-- Whether the vocabulary keeps a lexeme: 3 to 40 characters with a letter,
-- so not the numbers of a spreadsheet.
-- +goose StatementBegin
CREATE FUNCTION cloudos_search_word(word text) RETURNS boolean
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT length(word) BETWEEN 3 AND 40 AND word ~ '[[:alpha:]]'
$$;
-- +goose StatementEnd

CREATE TABLE search_words (
    word text PRIMARY KEY,
    -- How many rows of document_text have it; the row goes at 0. A word
    -- removed before it is counted can go below 0 for an instant: no CHECK.
    docs int NOT NULL,
    sound text GENERATED ALWAYS AS (cloudos_sound(word)) STORED
);
CREATE INDEX search_words_prefix ON search_words (word text_pattern_ops);
CREATE INDEX search_words_trgm ON search_words USING gin (word gin_trgm_ops);
CREATE INDEX search_words_sound ON search_words (sound);

-- Keeps the vocabulary with the texts, in the statement that writes them: the
-- worker's INSERT (text, OCR of an image), its UPDATE (OCR of a scan) and the
-- core's DELETE (doctext.Prune, a whole trash at once). Once per statement,
-- not per row: each word is counted once, whatever the number of texts, so a
-- statement that writes thousands of texts does not rewrite a common word
-- thousands of times. Words removed then added go as one change per word, in
-- the order of the words, so two writers lock them in the same order. Nothing
-- here can refuse a row: a text is never lost to it.
-- +goose StatementBegin
CREATE FUNCTION cloudos_search_words() RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    gone text[];
BEGIN
    IF TG_OP = 'INSERT' THEN
        INSERT INTO search_words (word, docs)
        SELECT w.word, count(*) FROM new_rows n, unnest(tsvector_to_array(n.tsv)) AS w(word)
        WHERE cloudos_search_word(w.word)
        GROUP BY w.word ORDER BY w.word
        ON CONFLICT (word) DO UPDATE SET docs = search_words.docs + EXCLUDED.docs;
        RETURN NULL;
    END IF;
    -- The words of the old texts out and, for an UPDATE, those of the new in;
    -- a DELETE has no new_rows, so each reads its own tables.
    IF TG_OP = 'DELETE' THEN
        WITH counted AS (
            INSERT INTO search_words (word, docs)
            SELECT w.word, -count(*) FROM old_rows o, unnest(tsvector_to_array(o.tsv)) AS w(word)
            WHERE cloudos_search_word(w.word)
            GROUP BY w.word ORDER BY w.word
            ON CONFLICT (word) DO UPDATE SET docs = search_words.docs + EXCLUDED.docs
            RETURNING word, docs)
        SELECT array_agg(word) INTO gone FROM counted WHERE docs <= 0;
    ELSE
        WITH change AS (
            SELECT w.word, -1 AS n FROM old_rows o, unnest(tsvector_to_array(o.tsv)) AS w(word)
            UNION ALL
            SELECT w.word, 1 FROM new_rows n, unnest(tsvector_to_array(n.tsv)) AS w(word)
        ), counted AS (
            INSERT INTO search_words (word, docs)
            SELECT c.word, sum(c.n) FROM change c
            WHERE cloudos_search_word(c.word)
            GROUP BY c.word HAVING sum(c.n) <> 0 ORDER BY c.word
            ON CONFLICT (word) DO UPDATE SET docs = search_words.docs + EXCLUDED.docs
            RETURNING word, docs)
        SELECT array_agg(word) INTO gone FROM counted WHERE docs <= 0;
    END IF;
    -- Only the words this statement took to 0, never a scan of the vocabulary.
    DELETE FROM search_words WHERE word = ANY (gone) AND docs <= 0;
    RETURN NULL;
END
$$;
-- +goose StatementEnd

-- A trigger with transition tables takes one event.
CREATE TRIGGER document_text_words_insert AFTER INSERT ON document_text
    REFERENCING NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION cloudos_search_words();
CREATE TRIGGER document_text_words_update AFTER UPDATE ON document_text
    REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION cloudos_search_words();
CREATE TRIGGER document_text_words_delete AFTER DELETE ON document_text
    REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION cloudos_search_words();

-- The texts already there.
INSERT INTO search_words (word, docs)
SELECT word, ndoc FROM ts_stat('SELECT tsv FROM document_text') WHERE cloudos_search_word(word);

-- +goose Down
DROP TRIGGER document_text_words_delete ON document_text;
DROP TRIGGER document_text_words_update ON document_text;
DROP TRIGGER document_text_words_insert ON document_text;
DROP FUNCTION cloudos_search_words();
DROP TABLE search_words;
DROP FUNCTION cloudos_search_word(text);
DROP FUNCTION cloudos_sound(text);
