package auth

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The NT hash of "Password", from MS-NLMP 4.2.1 (NTOWFv1, the key of NTOWFv2).
func TestNTHashMatchesMSNLMP(t *testing.T) {
	got := NTHash("Password")
	if want := "a4f49c406510bdcab6824ee7c30fd852"; hex.EncodeToString(got[:]) != want {
		t.Errorf("NTHash(Password) = %x, want %s", got, want)
	}
	// UTF-16LE, not the bytes of the UTF-8: a character outside ASCII changes it.
	if NTHash("é") == NTHash("\xc3\xa9\x00") {
		t.Error("the hash is not taken over UTF-16LE")
	}
}

func TestNewSMBPasswordShape(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		p := newSMBPassword()
		if len(p) != SMBPasswordLen {
			t.Fatalf("length %d: %q", len(p), p)
		}
		for _, r := range p {
			if !strings.ContainsRune(smbAlphabet, r) {
				t.Fatalf("%q has %q, outside the alphabet", p, r)
			}
		}
		if seen[p] {
			t.Fatalf("%q drawn twice", p)
		}
		seen[p] = true
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// smbBox is a box with two members, enzo and johan, whose revokes are recorded.
func smbBox(t *testing.T) (*Service, *[]string) {
	t.Helper()
	s := newService(t)
	ctx := context.Background()
	if _, err := s.Setup(ctx, "enzo", "", pw); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, "johan", "", pw, RoleMember); err != nil {
		t.Fatal(err)
	}
	revoked := &[]string{}
	s.OnSMBRevoke = func(name string) { *revoked = append(*revoked, name) }
	return s, revoked
}

func TestSMBPasswordLifecycle(t *testing.T) {
	s, revoked := smbBox(t)
	ctx := context.Background()

	if st, err := s.SMBPasswordStatus(ctx, "johan"); err != nil || st.Exists || st.CreatedAt != nil {
		t.Fatalf("status before: %+v, %v", st, err)
	}
	if _, err := s.SMBHash(ctx, "johan", "192.0.2.1"); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("hash without a password: %v", err)
	}

	first, st, err := s.NewSMBPassword(ctx, "johan")
	if err != nil || !st.Exists || st.CreatedAt == nil {
		t.Fatalf("new: %+v, %v", st, err)
	}
	// Only the NT hash is kept, never the password.
	var stored []byte
	var raw string
	if err := s.Pool.QueryRow(ctx, `SELECT nt_hash, row_to_json(p)::text FROM smb_passwords p WHERE account = 'johan'`).Scan(&stored, &raw); err != nil {
		t.Fatal(err)
	}
	if want := NTHash(first); string(stored) != string(want[:]) {
		t.Errorf("stored %x, want the NT hash %x", stored, want)
	}
	if strings.Contains(raw, first) {
		t.Errorf("the password is stored: %s", raw)
	}

	c, err := s.SMBHash(ctx, "Johan", "192.0.2.1") // as a client may type it
	if err != nil || c.NTHash != NTHash(first) || c.Account != "johan" {
		t.Fatalf("hash: %+v, %v", c, err)
	}
	// The hash never shows in a log line or a JSON dump of the credential.
	hexHash := hex.EncodeToString(c.NTHash[:])
	for _, out := range []string{fmt.Sprintf("%v %+v %#v %s", c, c, c, c), mustJSON(t, c)} {
		if strings.Contains(out, hexHash) || strings.Contains(out, fmt.Sprint(c.NTHash)) {
			t.Errorf("the hash leaks: %s", out)
		}
	}
	c.Succeeded(ctx)
	if st, _ := s.SMBPasswordStatus(ctx, "johan"); st.LastUsedAt == nil {
		t.Error("last use not noted")
	}

	// Regenerating replaces the old password, whose hash is gone.
	*revoked = nil
	second, st, err := s.NewSMBPassword(ctx, "johan")
	if err != nil || second == first || st.LastUsedAt != nil {
		t.Fatalf("regenerate: %q, %+v, %v", second, st, err)
	}
	if len(*revoked) != 1 || (*revoked)[0] != "johan" {
		t.Errorf("regenerating closed the sessions of %v, want [johan]", *revoked)
	}
	c, err = s.SMBHash(ctx, "johan", "192.0.2.1")
	if err != nil || c.NTHash != NTHash(second) || c.NTHash == NTHash(first) {
		t.Errorf("hash after regenerate: %x, %v", c.NTHash, err)
	}
	if st, _ := s.SMBPasswordStatus(ctx, "johan"); st.LastUsedAt != nil {
		t.Error("the new password inherited the old one's last use")
	}
	// A late success of the old password does not mark the new one as used.
	old := SMBCredential{Account: "johan", NTHash: NTHash(first), svc: s, key: "smb:johan|x"}
	old.Succeeded(ctx)
	if st, _ := s.SMBPasswordStatus(ctx, "johan"); st.LastUsedAt != nil {
		t.Error("the old password's success marked the new one as used")
	}

	// Each account has its own: enzo's is untouched by johan's.
	if _, err := s.SMBHash(ctx, "enzo", "192.0.2.1"); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("enzo has no SMB password, got %v", err)
	}

	// Revoking forgets it and closes its sessions.
	*revoked = nil
	if err := s.RevokeSMBPassword(ctx, "johan"); err != nil {
		t.Fatal(err)
	}
	if len(*revoked) != 1 || (*revoked)[0] != "johan" {
		t.Errorf("revoke closed the sessions of %v, want [johan]", *revoked)
	}
	if st, err := s.SMBPasswordStatus(ctx, "johan"); err != nil || st.Exists {
		t.Errorf("status after revoke: %+v, %v", st, err)
	}
	if _, err := s.SMBHash(ctx, "johan", "192.0.2.1"); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("hash after revoke: %v", err)
	}
	// Revoking twice is not an error.
	if err := s.RevokeSMBPassword(ctx, "johan"); err != nil {
		t.Errorf("second revoke: %v", err)
	}

	if _, _, err := s.NewSMBPassword(ctx, "ghost"); !errors.Is(err, ErrNoAccount) {
		t.Errorf("new for an unknown account: %v", err)
	}
}

