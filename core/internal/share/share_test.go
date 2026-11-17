package share

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/C2-POLY/cloud-os/core/internal/auth"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/testdb"
)

const linkPassword = "sesame ouvre-toi"

func newService(t *testing.T) *Service {
	t.Helper()
	pool, _ := testdb.Migrated(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO accounts (name, display_name, password_hash, role) VALUES
		('enzo', 'Enzo', 'x', 'admin'), ('johan', 'Johan', 'x', 'member')`); err != nil {
		t.Fatal(err)
	}
	s := NewService(pool, &storage.Store{Root: t.TempDir(), Pool: pool})
	for _, p := range []string{"Photos/plage.jpg", "Photos/2024/chien.jpg", "Photos-privé/secret.txt", "racine.txt"} {
		put(t, s, "enzo", p, "contenu de "+p)
	}
	return s
}

func put(t *testing.T, s *Service, owner, p, content string) storage.File {
	t.Helper()
	f, err := s.Files.Put(context.Background(), owner, p, strings.NewReader(content), storage.NoExpectations())
	if err != nil {
		t.Fatalf("put %s: %v", p, err)
	}
	return f
}

func create(t *testing.T, s *Service, owner, p, password string) (Share, string) {
	t.Helper()
	sh, token, err := s.Create(context.Background(), owner, p, time.Now().Add(time.Hour), password)
	if err != nil {
		t.Fatalf("create %s: %v", p, err)
	}
	return sh, token
}

func TestTokenIsRandomLongAndStoredHashed(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	_, a := create(t, s, "enzo", "Photos", "")
	_, b := create(t, s, "enzo", "Photos", "")
	if len(a) != 43 || a == b {
		t.Fatalf("tokens %q and %q: want two different 43-character tokens (256 bits)", a, b)
	}
	var stored int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM shares WHERE token_hash = sha256(convert_to($1, 'UTF8'))`, a).Scan(&stored); err != nil || stored != 1 {
		t.Errorf("rows holding sha256(token): %d (%v), want 1", stored, err)
	}
	var clear int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM shares WHERE position(convert_to($1, 'UTF8') in token_hash) > 0`, a).Scan(&clear); err != nil || clear != 0 {
		t.Errorf("rows holding the token in clear: %d (%v)", clear, err)
	}
}

func TestCreateRefusesBadInput(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	future := time.Now().Add(time.Hour)
	for name, err := range map[string]error{
		"past expiry": func() error {
			_, _, err := s.Create(ctx, "enzo", "Photos", time.Now().Add(-time.Second), "")
			return err
		}(),
		"no expiry":      func() error { _, _, err := s.Create(ctx, "enzo", "Photos", time.Time{}, ""); return err }(),
		"short password": func() error { _, _, err := s.Create(ctx, "enzo", "Photos", future, "abc"); return err }(),
	} {
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
	if _, _, err := s.Create(ctx, "johan", "Photos", future, ""); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("share another account's folder: %v, want ErrNotFound", err)
	}
	if _, _, err := s.Create(ctx, "enzo", "", future, ""); !errors.Is(err, storage.ErrInvalidPath) {
		t.Errorf("share the whole space: %v, want ErrInvalidPath", err)
	}
	if _, _, err := s.Create(ctx, "enzo", "../johan", future, ""); !errors.Is(err, storage.ErrInvalidPath) {
		t.Errorf("share ..: %v, want ErrInvalidPath", err)
	}
}

func TestResolveGivesNothingForDeadLinks(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	_, live := create(t, s, "enzo", "Photos", "")
	if _, f, err := s.Resolve(ctx, live); err != nil || f.Path != "Photos" {
		t.Fatalf("live link: %+v, %v", f, err)
	}

	expired, expiredToken := create(t, s, "enzo", "racine.txt", "")
	if _, err := s.Pool.Exec(ctx, `UPDATE shares SET expires_at = now() - interval '1 second' WHERE id = $1`, expired.ID); err != nil {
		t.Fatal(err)
	}
	revoked, revokedToken := create(t, s, "enzo", "racine.txt", "")
	if err := s.Revoke(ctx, "johan", revoked.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("johan revokes enzo's link: %v, want ErrNotFound", err)
	}
	if err := s.Revoke(ctx, "enzo", revoked.ID); err != nil {
		t.Fatal(err)
	}

	for name, token := range map[string]string{
		"expired": expiredToken, "revoked": revokedToken, "unknown": strings.Repeat("A", 43),
		"short": "abc", "empty": "", "live token with a suffix": live + "x",
	} {
		if _, _, err := s.Resolve(ctx, token); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: %v, want ErrNotFound", name, err)
		}
	}

	// A disabled account's links give nothing.
	if _, err := s.Pool.Exec(ctx, `UPDATE accounts SET disabled_at = now() WHERE name = 'enzo'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Resolve(ctx, live); !errors.Is(err, ErrNotFound) {
		t.Errorf("disabled account: %v, want ErrNotFound", err)
	}
}

