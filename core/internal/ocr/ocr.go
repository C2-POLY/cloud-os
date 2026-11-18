// Package ocr queues the reading of photographed or scanned documents: the
// images, and the PDFs whose text #67 found missing (a scan). The workers
// read them (worker/src/worker/ocr.py) and write the text to the
// document_text table, keyed by content; this package never touches the data
// root.
//
// An image is queued when it is stored. A PDF is queued by the worker itself,
// once its text task has found no text layer (worker/src/worker/doctext.py).
package ocr

import (
	"context"
	"encoding/hex"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/tasks"
	"github.com/C2-POLY/cloud-os/core/internal/thumbs"
)

// Kind is the task kind the workers run.
const Kind = "ocr"

// imageMIMEs are the photos the workers can read (thumbs), without the videos.
var imageMIMEs = slices.DeleteFunc(thumbs.MediaMIMEs(), func(m string) bool {
	return !strings.HasPrefix(m, "image/")
})

// IsImage reports whether a file with this MIME type, or whose first bytes
// are head, is an image the workers can read: JPEG, PNG, WebP, HEIC, HEIF.
func IsImage(mime string, head []byte) bool {
	return slices.Contains(imageMIMEs, mime) || len(head) > 0 && slices.Contains(imageMIMEs, storage.SniffMIME(head))
}

// Payload is what an ocr task carries: the content to read, wherever it is.
type Payload struct {
	SHA256 string `json:"sha256"`
}

// Enqueue queues the reading of a stored image, unless it is not an image,
// its content already has a row in document_text (read, or seen to be a plain
// photo), or a task is already queued for it.
func Enqueue(ctx context.Context, q *tasks.Queue, f storage.File, head []byte, priority int) (bool, error) {
	if f.IsDir() || !IsImage(f.MIME, head) {
		return false, nil
	}
	var exists bool
	if err := q.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM document_text WHERE sha256 = $1)`, f.SHA256).Scan(&exists); err != nil || exists {
		return false, err
	}
	return enqueue(ctx, q, f.SHA256, priority)
}

func enqueue(ctx context.Context, q *tasks.Queue, sha []byte, priority int) (bool, error) {
	sum := hex.EncodeToString(sha)
	_, ok, err := q.Enqueue(ctx, Kind, Payload{SHA256: sum}, tasks.EnqueueOptions{Priority: priority, DedupeKey: Kind + ":" + sum})
	return ok, err
}

// Backfill queues, at backfill priority, every live content the OCR has not
// read yet: the images without a row in document_text, and the PDFs found
// without text (a scan) that no OCR read. Files stored before the workers
// read images, or whose task was lost or failed (tried again each time).
func Backfill(ctx context.Context, q *tasks.Queue) (int, error) {
	rows, err := q.Pool.Query(ctx,
		`SELECT DISTINCT f.sha256 FROM files f
		 WHERE f.kind = 'file' AND f.trash_id IS NULL AND f.mime = ANY($1)
		   AND NOT EXISTS (SELECT 1 FROM document_text d WHERE d.sha256 = f.sha256)
		 UNION
		 SELECT d.sha256 FROM document_text d
		 WHERE d.source = 'pdf' AND NOT d.has_text AND d.ocr_model IS NULL
		   AND EXISTS (SELECT 1 FROM files f WHERE f.kind = 'file' AND f.trash_id IS NULL AND f.sha256 = d.sha256)`,
		imageMIMEs)
	if err != nil {
		return 0, err
	}
	shas, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil {
		return 0, err
	}
	n := 0
	for _, sha := range shas {
		ok, err := enqueue(ctx, q, sha, tasks.PriorityBackfill)
		if err != nil {
			return n, err
		}
		if ok {
			n++
		}
	}
	return n, nil
}
