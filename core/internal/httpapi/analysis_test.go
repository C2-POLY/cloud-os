package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/C2-POLY/cloud-os/core/internal/analysis"
	"github.com/C2-POLY/cloud-os/core/internal/auth"
	"github.com/C2-POLY/cloud-os/core/internal/share"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/tasks"
	"github.com/C2-POLY/cloud-os/core/internal/testdb"
)

// newAnalysisAPI is an API with two accounts, enzo (admin) and johan, and the
// queue the workers share.
func newAnalysisAPI(t *testing.T) (enzo, johan *client, files *storage.Store, q *tasks.Queue) {
	t.Helper()
	pool, _ := testdb.Migrated(t)
	files = &storage.Store{Root: t.TempDir(), Pool: pool}
	h := NewHandler(Config{
		Version: "test", Health: &fakeStore{}, Auth: auth.NewService(pool),
		Files: files, Shares: share.NewService(pool, files), Analysis: &analysis.Reader{Pool: pool},
	})
	enzo = &client{t: t, h: h}
	enzo.do("POST", "/api/setup", `{"name":"enzo","password":"`+pw+`"}`)
	enzo.do("POST", "/api/accounts", `{"name":"johan","password":"`+pw+`"}`)
	johan = &client{t: t, h: h}
	johan.do("POST", "/api/login", `{"name":"johan","password":"`+pw+`"}`)
	return enzo, johan, files, &tasks.Queue{Pool: pool}
}

