-- +goose Up
-- Metadata of photos (and later videos), read by the workers from the file's
-- EXIF without touching the file. Keyed by content like thumbnails: identical
-- files share one row, a new version gets its own. Rows whose content no file
-- has any more are pruned by the core. A field the file does not carry is NULL.
CREATE TABLE media_meta (
    sha256 bytea PRIMARY KEY CHECK (length(sha256) = 32),
    -- Wall-clock time the photo was taken, as the camera wrote it (no zone),
    -- and its offset from UTC in minutes when the camera wrote one too.
    taken_at timestamp,
    taken_offset smallint CHECK (taken_offset BETWEEN -1080 AND 1080),
    latitude double precision CHECK (latitude BETWEEN -90 AND 90),
    longitude double precision CHECK (longitude BETWEEN -180 AND 180),
    make text,
    model text,
    lens text,
    -- As displayed: the EXIF orientation is applied.
    width int CHECK (width > 0),
    height int CHECK (height > 0),
    orientation smallint CHECK (orientation BETWEEN 1 AND 8),
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((latitude IS NULL) = (longitude IS NULL)),
    CHECK (taken_offset IS NULL OR taken_at IS NOT NULL)
);

-- +goose Down
DROP TABLE media_meta;
