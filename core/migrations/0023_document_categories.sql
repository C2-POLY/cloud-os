-- +goose Up
-- The category of documents (#73): factures, contrats, impots, sante, banque,
-- travail, administratif or autre (docs/architecture/documents.md).
--
-- The workers classify a content by comparing the vectors of its text with
-- sentences that describe each category (worker/src/worker/doccat.py): one row
-- per content, like the text, gone with it when no file has the content any
-- more. version names the classifier (its sentences, its threshold and the
-- vectors' model): a row of another version is classified again.
CREATE TABLE document_categories (
    sha256 bytea PRIMARY KEY REFERENCES document_text (sha256) ON DELETE CASCADE,
    category text NOT NULL CHECK (category IN
        ('factures', 'contrats', 'impots', 'sante', 'banque', 'travail', 'administratif', 'autre')),
    -- The best cosine found (under 0.15 the document is 'autre'), and how far
    -- ahead of the next category it is.
    score real NOT NULL,
    margin real NOT NULL CHECK (margin >= 0),
    version text NOT NULL CHECK (version <> ''),
    classified_at timestamptz NOT NULL DEFAULT now()
);

-- The category a member chose for one of their files. Kept apart, so that a
-- new classification never overwrites it; by file, so that it belongs to the
-- file's account only, even when another account has the same content. A
-- file keeps its id when it is moved, renamed, put in the trash, restored or
-- replaced: the choice follows it, and goes with the purge.
CREATE TABLE file_categories (
    file_id bigint PRIMARY KEY REFERENCES files (id) ON DELETE CASCADE,
    category text NOT NULL CHECK (category IN
        ('factures', 'contrats', 'impots', 'sante', 'banque', 'travail', 'administratif', 'autre')),
    set_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
-- Only categories go: the automatic ones are computed again, the choices of
-- members are lost. No file is touched.
DROP TABLE file_categories;
DROP TABLE document_categories;