// TestLinkFollowsTheItem: renamed or moved, the link follows; in the trash it
// gives nothing; restored it works again; purged, the link is gone; a new file
// at the old path never inherits it.
func TestLinkFollowsTheItem(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	sh, token := create(t, s, "enzo", "racine.txt", "")

	if _, err := s.Files.Move(ctx, "enzo", "racine.txt", "Photos/renommé.txt"); err != nil {
		t.Fatal(err)
	}
	if _, f, err := s.Resolve(ctx, token); err != nil || f.Path != "Photos/renommé.txt" {
		t.Fatalf("after a move: %+v, %v", f, err)
	}

	// A new file at the old path is another item.
	put(t, s, "enzo", "racine.txt", "un autre fichier")
	if _, f, _ := s.Resolve(ctx, token); f.Path != "Photos/renommé.txt" {
		t.Errorf("the link moved to the new file at the old path: %+v", f)
	}

	item, err := s.Files.Trash(ctx, "enzo", "Photos/renommé.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Resolve(ctx, token); !errors.Is(err, ErrNotFound) {
		t.Errorf("item in the trash: %v, want ErrNotFound", err)
	}
	links, err := s.List(ctx, "enzo")
	if err != nil || len(links) != 1 || !links[0].Trashed || links[0].Expired {
		t.Errorf("owner's list while in the trash: %+v, %v", links, err)
	}
	if _, err := s.Files.Restore(ctx, "enzo", item.ID); err != nil {
		t.Fatal(err)
	}
	if _, f, err := s.Resolve(ctx, token); err != nil || f.Path != "Photos/renommé.txt" {
		t.Errorf("after a restore: %+v, %v", f, err)
	}

	// Trashing the folder above suspends the link too.
	folder, err := s.Files.Trash(ctx, "enzo", "Photos")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Resolve(ctx, token); !errors.Is(err, ErrNotFound) {
		t.Errorf("parent folder in the trash: %v, want ErrNotFound", err)
	}
	if err := s.Files.Purge(ctx, "enzo", folder.ID); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM shares WHERE id = $1`, sh.ID).Scan(&left); err != nil || left != 0 {
		t.Errorf("links left after the purge: %d (%v)", left, err)
	}
}

func TestPasswordAccess(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	open, openToken := create(t, s, "enzo", "Photos", "")
	if err := s.Check(ctx, open, ""); err != nil {
		t.Errorf("link without a password: %v", err)
	}
	if _, _, err := s.Unlock(ctx, open, "x", "ip"); !errors.Is(err, ErrInvalid) {
		t.Errorf("unlock a link without a password: %v", err)
	}
	_ = openToken

	sh, token := create(t, s, "enzo", "Photos", linkPassword)
	sh, _, err := s.Resolve(ctx, token)
	if err != nil || !sh.HasPassword {
		t.Fatalf("resolve: %+v, %v", sh, err)
	}
	if err := s.Check(ctx, sh, ""); !errors.Is(err, ErrPasswordRequired) {
		t.Errorf("no access: %v, want ErrPasswordRequired", err)
	}
	if _, _, err := s.Unlock(ctx, sh, "mauvais mot de passe", "ip"); !errors.Is(err, ErrWrongPassword) {
		t.Errorf("wrong password: %v", err)
	}
	grant, expires, err := s.Unlock(ctx, sh, linkPassword, "ip")
	if err != nil || len(grant) != 43 {
		t.Fatalf("right password: %q, %v", grant, err)
	}
	if expires.After(sh.ExpiresAt) || expires.After(time.Now().Add(DefaultGrantTTL)) {
		t.Errorf("access expires %v, after the link (%v) or after 24 h", expires, sh.ExpiresAt)
	}
	if err := s.Check(ctx, sh, grant); err != nil {
		t.Errorf("with the access: %v", err)
	}

	// The access of one link opens no other link.
	other, otherToken := create(t, s, "enzo", "racine.txt", linkPassword)
	other, _, _ = s.Resolve(ctx, otherToken)
	if err := s.Check(ctx, other, grant); !errors.Is(err, ErrPasswordRequired) {
		t.Errorf("access used on another link: %v", err)
	}

	// Revoking the link ends its accesses.
	if err := s.Revoke(ctx, "enzo", sh.ID); err != nil {
		t.Fatal(err)
	}
	var grants int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM share_grants`).Scan(&grants); err != nil || grants != 0 {
		t.Errorf("accesses left after the revoke: %d (%v)", grants, err)
	}
}

