package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/C2-POLY/cloud-os/core/internal/auth"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/testdb"
)

// filesAPI returns a handler with real accounts and storage, and signed-in
// clients for enzo (admin) and johan (member).
func newFilesAPI(t *testing.T) (enzo, johan *client) {
	t.Helper()
	enzo, johan, _ = newFilesAPIPool(t)
	return enzo, johan
}

// newFilesAPIPool is newFilesAPI with the database, to write what workers write.
func newFilesAPIPool(t *testing.T) (enzo, johan *client, pool *pgxpool.Pool) {
	t.Helper()
	pool, _ = testdb.Migrated(t)
	h := NewHandler(Config{
		Version: "test",
		Health:  &fakeStore{},
		Auth:    auth.NewService(pool),
		Files:   &storage.Store{Root: t.TempDir(), Pool: pool},
	})
	enzo = &client{t: t, h: h}
	enzo.do("POST", "/api/setup", `{"name":"enzo","password":"`+pw+`"}`)
	enzo.do("POST", "/api/accounts", `{"name":"johan","password":"`+pw+`"}`)
	johan = &client{t: t, h: h}
	johan.do("POST", "/api/login", `{"name":"johan","password":"`+pw+`"}`)
	return enzo, johan, pool
}

// upload sends content raw, as a client does, with the Cloud OS client header.
func (c *client) upload(path, content string, headers map[string]string) *httptest.ResponseRecorder {
	c.t.Helper()
	req := httptest.NewRequest("PUT", "/api/files/content?path="+path, strings.NewReader(content))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set(auth.ClientHeader, "test")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	return rec
}

func (c *client) get(path string, headers map[string]string) *httptest.ResponseRecorder {
	c.t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	return rec
}

func listing(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	var entries []Entry
	if err := json.Unmarshal(rec.Body.Bytes(), &entries); err != nil {
		t.Fatalf("listing %d %s: %v", rec.Code, rec.Body, err)
	}
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Name
		if e.Kind == storage.KindDir {
			out[i] += "/"
		}
	}
	return out
}

