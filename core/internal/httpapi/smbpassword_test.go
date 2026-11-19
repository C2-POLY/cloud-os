package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/C2-POLY/cloud-os/core/internal/auth"
)

type smbStatus struct {
	Exists     bool    `json:"exists"`
	CreatedAt  *string `json:"created_at"`
	LastUsedAt *string `json:"last_used_at"`
	Password   string  `json:"password"`
}

func decodeSMB(t *testing.T, rec *httptest.ResponseRecorder) smbStatus {
	t.Helper()
	var st smbStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("decode %q: %v", rec.Body, err)
	}
	return st
}

func TestSMBPasswordRoutes(t *testing.T) {
	cfg, h := newAPI(t)
	enzo := &client{t: t, h: h}
	enzo.do("POST", "/api/setup", `{"name":"enzo","password":"`+pw+`"}`)
	enzo.do("POST", "/api/accounts", `{"name":"johan","password":"`+pw+`"}`)
	johan := &client{t: t, h: h}
	johan.do("POST", "/api/login", `{"name":"johan","password":"`+pw+`"}`)
	var revoked []string
	cfg.Auth.OnSMBRevoke = func(name string) { revoked = append(revoked, name) }

	if st := decodeSMB(t, johan.do("GET", "/api/me/smb-password", "")); st.Exists || st.CreatedAt != nil || st.Password != "" {
		t.Fatalf("status before: %+v", st)
	}

	// A state change must be JSON: a cross-site form cannot draw a password.
	req := httptest.NewRequest("POST", "/api/me/smb-password", strings.NewReader("x=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(johan.cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("form post: %d, want 415", rec.Code)
	}

	rec = johan.do("POST", "/api/me/smb-password", `{}`)
	if rec.Code != http.StatusCreated || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("create: %d %s, Cache-Control %q", rec.Code, rec.Body, rec.Header().Get("Cache-Control"))
	}
	created := decodeSMB(t, rec)
	if !created.Exists || created.CreatedAt == nil || len(created.Password) != auth.SMBPasswordLen {
		t.Fatalf("created: %+v", created)
	}
	c, err := cfg.Auth.SMBHash(context.Background(), "johan", "192.0.2.9")
	if err != nil || c.NTHash != auth.NTHash(created.Password) {
		t.Fatalf("the password given is not the one kept: %v", err)
	}

	// The status never shows the password again.
	rec = johan.do("GET", "/api/me/smb-password", "")
	if st := decodeSMB(t, rec); !st.Exists || st.Password != "" || strings.Contains(rec.Body.String(), created.Password) {
		t.Errorf("status after create: %s", rec.Body)
	}
	// Each account sees and changes its own only.
	if st := decodeSMB(t, enzo.do("GET", "/api/me/smb-password", "")); st.Exists {
		t.Errorf("enzo sees johan's password: %+v", st)
	}
	revoked = nil
	if rec := enzo.do("DELETE", "/api/me/smb-password", `{}`); rec.Code != http.StatusNoContent {
		t.Errorf("enzo revokes: %d", rec.Code)
	}
	if len(revoked) != 1 || revoked[0] != "enzo" {
		t.Errorf("enzo's revoke closed the sessions of %v", revoked)
	}
	if st := decodeSMB(t, johan.do("GET", "/api/me/smb-password", "")); !st.Exists {
		t.Error("enzo's revoke removed johan's password")
	}

	// Regenerate, then revoke.
	again := decodeSMB(t, johan.do("POST", "/api/me/smb-password", `{}`))
	if again.Password == "" || again.Password == created.Password {
		t.Errorf("regenerate: %+v", again)
	}
	revoked = nil
	if rec := johan.do("DELETE", "/api/me/smb-password", `{}`); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body)
	}
	if len(revoked) != 1 || revoked[0] != "johan" {
		t.Errorf("revoke closed the sessions of %v", revoked)
	}
	if st := decodeSMB(t, johan.do("GET", "/api/me/smb-password", "")); st.Exists {
		t.Errorf("status after revoke: %+v", st)
	}

	// The SMB password signs in neither on the web nor over WebDAV.
	smb := decodeSMB(t, johan.do("POST", "/api/me/smb-password", `{}`)).Password
	anon := &client{t: t, h: h}
	if rec := anon.do("POST", "/api/login", `{"name":"johan","password":"`+smb+`"}`); rec.Code != http.StatusUnauthorized {
		t.Errorf("web login with the SMB password: %d", rec.Code)
	}
	req = httptest.NewRequest("PROPFIND", "/dav/", nil)
	req.SetBasicAuth("johan", smb)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("WebDAV with the SMB password: %d", rec.Code)
	}
}
