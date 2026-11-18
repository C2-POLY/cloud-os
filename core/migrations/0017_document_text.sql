-- +goose Up
-- Text of documents (PDF, Office, OpenDocument, plain text), extracted by the
-- workers without touching the file (#67). Keyed by content like thumbnails:
-- identical files share one row, a new version gets its own. Rows whose
-- content no file has any more are pruned by the core.
CREATE TABLE document_text (
    sha256 bytea PRIMARY KEY CHECK (length(sha256) = 32),
    -- The format the text was read from, recognised by the content.
    source text NOT NULL CHECK (source IN ('pdf', 'docx', 'xlsx', 'pptx', 'odt', 'ods', 'odp', 'text')),
    -- '' when the document has none (a scan, an empty file).
    text text NOT NULL,
    -- Length of text in characters, and whether the document had more.
    chars int NOT NULL CHECK (chars BETWEEN 0 AND 1000000),
    truncated boolean NOT NULL,
    -- Pages of a PDF, slides of a presentation, sheets of a spreadsheet; NULL otherwise.
    pages int CHECK (pages >= 0),
    -- False for a document without usable text: a PDF with fewer than 32
    -- visible characters per page on average (a scan, for the OCR of #68),
    -- or an empty one.
    has_text boolean NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE document_text;
