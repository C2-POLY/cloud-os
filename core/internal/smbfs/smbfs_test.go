package smbfs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hirochachacha/go-smb2"

	"github.com/C2-POLY/cloud-os/core/internal/auth"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/testdb"
)

const (
	testUser        = "alice"
	accountPassword = "alice-account-password"
)

func TestClean(t *testing.T) {
	ok := map[string]string{
		``:               ``,
		`\`:              ``,
		`docs\a.txt`:     `docs/a.txt`,
		`\docs\a.txt`:    `docs/a.txt`,
		`docs/a.txt`:     `docs/a.txt`,
		`docs\`:          `docs`,
		"e\u0301t\u00e9": "\u00e9t\u00e9", // NFD becomes NFC, as stored
	}
	for in, want := range ok {
		got, err := Clean(in)
		if err != nil || got != want {
			t.Errorf("Clean(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		`..`, `..\..\etc\passwd`, `docs\..\..\bob\x`, `docs/../../bob`, `.`, `docs\.\a.txt`,
		`docs\\a.txt`, `a.txt:Zone.Identifier:$DATA`, `a.txt:com.apple.metadata:_kMDItemUserTags`,
		`C:\Windows`, "a\x00b", "a\x01b", "\xff\xfe", strings.Repeat("x", 256),
	} {
		if got, err := Clean(in); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("Clean(%q) = %q, %v; want not found", in, got, err)
		}
	}
}

// fixture is a box with three members: alice with her files, bob with his
// beside them, and carol. Each has an SMB password, drawn as the Settings do.
type fixture struct {
	store *storage.Store
	files map[string][]byte // alice's files, by path
	// core is the core's auth service: what the API changes, with
	// OnSMBRevoke telling the smb service through Postgres.
	core      *auth.Service
	passwords map[string]string // the SMB password of each member
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool, _ := testdb.Migrated(t)
	s := &storage.Store{Root: t.TempDir(), Pool: pool}
	big := make([]byte, 3<<20+17) // several reads of 1 MiB, and a tail
	for i := range big {
		big[i] = byte(i * 7)
	}
	fx := &fixture{store: s, files: map[string][]byte{
		"docs/a.txt":        []byte("hello over smb\n"),
		"docs/sub/b.txt":    []byte("deeper\n"),
		"photos/big.bin":    big,
		"\u00e9t\u00e9.txt": []byte("summer\n"),
	}, passwords: map[string]string{}}
	ctx := context.Background()
	fx.core = auth.NewService(pool)
	fx.core.OnSMBRevoke = NotifyRevoke(pool, nil)
	if _, err := fx.core.Setup(ctx, testUser, "", accountPassword); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bob", "carol"} {
		if _, err := fx.core.Create(ctx, name, "", name+"-account-password", auth.RoleMember); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{testUser, "bob", "carol"} {
		pw, _, err := fx.core.NewSMBPassword(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		fx.passwords[name] = pw
	}
	for p, b := range fx.files {
		if _, err := s.Put(ctx, testUser, p, bytes.NewReader(b), storage.NoExpectations()); err != nil {
			t.Fatalf("put %s: %v", p, err)
		}
	}
	if _, err := s.Put(ctx, "bob", "secret.txt", strings.NewReader("bob's\n"), storage.NoExpectations()); err != nil {
		t.Fatal(err)
	}
	return fx
}

// snapshot is everything a write could change: every file and folder under
// the data root with its bytes and times, and the index with the journal.
func (fx *fixture) snapshot(t *testing.T) string {
	t.Helper()
	var lines []string
	err := filepath.WalkDir(fx.store.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		line := fmt.Sprintf("%s %v %d %d", p, fi.Mode(), fi.Size(), fi.ModTime().UnixNano())
		if fi.Mode().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(b)
			line += " " + hex.EncodeToString(sum[:])
		}
		lines = append(lines, line)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := fx.store.Pool.Query(context.Background(),
		`SELECT format('%s %s %s %s %s %s %s', id, owner, path, kind, size, encode(sha256, 'hex'), modified_at)
		   FROM files ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	var moves, trash int
	if err := fx.store.Pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM moves), (SELECT count(*) FROM files WHERE trash_id IS NOT NULL)`).Scan(&moves, &trash); err != nil {
		t.Fatal(err)
	}
	lines = append(lines, fmt.Sprintf("moves %d trash %d", moves, trash))
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// serve starts the smb side: its own auth service, as the smb process has,
// with a limiter of its own.
func serve(t *testing.T, fx *fixture) *Server {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv, err := Start(ctx, Config{
		Addr:       "127.0.0.1:0",
		Auth:       auth.NewService(fx.store.Pool),
		Store:      fx.store,
		loginDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

func dial(t *testing.T, addr, user, password string) (*smb2.Session, error) {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	d := &smb2.Dialer{Initiator: &smb2.NTLMInitiator{User: user, Password: password}}
	s, err := d.Dial(c)
	if err != nil {
		c.Close()
		return nil, err
	}
	t.Cleanup(func() { _ = s.Logoff() })
	return s, nil
}

func mountAs(t *testing.T, addr, user, password string) *smb2.Share {
	t.Helper()
	s, err := dial(t, addr, user, password)
	if err != nil {
		t.Fatalf("login of %s: %v", user, err)
	}
	share, err := s.Mount(ShareName)
	if err != nil {
		t.Fatalf("mount %s: %v", ShareName, err)
	}
	t.Cleanup(func() { _ = share.Umount() })
	return share
}

func (fx *fixture) mount(t *testing.T, addr string) *smb2.Share {
	t.Helper()
	return mountAs(t, addr, testUser, fx.passwords[testUser])
}

// gone waits until a request on share fails: its connection was closed.
func gone(t *testing.T, share *smb2.Share) bool {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := share.Stat("."); err != nil {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func TestServer(t *testing.T) {
	fx := newFixture(t)
	srv := serve(t, fx)
	addr := srv.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go srv.WatchRevocations(ctx, fx.store.Pool, time.Hour)
	waitListening(t, fx)
	t.Run("login", func(t *testing.T) { testLogin(t, addr, fx) })
	t.Run("own drive", func(t *testing.T) { testOwnDrive(t, addr, fx) })
	t.Run("read", func(t *testing.T) { testRead(t, addr, fx) })
	t.Run("paths", func(t *testing.T) { testPaths(t, addr, fx) })
	t.Run("disabled", func(t *testing.T) { testDisabled(t, addr, fx) })
}

func testLogin(t *testing.T, addr string, fx *fixture) {
	pw := fx.passwords[testUser]
	if _, err := dial(t, addr, testUser, pw); err != nil {
		t.Fatalf("the SMB password was refused: %v", err)
	}
	if _, err := dial(t, addr, strings.ToUpper(testUser), pw); err != nil {
		t.Errorf("the name in capitals was refused: %v", err)
	}
	if _, err := dial(t, addr, testUser, "wrong password"); err == nil {
		t.Error("a wrong password logged in")
	}
	if _, err := dial(t, addr, testUser, accountPassword); err == nil {
		t.Error("the account's own password logged in over SMB")
	}
	if _, err := dial(t, addr, "bob", pw); err == nil {
		t.Error("alice's SMB password opened bob's account")
	}
	if _, err := dial(t, addr, "nobody", pw); err == nil {
		t.Error("an unknown user logged in")
	}
	if _, err := dial(t, addr, "", ""); err == nil {
		t.Error("an anonymous login was accepted")
	}
	var used *time.Time
	if err := fx.store.Pool.QueryRow(context.Background(), `SELECT last_used_at FROM smb_passwords WHERE account = $1`, testUser).Scan(&used); err != nil || used == nil {
		t.Errorf("the SMB password's last use was not noted: %v, %v", used, err)
	}
}

// Each account sees its own drive, and only it.
func testOwnDrive(t *testing.T, addr string, fx *fixture) {
	bob := mountAs(t, addr, "bob", fx.passwords["bob"])
	list, err := bob.ReadDir(".")
	if err != nil || len(list) != 1 || list[0].Name() != "secret.txt" {
		t.Errorf("bob's drive lists %v, %v; want secret.txt alone", list, err)
	}
	if _, err := bob.Stat(`docs\a.txt`); err == nil {
		t.Error("bob sees alice's docs/a.txt")
	}
	alice := fx.mount(t, addr)
	if _, err := alice.Stat("secret.txt"); err == nil {
		t.Error("alice sees bob's secret.txt")
	}
	if b, err := bob.ReadFile("secret.txt"); err != nil || string(b) != "bob's\n" {
		t.Errorf("bob reads %q, %v", b, err)
	}
}

// A disabled account is refused, and its open sessions close.
func testDisabled(t *testing.T, addr string, fx *fixture) {
	carol := mountAs(t, addr, "carol", fx.passwords["carol"])
	yes := true
	if _, err := fx.core.Update(context.Background(), "carol", nil, &yes); err != nil {
		t.Fatal(err)
	}
	if !gone(t, carol) {
		t.Error("the session of a disabled account is still open")
	}
	if _, err := dial(t, addr, "carol", fx.passwords["carol"]); err == nil {
		t.Error("a disabled account logged in")
	}
}

func testRead(t *testing.T, addr string, fx *fixture) {
	share := fx.mount(t, addr)
	root, err := share.ReadDir(".")
	if err != nil {
		t.Fatalf("list the root: %v", err)
	}
	var names []string
	for _, fi := range root {
		names = append(names, fi.Name())
	}
	sort.Strings(names)
	if got, want := strings.Join(names, ","), "docs,photos,\u00e9t\u00e9.txt"; got != want {
		t.Errorf("root lists %q, want %q", got, want)
	}
	sub, err := share.ReadDir(`docs`)
	if err != nil || len(sub) != 2 {
		t.Errorf("docs lists %d entries, %v; want 2", len(sub), err)
	}
	for p, want := range fx.files {
		f, err := share.Open(strings.ReplaceAll(p, "/", `\`))
		if err != nil {
			t.Errorf("open %s: %v", p, err)
			continue
		}
		h := sha256.New()
		n, err := io.Copy(h, f)
		f.Close()
		if err != nil {
			t.Errorf("read %s: %v", p, err)
			continue
		}
		wantSum := sha256.Sum256(want)
		if n != int64(len(want)) || !bytes.Equal(h.Sum(nil), wantSum[:]) {
			t.Errorf("%s: read %d bytes with another hash, want %d", p, n, len(want))
		}
	}
	fi, err := share.Stat(`photos\big.bin`)
	if err != nil || fi.Size() != int64(len(fx.files["photos/big.bin"])) || fi.IsDir() {
		t.Errorf("stat big.bin = %v, %v", fi, err)
	}
}

func testPaths(t *testing.T, addr string, fx *fixture) {
	share := fx.mount(t, addr)
	for _, p := range []string{
		`..\bob\secret.txt`, `docs\..\..\bob\secret.txt`, `..\..\..\etc\passwd`,
		`/etc/passwd`, `docs\a.txt:Zone.Identifier`, `docs\a.txt::$DATA:x`, `C:\Windows\win.ini`,
		`missing.txt`,
		// U+F029 is the Mac's private-range period: smbsrv refuses it as
		// "." or ".." (PATCHES.md, 1), and Clean would too.
		"\\bob\\secret.txt", "docs\\\\\\bob\\secret.txt",
	} {
		if f, err := share.Open(p); err == nil {
			b, _ := io.ReadAll(f)
			f.Close()
			t.Errorf("open %q succeeded and read %q", p, b)
		}
	}
	if list, err := share.ReadDir(`..`); err == nil {
		t.Errorf("listing .. succeeded: %d entries", len(list))
	}
}

// After 5 failures of an account from one address, even its SMB password is
// refused for a minute; a success before that clears the count.
func TestLoginAttemptsAreLimited(t *testing.T) {
	fx := newFixture(t)
	addr := serve(t, fx).Addr().String()
	pw := fx.passwords[testUser]
	for range 4 {
		if _, err := dial(t, addr, testUser, "wrong"); err == nil {
			t.Fatal("a wrong password logged in")
		}
	}
	if _, err := dial(t, addr, testUser, pw); err != nil {
		t.Fatalf("the SMB password after 4 failures: %v", err)
	}
	for range 5 {
		if _, err := dial(t, addr, testUser, "wrong"); err == nil {
			t.Fatal("a wrong password logged in")
		}
	}
	if _, err := dial(t, addr, testUser, pw); err == nil {
		t.Error("the SMB password logged in after 5 failures in a row")
	}
	// Another account at the same address is not held back.
	if _, err := dial(t, addr, "bob", fx.passwords["bob"]); err != nil {
		t.Errorf("bob was held back by alice's failures: %v", err)
	}
}

// The core revokes or replaces an SMB password: Postgres tells the smb
// service, which closes the sessions it opened.
func TestRevokeByNotify(t *testing.T) {
	fx := newFixture(t)
	srv := serve(t, fx)
	addr := srv.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go srv.WatchRevocations(ctx, fx.store.Pool, time.Hour)
	waitListening(t, fx)

	old := fx.passwords[testUser]
	share := fx.mount(t, addr)
	bob := mountAs(t, addr, "bob", fx.passwords["bob"])
	if err := fx.core.RevokeSMBPassword(context.Background(), testUser); err != nil {
		t.Fatal(err)
	}
	if !gone(t, share) {
		t.Fatal("a request on the open session still works after the revocation")
	}
	if _, err := dial(t, addr, testUser, old); err == nil {
		t.Error("the revoked password logged in again")
	}
	if _, err := bob.Stat("secret.txt"); err != nil {
		t.Errorf("bob's session closed with alice's: %v", err)
	}

	// A new password closes the sessions of the one before.
	first, _, err := fx.core.NewSMBPassword(context.Background(), testUser)
	if err != nil {
		t.Fatal(err)
	}
	share = mountAs(t, addr, testUser, first)
	if _, _, err := fx.core.NewSMBPassword(context.Background(), testUser); err != nil {
		t.Fatal(err)
	}
	if !gone(t, share) {
		t.Error("a session of the replaced password is still open")
	}
	if _, err := dial(t, addr, testUser, first); err == nil {
		t.Error("the replaced password logged in")
	}
}

// waitListening waits until the smb side listens on RevokeChannel.
func waitListening(t *testing.T, fx *fixture) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := fx.store.Pool.QueryRow(context.Background(),
			`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND query = $1`,
			"LISTEN "+RevokeChannel).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the smb side is not listening")
}

// A notification lost (the smb service was not listening): the sweep closes
// the sessions anyway, every minute and as it starts listening again.
func TestRevokeBySweep(t *testing.T) {
	fx := newFixture(t)
	srv := serve(t, fx)
	addr := srv.Addr().String()
	ctx := context.Background()
	// The core's changes reach no one.
	quiet := auth.NewService(fx.store.Pool)

	alice := fx.mount(t, addr)
	bob := mountAs(t, addr, "bob", fx.passwords["bob"])
	carol := mountAs(t, addr, "carol", fx.passwords["carol"])
	if n, err := srv.Sweep(ctx, fx.store.Pool); err != nil || n != 0 {
		t.Fatalf("a sweep with nothing revoked closed %d connections, %v", n, err)
	}

	if err := quiet.RevokeSMBPassword(ctx, testUser); err != nil {
		t.Fatal(err)
	}
	if _, _, err := quiet.NewSMBPassword(ctx, "bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := alice.Stat("docs"); err != nil {
		t.Fatalf("the session closed without a notification: %v", err)
	}
	if n, err := srv.Sweep(ctx, fx.store.Pool); err != nil || n != 2 {
		t.Errorf("the sweep closed %d connections, %v; want alice's and bob's", n, err)
	}
	if !gone(t, alice) || !gone(t, bob) {
		t.Error("a session of a revoked or replaced password is still open")
	}
	if _, err := carol.Stat("."); err != nil {
		t.Errorf("carol's session closed: %v", err)
	}

	// Disabled while nobody listened: the sweep of the first LISTEN closes it.
	yes := true
	if _, err := quiet.Update(ctx, "carol", nil, &yes); err != nil {
		t.Fatal(err)
	}
	watch, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go srv.WatchRevocations(watch, fx.store.Pool, time.Hour)
	if !gone(t, carol) {
		t.Error("the session of an account disabled while nobody listened is still open")
	}
}
