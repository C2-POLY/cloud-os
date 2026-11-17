// Package thumbs queues the thumbnails of image files and serves them. The
// workers make them (worker/src/worker/thumbs.py) and write them to the
// thumbnails table, keyed by content; this package never touches the data root.
package thumbs

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/tasks"
)

// Kind is the task kind the workers run.
const Kind = "thumb"

// Size is the longest side of a thumbnail, in pixels.
const Size = 256

// formats are the image types the workers can read (HEIC: issue #30).
var formats = map[string]bool{"image/jpeg": true, "image/png": true, "image/webp": true}

// imageMIMEs is formats as a list, for SQL.
var imageMIMEs = []string{"image/jpeg", "image/png", "image/webp"}

// ErrNotFound is returned when a content has no thumbnail (yet).
var ErrNotFound = errors.New("no thumbnail")

// IsImage reports whether a file with this MIME type, or whose first bytes
// are head, is an image the workers can read.
func IsImage(mime string, head []byte) bool {
	return formats[mime] || len(head) > 0 && formats[http.DetectContentType(head)]
}

// Payload is what a thumb task carries: the content to read, wherever it is.
type Payload struct {
	SHA256 string `json:"sha256"`
}

// Enqueue queues the thumbnail of a stored file, unless it is not an image,
// its content already has one, or one is already queued for it.
func Enqueue(ctx context.Context, q *tasks.Queue, f storage.File, head []byte, priority int) (bool, error) {
	if f.IsDir() || !IsImage(f.MIME, head) {
		return false, nil
	}
	var exists bool
	if err := q.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM thumbnails WHERE sha256 = $1 AND size = $2)`, f.SHA256, Size).Scan(&exists); err != nil || exists {
		return false, err
	}
	sum := hex.EncodeToString(f.SHA256)
	_, ok, err := q.Enqueue(ctx, Kind, Payload{SHA256: sum}, tasks.EnqueueOptions{Priority: priority, DedupeKey: Kind + ":" + sum})
	return ok, err
}

// Backfill queues, at backfill priority, the thumbnail of every live image
// content that has none: the catch-up of files stored before the workers
// made thumbnails, or whose task was lost.
func Backfill(ctx context.Context, q *tasks.Queue) (int, error) {
	rows, err := q.Pool.Query(ctx,
		`SELECT DISTINCT f.sha256, f.mime FROM files f
		 WHERE f.kind = 'file' AND f.trash_id IS NULL AND f.mime = ANY($1)
		   AND NOT EXISTS (SELECT 1 FROM thumbnails t WHERE t.sha256 = f.sha256 AND t.size = $2)`,
		imageMIMEs, Size)
	if err != nil {
		return 0, err
	}
	var files []storage.File
	for rows.Next() {
		var f storage.File
		if err := rows.Scan(&f.SHA256, &f.MIME); err != nil {
			rows.Close()
			return 0, err
		}
		files = append(files, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
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

// Prune deletes the thumbnails whose content no file has any more, in the
// trash or not. Rows only: thumbnails are never files.
func Prune(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	tag, err := pool.Exec(ctx,
		`DELETE FROM thumbnails t WHERE NOT EXISTS (
		     SELECT 1 FROM files f WHERE f.kind = 'file' AND f.sha256 = t.sha256)`)
	return tag.RowsAffected(), err
}

// Thumb is one stored thumbnail.
type Thumb struct {
	MIME          string
	Width, Height int
	Bytes         []byte
	CreatedAt     time.Time
}

// Get returns the thumbnail of a content, or ErrNotFound.
func Get(ctx context.Context, pool *pgxpool.Pool, sha256 []byte) (Thumb, error) {
	var t Thumb
	err := pool.QueryRow(ctx, `SELECT mime, width, height, bytes, created_at FROM thumbnails WHERE sha256 = $1 AND size = $2`, sha256, Size).
		Scan(&t.MIME, &t.Width, &t.Height, &t.Bytes, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}
