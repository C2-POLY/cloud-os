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

	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/testdb"
)

const (
	testUser     = "alice"
	testPassword = "trial-password-1"
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

// fixture is a store with alice's files, and bob's beside them.
type fixture struct {
	store *storage.Store
	files map[string][]byte // alice's files, by path
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
	}}
	ctx := context.Background()
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

func serve(t *testing.T, fx *fixture) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv, err := Start(ctx, Config{
		Addr: "127.0.0.1:0",
		User: testUser, Password: testPassword, Owner: testUser,
		Store: fx.store,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv.Addr().String()
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

func mount(t *testing.T, addr string) *smb2.Share {
	t.Helper()
	s, err := dial(t, addr, testUser, testPassword)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	share, err := s.Mount(ShareName)
	if err != nil {
		t.Fatalf("mount %s: %v", ShareName, err)
	}
	t.Cleanup(func() { _ = share.Umount() })
	return share
}

func TestServer(t *testing.T) {
	fx := newFixture(t)
	addr := serve(t, fx)
	t.Run("login", func(t *testing.T) { testLogin(t, addr) })
	t.Run("read", func(t *testing.T) { testRead(t, addr, fx) })
	t.Run("paths", func(t *testing.T) { testPaths(t, addr) })
	t.Run("writes", func(t *testing.T) { testWrites(t, addr, fx) })
}

func testLogin(t *testing.T, addr string) {
	if _, err := dial(t, addr, testUser, "wrong password"); err == nil {
		t.Error("a wrong password logged in")
	}
	if _, err := dial(t, addr, "bob", testPassword); err == nil {
		t.Error("an unknown user logged in")
	}
	if _, err := dial(t, addr, "", ""); err == nil {
		t.Error("an anonymous login was accepted")
	}
	if _, err := dial(t, addr, strings.ToUpper(testUser), testPassword); err != nil {
		t.Errorf("the name in capitals was refused: %v", err)
	}
}

func testRead(t *testing.T, addr string, fx *fixture) {
	share := mount(t, addr)
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

func testPaths(t *testing.T, addr string) {
	share := mount(t, addr)
	for _, p := range []string{
		`..\bob\secret.txt`, `docs\..\..\bob\secret.txt`, `..\..\..\etc\passwd`,
		`/etc/passwd`, `docs\a.txt:Zone.Identifier`, `docs\a.txt::$DATA:x`, `C:\Windows\win.ini`,
		`missing.txt`,
		// U+F029 is the Mac's private-range period: smbsrv turns it into "."
		// after its own ".." check, so only Clean stands in the way.
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

func testWrites(t *testing.T, addr string, fx *fixture) {
	share := mount(t, addr)
	before := fx.snapshot(t)
	refused := func(what string, fn func() error) {
		t.Helper()
		if err := fn(); err == nil {
			t.Errorf("%s was not refused", what)
		}
	}
	refused("create", func() error {
		f, err := share.Create(`new.txt`)
		if err != nil {
			return err
		}
		_, werr := f.Write([]byte("x"))
		return errors.Join(werr, f.Close())
	})
	refused("create in a folder", func() error { return share.WriteFile(`docs\new.txt`, []byte("x"), 0o644) })
	refused("overwrite", func() error { return share.WriteFile(`docs\a.txt`, []byte("changed"), 0o644) })
	refused("write in place", func() error {
		f, err := share.OpenFile(`docs\a.txt`, os.O_RDWR, 0)
		if err != nil {
			return err
		}
		_, werr := f.WriteAt([]byte("HELLO"), 0)
		return errors.Join(werr, f.Close())
	})
	refused("append", func() error {
		f, err := share.OpenFile(`docs\a.txt`, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			return err
		}
		_, werr := f.Write([]byte("more"))
		return errors.Join(werr, f.Close())
	})
	refused("truncate by name", func() error { return share.Truncate(`docs\a.txt`, 0) })
	refused("truncate an open file", func() error {
		f, err := share.Open(`photos\big.bin`)
		if err != nil {
			return err
		}
		return errors.Join(f.Truncate(1), f.Close())
	})
	refused("rename", func() error { return share.Rename(`docs\a.txt`, `docs\renamed.txt`) })
	refused("move a folder", func() error { return share.Rename(`docs\sub`, `moved`) })
	refused("delete a file", func() error { return share.Remove(`docs\a.txt`) })
	refused("delete an empty-able folder", func() error { return share.RemoveAll(`docs`) })
	refused("make a folder", func() error { return share.Mkdir(`newdir`, 0o755) })
	refused("change times", func() error {
		return share.Chtimes(`docs\a.txt`, time.Unix(1, 0), time.Unix(1, 0))
	})
	refused("change attributes", func() error { return share.Chmod(`docs\a.txt`, 0o444) })
	refused("write a named stream", func() error {
		return share.WriteFile(`docs\a.txt:com.apple.metadata:_kMDItemUserTags`, []byte("tag"), 0o644)
	})
	refused("write a Finder file", func() error { return share.WriteFile(`._a.txt`, []byte("x"), 0o644) })
	refused("write a .DS_Store", func() error { return share.WriteFile(`.DS_Store`, []byte("x"), 0o644) })

	if after := fx.snapshot(t); after != before {
		t.Errorf("the disk or the index changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	// The drive still reads as it did.
	b, err := share.ReadFile(`docs\a.txt`)
	if err != nil || !bytes.Equal(b, fx.files["docs/a.txt"]) {
		t.Errorf("docs/a.txt reads %q, %v after the writes", b, err)
	}
}