func TestSMBRefusesADisabledAccount(t *testing.T) {
	s, revoked := smbBox(t)
	ctx := context.Background()
	if _, _, err := s.NewSMBPassword(ctx, "johan"); err != nil {
		t.Fatal(err)
	}
	*revoked = nil
	yes := true
	if _, err := s.Update(ctx, "johan", nil, &yes); err != nil {
		t.Fatal(err)
	}
	if len(*revoked) != 1 || (*revoked)[0] != "johan" {
		t.Errorf("disabling closed the SMB sessions of %v, want [johan]", *revoked)
	}
	if _, err := s.SMBHash(ctx, "johan", "192.0.2.1"); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("disabled account: %v", err)
	}
	if _, _, err := s.NewSMBPassword(ctx, "johan"); !errors.Is(err, ErrNoAccount) {
		t.Errorf("new for a disabled account: %v", err)
	}
	// Enabling it again gives back the access with the same password.
	no := false
	if _, err := s.Update(ctx, "johan", nil, &no); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SMBHash(ctx, "johan", "192.0.2.1"); err != nil {
		t.Errorf("enabled again: %v", err)
	}
}

func TestSMBAttemptsAreLimited(t *testing.T) {
	s, _ := smbBox(t)
	ctx := context.Background()
	if _, _, err := s.NewSMBPassword(ctx, "johan"); err != nil {
		t.Fatal(err)
	}
	// An attempt the server never reports as a success counts as a failure.
	for i := range 5 {
		if _, err := s.SMBHash(ctx, "johan", "192.0.2.1"); err != nil {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
	}
	if _, err := s.SMBHash(ctx, "johan", "192.0.2.1"); !errors.Is(err, ErrThrottled) {
		t.Fatalf("6th attempt: %v, want ErrThrottled", err)
	}
	// An unknown name spends attempts too.
	for range 5 {
		s.SMBHash(ctx, "ghost", "192.0.2.1") //nolint:errcheck
	}
	if _, err := s.SMBHash(ctx, "ghost", "192.0.2.1"); !errors.Is(err, ErrThrottled) {
		t.Errorf("unknown name not limited: %v", err)
	}
	// Apart from the web login: johan still signs in on the web from there.
	if _, _, err := s.Login(ctx, "johan", pw, "192.0.2.1"); err != nil {
		t.Errorf("web login blocked by SMB failures: %v", err)
	}
	// Another client is not blocked; a success forgets its failures.
	for range 4 {
		s.SMBHash(ctx, "johan", "192.0.2.2") //nolint:errcheck
	}
	c, err := s.SMBHash(ctx, "johan", "192.0.2.2")
	if err != nil {
		t.Fatalf("other client: %v", err)
	}
	c.Succeeded(ctx)
	if _, err := s.SMBHash(ctx, "johan", "192.0.2.2"); err != nil {
		t.Errorf("after a success: %v", err)
	}
}

// The two secrets never stand in for each other.
func TestSMBAndAccountPasswordsAreApart(t *testing.T) {
	s, _ := smbBox(t)
	ctx := context.Background()
	smb, _, err := s.NewSMBPassword(ctx, "johan")
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.Login(ctx, "johan", smb, "192.0.2.1"); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("Login with the SMB password: %v", err)
	}
	if _, err := s.CheckCredentials(ctx, "johan", smb, "192.0.2.1"); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("CheckCredentials with the SMB password: %v", err)
	}
	h := s.NewBasicAuth("test").Require(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	req := httptest.NewRequest("PROPFIND", "/dav/", nil)
	req.SetBasicAuth("johan", smb)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("WebDAV with the SMB password: %d", rec.Code)
	}

	// SMBHash only ever gives the SMB password's hash: the account's own
	// password, which has no NT hash, does not open SMB.
	c, err := s.SMBHash(ctx, "johan", "192.0.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if c.NTHash == NTHash(pw) || c.NTHash != NTHash(smb) {
		t.Errorf("SMBHash gave %x, not the SMB password's", c.NTHash)
	}
	if _, err := s.SMBHash(ctx, "enzo", "192.0.2.3"); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("an account without an SMB password opens SMB: %v", err)
	}
}