func TestFilesUploadListMoveDownload(t *testing.T) {
	enzo, _ := newFilesAPI(t)
	content := "contrat de l'appart, signé"
	sum := sha256.Sum256([]byte(content))

	rec := enzo.upload("Documents/contrat.pdf", content, map[string]string{"X-Content-SHA256": hex.EncodeToString(sum[:])})
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	if rec := enzo.upload("Documents/contrat.pdf", "other", nil); rec.Code != http.StatusConflict {
		t.Errorf("upload onto an existing file: %d %s", rec.Code, rec.Body)
	}
	wrong := sha256.Sum256([]byte("not it"))
	if rec := enzo.upload("Documents/faux.pdf", content, map[string]string{"X-Content-SHA256": hex.EncodeToString(wrong[:])}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("upload with a wrong hash: %d %s", rec.Code, rec.Body)
	}
	if rec := enzo.do("POST", "/api/files/folders", `{"path":"Photos/2024"}`); rec.Code != http.StatusCreated {
		t.Fatalf("mkdir: %d %s", rec.Code, rec.Body)
	}

	if got := listing(t, enzo.get("/api/files", nil)); strings.Join(got, ",") != "Documents/,Photos/" {
		t.Errorf("root = %v", got)
	}
	if got := listing(t, enzo.get("/api/files?path=Documents", nil)); strings.Join(got, ",") != "contrat.pdf" {
		t.Errorf("Documents = %v", got)
	}

	if rec := enzo.do("POST", "/api/files/move", `{"from":"Documents/contrat.pdf","to":"Photos/2024/contrat signé.pdf"}`); rec.Code != http.StatusOK {
		t.Fatalf("move: %d %s", rec.Code, rec.Body)
	}
	if rec := enzo.do("POST", "/api/files/move", `{"from":"Photos","to":"Photos/dedans"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("move into itself: %d %s", rec.Code, rec.Body)
	}

	path := "/api/files/content?path=Photos/2024/contrat%20sign%C3%A9.pdf"
	rec = enzo.get(path, nil)
	if rec.Code != http.StatusOK || rec.Body.String() != content {
		t.Fatalf("download: %d %q", rec.Code, rec.Body)
	}
	if etag := rec.Header().Get("ETag"); etag != `"`+hex.EncodeToString(sum[:])+`"` {
		t.Errorf("ETag %q", etag)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "contrat%20sign%C3%A9.pdf") {
		t.Errorf("Content-Disposition %q", cd)
	}
	rec = enzo.get(path, map[string]string{"Range": "bytes=0-6"})
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "contrat" {
		t.Errorf("range: %d %q", rec.Code, rec.Body)
	}
	if rec := enzo.get(path, map[string]string{"If-None-Match": `"` + hex.EncodeToString(sum[:]) + `"`}); rec.Code != http.StatusNotModified {
		t.Errorf("If-None-Match: %d", rec.Code)
	}
	if rec := enzo.get("/api/files/content?path=Photos", nil); rec.Code != http.StatusNotFound {
		t.Errorf("download a folder: %d", rec.Code)
	}
}

func TestEachAccountSeesOnlyItsOwnFiles(t *testing.T) {
	enzo, johan := newFilesAPI(t)
	enzo.upload("secret.txt", "enzo's", nil)

	if got := listing(t, johan.get("/api/files", nil)); len(got) != 0 {
		t.Errorf("johan sees %v", got)
	}
	if rec := johan.get("/api/files/content?path=secret.txt", nil); rec.Code != http.StatusNotFound {
		t.Errorf("johan downloads enzo's file: %d %s", rec.Code, rec.Body)
	}
	if rec := johan.do("POST", "/api/files/move", `{"from":"secret.txt","to":"mine.txt"}`); rec.Code != http.StatusNotFound {
		t.Errorf("johan moves enzo's file: %d", rec.Code)
	}
	// The same name in johan's space is johan's own file.
	if rec := johan.upload("secret.txt", "johan's", nil); rec.Code != http.StatusCreated {
		t.Errorf("johan's own secret.txt: %d %s", rec.Code, rec.Body)
	}
	if rec := enzo.get("/api/files/content?path=secret.txt", nil); rec.Body.String() != "enzo's" {
		t.Errorf("enzo's file became %q", rec.Body)
	}
}

func TestDangerousPathsAreRefused(t *testing.T) {
	enzo, _ := newFilesAPI(t)
	for _, p := range []string{"../escape.txt", "/etc/passwd", "a/../../b", "a%5Cb"} {
		if rec := enzo.upload(p, "x", nil); rec.Code != http.StatusBadRequest {
			t.Errorf("upload %q: %d %s", p, rec.Code, rec.Body)
		}
	}
	if rec := enzo.get("/api/files/content?path=../../etc/passwd", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("download outside: %d", rec.Code)
	}
}

func TestRawUploadNeedsTheClientHeader(t *testing.T) {
	enzo, _ := newFilesAPI(t)
	req := httptest.NewRequest("PUT", "/api/files/content?path=a.txt", strings.NewReader("x"))
	req.Header.Set("Content-Type", "text/plain")
	req.AddCookie(enzo.cookie)
	rec := httptest.NewRecorder()
	enzo.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("upload without the client header: %d, want 415", rec.Code)
	}
}

func TestThumb(t *testing.T) {
	enzo, johan, pool := newFilesAPIPool(t)
	enzo.upload("a.jpg", "jpeg bytes", nil)
	if rec := enzo.get("/api/files/thumb?path=a.jpg", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("before the worker: %d", rec.Code)
	}
	sum := sha256.Sum256([]byte("jpeg bytes"))
	if _, err := pool.Exec(t.Context(), `INSERT INTO thumbnails (sha256, size, mime, width, height, bytes) VALUES ($1, 256, 'image/webp', 2, 1, 'webp!')`, sum[:]); err != nil {
		t.Fatal(err)
	}
	rec := enzo.get("/api/files/thumb?path=a.jpg", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "webp!" || rec.Header().Get("Content-Type") != "image/webp" {
		t.Fatalf("thumb: %d %q %v", rec.Code, rec.Body, rec.Header())
	}
	etag := rec.Header().Get("ETag")
	if etag != `"`+hex.EncodeToString(sum[:])+`"` || !strings.Contains(rec.Header().Get("Cache-Control"), "private") {
		t.Errorf("headers %v", rec.Header())
	}
	if rec := enzo.get("/api/files/thumb?path=a.jpg", map[string]string{"If-None-Match": etag}); rec.Code != http.StatusNotModified {
		t.Errorf("revalidation: %d", rec.Code)
	}
	// Owner-scoped like every file route; folders and missing files are 404.
	if rec := johan.get("/api/files/thumb?path=a.jpg", nil); rec.Code != http.StatusNotFound {
		t.Errorf("another account: %d", rec.Code)
	}
	enzo.do("POST", "/api/files/folders", `{"path":"Photos"}`)
	for _, p := range []string{"Photos", "missing.jpg"} {
		if rec := enzo.get("/api/files/thumb?path="+p, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
}
