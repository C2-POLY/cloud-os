package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/C2-POLY/cloud-os/core/internal/albums"
	"github.com/C2-POLY/cloud-os/core/internal/auth"
	"github.com/C2-POLY/cloud-os/core/internal/share"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/testdb"
)

const pw = "correct horse battery"

type client struct {
	t      *testing.T
	h      http.Handler
	cookie *http.Cookie
}

func (c *client) do(method, path, body string) *httptest.ResponseRecorder {
	c.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "192.0.2.1:5000"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == auth.CookieName {
			if ck.MaxAge < 0 {
				c.cookie = nil
			} else {
				c.cookie = ck
			}
		}
	}
	return rec
}

func newAPI(t *testing.T) (Config, http.Handler) {
	t.Helper()
	pool, p := testdb.Migrated(t)
	files := &storage.Store{Root: t.TempDir(), Pool: pool}
	cfg := Config{Version: "test", Auth: auth.NewService(pool), Health: &fakeStore{version: 3}, Files: files, Shares: share.NewService(pool, files), Albums: &albums.Service{Pool: pool, Files: files}}
	_ = p
	return cfg, NewHandler(cfg)
}

func TestSetupLoginAndAdmin(t *testing.T) {
	_, h := newAPI(t)
	admin := &client{t: t, h: h}

	if rec := admin.do("GET", "/api/setup", ""); !strings.Contains(rec.Body.String(), `"needed":true`) {
		t.Fatalf("setup needed: %d %s", rec.Code, rec.Body)
	}
	rec := admin.do("POST", "/api/setup", `{"name":"enzo","display_name":"Enzo","password":"`+pw+`"}`)
	if rec.Code != http.StatusCreated || admin.cookie == nil {
		t.Fatalf("setup: %d %s", rec.Code, rec.Body)
	}
	if !admin.cookie.HttpOnly || admin.cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie flags: %+v", admin.cookie)
	}
	if rec := admin.do("POST", "/api/setup", `{"name":"evil","password":"`+pw+`"}`); rec.Code != http.StatusConflict {
		t.Errorf("second setup: %d %s", rec.Code, rec.Body)
	}
	if rec := admin.do("GET", "/api/me", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"role":"admin"`) {
		t.Errorf("me: %d %s", rec.Code, rec.Body)
	}

	if rec := admin.do("POST", "/api/accounts", `{"name":"johan","password":"`+pw+`"}`); rec.Code != http.StatusCreated {
		t.Fatalf("create member: %d %s", rec.Code, rec.Body)
	}
	member := &client{t: t, h: h}
	if rec := member.do("POST", "/api/login", `{"name":"johan","password":"`+pw+`"}`); rec.Code != http.StatusOK || member.cookie == nil {
		t.Fatalf("member login: %d %s", rec.Code, rec.Body)
	}
	if rec := member.do("GET", "/api/accounts", ""); rec.Code != http.StatusForbidden {
		t.Errorf("member lists accounts: %d", rec.Code)
	}
	if rec := member.do("PATCH", "/api/accounts/johan", `{"role":"admin"}`); rec.Code != http.StatusForbidden {
		t.Errorf("member promotes itself: %d", rec.Code)
	}
	if rec := admin.do("PATCH", "/api/accounts/enzo", `{"role":"member"}`); rec.Code != http.StatusConflict {
		t.Errorf("demote the last admin: %d %s", rec.Code, rec.Body)
	}

	// Disabling ends the member's session at once.
	if rec := admin.do("PATCH", "/api/accounts/johan", `{"disabled":true}`); rec.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", rec.Code, rec.Body)
	}
	if rec := member.do("GET", "/api/me", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("disabled member still signed in: %d", rec.Code)
	}

	var list []auth.Account
	rec = admin.do("GET", "/api/accounts", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list) != 2 || list[1].DisabledAt == nil {
		t.Errorf("accounts: %s, %v", rec.Body, err)
	}

	if rec := admin.do("POST", "/api/logout", `{}`); rec.Code != http.StatusNoContent || admin.cookie != nil {
		t.Errorf("logout: %d, cookie %v", rec.Code, admin.cookie)
	}
	if rec := admin.do("GET", "/api/me", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("after logout: %d", rec.Code)
	}
}

func TestLoginErrorsRevealNothingAndAreThrottled(t *testing.T) {
	_, h := newAPI(t)
	c := &client{t: t, h: h}
	c.do("POST", "/api/setup", `{"name":"enzo","password":"`+pw+`"}`)
	c.cookie = nil

	unknown := c.do("POST", "/api/login", `{"name":"ghost","password":"`+pw+`"}`)
	wrong := c.do("POST", "/api/login", `{"name":"enzo","password":"wrong password!"}`)
	if unknown.Code != http.StatusUnauthorized || wrong.Code != http.StatusUnauthorized || unknown.Body.String() != wrong.Body.String() {
		t.Errorf("unknown %d %s / wrong %d %s", unknown.Code, unknown.Body, wrong.Code, wrong.Body)
	}
	for range 4 {
		c.do("POST", "/api/login", `{"name":"enzo","password":"wrong password!"}`)
	}
	if rec := c.do("POST", "/api/login", `{"name":"enzo","password":"`+pw+`"}`); rec.Code != http.StatusTooManyRequests {
		t.Errorf("after 5 failures: %d %s", rec.Code, rec.Body)
	}
}

// TestEveryRouteIsProtected walks the route table: without a session, every
// route that is not public answers 401; with a member session, admin routes answer 403.
func TestEveryRouteIsProtected(t *testing.T) {
	cfg, h := newAPI(t)
	setup := &client{t: t, h: h}
	setup.do("POST", "/api/setup", `{"name":"enzo","password":"`+pw+`"}`)
	setup.do("POST", "/api/accounts", `{"name":"johan","password":"`+pw+`"}`)
	johan := &client{t: t, h: h}
	johan.do("POST", "/api/login", `{"name":"johan","password":"`+pw+`"}`)

	publics := map[string]bool{}
	for _, rt := range routes(cfg) {
		method, path, _ := strings.Cut(rt.pattern, " ")
		path = strings.ReplaceAll(path, "{name}", "johan")
		path = strings.ReplaceAll(path, "{id}", "1")
		path = strings.ReplaceAll(path, "{token}", strings.Repeat("A", 43))
		body := ""
		if method != "GET" {
			body = `{}`
		}
		anon := &client{t: t, h: h}
		res := anon.do(method, path, body)
		code := res.Code
		switch rt.access {
		case public:
			publics[rt.pattern] = true
			// A public route may refuse bad input, never for want of a session.
			if strings.Contains(res.Body.String(), auth.ErrNoSession.Error()) {
				t.Errorf("%s is public but asked for a session", rt.pattern)
			}
		case member, admin:
			if code != http.StatusUnauthorized {
				t.Errorf("%s without a session: %d, want 401", rt.pattern, code)
			}
		}
		if rt.access == admin {
			if code := johan.do(method, path, body).Code; code != http.StatusForbidden {
				t.Errorf("%s as a member: %d, want 403", rt.pattern, code)
			}
		}
	}
	// The public list is deliberate: adding a public route must change this test.
	// The share link routes are the guest's: read-only, behind the link's token
	// (and its password), never a session. See docs/architecture/share.md.
	want := []string{"GET /api/health", "GET /api/setup", "POST /api/setup", "POST /api/login", "POST /api/logout",
		"GET /api/s/{token}", "POST /api/s/{token}/unlock", "GET /api/s/{token}/files", "GET /api/s/{token}/content", "GET /api/s/{token}/zip"}
	if len(publics) != len(want) {
		t.Errorf("public routes %v, want %v", publics, want)
	}
	for _, p := range want {
		if !publics[p] {
			t.Errorf("%s should be public", p)
		}
	}
}

func TestStateChangesRequireJSON(t *testing.T) {
	_, h := newAPI(t)
	req := httptest.NewRequest("POST", "/api/login", strings.NewReader("name=enzo&password=x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("form post: %d, want 415", rec.Code)
	}
}