// stored puts a file in an account's space and returns the hex of its content.
func stored(t *testing.T, files *storage.Store, owner, path, content string) string {
	t.Helper()
	if _, err := files.Put(context.Background(), owner, path, strings.NewReader(content), storage.NoExpectations()); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// queue adds a task of kind for a content, as the core or a worker does.
func queue(t *testing.T, q *tasks.Queue, kind, sha string) int64 {
	t.Helper()
	id, ok, err := q.Enqueue(context.Background(), kind, map[string]string{"sha256": sha},
		tasks.EnqueueOptions{Priority: tasks.PriorityNew, DedupeKey: kind + ":" + sha})
	if err != nil || !ok {
		t.Fatalf("enqueue %s: %v %v", kind, ok, err)
	}
	return id
}

func analysisOf(t *testing.T, c *client, path string) analysis.FileStatus {
	t.Helper()
	rec := c.do("GET", "/api/analysis/file?path="+url.QueryEscape(path), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("analysis of %s: %d %s", path, rec.Code, rec.Body)
	}
	return decode[analysis.FileStatus](t, rec.Body.String())
}

func summaryOf(t *testing.T, c *client) analysis.Summary {
	t.Helper()
	rec := c.do("GET", "/api/analysis", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("summary: %d %s", rec.Code, rec.Body)
	}
	return decode[analysis.Summary](t, rec.Body.String())
}

// states lists "kind:state" of the steps, in order.
func states(st analysis.FileStatus) []string {
	var out []string
	for _, s := range st.Steps {
		out = append(out, s.Kind+":"+s.State)
	}
	return out
}

func wantSteps(t *testing.T, what string, st analysis.FileStatus, state string, want ...string) {
	t.Helper()
	if got := states(st); !slices.Equal(got, want) || st.State != state {
		t.Errorf("%s: %s %v, want %s %v", what, st.State, got, state, want)
	}
}

func TestDocumentAnalysisGoesFromWaitingToDone(t *testing.T) {
	enzo, _, files, q := newAnalysisAPI(t)
	ctx := context.Background()
	sha := stored(t, files, "enzo", "Contrats/bail.pdf", "%PDF bail")
	shaBytes, _ := hex.DecodeString(sha)

	// Dropped: the text is queued, so the steps after it wait for it.
	queue(t, q, "text", sha)
	wantSteps(t, "queued", analysisOf(t, enzo, "Contrats/bail.pdf"), "pending",
		"text:waiting", "textemb:waiting", "doccat:waiting")
	if s := summaryOf(t, enzo); s != (analysis.Summary{Analysing: 1}) {
		t.Errorf("summary = %+v", s)
	}

	// A worker takes it.
	task, err := q.Claim(ctx, "w1", []string{"text"})
	if err != nil || task == nil {
		t.Fatalf("claim: %v %v", task, err)
	}
	wantSteps(t, "running", analysisOf(t, enzo, "Contrats/bail.pdf"), "pending",
		"text:running", "textemb:waiting", "doccat:waiting")

	// The text is read and the vectors are queued by the worker.
	if _, err := files.Pool.Exec(ctx, `INSERT INTO document_text (sha256, source, text, chars, truncated, has_text)
		VALUES ($1, 'pdf', 'Contrat de location', 19, false, true)`, shaBytes); err != nil {
		t.Fatal(err)
	}
	if err := q.Complete(ctx, task.ID, "w1"); err != nil {
		t.Fatal(err)
	}
	queue(t, q, "textemb", sha)
	wantSteps(t, "text done", analysisOf(t, enzo, "Contrats/bail.pdf"), "pending",
		"text:done", "textemb:waiting", "doccat:waiting")

	// The vectors and the category are written in one transaction.
	task, _ = q.Claim(ctx, "w1", []string{"textemb"})
	if _, err := files.Pool.Exec(ctx, `INSERT INTO text_vectors_arctic_m_v2 (sha256, chunk, start_char, end_char, model, version, embedding)
		VALUES ($1, 0, 0, 19, 'arctic-embed-m-v2.0', 'v', array_fill(0.036, ARRAY[768])::halfvec)`, shaBytes); err != nil {
		t.Fatal(err)
	}
	if _, err := files.Pool.Exec(ctx, `INSERT INTO document_categories (sha256, category, score, margin, version) VALUES ($1, 'contrats', 0.4, 0.1, 'c1')`, shaBytes); err != nil {
		t.Fatal(err)
	}
	if err := q.Complete(ctx, task.ID, "w1"); err != nil {
		t.Fatal(err)
	}
	wantSteps(t, "done", analysisOf(t, enzo, "Contrats/bail.pdf"), "ready",
		"text:done", "textemb:done", "doccat:done")
	if s := summaryOf(t, enzo); s != (analysis.Summary{}) {
		t.Errorf("summary once done = %+v", s)
	}
}

func TestAFailedStepSaysWhyAndIsCounted(t *testing.T) {
	enzo, _, files, q := newAnalysisAPI(t)
	ctx := context.Background()
	sha := stored(t, files, "enzo", "scan.pdf", "%PDF cassé")
	id := queue(t, q, "text", sha)
	if task, _ := q.Claim(ctx, "w1", []string{"text"}); task == nil || task.ID != id {
		t.Fatalf("claim: %+v", task)
	}
	if err := q.GiveUp(ctx, id, "w1", "PdfReadError: EOF marker not found"); err != nil {
		t.Fatal(err)
	}

	st := analysisOf(t, enzo, "scan.pdf")
	wantSteps(t, "failed", st, "failed", "text:failed", "textemb:idle", "doccat:idle")
	if got := st.Steps[0]; got.Error != "PdfReadError: EOF marker not found" || got.Attempts != 1 || got.MaxAttempts != 5 {
		t.Errorf("the failed step = %+v", got)
	}
	if s := summaryOf(t, enzo); s != (analysis.Summary{Failed: 1}) {
		t.Errorf("summary = %+v", s)
	}

	// A later task that works clears the failure, in the file and in the count.
	queue(t, q, "text", sha)
	task, _ := q.Claim(ctx, "w1", []string{"text"})
	if err := q.Complete(ctx, task.ID, "w1"); err != nil {
		t.Fatal(err)
	}
	if s := summaryOf(t, enzo); s != (analysis.Summary{}) {
		t.Errorf("summary after a new try = %+v", s)
	}
	if st := analysisOf(t, enzo, "scan.pdf"); st.Steps[0].State != "done" {
		t.Errorf("text after a new try: %+v", st.Steps[0])
	}
}

func TestARetriedStepKeepsItsLastError(t *testing.T) {
	enzo, _, files, q := newAnalysisAPI(t)
	ctx := context.Background()
	sha := stored(t, files, "enzo", "Photos/a.jpg", "jpeg a")
	for _, k := range []string{"thumb", "meta", "ocr", "photovec"} {
		queue(t, q, k, sha)
	}
	task, _ := q.Claim(ctx, "w1", []string{"thumb"})
	if err := q.Fail(ctx, task.ID, "w1", "OSError: "+strings.Repeat("x", 500)); err != nil {
		t.Fatal(err)
	}
	st := analysisOf(t, enzo, "Photos/a.jpg")
	wantSteps(t, "retry", st, "pending", "thumb:waiting", "meta:waiting", "ocr:waiting", "photovec:waiting")
	got := st.Steps[0]
	if !strings.HasPrefix(got.Error, "OSError: xxx") || len([]rune(got.Error)) != analysis.MaxError+1 || got.Attempts != 1 {
		t.Errorf("the retried step = %+v", got)
	}
}

func TestStepsFollowTheTypeOfFile(t *testing.T) {
	enzo, _, files, q := newAnalysisAPI(t)
	ctx := context.Background()

	// A photo: thumbnail, metadata, OCR, what it shows. Its text is only a
	// step once the OCR found some.
	photo := stored(t, files, "enzo", "Photos/plage.jpg", "jpeg plage")
	photoSum, _ := hex.DecodeString(photo)
	wantSteps(t, "photo", analysisOf(t, enzo, "Photos/plage.jpg"), "ready", "thumb:idle", "meta:idle", "ocr:idle", "photovec:idle")
	if _, err := files.Pool.Exec(ctx, `INSERT INTO thumbnails (sha256, size, mime, width, height, bytes) VALUES ($1, 256, 'image/jpeg', 1, 1, '\x00')`, photoSum); err != nil {
		t.Fatal(err)
	}
	if _, err := files.Pool.Exec(ctx, `INSERT INTO media_meta (sha256) VALUES ($1)`, photoSum); err != nil {
		t.Fatal(err)
	}
	if _, err := files.Pool.Exec(ctx, `INSERT INTO document_text (sha256, source, text, chars, truncated, has_text, ocr_model) VALUES ($1, 'image', '', 0, false, false, 'm')`, photoSum); err != nil {
		t.Fatal(err)
	}
	queue(t, q, "photovec", photo)
	wantSteps(t, "photo, one step left", analysisOf(t, enzo, "Photos/plage.jpg"), "pending", "thumb:done", "meta:done", "ocr:done", "photovec:waiting")

	// A photo of a letter: the OCR found text, so its meaning is indexed too.
	letter := stored(t, files, "enzo", "Photos/lettre.png", "png lettre")
	letterSum, _ := hex.DecodeString(letter)
	if _, err := files.Pool.Exec(ctx, `INSERT INTO document_text (sha256, source, text, chars, truncated, has_text, ocr_model) VALUES ($1, 'image', 'Cher Enzo', 9, false, true, 'm')`, letterSum); err != nil {
		t.Fatal(err)
	}
	queue(t, q, "textemb", letter)
	wantSteps(t, "photo of a letter", analysisOf(t, enzo, "Photos/lettre.png"), "pending",
		"thumb:idle", "meta:idle", "ocr:done", "photovec:idle", "textemb:waiting", "doccat:waiting")

	// A video has no OCR and no text.
	stored(t, files, "enzo", "Videos/fete.mp4", "mp4 fete")
	wantSteps(t, "video", analysisOf(t, enzo, "Videos/fete.mp4"), "ready", "thumb:idle", "meta:idle", "photovec:idle")

	// A scanned PDF: no text layer, so the OCR runs, and nothing is indexed if it finds none.
	scan := stored(t, files, "enzo", "Papiers/scan.pdf", "%PDF scan")
	scanSum, _ := hex.DecodeString(scan)
	if _, err := files.Pool.Exec(ctx, `INSERT INTO document_text (sha256, source, text, chars, truncated, has_text) VALUES ($1, 'pdf', '', 0, false, false)`, scanSum); err != nil {
		t.Fatal(err)
	}
	queue(t, q, "ocr", scan)
	wantSteps(t, "scan waiting for OCR", analysisOf(t, enzo, "Papiers/scan.pdf"), "pending",
		"text:done", "ocr:waiting", "textemb:waiting", "doccat:waiting")
	task, _ := q.Claim(ctx, "w1", []string{"ocr"})
	if _, err := files.Pool.Exec(ctx, `UPDATE document_text SET ocr_model = 'm' WHERE sha256 = $1`, scanSum); err != nil {
		t.Fatal(err)
	}
	if err := q.Complete(ctx, task.ID, "w1"); err != nil {
		t.Fatal(err)
	}
	wantSteps(t, "scan read, no text found", analysisOf(t, enzo, "Papiers/scan.pdf"), "ready",
		"text:done", "ocr:done", "textemb:skipped", "doccat:skipped")

	// Markdown is a document; a zip is not analysed at all.
	stored(t, files, "enzo", "notes.md", "# notes")
	wantSteps(t, "markdown", analysisOf(t, enzo, "notes.md"), "ready", "text:idle", "textemb:idle", "doccat:idle")
	stored(t, files, "enzo", "archive.zip", "PK zip")
	wantSteps(t, "zip", analysisOf(t, enzo, "archive.zip"), "none")

	for _, bad := range []string{"/api/analysis/file?path=Photos", "/api/analysis/file?path=nope.pdf", "/api/analysis/file?path="} {
		if rec := enzo.do("GET", bad, ""); rec.Code != http.StatusNotFound && rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 404 or 400", bad, rec.Code)
		}
	}
}

