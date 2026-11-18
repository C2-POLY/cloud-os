-- +goose Up
-- Length of a video, read by the workers with ffprobe (#41). NULL for a photo,
-- or for a video whose container does not say.
ALTER TABLE media_meta ADD COLUMN duration_ms bigint CHECK (duration_ms >= 0);

-- +goose Down
ALTER TABLE media_meta DROP COLUMN duration_ms;
