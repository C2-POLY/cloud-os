// Package doctext queues the extraction of the text of documents (PDF,
// Office, OpenDocument, plain text). The workers read it
// (worker/src/worker/doctext.py) and write it to the document_text table,
// keyed by content; this package never touches the data root.
package doctext

import (
	"context"
	"encoding/hex"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/tasks"
)

// Kind is the task kind the workers run.
const Kind = "text"

// documentMIMEs are the types whose text the workers read, without their
// parameters ("; charset=utf-8"): pypdf for PDF, the standard library for
// Office (OOXML), OpenDocument and plain text.
var documentMIMEs = []string{
	"application/pdf",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation",
	"application/vnd.oasis.opendocument.text",
	"application/vnd.oasis.opendocument.spreadsheet",
	"application/vnd.oasis.opendocument.presentation",
	"text/plain", "text/markdown", "text/csv", "text/tab-separated-values",
}

// IsDocument reports whether a file of this MIME type is a document whose
// text the workers read. The type is the one storage gave the file: by its
// extension, or by its first bytes when the extension is unknown.
func IsDocument(mime string) bool {
	base, _, _ := strings.Cut(mime, ";")
	return slices.Contains(documentMIMEs, strings.TrimSpace(base))
}

// Payload is what a text task carries: the content to read, wherever it is.
type Payload struct {
	SHA256 string `json:"sha256"`
}

// Enqueue queues the text of a stored file, unless it is not a document, its
// content already has its text, or a task is already queued for it.
func Enqueue(ctx context.Context, q *tasks.Queue, f storage.File, priority int) (bool, error) {
	if f.IsDir() || !IsDocument(f.MIME) {
		return false, nil
	}
	var exists bool
	if err := q.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM document_text WHERE sha256 = $1)`, f.SHA256).Scan(&exists); err != nil || exists {
		return false, err
	}
	sum := hex.EncodeToString(f.SHA256)
	_, ok, err := q.Enqueue(ctx, Kind, Payload{SHA256: sum}, tasks.EnqueueOptions{Priority: priority, DedupeKey: Kind + ":" + sum})
	return ok, err
}

// Backfill queues, at backfill priority, the text of every live document
// content that has none: files stored before the workers read documents, or
// whose task was lost or failed.
func Backfill(ctx context.Context, q *tasks.Queue) (int, error) {
	rows, err := q.Pool.Query(ctx,
		`SELECT DISTINCT f.sha256, f.mime FROM files f
		 WHERE f.kind = 'file' AND f.trash_id IS NULL AND trim(split_part(f.mime, ';', 1)) = ANY($1)
		   AND NOT EXISTS (SELECT 1 FROM document_text d WHERE d.sha256 = f.sha256)`,
		documentMIMEs)
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
		ok, err := Enqueue(ctx, q, f, tasks.PriorityBackfill)
		if err != nil {
			return n, err
		}
		if ok {
			n++
		}
	}
	return n, nil
}

// Prune deletes the text whose content no file has any more, in the trash or
// not. Rows only: the text is never written to a file.
func Prune(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	tag, err := pool.Exec(ctx,
		`DELETE FROM document_text d WHERE NOT EXISTS (
		     SELECT 1 FROM files f WHERE f.kind = 'file' AND f.sha256 = d.sha256)`)
	return tag.RowsAffected(), err
}
