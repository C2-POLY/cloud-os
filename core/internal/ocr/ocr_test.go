package ocr_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/C2-POLY/cloud-os/core/internal/ocr"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/tasks"
	"github.com/C2-POLY/cloud-os/core/internal/testdb"
)

// Enough of each format for its type; the workers read the real thing.
const (
	jpeg = "\xff\xd8\xff\xe0\x00\x10JFIF\x00 an invoice"
	png  = "\x89PNG\r\n\x1a\n a screenshot"
	heic = "\x00\x00\x00\x18ftypheic\x00\x00\x00\x00 a phone photo"
	mp4  = "\x00\x00\x00\x18ftypisom\x00\x00\x00\x00 a video"
	pdf  = "%PDF-1.7\n a scan"
)

type env struct {
	ctx   context.Context
	pool  *pgxpool.Pool
	queue *tasks.Queue
	store *storage.Store
}

// newEnv returns a store whose writes queue the OCR of images, as the core's.
func newEnv(t *testing.T) env {
	t.Helper()
	pool, _ := testdb.Migrated(t)
	e := env{ctx: context.Background(), pool: pool, queue: &tasks.Queue{Pool: pool}, store: &storage.Store{Root: t.TempDir(), Pool: pool}}
	e.store.AfterStore = func(ctx context.Context, f storage.File, head []byte) {
		if _, err := ocr.Enqueue(ctx, e.queue, f, head, tasks.PriorityNew); err != nil {
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

// queued returns the ocr tasks, as "<sha256 hex>/<priority>", in order.
func (e env) queued(t *testing.T) []string {
	t.Helper()
	rows, err := e.pool.Query(e.ctx, `SELECT payload->>'sha256' || '/' || priority FROM tasks WHERE kind = 'ocr' ORDER BY id`)
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

// addRow writes what a worker writes: the text of a document (#67), or what
// the OCR found in an image (ocrModel set).
func (e env) addRow(t *testing.T, content, source string, hasText bool, ocrModel any) {
	t.Helper()
	if _, err := e.pool.Exec(e.ctx,
		`INSERT INTO document_text (sha256, source, text, chars, truncated, has_text, ocr_model) VALUES ($1, $2, '', 0, false, $3, $4)`,
		sum(content), source, hasText, ocrModel); err != nil {
		t.Fatal(err)
	}
}

func TestIsImage(t *testing.T) {
	for _, c := range []struct {
		mime, head string
		want       bool
	}{
		{"image/jpeg", "", true},
		{"image/png", "", true},
		{"image/webp", "", true},
		{"image/heic", "", true},
		{"image/heif", "", true},
		{"application/octet-stream", heic, true}, // a HEIC without its extension, by its first bytes
		{"video/mp4", "", false},
		{"video/quicktime", "", false},
		{"application/octet-stream", mp4, false},
		{"application/pdf", "", false}, // the worker queues a PDF once it found no text in it
		{"image/tiff", "", false},
		{"text/plain; charset=utf-8", "", false},
		{"", "", false},
	} {
		if got := ocr.IsImage(c.mime, []byte(c.head)); got != c.want {
			t.Errorf("IsImage(%q, %q) = %v, want %v", c.mime, c.head, got, c.want)
		}
	}
}

func TestStoringAnImageQueuesItsOCROnce(t *testing.T) {
	e := newEnv(t)
	e.put(t, "Factures/edf.jpg", jpeg)
	e.put(t, "capture.png", png)
	e.put(t, "IMG_0001.HEIC", heic)
	e.put(t, "sans-extension", heic+"!") // a HEIC by its first bytes
	e.put(t, "video.mp4", mp4)
	e.put(t, "scan.pdf", pdf)
	e.put(t, "notes.txt", "hello")
	e.put(t, "Copie de edf.jpg", jpeg) // same content: read once
	want := []string{}
	for _, c := range []string{jpeg, png, heic, heic + "!"} {
		want = append(want, hexsum(c)+"/10")
	}
	if got := e.queued(t); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("queued %v, want %v", got, want)
	}

	// A content the OCR has already looked at queues nothing.
	e.addRow(t, jpeg+"seen", "image", false, "pp-ocrv5-latin")
	e.put(t, "vacances.jpg", jpeg+"seen")
	if got := e.queued(t); len(got) != len(want) {
		t.Errorf("queued %v", got)
	}
}

func TestBackfillQueuesWhatTheOCRHasNotRead(t *testing.T) {
	e := newEnv(t)
	e.store.AfterStore = nil // files stored before the workers read images
	e.put(t, "facture.jpg", jpeg)
	e.put(t, "photo.heic", heic)
	e.put(t, "vue.jpg", jpeg+"seen")      // already looked at
	e.put(t, "scan.pdf", pdf)             // no text layer, never read by the OCR
	e.put(t, "scan-lu.pdf", pdf+"read")   // no text layer, read by the OCR
	e.put(t, "bail.pdf", pdf+"text")      // a text layer: nothing to read
	e.put(t, "sans-ligne.pdf", pdf+"new") // its text task has not run yet: not the OCR's turn
	e.put(t, "jetee.jpg", jpeg+"gone")    // in the trash
	e.put(t, "jete.pdf", pdf+"gone")      // in the trash
	e.put(t, "video.mp4", mp4)            // not an image
	e.addRow(t, jpeg+"seen", "image", false, "pp-ocrv5-latin")
	e.addRow(t, pdf, "pdf", false, nil)
	e.addRow(t, pdf+"read", "pdf", true, "pp-ocrv5-latin")
	e.addRow(t, pdf+"text", "pdf", true, nil)
	e.addRow(t, pdf+"gone", "pdf", false, nil)
	for _, p := range []string{"jetee.jpg", "jete.pdf"} {
		if _, err := e.store.Trash(e.ctx, "enzo", p); err != nil {
			t.Fatal(err)
		}
	}
	n, err := ocr.Backfill(e.ctx, e.queue)
	if err != nil || n != 3 {
		t.Fatalf("backfill = %d, %v", n, err)
	}
	got := e.queued(t)
	for _, c := range []string{jpeg, heic, pdf} {
		if !strings.Contains(strings.Join(got, " "), hexsum(c)+"/0") {
			t.Errorf("%q not queued at backfill priority: %v", c, got)
		}
	}
	if n, _ := ocr.Backfill(e.ctx, e.queue); n != 0 {
		t.Errorf("second backfill queued %d", n)
	}
}

func TestTheSchemaKeepsAnImageRowToTheOCR(t *testing.T) {
	e := newEnv(t)
	// An image's row always says which model read it.
	if _, err := e.pool.Exec(e.ctx,
		`INSERT INTO document_text (sha256, source, text, chars, truncated, has_text) VALUES ($1, 'image', '', 0, false, false)`,
		sum(jpeg)); err == nil {
		t.Error("an image row without ocr_model was accepted")
	}
}
