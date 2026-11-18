-- +goose Up
-- The embedding models the workers serve (#71, shared with the text vectors
-- of #70): each worker declares, at startup, the name, version and dimension
-- of every model it stores vectors of (worker/src/worker/vectors.py). A new
-- version sets requeued_at back to NULL; the core then queues every content
-- again for it, at backfill priority, and sets requeued_at.
CREATE TABLE embedding_models (
    name text PRIMARY KEY,
    version text NOT NULL,
    dims int NOT NULL CHECK (dims > 0),
    declared_at timestamptz NOT NULL DEFAULT now(),
    requeued_at timestamptz
);

-- What photos and videos show, as SigLIP 2 base/16 224 vectors (one table per
-- model; docs/architecture/embeddings.md). Keyed by content like thumbnails:
-- identical files share one row, a new version of a file gets its own. A
-- video is read by its key frame. Rows whose content no file has any more are
-- pruned by the core; a row of an older model version is never searched, and
-- is replaced when the content is encoded again.
CREATE TABLE photo_vectors_siglip2_b16_224 (
    sha256 bytea PRIMARY KEY CHECK (length(sha256) = 32),
    model text NOT NULL CHECK (model = 'siglip2-base-patch16-224'),
    version text NOT NULL,
    source text NOT NULL CHECK (source IN ('image', 'video')),
    embedding vector(768) NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- The vectors are unit length: the cosine distance orders them.
CREATE INDEX photo_vectors_siglip2_b16_224_embedding ON photo_vectors_siglip2_b16_224
    USING hnsw (embedding vector_cosine_ops);

-- +goose Down
DROP TABLE photo_vectors_siglip2_b16_224;
DROP TABLE embedding_models;
