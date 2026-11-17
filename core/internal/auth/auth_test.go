package auth

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/C2-POLY/cloud-os/core/internal/testdb"
)

const pw = "correct horse battery"

func newService(t *testing.T) *Service {
	t.Helper()
	pool, _ := testdb.Migrated(t)
	return NewService(pool)
}

func TestPasswordHash(t *testing.T) {
	h, err := HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Errorf("hash %q", h)
	}
	if ok, err := VerifyPassword(pw, h); !ok || err != nil {
		t.Errorf("right password: %v, %v", ok, err)
	}
	if ok, err := VerifyPassword(pw+"!", h); ok || err != nil {
		t.Errorf("wrong password: %v, %v", ok, err)
	}
	if h2, _ := HashPassword(pw); h2 == h {
		t.Error("two hashes of one password are equal: the salt is not random")
	}
	for _, bad := range []string{"", "$argon2i$v=19$m=1,t=1,p=1$c2FsdA$a2V5", "$argon2id$v=19$m=x$c2FsdA$a2V5", "$argon2id$v=19$m=1,t=1,p=1$!!$a2V5"} {
		if _, err := VerifyPassword(pw, bad); err == nil {
			t.Errorf("VerifyPassword(%q) accepted a malformed hash", bad)
		}
	}
	if err := CheckPassword("short"); !errors.Is(err, ErrWeakPassword) {
		t.Errorf("short password: %v", err)
	}
}

func TestLimiter(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := NewLimiter(3, time.Minute)
	l.now = func() time.Time { return now }
	for range 3 {
		if !l.Allow("k") {
			t.Fatal("blocked too early")
		}
		l.Fail("k")
	}
	if l.Allow("k") {
		t.Fatal("not blocked after 3 failures")
	}
	if !l.Allow("other") {
		t.Fatal("another key is blocked")
	}
	now = now.Add(61 * time.Second)
	if !l.Allow("k") {
		t.Fatal("still blocked after the window")
	}
}

func TestSetupOnlyOnceEvenConcurrently(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	if needed, err := s.SetupNeeded(ctx); !needed || err != nil {
		t.Fatalf("needed = %v, %v", needed, err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = s.Setup(ctx, []string{"enzo", "johan", "a", "b", "c", "d"}[i], "", pw)
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, ErrSetupDone):
			t.Errorf("unexpected: %v", err)
		}
	}
	accounts, _ := s.List(ctx)
	if ok != 1 || len(accounts) != 1 || !accounts[0].IsAdmin() {
		t.Errorf("%d setups succeeded, accounts %+v", ok, accounts)
	}
	if needed, _ := s.SetupNeeded(ctx); needed {
		t.Error("setup still offered")
	}
}

func TestCreateValidates(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	if _, err := s.Setup(ctx, "enzo", "Enzo", pw); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, role, password string
		want                 error
	}{
		{"enzo", RoleMember, pw, ErrAccountExists},
		{"Johan", RoleMember, pw, ErrInvalid},
		{"../x", RoleMember, pw, ErrInvalid},
		{"johan", "owner", pw, ErrInvalid},
		{"johan", RoleMember, "short", ErrWeakPassword},
	}
	for _, c := range cases {
		if _, err := s.Create(ctx, c.name, "", c.password, c.role); !errors.Is(err, c.want) {
			t.Errorf("Create(%q, %q): %v, want %v", c.name, c.role, err, c.want)
		}
	}
	a, err := s.Create(ctx, "johan", "", pw, RoleMember)
	if err != nil || a.DisplayName != "johan" || a.IsAdmin() {
		t.Errorf("Create johan: %+v, %v", a, err)
	}
}

