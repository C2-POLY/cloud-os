package httpapi

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// guestClient reads through a link without a session, keeping the access
// cookie that unlock gives.
type guestClient struct {
	t     *testing.T
	h     http.Handler
	grant *http.Cookie
}

func (g *guestClient) do(method, path, body string) *httptest.ResponseRecorder {
	g.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "198.51.100.7:4000"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if g.grant != nil && strings.HasPrefix(req.URL.Path, g.grant.Path) {
		req.AddCookie(g.grant)
	}
	rec := httptest.NewRecorder()
	g.h.ServeHTTP(rec, req)
	for _, c := range rec.Result().Cookies() {
		if c.Name == GrantCookie {
			g.grant = c
		}
	}
	return rec
}

type created struct {
	Share ShareEntry `json:"share"`
	Token string     `json:"token"`
}

// shareLink creates a link as c and returns its token and id.
func shareLink(t *testing.T, c *client, path, password string) created {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"path": path, "expires_at": time.Now().Add(time.Hour), "password": password})
	rec := c.do("POST", "/api/shares", string(body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create link to %s: %d %s", path, rec.Code, rec.Body)
	}
	var out created
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func newShareFixture(t *testing.T) (enzo, johan *client, guest *guestClient) {
	t.Helper()
	enzo, johan = newFilesAPI(t)
	for p, content := range map[string]string{
		"Photos/plage.jpg":        "sable",
		"Photos/2024/chien.jpg":   "wouf",
		"Photos-privé/secret.txt": "à ne pas montrer",
		"racine.txt":              "texte",
		"Photos/page.html":        "<script>alert(1)</script>",
	} {
		if rec := enzo.upload(strings.ReplaceAll(p, "é", "%C3%A9"), content, nil); rec.Code != http.StatusCreated {
			t.Fatalf("upload %s: %d %s", p, rec.Code, rec.Body)
		}
	}
	return enzo, johan, &guestClient{t: t, h: enzo.h}
}

func zipNames(t *testing.T, rec *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("zip %d: %v", rec.Code, err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		_, _ = b.ReadFrom(r)
		r.Close()
		out[f.Name] = b.String()
	}
	return out
}

func TestGuestReadsOnlyTheSharedSubtree(t *testing.T) {
	owner, _, guest := newShareFixture(t)
	link := shareLink(t, owner, "Photos", "")
	base := "/api/s/" + link.Token

	rec := guest.do("GET", base, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"Photos"`) || !strings.Contains(rec.Body.String(), `"kind":"dir"`) {
		t.Fatalf("info: %d %s", rec.Code, rec.Body)
	}
	for h, want := range map[string]string{"Referrer-Policy": "no-referrer", "Cache-Control": "no-store", "X-Robots-Tag": "noindex, nofollow"} {
		if got := rec.Header().Get(h); got != want {
			t.Errorf("%s = %q, want %q", h, got, want)
		}
	}

	rec = guest.do("GET", base+"/files", "")
	if got := strings.Join(listing(t, rec), ","); got != "2024/,page.html,plage.jpg" {
		t.Errorf("root listing = %s", got)
	}
	var entries []Entry
	_ = json.Unmarshal(rec.Body.Bytes(), &entries)
	for _, e := range entries {
		if strings.HasPrefix(e.Path, "Photos") {
			t.Errorf("entry %+v shows the owner's path", e)
		}
	}
	if got := strings.Join(listing(t, guest.do("GET", base+"/files?path=2024", "")), ","); got != "chien.jpg" {
		t.Errorf("2024 listing = %s", got)
	}

	rec = guest.do("GET", base+"/content?path=2024/chien.jpg", "")
	if rec.Code != http.StatusOK || rec.Body.String() != "wouf" {
		t.Errorf("download: %d %q", rec.Code, rec.Body)
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment;") || rec.Header().Get("Content-Security-Policy") != "sandbox" ||
		rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("download headers: %v", rec.Header())
	}
	rec = guest.do("GET", base+"/content?path=page.html", "")
	if !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment;") || rec.Header().Get("Content-Security-Policy") != "sandbox" {
		t.Errorf("an HTML file is not forced to a sandboxed download: %v", rec.Header())
	}
	rec = guest.do("GET", base+"/content?path=plage.jpg", "")
	if rec.Code != http.StatusOK || rec.Body.String() != "sable" {
		t.Errorf("download plage.jpg: %d %q", rec.Code, rec.Body)
	}

	// Every way out of the subtree is refused.
	for _, q := range []string{
		"..", "../Photos-priv%C3%A9/secret.txt", "../racine.txt", "%2e%2e/racine.txt", "2024/../../racine.txt",
		"/racine.txt", "..%2Fracine.txt", "..%5Cracine.txt", "2024%2F..%2F..%2Fracine.txt",
	} {
		for _, route := range []string{"/files", "/content", "/zip"} {
			rec := guest.do("GET", base+route+"?path="+q, "")
			if rec.Code < 400 || strings.Contains(rec.Body.String(), "texte") || strings.Contains(rec.Body.String(), "montrer") {
				t.Errorf("%s?path=%s: %d %q", route, q, rec.Code, rec.Body)
			}
		}
	}
	// A folder named like the shared one plus a suffix is not inside it.
	if rec := guest.do("GET", base+"/content?path=-priv%C3%A9/secret.txt", ""); rec.Code != http.StatusNotFound {
		t.Errorf("sibling folder with a common prefix: %d %s", rec.Code, rec.Body)
	}
	if rec := guest.do("GET", base+"/content?path=2024", ""); rec.Code != http.StatusNotFound {
		t.Errorf("download a folder: %d", rec.Code)
	}

	names := zipNames(t, guest.do("GET", base+"/zip", ""))
	keys := make([]string, 0, len(names))
	for k := range names {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != "Photos/2024/chien.jpg,Photos/page.html,Photos/plage.jpg" || names["Photos/plage.jpg"] != "sable" {
		t.Errorf("zip = %v", names)
	}
	if names := zipNames(t, guest.do("GET", base+"/zip?path=2024", "")); len(names) != 1 || names["2024/chien.jpg"] != "wouf" {
		t.Errorf("zip of 2024 = %v", names)
	}
}

func TestGuestCannotWrite(t *testing.T) {
	owner, _, guest := newShareFixture(t)
	link := shareLink(t, owner, "Photos", "")
	base := "/api/s/" + link.Token
	for _, method := range []string{"PUT", "POST", "PATCH", "DELETE", "MOVE", "COPY", "MKCOL", "PROPPATCH"} {
		for _, path := range []string{base, base + "/files?path=2024", base + "/content?path=plage.jpg", base + "/zip", base + "/trash", base + "/move"} {
			req := httptest.NewRequest(method, path, strings.NewReader(`{"path":"plage.jpg","to":"x.jpg"}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Cloudos-Client", "test")
			rec := httptest.NewRecorder()
			guest.h.ServeHTTP(rec, req)
			if rec.Code < 400 {
				t.Errorf("%s %s: %d %s", method, path, rec.Code, rec.Body)
			}
		}
	}
	if rec := owner.get("/api/files/content?path=Photos/plage.jpg", nil); rec.Body.String() != "sable" {
		t.Errorf("the shared file changed: %q", rec.Body)
	}
	if got := strings.Join(listing(t, owner.get("/api/files?path=Photos", nil)), ","); got != "2024/,page.html,plage.jpg" {
		t.Errorf("the shared folder changed: %s", got)
	}
}