func TestPasswordAttemptsAreLimited(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	_, token := create(t, s, "enzo", "Photos", linkPassword)
	sh, _, _ := s.Resolve(ctx, token)
	for range 5 {
		if _, _, err := s.Unlock(ctx, sh, "mauvais mot de passe", "192.0.2.1"); !errors.Is(err, ErrWrongPassword) {
			t.Fatalf("wrong password: %v", err)
		}
	}
	if _, _, err := s.Unlock(ctx, sh, linkPassword, "192.0.2.1"); !errors.Is(err, auth.ErrThrottled) {
		t.Errorf("after 5 failures: %v, want ErrThrottled", err)
	}
	if _, _, err := s.Unlock(ctx, sh, linkPassword, "192.0.2.2"); err != nil {
		t.Errorf("another client: %v", err)
	}
}

func TestWithinStaysInTheSubtree(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	photos, _ := s.Files.Lookup(ctx, "enzo", "Photos")
	file, _ := s.Files.Lookup(ctx, "enzo", "racine.txt")

	for rel, want := range map[string]string{"": "Photos", "2024": "Photos/2024", "2024/chien.jpg": "Photos/2024/chien.jpg"} {
		if got, err := Within(photos, rel); err != nil || got != want {
			t.Errorf("Within(Photos, %q) = %q, %v; want %q", rel, got, err, want)
		}
	}
	for _, rel := range []string{"..", "../Photos-privé", "2024/../../racine.txt", "/racine.txt", "./2024", "2024//chien.jpg", `..\racine.txt`} {
		if got, err := Within(photos, rel); err == nil {
			t.Errorf("Within(Photos, %q) = %q: it should be refused", rel, got)
		}
	}
	if got, err := Within(file, ""); err != nil || got != "racine.txt" {
		t.Errorf("Within(file, \"\") = %q, %v", got, err)
	}
	if _, err := Within(file, "x"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("a path inside a shared file: %v, want ErrNotFound", err)
	}
	if got := Relative(photos, "Photos/2024/chien.jpg"); got != "2024/chien.jpg" {
		t.Errorf("Relative = %q", got)
	}
}

func TestCleanDeletesOnlyOldLinks(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	old, _ := create(t, s, "enzo", "Photos", "")
	recent, _ := create(t, s, "enzo", "Photos", "")
	live, liveToken := create(t, s, "enzo", "Photos", linkPassword)
	live, _, _ = s.Resolve(ctx, liveToken)
	if _, _, err := s.Unlock(ctx, live, linkPassword, "ip"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE shares SET expires_at = now() - interval '31 days' WHERE id = $1;`, old.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE shares SET expires_at = now() - interval '1 day' WHERE id = $1`, recent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE share_grants SET expires_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	n, err := s.Clean(ctx)
	if err != nil || n != 1 {
		t.Fatalf("clean: %d, %v; want 1 link deleted", n, err)
	}
	links, _ := s.List(ctx, "enzo")
	if len(links) != 2 || links[1].ID != recent.ID || !links[1].Expired || links[0].Expired {
		t.Errorf("links left: %+v", links)
	}
	var grants int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM share_grants`).Scan(&grants); err != nil || grants != 0 {
		t.Errorf("expired accesses left: %d (%v)", grants, err)
	}
	if v, err := s.Files.Verify(ctx); err != nil || !v.Clean() {
		t.Errorf("files after the clean: %+v, %v", v, err)
	}
}
