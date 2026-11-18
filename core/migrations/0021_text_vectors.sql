-- +goose Up
-- Vectors of the text of documents (#70), one table per embedding model
-- (CLAUDE.md): Snowflake Arctic Embed M v2.0, 768 dimensions
-- (docs/decisions/005-modeles-locaux.md). The workers cut a document's text
-- into windows of 512 tokens that overlap by 64, and store one row per window.
-- Keyed by content like the text: identical files share their vectors, which
-- go with the text when no file has the content any more (doctext.Prune).
CREATE TABLE text_vectors_arctic_m_v2 (
    sha256 bytea NOT NULL REFERENCES document_text (sha256) ON DELETE CASCADE,
    chunk int NOT NULL CHECK (chunk >= 0),
    -- The window in document_text.text, in characters: [start_char, end_char).
    start_char int NOT NULL CHECK (start_char >= 0),
    end_char int NOT NULL CHECK (end_char > start_char),
    -- The model, and the version of its file and of the cutting
    -- (worker.textemb.version()): a content of another version is indexed again.
    model text NOT NULL CHECK (model = 'arctic-embed-m-v2.0'),
    version text NOT NULL CHECK (version <> ''),
    -- Unit length, in half precision: the cosine distance (<=>) ranks them.
    embedding halfvec(768) NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (sha256, chunk)
);

CREATE INDEX text_vectors_arctic_m_v2_embedding ON text_vectors_arctic_m_v2
    USING hnsw (embedding halfvec_cosine_ops);

-- +goose Down
DROP TABLE text_vectors_arctic_m_v2;
