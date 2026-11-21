package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/C2-POLY/cloud-os/core/internal/activity"
	"github.com/C2-POLY/cloud-os/core/internal/albums"
	"github.com/C2-POLY/cloud-os/core/internal/auth"
	"github.com/C2-POLY/cloud-os/core/internal/edge"
	"github.com/C2-POLY/cloud-os/core/internal/share"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/testdb"
)

// The tailscale service's address on the Compose network, in these tests.
const tsProxy = "172.18.0.9"

// newEdgeAPI is the API behind the box's front door: the tailscale service is
// the trusted proxy, the built front is served, and enzo is signed up.
func newEdgeAPI(t *testing.T, front edge.Config, shareOrigin string) http.Handler {
	t.Helper()
	pool, _ := testdb.Migrated(t)
	files := &storage.Store{Root: t.TempDir(), Pool: pool}
	static := t.TempDir()
	if err := os.MkdirAll(filepath.Join(static, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"index.html": "<html>", "assets/app.js": "js"} {
		if err := os.WriteFile(filepath.Join(static, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if front.Proxies == nil {
		p, err := edge.ParseProxies(tsProxy)
		if err != nil {
			t.Fatal(err)
		}
		front.Proxies = p
	}
	h := NewHandler(Config{
		Version: "test", Health: &fakeStore{version: 3}, Auth: auth.NewService(pool), Files: files,
		Shares: share.NewService(pool, files), Albums: &albums.Service{Pool: pool, Files: files},
		Activity: &activity.Log{Pool: pool}, StaticDir: static, Edge: front, ShareOrigin: shareOrigin,
	})
	setup := send(h, tsProxy+":40000", "POST", "/api/setup", `{"name":"enzo","password":"`+pw+`"}`, "X-Forwarded-For", "100.101.0.1")
	if setup.Code != http.StatusCreated {
		t.Fatalf("setup: %d %s", setup.Code, setup.Body)
	}
	return h
}

// send makes one request from remote, with header pairs.
func send(h http.Handler, remote, method, path, body string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = remote
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Add(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func cookieNamed(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// The per-address limit of logins counts each device behind the proxy apart,
// and a client that writes X-Forwarded-For itself cannot dodge it.
func TestLoginLimitBehindTheProxy(t *testing.T) {
	h := newEdgeAPI(t, edge.Config{}, "")
	bad := `{"name":"enzo","password":"wrong"}`
	for i := range 5 {
		if rec := send(h, tsProxy+":40000", "POST", "/api/login", bad, "X-Forwarded-For", "100.101.0.7"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d %s", i, rec.Code, rec.Body)
		}
	}
	if rec := send(h, tsProxy+":40000", "POST", "/api/login", bad, "X-Forwarded-For", "100.101.0.7"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("sixth attempt from the same device: %d", rec.Code)
	}
	// Another device behind the same proxy is not held back by the first.
	good := `{"name":"enzo","password":"` + pw + `"}`
	if rec := send(h, tsProxy+":40000", "POST", "/api/login", good, "X-Forwarded-For", "100.101.0.8"); rec.Code != http.StatusOK {
		t.Fatalf("another device: %d %s", rec.Code, rec.Body)
	}

	// A client that is not the proxy changes its X-Forwarded-For each time:
	// the limit still counts its own address.
	for i := range 5 {
		send(h, "192.168.1.66:5000", "POST", "/api/login", bad, "X-Forwarded-For", "10.0.0."+string(rune('1'+i)))
	}
	if rec := send(h, "192.168.1.66:5000", "POST", "/api/login", bad, "X-Forwarded-For", "10.0.0.99"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("a forged X-Forwarded-For dodged the limit: %d", rec.Code)
	}

	// WebDAV's Basic logins count by the device too.
	for range 5 {
		req := httptest.NewRequest("PROPFIND", "/dav/", nil)
		req.RemoteAddr = tsProxy + ":40000"
		req.Header.Set("X-Forwarded-For", "100.101.0.9")
		req.SetBasicAuth("enzo", "wrong")
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	for _, c := range []struct {
		device string
		code   int
	}{{"100.101.0.9", http.StatusTooManyRequests}, {"100.101.0.10", http.StatusUnauthorized}} {
		req := httptest.NewRequest("PROPFIND", "/dav/", nil)
		req.RemoteAddr = tsProxy + ":40000"
		req.Header.Set("X-Forwarded-For", c.device)
		req.SetBasicAuth("enzo", "wrong")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.code {
			t.Errorf("WebDAV from %s: %d, want %d", c.device, rec.Code, c.code)
		}
	}
}

func TestSessionCookieSecure(t *testing.T) {
	login := `{"name":"enzo","password":"` + pw + `"}`
	plain := newEdgeAPI(t, edge.Config{}, "")
	for _, c := range []struct {
		name   string
		remote string
		hdr    []string
		want   bool
	}{
		{"local http", "192.168.1.20:5000", nil, false},
		{"a client's X-Forwarded-Proto", "192.168.1.20:5000", []string{"X-Forwarded-Proto", "https"}, false},
		{"the proxy's X-Forwarded-Proto", tsProxy + ":40000", []string{"X-Forwarded-Proto", "https", "X-Forwarded-For", "100.101.0.2"}, true},
	} {
		rec := send(plain, c.remote, "POST", "/api/login", login, c.hdr...)
		ck := cookieNamed(rec, auth.CookieName)
		if rec.Code != http.StatusOK || ck == nil {
			t.Fatalf("%s: login %d %s", c.name, rec.Code, rec.Body)
		}
		if ck.Secure != c.want {
			t.Errorf("%s: Secure %v, want %v", c.name, ck.Secure, c.want)
		}
	}
	// CLOUDOS_PUBLIC_URL in https: Secure, even for a plain request.
	public := newEdgeAPI(t, edge.Config{PublicHTTPS: true}, "")
	ck := cookieNamed(send(public, "192.168.1.20:5000", "POST", "/api/login", login), auth.CookieName)
	if ck == nil || !ck.Secure {
		t.Errorf("public https: cookie %+v", ck)
	}
}

func TestConfigGivesTheShareOrigin(t *testing.T) {
	h := newEdgeAPI(t, edge.Config{}, "https://cloudos.example.ts.net:8443")
	if rec := send(h, "192.168.1.20:5000", "GET", "/api/config", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("without a session: %d", rec.Code)
	}
	login := send(h, "192.168.1.20:5000", "POST", "/api/login", `{"name":"enzo","password":"`+pw+`"}`)
	var signed struct{ Token string }
	if err := json.Unmarshal(login.Body.Bytes(), &signed); err != nil || signed.Token == "" {
		t.Fatalf("login: %s", login.Body)
	}
	rec := send(h, "192.168.1.20:5000", "GET", "/api/config", "", "Authorization", "Bearer "+signed.Token)
	var cfg struct {
		ShareOrigin *string `json:"share_origin"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &cfg); rec.Code != http.StatusOK || err != nil || cfg.ShareOrigin == nil || *cfg.ShareOrigin != "https://cloudos.example.ts.net:8443" {
		t.Fatalf("config: %d %s", rec.Code, rec.Body)
	}

	// Without Funnel, the origin is empty and the web front keeps its own.
	h = newEdgeAPI(t, edge.Config{}, "")
	login = send(h, "192.168.1.20:5000", "POST", "/api/login", `{"name":"enzo","password":"`+pw+`"}`)
	_ = json.Unmarshal(login.Body.Bytes(), &signed)
	if rec := send(h, "192.168.1.20:5000", "GET", "/api/config", "", "Authorization", "Bearer "+signed.Token); !strings.Contains(rec.Body.String(), `"share_origin":""`) {
		t.Fatalf("config without Funnel: %s", rec.Body)
	}
}

// A guest from the internet, through Funnel, opens a share link and its
// password, and reaches nothing else of the box.
func TestFunnelGuestReachesOnlyTheShareLink(t *testing.T) {
	h := newEdgeAPI(t, edge.Config{PublicHTTPS: true}, "https://cloudos.example.ts.net:8443")
	member := func(method, path, body string, hdr ...string) *httptest.ResponseRecorder {
		return send(h, tsProxy+":40000", method, path, body, append([]string{"X-Forwarded-For", "100.101.0.1", "X-Forwarded-Proto", "https"}, hdr...)...)
	}
	login := member("POST", "/api/login", `{"name":"enzo","password":"`+pw+`"}`)
	session := cookieNamed(login, auth.CookieName)
	if session == nil {
		t.Fatalf("login: %d %s", login.Code, login.Body)
	}
	cookie := "Cookie"
	sess := auth.CookieName + "=" + session.Value
	if rec := member("PUT", "/api/files/content?path=plage.jpg", "sable", cookie, sess, auth.ClientHeader, "1"); rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	body, _ := json.Marshal(map[string]any{"path": "plage.jpg", "expires_at": time.Now().Add(time.Hour), "password": "sésame"})
	rec := member("POST", "/api/shares", string(body), cookie, sess)
	var link created
	if err := json.Unmarshal(rec.Body.Bytes(), &link); rec.Code != http.StatusCreated || err != nil {
		t.Fatalf("share: %d %s", rec.Code, rec.Body)
	}

	// The guest comes through Funnel: the proxy names a public address.
	guest := func(method, path, body string, hdr ...string) *httptest.ResponseRecorder {
		return send(h, tsProxy+":40000", method, path, body, append([]string{
			"X-Forwarded-For", "203.0.113.50", "X-Forwarded-Proto", "https", edge.FunnelHeader, "?1",
		}, hdr...)...)
	}
	base := "/api/s/" + link.Token
	if rec := guest("GET", "/s/"+link.Token, ""); rec.Code != http.StatusOK || rec.Body.String() != "<html>" {
		t.Errorf("guest page: %d %q", rec.Code, rec.Body)
	}
	if rec := guest("GET", "/assets/app.js", ""); rec.Code != http.StatusOK {
		t.Errorf("assets: %d", rec.Code)
	}
	// The link's own answer before its password: the route was reached.
	if rec := guest("GET", base, ""); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "password required") {
		t.Errorf("link info: %d %s", rec.Code, rec.Body)
	}
	unlock := guest("POST", base+"/unlock", `{"password":"sésame"}`)
	grant := cookieNamed(unlock, GrantCookie)
	if unlock.Code != http.StatusNoContent || grant == nil {
		t.Fatalf("unlock: %d %s", unlock.Code, unlock.Body)
	}
	if !grant.Secure {
		t.Error("the share's access cookie is not Secure through Funnel")
	}
	if rec := guest("GET", base+"/content?path=", "", cookie, GrantCookie+"="+grant.Value); rec.Code != http.StatusOK || rec.Body.String() != "sable" {
		t.Errorf("content: %d %q", rec.Code, rec.Body)
	}

	// Everything else answers the guard's 404, even with the member's session.
	for _, p := range []struct{ method, path, body string }{
		{"GET", "/", ""}, {"GET", "/index.html", ""}, {"POST", "/api/login", `{"name":"enzo","password":"` + pw + `"}`},
		{"GET", "/api/setup", ""}, {"POST", "/api/setup", `{}`}, {"GET", "/api/me", ""}, {"GET", "/api/files?path=", ""},
		{"GET", "/api/files/content?path=plage.jpg", ""}, {"GET", "/api/shares", ""}, {"GET", "/api/config", ""},
		{"GET", "/api/health", ""}, {"PROPFIND", "/dav/", ""}, {"GET", "/dav", ""}, {"GET", "/s/../api/me", ""},
	} {
		rec := guest(p.method, p.path, p.body, cookie, sess)
		if rec.Code != http.StatusNotFound || strings.Contains(rec.Header().Get("Content-Type"), "json") {
			t.Errorf("Funnel %s %s: %d %s, want the guard's 404", p.method, p.path, rec.Code, rec.Body)
		}
	}
	if rec := guest("PROPFIND", "/dav/", "", "Authorization", "Basic ZW56bzp4"); rec.Code != http.StatusNotFound {
		t.Errorf("Funnel WebDAV with Basic: %d", rec.Code)
	}
}
