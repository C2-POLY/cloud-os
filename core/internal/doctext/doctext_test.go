package doctext_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/C2-POLY/cloud-os/core/internal/doctext"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/tasks"
	"github.com/C2-POLY/cloud-os/core/internal/testdb"
)

// Enough of each format for its type; the workers read the real thing.
const (
	pdf  = "%PDF-1.7\n a lease"
	docx = "PK\x03\x04 a letter"
	odt  = "PK\x03\x04mimetypeapplication/vnd.oasis.opendocument.text a letter"
	jpeg = "\xff\xd8\xff\xe0\x00\x10JFIF\x00 a photo"
	zip  = "PK\x03\x04 an archive"
)

type env struct {
	ctx   context.Context
	pool  *pgxpool.Pool
	queue *tasks.Queue
	store *storage.Store
}

// newEnv returns a store whose writes queue the text of documents, as the core's.
func newEnv(t *testing.T) env {
	t.Helper()
	pool, _ := testdb.Migrated(t)
	e := env{ctx: context.Background(), pool: pool, queue: &tasks.Queue{Pool: pool}, store: &storage.Store{Root: t.TempDir(), Pool: pool}}
	e.store.AfterStore = func(ctx context.Context, f storage.File, _ []byte) {
		if _, err := doctext.Enqueue(ctx, e.queue, f, tasks.PriorityNew); err != nil {
			t.Error(err)
		}
	}
	return e
}

func (e env) put(t *testing.T, path, content string) {
	t.Helper()
	if _, err := e.store.Put(e.ctx, "enzo", path, strings.NewReader(content), storage.NoExpectations()); err != nil {
		t.Fatal(err)
	}
}

// queued returns the text tasks, as "<sha256 hex>/<priority>", in order.
func (e env) queued(t *testing.T) []string {
	t.Helper()
	rows, err := e.pool.Query(e.ctx, `SELECT payload->>'sha256' || '/' || priority FROM tasks WHERE kind = 'text' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func sum(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

func hexsum(s string) string { return hex.EncodeToString(sum(s)) }

// addText writes what a worker writes for a document.
func (e env) addText(t *testing.T, content string) {
	t.Helper()
	if _, err := e.pool.Exec(e.ctx,
		`INSERT INTO document_text (sha256, source, text, chars, truncated, has_text) VALUES ($1, 'text', 'x', 1, false, true)`,
		sum(content)); err != nil {
		t.Fatal(err)
	}
}

func TestIsDocument(t *testing.T) {
	for mime, want := range map[string]bool{
		"application/pdf": true,
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   true,
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         true,
		"application/vnd.openxmlformats-officedocument.presentationml.presentation": true,
		"application/vnd.oasis.opendocument.text":                                   true,
		"application/vnd.oasis.opendocument.spreadsheet":                            true,
		"application/vnd.oasis.opendocument.presentation":                           true,
		"text/plain; charset=utf-8":                                                 true, // as storage gives it
		"text/markdown; charset=utf-8":                                              true,
		"text/csv":                                                                  true,
		"text/tab-separated-values":                                                 true,
		"text/html; charset=utf-8":                                                  false,
		"application/zip":                                                           false,
		"application/msword":                                                        false, // the old binary .doc: not read
		"image/jpeg":                                                                false,
		"":                                                                          false,
	} {
		if got := doctext.IsDocument(mime); got != want {
			t.Errorf("IsDocument(%q) = %v, want %v", mime, got, want)
		}
	}
}

func TestStoringADocumentQueuesItsTextOnce(t *testing.T) {
	e := newEnv(t)
	e.put(t, "Contrats/bail.pdf", pdf)
	e.put(t, "lettre.docx", docx)
	e.put(t, "lettre.odt", odt)
	e.put(t, "notes.md", "# Courses\n")
	e.put(t, "comptes.csv", "date;montant\n")
	e.put(t, "notes", "hello, no extension") // plain text, by its content
	e.put(t, "photo.jpg", jpeg)
	e.put(t, "archive.zip", zip)
	e.put(t, "Copie de bail.pdf", pdf) // same content: same text
	want := []string{}
	for _, c := range []string{pdf, docx, odt, "# Courses\n", "date;montant\n", "hello, no extension"} {
		want = append(want, hexsum(c)+"/10")
	}
	if got := e.queued(t); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("queued %v, want %v", got, want)
	}

	// A content that already has its text queues nothing.
	e.addText(t, pdf+"!")
	e.put(t, "autre.pdf", pdf+"!")
	if got := e.queued(t); len(got) != len(want) {
		t.Errorf("queued %v", got)
	}
}

func TestBackfillQueuesLiveDocumentsWithoutText(t *testing.T) {
	e := newEnv(t)
	e.store.AfterStore = nil // files stored before the workers read documents
	e.put(t, "bail.pdf", pdf)
	e.put(t, "lettre.docx", docx)
	e.put(t, "notes.txt", "hello")
	e.put(t, "done.pdf", pdf+"done")
	e.put(t, "gone.pdf", pdf+"gone")
	e.put(t, "photo.jpg", jpeg)
	e.addText(t, pdf+"done")
	if _, err := e.store.Trash(e.ctx, "enzo", "gone.pdf"); err != nil {
		t.Fatal(err)
	}
	n, err := doctext.Backfill(e.ctx, e.queue)
	if err != nil || n != 3 {
		t.Fatalf("backfill = %d, %v", n, err)
	}
	for _, q := range e.queued(t) {
		if !strings.HasSuffix(q, "/0") {
			t.Errorf("%s is not at backfill priority", q)
		}
	}
	if n, _ := doctext.Backfill(e.ctx, e.queue); n != 0 {
		t.Errorf("second backfill queued %d", n)
	}
}

func TestPruneKeepsTextOfStoredContents(t *testing.T) {
	e := newEnv(t)
	e.put(t, "kept.pdf", pdf)
	e.put(t, "trashed.pdf", pdf+"t")
	e.put(t, "deleted.pdf", pdf+"d")
	for _, c := range []string{pdf, pdf + "t", pdf + "d", pdf + "never stored"} {
		e.addText(t, c)
	}
	if _, err := e.store.Trash(e.ctx, "enzo", "trashed.pdf"); err != nil {
		t.Fatal(err)
	}
	// What a purge leaves: no row for the content any more.
	if _, err := e.pool.Exec(e.ctx, `DELETE FROM files WHERE path = 'deleted.pdf'`); err != nil {
		t.Fatal(err)
	}
	if n, err := doctext.Prune(e.ctx, e.pool); err != nil || n != 2 {
		t.Fatalf("prune = %d, %v", n, err)
	}
	for c, want := range map[string]bool{pdf: true, pdf + "t": true, pdf + "d": false} {
		var kept bool
		if err := e.pool.QueryRow(e.ctx, `SELECT EXISTS (SELECT 1 FROM document_text WHERE sha256 = $1)`, sum(c)).Scan(&kept); err != nil {
			t.Fatal(err)
		}
		if kept != want {
			t.Errorf("%q kept = %v, want %v", c, kept, want)
		}
	}
}
