// Package meta queues the reading of photo metadata (date taken, GPS, camera,
// dimensions) and serves it. The workers read it from the file's EXIF
// (worker/src/worker/meta.py) and write it to the media_meta table, keyed by
// content; this package never touches the data root.
package meta

import (
	"context"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/tasks"
	"github.com/C2-POLY/cloud-os/core/internal/thumbs"
)

// Kind is the task kind the workers run.
const Kind = "meta"

// ErrNotFound is returned when a content has no metadata (yet).
var ErrNotFound = errors.New("no metadata")

// Payload is what a meta task carries: the content to read, wherever it is.
type Payload struct {
	SHA256 string `json:"sha256"`
}

// Enqueue queues the metadata of a stored file, unless it is not a photo
// or a video the workers can read, its content already has its metadata, or a task is
// already queued for it.
func Enqueue(ctx context.Context, q *tasks.Queue, f storage.File, head []byte, priority int) (bool, error) {
	if f.IsDir() || !thumbs.IsMedia(f.MIME, head) {
		return false, nil
	}
	var exists bool
	if err := q.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM media_meta WHERE sha256 = $1)`, f.SHA256).Scan(&exists); err != nil || exists {
		return false, err
	}
	sum := hex.EncodeToString(f.SHA256)
	_, ok, err := q.Enqueue(ctx, Kind, Payload{SHA256: sum}, tasks.EnqueueOptions{Priority: priority, DedupeKey: Kind + ":" + sum})
	return ok, err
}

// Backfill queues, at backfill priority, the metadata of every live image
// content that has none: files stored before the workers read metadata, or
// whose task was lost.
func Backfill(ctx context.Context, q *tasks.Queue) (int, error) {
	rows, err := q.Pool.Query(ctx,
		`SELECT DISTINCT f.sha256, f.mime FROM files f
		 WHERE f.kind = 'file' AND f.trash_id IS NULL AND f.mime = ANY($1)
		   AND NOT EXISTS (SELECT 1 FROM media_meta m WHERE m.sha256 = f.sha256)`,
		thumbs.MediaMIMEs())
	if err != nil {
		return 0, err
	}
	files, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (storage.File, error) {
		var f storage.File
		return f, row.Scan(&f.SHA256, &f.MIME)
	})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, f := range files {
		ok, err := Enqueue(ctx, q, f, nil, tasks.PriorityBackfill)
		if err != nil {
			return n, err
		}
		if ok {
			n++
		}
	}
	return n, nil
}

// Prune deletes the metadata whose content no file has any more, in the
// trash or not. Rows only: metadata is never written to a file.
func Prune(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	tag, err := pool.Exec(ctx,
		`DELETE FROM media_meta m WHERE NOT EXISTS (
		     SELECT 1 FROM files f WHERE f.kind = 'file' AND f.sha256 = m.sha256)`)
	return tag.RowsAffected(), err
}

// Meta is the metadata of one content. A field the file does not carry is nil.
type Meta struct {
	// TakenAt is the wall-clock time the photo was taken, as the camera
	// wrote it; its Location is UTC but means "no zone". TakenOffset is its
	// offset from UTC in minutes, when the camera wrote one.
	TakenAt             *time.Time
	TakenOffset         *int
	Latitude, Longitude *float64
	Make, Model, Lens   *string
	Width, Height       *int
	Orientation         *int
	// DurationMS is the length of a video, in milliseconds.
	DurationMS *int64
}

// Get returns the metadata of a content, or ErrNotFound.
func Get(ctx context.Context, pool *pgxpool.Pool, sha256 []byte) (Meta, error) {
	var m Meta
	err := pool.QueryRow(ctx,
		`SELECT taken_at, taken_offset, latitude, longitude, make, model, lens, width, height, orientation, duration_ms
		 FROM media_meta WHERE sha256 = $1`, sha256).
		Scan(&m.TakenAt, &m.TakenOffset, &m.Latitude, &m.Longitude, &m.Make, &m.Model, &m.Lens, &m.Width, &m.Height, &m.Orientation, &m.DurationMS)
	if errors.Is(err, pgx.ErrNoRows) {
		return m, ErrNotFound
	}
	return m, err
}
