package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/C2-POLY/cloud-os/core/internal/activity"
	"github.com/C2-POLY/cloud-os/core/internal/auth"
	"github.com/C2-POLY/cloud-os/core/internal/share"
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
	files := &storage.Store{Root: t.TempDir(), Pool: pool}
	journal := &activity.Log{Pool: pool}
	files.Notify = journal.Record
	h := NewHandler(Config{
		Version:  "test",
		Health:   &fakeStore{},
		Auth:     auth.NewService(pool),
		Files:    files,
		Shares:   share.NewService(pool, files),
		Activity: journal,
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

func TestSearch(t *testing.T) {
	enzo, johan := newFilesAPI(t)
	enzo.upload("Photos/%C3%89t%C3%A9/plage.jpg", "a", nil)
	enzo.upload("notes.txt", "b", nil)
	johan.upload("plage.txt", "c", nil)

	rec := enzo.get("/api/search?q=ETE", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("search: %d %s", rec.Code, rec.Body)
	}
	if got := listing(t, rec); strings.Join(got, ",") != "Été/,plage.jpg" {
		t.Errorf("ete: %v", got)
	}
	// Owner-scoped: johan's plage.txt is not enzo's.
	if got := listing(t, enzo.get("/api/search?q=plage&limit=1", nil)); strings.Join(got, ",") != "plage.jpg" {
		t.Errorf("plage: %v", got)
	}
	if got := listing(t, enzo.get("/api/search?q=", nil)); len(got) != 0 {
		t.Errorf("empty query: %v", got)
	}
	anon := &client{t: t, h: enzo.h}
	if rec := anon.get("/api/search?q=plage", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("signed out: %d", rec.Code)
	}
}

func TestDesktopWidgets(t *testing.T) {
	enzo, johan := newFilesAPI(t)
	enzo.upload("Photos/a.jpg", "a", nil)
	enzo.upload("Photos/b.jpg", "b", nil)
	enzo.upload("notes.txt", "n", nil)
	johan.upload("Photos/x.jpg", "x", nil)

	if got := listing(t, enzo.get("/api/files/recent?limit=2", nil)); strings.Join(got, ",") != "notes.txt,b.jpg" {
		t.Errorf("recent: %v", got)
	}

	// A guest opens a link to Photos: the owner's journal says so.
	expires := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	rec := enzo.do("POST", "/api/shares", `{"path":"Photos","expires_at":"`+expires+`"}`)
	var created struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || created.Token == "" {
		t.Fatalf("share: %d %s", rec.Code, rec.Body)
	}
	guest := &client{t: t, h: enzo.h}
	if rec := guest.get("/api/s/"+created.Token, nil); rec.Code != http.StatusOK {
		t.Fatalf("guest: %d %s", rec.Code, rec.Body)
	}

	rec = enzo.get("/api/activity?tz=Europe/Paris", nil)
	var groups []struct {
		Kind  string `json:"kind"`
		Place string `json:"place"`
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &groups); err != nil {
		t.Fatalf("activity: %d %s", rec.Code, rec.Body)
	}
	got := map[string]int{}
	for _, g := range groups {
		got[g.Kind+" "+g.Place] = g.Count
	}
	want := map[string]int{"added Photos": 2, "added ": 1, "share_opened Photos": 1}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("activity %v, want %v", got, want)
	}
	// An unknown time zone falls back to UTC instead of failing.
	if rec := enzo.get("/api/activity?tz=Nowhere/City", nil); rec.Code != http.StatusOK {
		t.Errorf("bad tz: %d", rec.Code)
	}
}

func TestMeta(t *testing.T) {
	enzo, johan, pool := newFilesAPIPool(t)
	enzo.upload("a.jpg", "jpeg bytes", nil)
	enzo.upload("b.jpg", "no exif", nil)
	if rec := enzo.get("/api/files/meta?path=a.jpg", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("before the worker: %d", rec.Code)
	}
	a, b := sha256.Sum256([]byte("jpeg bytes")), sha256.Sum256([]byte("no exif"))
	if _, err := pool.Exec(t.Context(),
		`INSERT INTO media_meta (sha256, taken_at, taken_offset, latitude, longitude, make, model, lens, width, height, orientation)
		 VALUES ($1, '2024-07-14 18:03:22', -330, 48.8584, 2.2945, 'Apple', 'iPhone 15', 'back camera', 3024, 4032, 6),
		        ($2, NULL, NULL, NULL, NULL, NULL, NULL, NULL, 640, 480, NULL)`, a[:], b[:]); err != nil {
		t.Fatal(err)
	}
	rec := enzo.get("/api/files/meta?path=a.jpg", nil)
	want := `{"taken_at":"2024-07-14T18:03:22","taken_offset":"-05:30","latitude":48.8584,"longitude":2.2945,"make":"Apple","model":"iPhone 15","lens":"back camera","width":3024,"height":4032,"orientation":6}`
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != want {
		t.Fatalf("meta: %d %s", rec.Code, rec.Body)
	}
	rec = enzo.get("/api/files/meta?path=b.jpg", nil)
	want = `{"taken_at":null,"taken_offset":null,"latitude":null,"longitude":null,"make":null,"model":null,"lens":null,"width":640,"height":480,"orientation":null}`
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != want {
		t.Fatalf("meta without exif: %d %s", rec.Code, rec.Body)
	}
	// Owner-scoped like every file route; folders and missing files are 404.
	if rec := johan.get("/api/files/meta?path=a.jpg", nil); rec.Code != http.StatusNotFound {
		t.Errorf("another account: %d", rec.Code)
	}
	enzo.do("POST", "/api/files/folders", `{"path":"Photos"}`)
	for _, p := range []string{"Photos", "missing.jpg"} {
		if rec := enzo.get("/api/files/meta?path="+p, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
}

func TestTimelineRoutes(t *testing.T) {
	enzo, _ := newFilesAPI(t)
	for i := range 3 {
		enzo.upload(fmt.Sprintf("Docs/n%d.txt", i), fmt.Sprint(i), map[string]string{"X-Modified-At": fmt.Sprint(1700000000 + i)})
	}
	rec := enzo.get("/api/timeline?limit=2&tz=Europe/Paris", nil)
	var page struct {
		Items []struct {
			Path  string `json:"path"`
			At    string `json:"at"`
			Taken bool   `json:"taken"`
		} `json:"items"`
		Next *struct {
			At   string `json:"at"`
			Path string `json:"path"`
		} `json:"next"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || len(page.Items) != 2 || page.Next == nil {
		t.Fatalf("page 1: %d %s", rec.Code, rec.Body)
	}
	if page.Items[0].Path != "Docs/n2.txt" || page.Items[0].At != "2023-11-14T23:13:22" {
		t.Errorf("first: %+v", page.Items[0])
	}
	rec = enzo.get("/api/timeline?limit=2&tz=Europe/Paris&before_at="+page.Next.At+"&before_path="+page.Next.Path, nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || len(page.Items) != 1 || page.Items[0].Path != "Docs/n0.txt" || page.Next != nil {
		t.Errorf("page 2: %s", rec.Body)
	}
	if rec := enzo.get("/api/timeline?before_at=hier", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("bad cursor: %d", rec.Code)
	}
	if rec := enzo.get("/api/timeline/months?tz=UTC", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `{"month":"2023-11","count":3}`) {
		t.Errorf("months: %d %s", rec.Code, rec.Body)
	}
}

func TestKnownAndDedupe(t *testing.T) {
	enzo, johan := newFilesAPI(t)
	enzo.upload("Photos/plage.jpg", "plage", nil)
	johan.upload("secret.jpg", "secret", nil)
	hexOf := func(s string) string {
		sum := sha256.Sum256([]byte(s))
		return hex.EncodeToString(sum[:])
	}

	rec := enzo.do("POST", "/api/files/known", `{"sha256":["`+hexOf("plage")+`","`+hexOf("secret")+`"]}`)
	if rec.Code != http.StatusOK || rec.Body.String() != `{"known":["`+hexOf("plage")+`"]}`+"\n" {
		t.Errorf("known: %d %q", rec.Code, rec.Body)
	}
	if rec := enzo.do("POST", "/api/files/known", `{"sha256":["zz"]}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad hash: %d", rec.Code)
	}

	body := `{"path":"Phone/IMG_0001.jpg","sha256":"` + hexOf("plage") + `","modified_at":1721000000}`
	if rec := enzo.do("POST", "/api/files/dedupe", body); rec.Code != http.StatusCreated {
		t.Fatalf("dedupe: %d %s", rec.Code, rec.Body)
	}
	if rec := enzo.get("/api/files/content?path=Phone/IMG_0001.jpg", nil); rec.Body.String() != "plage" {
		t.Errorf("copied content %q", rec.Body)
	}
	if rec := enzo.do("POST", "/api/files/dedupe", body); rec.Code != http.StatusOK {
		t.Errorf("again: %d", rec.Code)
	}
	if rec := enzo.do("POST", "/api/files/dedupe", `{"path":"Phone/IMG_0001.jpg","sha256":"`+hexOf("autre")+`"}`); rec.Code != http.StatusConflict {
		t.Errorf("taken path: %d", rec.Code)
	}
	// Johan's content is not enzo's to copy, and nothing says it exists.
	if rec := enzo.do("POST", "/api/files/dedupe", `{"path":"x.jpg","sha256":"`+hexOf("secret")+`"}`); rec.Code != http.StatusNotFound {
		t.Errorf("other account: %d", rec.Code)
	}
	anon := &client{t: t, h: enzo.h}
	if rec := anon.do("POST", "/api/files/dedupe", body); rec.Code != http.StatusUnauthorized {
		t.Errorf("signed out: %d", rec.Code)
	}
}
