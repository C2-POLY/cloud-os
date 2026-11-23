package smbfs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hirochachacha/go-smb2"

	"github.com/C2-POLY/cloud-os/core/internal/auth"
	"github.com/C2-POLY/cloud-os/core/internal/doctext"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/tasks"
)

// writeServer serves fx's drives with a store that queues the text of a new
// document, as the core's hook does, and with free reporting the disk's
// free space (nil is plenty).
func writeServer(t *testing.T, fx *fixture, free func() uint64) (*Server, *storage.Store) {
	t.Helper()
	store := &storage.Store{Root: fx.store.Root, Pool: fx.store.Pool}
	store.AfterStore = func(ctx context.Context, f storage.File, _ []byte) {
		if _, err := doctext.Enqueue(ctx, &tasks.Queue{Pool: store.Pool}, f, tasks.PriorityNew); err != nil {
			t.Errorf("queue the text of %s: %v", f.Path, err)
		}
	}
	if free == nil {
		free = func() uint64 { return 1 << 40 }
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv, err := Start(ctx, Config{
		Addr: "127.0.0.1:0", Auth: auth.NewService(fx.store.Pool), Store: store,
		loginDelay: time.Millisecond, freeSpace: free,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv, store
}

// mountConn mounts alice's drive and returns the connection under it, for a
// test that cuts it.
func mountConn(t *testing.T, addr, password string) (*smb2.Share, net.Conn) {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	d := &smb2.Dialer{Initiator: &smb2.NTLMInitiator{User: testUser, Password: password}}
	s, err := d.Dial(c)
	if err != nil {
		c.Close()
		t.Fatalf("login: %v", err)
	}
	share, err := s.Mount(ShareName)
	if err != nil {
		t.Fatalf("mount: %v", err)
	}
	return share, c
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// stored returns alice's indexed file at p, its bytes on disk checked
// against the index.
func (fx *fixture) stored(t *testing.T, p string) (storage.File, []byte) {
	t.Helper()
	f, err := fx.store.Lookup(context.Background(), testUser, p)
	if err != nil {
		t.Fatalf("lookup %s: %v", p, err)
	}
	b, err := os.ReadFile(filepath.Join(fx.store.Root, testUser, filepath.FromSlash(f.Path)))
	if err != nil {
		t.Fatalf("read %s on disk: %v", p, err)
	}
	if hex.EncodeToString(f.SHA256) != sum(b) || f.Size != int64(len(b)) {
		t.Fatalf("%s: the index says %d bytes %x, the disk holds %d bytes %s", p, f.Size, f.SHA256, len(b), sum(b))
	}
	return f, b
}

func (fx *fixture) missing(t *testing.T, p string) {
	t.Helper()
	if f, err := fx.store.Lookup(context.Background(), testUser, p); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("%s is indexed (%v, %v)", p, f.Path, err)
	}
	if _, err := os.Lstat(filepath.Join(fx.store.Root, testUser, filepath.FromSlash(p))); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s is on disk: %v", p, err)
	}
}

// staged waits until no SMB working file is left: an abort runs on the
// server's own goroutines once it sees a connection gone.
func (fx *fixture) noneStaged(t *testing.T) {
	t.Helper()
	dir := filepath.Join(fx.store.Root, ".cloudos", "tmp", "smb")
	deadline := time.Now().Add(5 * time.Second)
	for {
		entries, err := os.ReadDir(dir)
		if (err == nil && len(entries) == 0) || errors.Is(err, os.ErrNotExist) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("SMB working files left: %v, %v", entries, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (fx *fixture) versions(t *testing.T, p string) []storage.Version {
	t.Helper()
	v, err := fx.store.ListVersions(context.Background(), testUser, p)
	if err != nil {
		t.Fatalf("versions of %s: %v", p, err)
	}
	return v
}

func (fx *fixture) versionBytes(t *testing.T, v storage.Version) []byte {
	t.Helper()
	r, _, err := fx.store.OpenVersion(context.Background(), testUser, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// writeOpen opens p for reading and writing, as the Finder does.
func writeOpen(t *testing.T, share *smb2.Share, p string, flag int) *smb2.File {
	t.Helper()
	f, err := share.OpenFile(p, os.O_RDWR|flag, 0o644)
	if err != nil {
		t.Fatalf("open %s: %v", p, err)
	}
	return f
}

func TestWrite(t *testing.T) {
	fx := newFixture(t)
	srv, _ := writeServer(t, fx, nil)
	addr := srv.Addr().String()
	ctx := context.Background()

	// Random offsets, a truncate and a close: the bytes stored are the bytes
	// written, the index has them, and the text of the document is queued.
	// Until the close, the file shows with the size it has, and nothing is stored.
	t.Run("random writes", func(t *testing.T) {
		share := fx.mount(t, addr)
		other := fx.mount(t, addr)
		want := make([]byte, 3<<20+123)
		rnd := rand.New(rand.NewPCG(1, 2))
		for i := range want {
			want[i] = byte('a' + rnd.IntN(26))
		}
		const chunk = 64 << 10
		f := writeOpen(t, share, `docs\random.txt`, os.O_CREATE|os.O_EXCL)
		for _, i := range rnd.Perm((len(want) + chunk - 1) / chunk) {
			end := min((i+1)*chunk, len(want))
			if _, err := f.WriteAt(want[i*chunk:end], int64(i*chunk)); err != nil {
				t.Fatalf("write at %d: %v", i*chunk, err)
			}
		}
		if _, err := f.WriteAt([]byte("tail to cut"), int64(len(want))); err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(int64(len(want))); err != nil {
			t.Fatalf("truncate: %v", err)
		}

		// Pending: seen with its size, from another connection too, stored nowhere
		for _, s := range []*smb2.Share{share, other} {
			fi, err := s.Stat(`docs\random.txt`)
			if err != nil || fi.Size() != int64(len(want)) {
				t.Errorf("stat while written = %v, %v; want %d bytes", fi, err, len(want))
			}
			list, err := s.ReadDir(`docs`)
			found := false
			for _, e := range list {
				if e.Name() == "random.txt" {
					found = e.Size() == int64(len(want))
				}
			}
			if err != nil || !found {
				t.Errorf("docs lists random.txt with its size = %v (%v)", found, err)
			}
		}
		fx.missing(t, "docs/random.txt")
		got := make([]byte, len(want))
		if _, err := f.ReadAt(got, 0); err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("read back: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Error("reading the file being written gives other bytes")
		}

		if err := f.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		row, disk := fx.stored(t, "docs/random.txt")
		if sum(disk) != sum(want) {
			t.Errorf("stored %d bytes %s, want %d bytes %s", len(disk), sum(disk), len(want), sum(want))
		}
		var queued bool
		if err := fx.store.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tasks WHERE kind = $1 AND payload ->> 'sha256' = $2)`,
			doctext.Kind, hex.EncodeToString(row.SHA256)).Scan(&queued); err != nil || !queued {
			t.Errorf("the text of the new document is not queued (%v)", err)
		}
		fx.noneStaged(t)
	})

	// An edit in place keeps the content it replaces as a version. An open
	// for writing that writes nothing stores nothing.
	t.Run("edit keeps the version", func(t *testing.T) {
		share := fx.mount(t, addr)
		f := writeOpen(t, share, `docs\a.txt`, 0)
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		if v := fx.versions(t, "docs/a.txt"); len(v) != 0 {
			t.Fatalf("an open that wrote nothing made %d versions", len(v))
		}
		before, _ := fx.stored(t, "docs/a.txt")

		f = writeOpen(t, share, `docs\a.txt`, 0)
		if _, err := f.WriteAt([]byte("HELLO"), 0); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		after, disk := fx.stored(t, "docs/a.txt")
		if string(disk) != "HELLO over smb\n" || after.ID != before.ID {
			t.Errorf("after the edit: %q, id %d (was %d)", disk, after.ID, before.ID)
		}
		v := fx.versions(t, "docs/a.txt")
		if len(v) != 1 || !bytes.Equal(fx.versionBytes(t, v[0]), fx.files["docs/a.txt"]) {
			t.Errorf("versions %v: want the content before", v)
		}

		// Overwriting with O_TRUNC keeps one more version
		if err := share.WriteFile(`docs\a.txt`, []byte("rewritten\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, disk := fx.stored(t, "docs/a.txt"); string(disk) != "rewritten\n" {
			t.Errorf("after the overwrite: %q", disk)
		}
		if v := fx.versions(t, "docs/a.txt"); len(v) != 2 {
			t.Errorf("%d versions after the overwrite, want 2", len(v))
		}
	})

	// A date set by the client is the stored file's date, and a folder made
	// over SMB is indexed with its parents.
	t.Run("dates and folders", func(t *testing.T) {
		share := fx.mount(t, addr)
		when := time.Date(2019, 7, 14, 18, 3, 22, 0, time.UTC)
		if err := share.Chtimes(`docs\sub\b.txt`, when, when); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
		f, disk := fx.stored(t, "docs/sub/b.txt")
		if !f.ModifiedAt.Equal(when) || string(disk) != "deeper\n" {
			t.Errorf("after chtimes: modified %v, content %q", f.ModifiedAt, disk)
		}
		if info, err := os.Stat(filepath.Join(fx.store.Root, testUser, "docs", "sub", "b.txt")); err != nil || !info.ModTime().Equal(when) {
			t.Errorf("disk date %v, %v", info.ModTime(), err)
		}
		if err := share.MkdirAll(`albums\2019\juillet`, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if d, err := fx.store.Lookup(ctx, testUser, "albums/2019/juillet"); err != nil || !d.IsDir() {
			t.Errorf("the new folder: %v, %v", d, err)
		}
		// A new file keeps the date the client gives it while it is open
		nf := writeOpen(t, share, `albums\2019\juillet\note.txt`, os.O_CREATE)
		if _, err := nf.Write([]byte("plage\n")); err != nil {
			t.Fatal(err)
		}
		if err := share.Chtimes(`albums\2019\juillet\note.txt`, when, when); err != nil {
			t.Fatalf("chtimes while open: %v", err)
		}
		if err := nf.Close(); err != nil {
			t.Fatal(err)
		}
		if f, _ := fx.stored(t, "albums/2019/juillet/note.txt"); !f.ModifiedAt.Equal(when) {
			t.Errorf("new file's date %v, want %v", f.ModifiedAt, when)
		}
	})

	// A rename is a journaled move. A rename onto an existing file is
	// refused unless the client asks to replace it; then the target's
	// content before is kept as its version.
	t.Run("rename", func(t *testing.T) {
		share := fx.mount(t, addr)
		before, _ := fx.stored(t, "docs/random.txt")
		if err := share.Rename(`docs\random.txt`, `docs\renamed.txt`); err != nil {
			t.Fatalf("rename: %v", err)
		}
		after, _ := fx.stored(t, "docs/renamed.txt")
		if after.ID != before.ID {
			t.Errorf("the rename made a new row: %d, was %d", after.ID, before.ID)
		}
		fx.missing(t, "docs/random.txt")
		if err := share.Rename(`docs\sub`, `moved`); err != nil {
			t.Fatalf("move a folder: %v", err)
		}
		fx.stored(t, "moved/b.txt")

		if err := share.WriteFile(`docs\draft.txt`, []byte("new text\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		target, old := fx.stored(t, "docs/renamed.txt")
		// go-smb2 never asks to replace: smbsrv refuses, and both stay
		if err := share.Rename(`docs\draft.txt`, `docs\renamed.txt`); err == nil {
			t.Fatal("a rename onto a file replaced it without being asked to")
		}
		fx.stored(t, "docs/draft.txt")
		// What smbsrv calls once the client asked to replace (FileRenameInformation, ReplaceIfExists)
		view, err := srv.backend.Login(testUser)
		if err != nil {
			t.Fatal(err)
		}
		if err := view.Shares()[0].Rename("docs/draft.txt", "docs/renamed.txt"); err != nil {
			t.Fatalf("rename with replace: %v", err)
		}
		fx.missing(t, "docs/draft.txt")
		replaced, disk := fx.stored(t, "docs/renamed.txt")
		if string(disk) != "new text\n" || replaced.ID != target.ID {
			t.Errorf("after the replace: %q, id %d (target was %d)", disk, replaced.ID, target.ID)
		}
		v := fx.versions(t, "docs/renamed.txt")
		if len(v) != 1 || sum(fx.versionBytes(t, v[0])) != sum(old) {
			t.Errorf("the replaced content is not a version: %v", v)
		}
	})

	// A file and a folder deleted over SMB go to the trash, and restore.
	t.Run("delete and restore", func(t *testing.T) {
		share := fx.mount(t, addr)
		_, file := fx.stored(t, "docs/renamed.txt")
		_, inFolder := fx.stored(t, "moved/b.txt")
		if err := share.Remove(`docs\renamed.txt`); err != nil {
			t.Fatalf("delete a file: %v", err)
		}
		if err := share.RemoveAll(`moved`); err != nil {
			t.Fatalf("delete a folder: %v", err)
		}
		fx.missing(t, "docs/renamed.txt")
		fx.missing(t, "moved")
		items, err := fx.store.ListTrash(ctx, testUser)
		if err != nil || len(items) < 3 {
			t.Fatalf("trash: %v, %v", items, err)
		}
		// Oldest first: the file, then the folder's file, then the folder itself
		for i := len(items) - 1; i >= 0; i-- {
			if _, err := fx.store.Restore(ctx, testUser, items[i].ID); err != nil {
				t.Fatalf("restore %s: %v", items[i].Path, err)
			}
		}
		if _, b := fx.stored(t, "docs/renamed.txt"); sum(b) != sum(file) {
			t.Error("the restored file has other bytes")
		}
		if _, b := fx.stored(t, "moved/b.txt"); sum(b) != sum(inFolder) {
			t.Error("the restored folder's file has other bytes")
		}
		if v := fx.versions(t, "docs/renamed.txt"); len(v) != 1 {
			t.Errorf("the restored file has %d versions, want its 1", len(v))
		}
	})

	// The Finder's own files are kept apart, out of the index and out of
	// listings, and erased when the Finder deletes them.
	t.Run("finder files", func(t *testing.T) {
		share := fx.mount(t, addr)
		before := fx.snapshotIndex(t)
		for p, b := range map[string]string{`.DS_Store`: "ds", `docs\._a.txt`: "apple double", `docs\.DS_Store`: "ds2"} {
			if err := share.WriteFile(p, []byte(b), 0o644); err != nil {
				t.Fatalf("write %s: %v", p, err)
			}
			if got, err := share.ReadFile(p); err != nil || string(got) != b {
				t.Errorf("read %s back: %q, %v", p, got, err)
			}
		}
		if after := fx.snapshotIndex(t); after != before {
			t.Errorf("the Finder's files reached the index:\n%s", after)
		}
		apart := filepath.Join(fx.store.Root, ".cloudos", "appledouble", testUser, "docs", "._a.txt")
		if b, err := os.ReadFile(apart); err != nil || string(b) != "apple double" {
			t.Errorf("._a.txt apart: %q, %v", b, err)
		}
		list, err := share.ReadDir(`docs`)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range list {
			if strings.HasPrefix(e.Name(), "._") || e.Name() == ".DS_Store" {
				t.Errorf("docs lists %s", e.Name())
			}
		}
		if err := share.Remove(`docs\._a.txt`); err != nil {
			t.Fatalf("delete ._a.txt: %v", err)
		}
		if _, err := os.Stat(apart); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("._a.txt is still apart: %v", err)
		}
		// An app that saves through .TemporaryItems: the document is stored,
		// and the temporary file goes
		if err := share.MkdirAll(`.TemporaryItems\folders.501\TemporaryItems`, 0o755); err != nil {
			t.Fatalf("mkdir .TemporaryItems: %v", err)
		}
		tmp := `.TemporaryItems\folders.501\TemporaryItems\saved.txt`
		if err := share.WriteFile(tmp, []byte("saved through the Finder\n"), 0o644); err != nil {
			t.Fatalf("write in .TemporaryItems: %v", err)
		}
		if err := share.Rename(tmp, `docs\saved.txt`); err != nil {
			t.Fatalf("rename out of .TemporaryItems: %v", err)
		}
		if _, b := fx.stored(t, "docs/saved.txt"); string(b) != "saved through the Finder\n" {
			t.Errorf("docs/saved.txt: %q", b)
		}
		if _, err := share.Stat(tmp); err == nil {
			t.Error("the temporary file is still there")
		}
		if err := share.Remove(`.TemporaryItems\folders.501\TemporaryItems`); err != nil {
			t.Errorf("remove a folder inside .TemporaryItems: %v", err)
		}
		// A Finder file with a hole past the free space is refused
		sparse := writeOpen(t, share, `docs\._sparse`, os.O_CREATE)
		if _, err := sparse.WriteAt([]byte("x"), 2<<40); err == nil {
			t.Error("a write 2 TiB into a Finder file was accepted")
		}
		sparse.Close()
		// Named streams stay refused: the Mac falls back on ._ files
		if err := share.WriteFile(`docs\a.txt:com.apple.metadata:_kMDItemUserTags`, []byte("tag"), 0o644); err == nil {
			t.Error("a named stream was written")
		}
	})

	// Two handles on one file share what is written; the file is stored
	// when the last one closes.
	t.Run("two handles", func(t *testing.T) {
		share := fx.mount(t, addr)
		a := writeOpen(t, share, `docs\two.txt`, os.O_CREATE)
		if _, err := a.WriteAt([]byte("aaaa"), 0); err != nil {
			t.Fatal(err)
		}
		b := writeOpen(t, share, `docs\two.txt`, 0)
		got := make([]byte, 4)
		if _, err := b.ReadAt(got, 0); err != nil || string(got) != "aaaa" {
			t.Errorf("the second handle reads %q, %v", got, err)
		}
		if _, err := b.WriteAt([]byte("bb"), 4); err != nil {
			t.Fatal(err)
		}
		if err := a.Close(); err != nil {
			t.Fatal(err)
		}
		fx.missing(t, "docs/two.txt")
		if err := b.Close(); err != nil {
			t.Fatal(err)
		}
		if _, disk := fx.stored(t, "docs/two.txt"); string(disk) != "aaaabb" {
			t.Errorf("stored %q, want aaaabb", disk)
		}
		fx.noneStaged(t)
	})

	// A connection cut before the CLOSE stores nothing: the file being
	// edited keeps its content, and a new one is not created.
	t.Run("cut before close", func(t *testing.T) {
		before, kept := fx.stored(t, "docs/a.txt")
		versions := len(fx.versions(t, "docs/a.txt"))
		share, conn := mountConn(t, addr, fx.passwords[testUser])
		edit := writeOpen(t, share, `docs\a.txt`, 0)
		if _, err := edit.WriteAt([]byte("HALF WRITTEN"), 0); err != nil {
			t.Fatal(err)
		}
		fresh := writeOpen(t, share, `docs\half.bin`, os.O_CREATE)
		if _, err := fresh.Write(make([]byte, 1<<20)); err != nil {
			t.Fatal(err)
		}
		conn.Close()
		fx.noneStaged(t)
		after, disk := fx.stored(t, "docs/a.txt")
		if sum(disk) != sum(kept) || after.ID != before.ID || !bytes.Equal(after.SHA256, before.SHA256) {
			t.Errorf("the cut edit changed docs/a.txt: %q", disk)
		}
		if n := len(fx.versions(t, "docs/a.txt")); n != versions {
			t.Errorf("the cut edit made a version: %d, was %d", n, versions)
		}
		fx.missing(t, "docs/half.bin")
		// And the drive still lists without them
		other := fx.mount(t, addr)
		if _, err := other.Stat(`docs\half.bin`); err == nil {
			t.Error("the cut file still shows")
		}
	})

	// A CLOSE whose storing fails (here a folder took the name meanwhile)
	// gets an error, and the content is in the trash at its path, restorable.
	t.Run("not stored is kept in the trash", func(t *testing.T) {
		share := fx.mount(t, addr)
		content := []byte("written while the name was taken\n")
		f := writeOpen(t, share, `docs\kept.txt`, os.O_CREATE)
		if _, err := f.Write(content); err != nil {
			t.Fatal(err)
		}
		if _, err := fx.store.Mkdir(ctx, testUser, "docs/kept.txt"); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err == nil {
			t.Fatal("the CLOSE of a write that was not stored succeeded")
		}
		fx.noneStaged(t)
		items, err := fx.store.ListTrash(ctx, testUser)
		if err != nil {
			t.Fatal(err)
		}
		var kept []storage.TrashItem
		for _, it := range items {
			if it.Reason == storage.RescueReason {
				kept = append(kept, it)
			}
		}
		if len(kept) != 1 || kept[0].Path != "docs/kept.txt" || kept[0].Size != int64(len(content)) {
			t.Fatalf("kept in the trash: %+v", kept)
		}
		restored, err := fx.store.Restore(ctx, testUser, kept[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, b := fx.stored(t, restored.Path); restored.Path != "docs/kept (restauré).txt" || sum(b) != sum(content) {
			t.Errorf("restored at %s with other bytes", restored.Path)
		}
	})
}

// A write the disk has no room for is refused, and leaves nothing: neither
// the file it would create nor a change to the one it would edit.
func TestWriteDiskFull(t *testing.T) {
	fx := newFixture(t)
	// 1 MiB above the margin, less what the working files hold, as a disk
	stage := filepath.Join(fx.store.Root, ".cloudos", "tmp", "smb")
	free := func() uint64 {
		var used int64
		entries, _ := os.ReadDir(stage)
		for _, e := range entries {
			if info, err := e.Info(); err == nil {
				used += info.Size()
			}
		}
		return uint64(spaceMargin + 1<<20 - used)
	}
	srv, _ := writeServer(t, fx, free)
	share := fx.mount(t, srv.Addr().String())

	f := writeOpen(t, share, `docs\big.bin`, os.O_CREATE)
	if _, err := f.Write(make([]byte, 2<<20)); err == nil {
		t.Error("a write beyond the free space was accepted")
	}
	f.Close()
	fx.noneStaged(t)
	// Editing photos/big.bin (3 MiB) needs its copy: no room for it either
	e := writeOpen(t, share, `photos\big.bin`, 0)
	if _, err := e.WriteAt([]byte("x"), 0); err == nil {
		t.Error("an edit whose copy has no room was accepted")
	}
	e.Close()
	fx.noneStaged(t)
	// A write that fits goes through
	if err := share.WriteFile(`docs\small.txt`, []byte("fits\n"), 0o644); err != nil {
		t.Errorf("a small write: %v", err)
	}

	fx.stored(t, "docs/small.txt")
	fx.missing(t, "docs/big.bin")
	if _, b := fx.stored(t, "photos/big.bin"); sum(b) != sum(fx.files["photos/big.bin"]) {
		t.Error("photos/big.bin changed")
	}
	if v := fx.versions(t, "photos/big.bin"); len(v) != 0 {
		t.Errorf("the refused edit made %d versions", len(v))
	}
}

// snapshotIndex is the index alone, with the journal.
func (fx *fixture) snapshotIndex(t *testing.T) string {
	t.Helper()
	rows, err := fx.store.Pool.Query(context.Background(),
		`SELECT format('%s %s %s %s', owner, path, kind, encode(sha256, 'hex')) FROM files ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, s)
	}
	return strings.Join(lines, "\n")
}