// Content is deduplicated across accounts: the queue is keyed by hash, but an
// account sees only the files it owns.
func TestAnotherAccountSeesNothingOfMyAnalysis(t *testing.T) {
	enzo, johan, files, q := newAnalysisAPI(t)
	ctx := context.Background()
	// One of enzo's files failed, another one waits.
	failing := stored(t, files, "enzo", "Banque/releve-secret.pdf", "%PDF releve")
	id := queue(t, q, "text", failing)
	if task, _ := q.Claim(ctx, "w1", []string{"text"}); task == nil || task.ID != id {
		t.Fatalf("claim: %+v", task)
	}
	if err := q.GiveUp(ctx, id, "w1", "PdfReadError: broken"); err != nil {
		t.Fatal(err)
	}
	waiting := stored(t, files, "enzo", "Sante/ordonnance-secrete.pdf", "%PDF ordonnance")
	queue(t, q, "text", waiting)

	if s := summaryOf(t, enzo); s != (analysis.Summary{Analysing: 1, Failed: 1}) {
		t.Fatalf("enzo's summary = %+v", s)
	}
	if s := summaryOf(t, johan); s != (analysis.Summary{}) {
		t.Errorf("johan's summary = %+v, want nothing", s)
	}
	for _, p := range []string{"Sante/ordonnance-secrete.pdf", "Banque/releve-secret.pdf"} {
		rec := johan.do("GET", "/api/analysis/file?path="+url.QueryEscape(p), "")
		if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "secret") || strings.Contains(rec.Body.String(), "PdfReadError") {
			t.Errorf("johan reads the analysis of enzo's %s: %d %s", p, rec.Code, rec.Body)
		}
	}

	// Johan drops the same bytes under his own name: he sees his own file,
	// under his own path, and none of enzo's names.
	stored(t, files, "johan", "Papiers/ma-copie.pdf", "%PDF ordonnance")
	if s := summaryOf(t, johan); s != (analysis.Summary{Analysing: 1}) {
		t.Errorf("johan's summary with his own copy = %+v", s)
	}
	if st := analysisOf(t, johan, "Papiers/ma-copie.pdf"); st.State != "pending" {
		t.Errorf("johan's copy = %+v", st)
	}
	if rec := johan.do("GET", "/api/analysis/file?path="+url.QueryEscape("Sante/ordonnance-secrete.pdf"), ""); rec.Code != http.StatusNotFound {
		t.Errorf("enzo's path through johan's copy: %d %s", rec.Code, rec.Body)
	}

	// Enzo's file in the trash is no longer analysed for him.
	if _, err := files.Trash(ctx, "enzo", "Sante/ordonnance-secrete.pdf"); err != nil {
		t.Fatal(err)
	}
	if s := summaryOf(t, enzo); s != (analysis.Summary{Failed: 1}) {
		t.Errorf("enzo's summary after the trash = %+v", s)
	}
}

func TestAnalysisNeedsTheReader(t *testing.T) {
	pool, _ := testdb.Migrated(t)
	files := &storage.Store{Root: t.TempDir(), Pool: pool}
	h := NewHandler(Config{Version: "test", Health: &fakeStore{}, Auth: auth.NewService(pool), Files: files})
	c := &client{t: t, h: h}
	c.do("POST", "/api/setup", `{"name":"enzo","password":"`+pw+`"}`)
	for _, p := range []string{"/api/analysis", "/api/analysis/file?path=a.pdf"} {
		if rec := c.do("GET", p, ""); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: %d, want 503", p, rec.Code)
		}
	}
}