func TestLastAdminIsKept(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	if _, err := s.Setup(ctx, "enzo", "", pw); err != nil {
		t.Fatal(err)
	}
	member, yes := RoleMember, true
	if _, err := s.Update(ctx, "enzo", &member, nil); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("demote last admin: %v", err)
	}
	if _, err := s.Update(ctx, "enzo", nil, &yes); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("disable last admin: %v", err)
	}
	if _, err := s.Create(ctx, "johan", "", pw, RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if a, err := s.Update(ctx, "enzo", &member, nil); err != nil || a.IsAdmin() {
		t.Errorf("demote with another admin: %+v, %v", a, err)
	}
	if _, err := s.Update(ctx, "nobody", &member, nil); !errors.Is(err, ErrNoAccount) {
		t.Errorf("unknown account: %v", err)
	}
}

func TestLoginAndSessions(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	if _, err := s.Setup(ctx, "enzo", "", pw); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, "johan", "", pw, RoleMember); err != nil {
		t.Fatal(err)
	}

	// Wrong password, unknown account and disabled account give the same error.
	if _, _, err := s.Login(ctx, "enzo", "wrong password!", "ip"); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("wrong password: %v", err)
	}
	if _, _, err := s.Login(ctx, "ghost", pw, "ip"); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("unknown account: %v", err)
	}

	token, a, err := s.Login(ctx, "johan", pw, "ip")
	if err != nil || a.Name != "johan" {
		t.Fatalf("login: %+v, %v", a, err)
	}
	if got, err := s.Authenticate(ctx, token); err != nil || got.Name != "johan" {
		t.Fatalf("authenticate: %+v, %v", got, err)
	}
	if _, err := s.Authenticate(ctx, token+"x"); !errors.Is(err, ErrNoSession) {
		t.Errorf("forged token: %v", err)
	}

	// Disabling ends the sessions and blocks the login.
	yes := true
	if _, err := s.Update(ctx, "johan", nil, &yes); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Errorf("session of a disabled account: %v", err)
	}
	if _, _, err := s.Login(ctx, "johan", pw, "ip"); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("login of a disabled account: %v", err)
	}

	// Logout and expiry.
	token, _, err = s.Login(ctx, "enzo", pw, "ip")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Errorf("after logout: %v", err)
	}
	token, _, _ = s.Login(ctx, "enzo", pw, "ip")
	if _, err := s.Pool.Exec(ctx, `UPDATE sessions SET expires_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Errorf("expired session: %v", err)
	}

	// The database holds only the token's hash.
	var stored int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE convert_to($1, 'UTF8') = token_hash`, token).Scan(&stored); err != nil || stored != 0 {
		t.Errorf("token stored in clear: %d, %v", stored, err)
	}
}

func TestLoginIsThrottled(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	if _, err := s.Setup(ctx, "enzo", "", pw); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		if _, _, err := s.Login(ctx, "enzo", "wrong password!", "1.2.3.4"); !errors.Is(err, ErrBadCredentials) {
			t.Fatalf("attempt: %v", err)
		}
	}
	if _, _, err := s.Login(ctx, "enzo", pw, "1.2.3.4"); !errors.Is(err, ErrThrottled) {
		t.Errorf("6th attempt, even right: %v", err)
	}
	if _, _, err := s.Login(ctx, "enzo", pw, "5.6.7.8"); err != nil {
		t.Errorf("another IP: %v", err)
	}
}

func TestResetPasswordEndsSessions(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	if _, err := s.Setup(ctx, "enzo", "", pw); err != nil {
		t.Fatal(err)
	}
	token, _, _ := s.Login(ctx, "enzo", pw, "ip")
	if err := s.ResetPassword(ctx, "enzo", "a brand new password"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Errorf("old session: %v", err)
	}
	if _, _, err := s.Login(ctx, "enzo", "a brand new password", "ip"); err != nil {
		t.Errorf("new password: %v", err)
	}
	if err := s.ResetPassword(ctx, "ghost", "a brand new password"); !errors.Is(err, ErrNoAccount) {
		t.Errorf("unknown account: %v", err)
	}
}
