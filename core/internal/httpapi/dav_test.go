package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/C2-POLY/cloud-os/core/internal/auth"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/testdb"
)

type davClient struct {
	t      *testing.T
	h      http.Handler
	user   string
	pass   string
	files  *storage.Store
	server *httptest.Server
}

func newDav(t *testing.T) *davClient {
	t.Helper()
	pool, _ := testdb.Migrated(t)
	files := &storage.Store{Root: t.TempDir(), Pool: pool}
	h := NewHandler(Config{Version: "test", Health: &fakeStore{}, Auth: auth.NewService(pool), Files: files})
	setup := &client{t: t, h: h}
	setup.do("POST", "/api/setup", `{"name":"enzo","password":"`+pw+`"}`)
	setup.do("POST", "/api/accounts", `{"name":"johan","password":"`+pw+`"}`)
	return &davClient{t: t, h: h, user: "enzo", pass: pw, files: files}
}

func (d *davClient) req(method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	d.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "192.0.2.9:5000"
	if d.user != "" {
		req.SetBasicAuth(d.user, d.pass)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	d.h.ServeHTTP(rec, req)
	return rec
}

func TestDavNeedsTheAccountPassword(t *testing.T) {
	d := newDav(t)
	anon := *d
	anon.user = ""
	rec := anon.req("PROPFIND", "/dav/", "", map[string]string{"Depth": "1"})
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Header().Get("WWW-Authenticate"), "Basic") {
		t.Errorf("no credentials: %d %v", rec.Code, rec.Header())
	}
	wrong := *d
	wrong.pass = "wrong password!"
	if rec := wrong.req("PROPFIND", "/dav/", "", map[string]string{"Depth": "0"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong password: %d", rec.Code)
	}
	if rec := d.req("PROPFIND", "/dav/", "", map[string]string{"Depth": "0"}); rec.Code != http.StatusMultiStatus {
		t.Errorf("right password: %d %s", rec.Code, rec.Body)
	}
}

func TestDavLikeTheFinder(t *testing.T) {
	d := newDav(t)

	// New folder, then save a document in it.
	if rec := d.req("MKCOL", "/dav/Documents", "", nil); rec.Code != http.StatusCreated {
		t.Fatalf("MKCOL: %d %s", rec.Code, rec.Body)
	}
	if rec := d.req("MKCOL", "/dav/Absent/Sous", "", nil); rec.Code != http.StatusConflict {
		t.Errorf("MKCOL under a missing folder: %d", rec.Code)
	}
	if rec := d.req("PUT", "/dav/Documents/lettre.txt", "brouillon", nil); rec.Code != http.StatusCreated {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	if rec := d.req("PUT", "/dav/Absent/x.txt", "x", nil); rec.Code != http.StatusConflict {
		t.Errorf("PUT under a missing folder: %d", rec.Code)
	}

	// Saving again replaces it; the draft becomes a version.
	if rec := d.req("PUT", "/dav/Documents/lettre.txt", "version finale", nil); rec.Code != http.StatusCreated && rec.Code != http.StatusNoContent {
		t.Fatalf("PUT again: %d %s", rec.Code, rec.Body)
	}
	if rec := d.req("GET", "/dav/Documents/lettre.txt", "", nil); rec.Body.String() != "version finale" {
		t.Errorf("GET = %q", rec.Body)
	}
	versions, err := d.files.ListVersions(t.Context(), "enzo", "Documents/lettre.txt")
	if err != nil || len(versions) != 1 || versions[0].Size != int64(len("brouillon")) {
		t.Errorf("versions after a save: %+v, %v", versions, err)
	}

	// Listing shows the document with its size and type.
	rec := d.req("PROPFIND", "/dav/Documents/", "", map[string]string{"Depth": "1"})
	if rec.Code != http.StatusMultiStatus || !strings.Contains(rec.Body.String(), "lettre.txt") || !strings.Contains(rec.Body.String(), "<D:getcontentlength>14</D:getcontentlength>") {
		t.Errorf("PROPFIND: %d %s", rec.Code, rec.Body)
	}

	// Rename, copy, then delete: the delete goes to the trash.
	if rec := d.req("MOVE", "/dav/Documents/lettre.txt", "", map[string]string{"Destination": "/dav/Documents/lettre-envoyee.txt"}); rec.Code != http.StatusCreated {
		t.Fatalf("MOVE: %d %s", rec.Code, rec.Body)
	}
	if rec := d.req("COPY", "/dav/Documents/lettre-envoyee.txt", "", map[string]string{"Destination": "/dav/copie.txt"}); rec.Code != http.StatusCreated {
		t.Fatalf("COPY: %d %s", rec.Code, rec.Body)
	}
	if rec := d.req("DELETE", "/dav/Documents/lettre-envoyee.txt", "", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE: %d %s", rec.Code, rec.Body)
	}
	items, _ := d.files.ListTrash(t.Context(), "enzo")
	if len(items) != 1 || items[0].Path != "Documents/lettre-envoyee.txt" {
		t.Errorf("trash after DELETE: %+v", items)
	}
	if rec := d.req("GET", "/dav/copie.txt", "", nil); rec.Body.String() != "version finale" {
		t.Errorf("copy = %q", rec.Body)
	}

	// MOVE over an existing file trashes the file it replaces.
	d.req("PUT", "/dav/a.txt", "A", nil)
	d.req("PUT", "/dav/b.txt", "B", nil)
	if rec := d.req("MOVE", "/dav/a.txt", "", map[string]string{"Destination": "/dav/b.txt", "Overwrite": "T"}); rec.Code != http.StatusNoContent && rec.Code != http.StatusCreated {
		t.Fatalf("MOVE over a file: %d %s", rec.Code, rec.Body)
	}
	if items, _ := d.files.ListTrash(t.Context(), "enzo"); len(items) != 2 {
		t.Errorf("the overwritten file should be in the trash: %+v", items)
	}

	rep, err := d.files.Verify(t.Context())
	if err != nil || !rep.Clean() {
		t.Errorf("verify: %+v, %v", rep.Problems, err)
	}
}

func TestDavKeepsFinderFilesApart(t *testing.T) {
	d := newDav(t)
	d.req("MKCOL", "/dav/Photos", "", nil)
	for _, p := range []string{"/dav/.DS_Store", "/dav/Photos/._plage.jpg", "/dav/Photos/.DS_Store"} {
		if rec := d.req("PUT", p, "finder metadata", nil); rec.Code != http.StatusCreated {
			t.Fatalf("PUT %s: %d %s", p, rec.Code, rec.Body)
		}
		if rec := d.req("GET", p, "", nil); rec.Body.String() != "finder metadata" {
			t.Errorf("GET %s = %q", p, rec.Body)
		}
	}
	// Not in the index, not in the listing, not on the user's tree.
	if entries, _ := d.files.List(t.Context(), "enzo", "Photos"); len(entries) != 0 {
		t.Errorf("Finder files indexed: %+v", entries)
	}
	if rec := d.req("PROPFIND", "/dav/Photos/", "", map[string]string{"Depth": "1"}); strings.Contains(rec.Body.String(), "DS_Store") {
		t.Errorf("Finder files listed: %s", rec.Body)
	}
	if _, err := os.Stat(filepath.Join(d.files.Root, "enzo", ".DS_Store")); !os.IsNotExist(err) {
		t.Errorf(".DS_Store in the user's tree: %v", err)
	}
	// Deleting one erases it without the trash.
	if rec := d.req("DELETE", "/dav/Photos/._plage.jpg", "", nil); rec.Code != http.StatusNoContent {
		t.Errorf("DELETE ._ file: %d", rec.Code)
	}
	if items, _ := d.files.ListTrash(t.Context(), "enzo"); len(items) != 0 {
		t.Errorf("Finder file in the trash: %+v", items)
	}
	rep, err := d.files.Verify(t.Context())
	if err != nil || !rep.Clean() {
		t.Errorf("verify: %+v, %v", rep.Problems, err)
	}
}

func TestDavEachAccountSeesOnlyItsOwnSpace(t *testing.T) {
	d := newDav(t)
	d.req("PUT", "/dav/secret.txt", "enzo's", nil)
	johan := *d
	johan.user = "johan"
	if rec := johan.req("GET", "/dav/secret.txt", "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("johan reads enzo's file: %d", rec.Code)
	}
	rec := johan.req("PROPFIND", "/dav/", "", map[string]string{"Depth": "1"})
	if strings.Contains(rec.Body.String(), "secret.txt") {
		t.Errorf("johan lists enzo's file: %s", rec.Body)
	}
	// A path escaping the space stays inside it.
	if rec := d.req("GET", "/dav/../../etc/passwd", "", nil); rec.Code == http.StatusOK {
		b, _ := io.ReadAll(rec.Body)
		t.Errorf("escape: %d %q", rec.Code, b)
	}
}