func TestPasswordLink(t *testing.T) {
	owner, _, guest := newShareFixture(t)
	link := shareLink(t, owner, "Photos", "sesame ouvre-toi")
	base := "/api/s/" + link.Token
	if !link.Share.HasPassword {
		t.Errorf("created link: %+v", link.Share)
	}

	for _, route := range []string{"", "/files", "/content?path=plage.jpg", "/zip"} {
		rec := guest.do("GET", base+route, "")
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "password required") || strings.Contains(rec.Body.String(), "Photos") {
			t.Errorf("%s without the password: %d %s", route, rec.Code, rec.Body)
		}
	}
	if rec := guest.do("POST", base+"/unlock", `{"password":"mauvais mot de passe"}`); rec.Code != http.StatusForbidden || guest.grant != nil {
		t.Errorf("wrong password: %d %s", rec.Code, rec.Body)
	}
	if rec := guest.do("POST", base+"/unlock", `{"password":"sesame ouvre-toi"}`); rec.Code != http.StatusNoContent || guest.grant == nil {
		t.Fatalf("right password: %d %s", rec.Code, rec.Body)
	}
	if g := guest.grant; !g.HttpOnly || g.SameSite != http.SameSiteStrictMode || g.Path != base || g.MaxAge <= 0 || g.MaxAge > 24*3600 {
		t.Errorf("access cookie: %+v", g)
	}
	if rec := guest.do("GET", base+"/content?path=plage.jpg", ""); rec.Code != http.StatusOK || rec.Body.String() != "sable" {
		t.Errorf("with the access: %d %s", rec.Code, rec.Body)
	}

	// The access does not open another link of the same owner.
	other := shareLink(t, owner, "racine.txt", "sesame ouvre-toi")
	req := httptest.NewRequest("GET", "/api/s/"+other.Token, nil)
	req.AddCookie(guest.grant)
	rec := httptest.NewRecorder()
	guest.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("another link with this access: %d %s", rec.Code, rec.Body)
	}

	// Too many wrong passwords.
	stranger := &guestClient{t: t, h: owner.h}
	for range 5 {
		stranger.do("POST", "/api/s/"+other.Token+"/unlock", `{"password":"mauvais mot de passe"}`)
	}
	if rec := stranger.do("POST", "/api/s/"+other.Token+"/unlock", `{"password":"sesame ouvre-toi"}`); rec.Code != http.StatusTooManyRequests {
		t.Errorf("after 5 failures: %d %s", rec.Code, rec.Body)
	}
}

func TestOwnerListsAndRevokes(t *testing.T) {
	owner, johan, guest := newShareFixture(t)
	link := shareLink(t, owner, "racine.txt", "")
	base := "/api/s/" + link.Token

	// A file link serves the file itself, and nothing inside it.
	if rec := guest.do("GET", base+"/content", ""); rec.Code != http.StatusOK || rec.Body.String() != "texte" {
		t.Errorf("file link: %d %q", rec.Code, rec.Body)
	}
	for _, route := range []string{"/files", "/zip", "/content?path=x"} {
		if rec := guest.do("GET", base+route, ""); rec.Code != http.StatusNotFound {
			t.Errorf("file link %s: %d %s", route, rec.Code, rec.Body)
		}
	}

	var links []ShareEntry
	rec := owner.do("GET", "/api/shares", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &links); err != nil || len(links) != 1 || links[0].Name != "racine.txt" || links[0].Kind != "file" || links[0].ID != link.Share.ID {
		t.Fatalf("owner's list: %s, %v", rec.Body, err)
	}
	if strings.Contains(rec.Body.String(), link.Token) || strings.Contains(rec.Body.String(), "token") {
		t.Errorf("the list shows the token: %s", rec.Body)
	}
	if got := johan.do("GET", "/api/shares", ""); got.Body.String() != "[]\n" {
		t.Errorf("johan's list: %s", got.Body)
	}
	id := "/api/shares/" + jsonNumber(link.Share.ID)
	if rec := johan.do("DELETE", id, `{}`); rec.Code != http.StatusNotFound {
		t.Errorf("johan revokes enzo's link: %d", rec.Code)
	}
	if rec := owner.do("DELETE", id, `{}`); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body)
	}
	if rec := guest.do("GET", base+"/content", ""); rec.Code != http.StatusNotFound || rec.Body.String() != `{"error":"no such link"}`+"\n" {
		t.Errorf("revoked link: %d %s", rec.Code, rec.Body)
	}

	for name, body := range map[string]string{
		"past expiry":   `{"path":"racine.txt","expires_at":"2020-01-01T00:00:00Z"}`,
		"no expiry":     `{"path":"racine.txt"}`,
		"short pass":    `{"path":"racine.txt","expires_at":"2999-01-01T00:00:00Z","password":"abc"}`,
		"outside":       `{"path":"../johan","expires_at":"2999-01-01T00:00:00Z"}`,
		"unknown field": `{"path":"racine.txt","expires_at":"2999-01-01T00:00:00Z","write":true}`,
	} {
		if rec := owner.do("POST", "/api/shares", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := johan.do("POST", "/api/shares", `{"path":"racine.txt","expires_at":"2999-01-01T00:00:00Z"}`); rec.Code != http.StatusNotFound {
		t.Errorf("johan shares enzo's file: %d %s", rec.Code, rec.Body)
	}
}

func TestTrashedItemGivesNothing(t *testing.T) {
	owner, _, guest := newShareFixture(t)
	link := shareLink(t, owner, "Photos/2024", "")
	base := "/api/s/" + link.Token
	if rec := owner.do("POST", "/api/files/trash", `{"path":"Photos"}`); rec.Code != http.StatusOK {
		t.Fatalf("trash: %d %s", rec.Code, rec.Body)
	}
	for _, route := range []string{"", "/files", "/content?path=chien.jpg", "/zip"} {
		if rec := guest.do("GET", base+route, ""); rec.Code != http.StatusNotFound {
			t.Errorf("%s in the trash: %d %s", route, rec.Code, rec.Body)
		}
	}
	// A new folder at the old path is not the shared one.
	owner.upload("Photos/2024/nouveau.jpg", "nouveau", nil)
	if rec := guest.do("GET", base+"/files", ""); rec.Code != http.StatusNotFound {
		t.Errorf("new folder at the old path: %d %s", rec.Code, rec.Body)
	}
}

func TestSharePageHeaders(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<p>app</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(Config{Version: "test", Health: &fakeStore{}, StaticDir: dir})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/s/"+strings.Repeat("A", 43), nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Referrer-Policy") != "no-referrer" || rec.Header().Get("X-Robots-Tag") == "" {
		t.Errorf("share page: %d %v", rec.Code, rec.Header())
	}
}

func jsonNumber(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
