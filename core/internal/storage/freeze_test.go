package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// hashTree returns the SHA-256 of every regular file under dir, by path,
// leaving out the relative folders in skip.
func hashTree(t *testing.T, dir string, skip ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		for _, s := range skip {
			if rel == s {
				return filepath.SkipDir
			}
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[rel] = fmt.Sprintf("%x", sha256.Sum256(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// fillStore puts live files, a version and a trashed file for two owners,
// and a temporary file that must stay out of a snapshot.
func fillStore(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	put(t, s, "enzo", "Documents/contrat.pdf", "contrat v1")
	put(t, s, "enzo", "Photos/2024/plage.jpg", strings.Repeat("sable", 1000))
	put(t, s, "enzo", "notes.txt", "à faire")
	put(t, s, "lea", "devoirs.txt", "maths")
	if _, err := s.Put(ctx, "enzo", "Documents/contrat.pdf", strings.NewReader("contrat v2"), PutOptions{ExpectSize: -1, Replace: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Trash(ctx, "enzo", "notes.txt"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(s.tmpDir(), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.tmpDir(), "put-123"), []byte("half written"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// snapshotRows reads, in the exported snapshot, every file row with its path
// on disk relative to the data root.
func snapshotRows(t *testing.T, s *Store, snapshotID string) map[string][]byte {
	t.Helper()
	ctx := context.Background()
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SET TRANSACTION SNAPSHOT '`+snapshotID+`'`); err != nil {
		t.Fatalf("import the snapshot: %v", err)
	}
	rows, err := tx.Query(ctx, `SELECT f.owner, f.path, f.sha256, f.trash_id, t.path
		FROM files f LEFT JOIN trash t ON t.id = f.trash_id WHERE f.kind = 'file'`)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for rows.Next() {
		var owner, p string
		var sum []byte
		var trashID *int64
		var original *string
		if err := rows.Scan(&owner, &p, &sum, &trashID, &original); err != nil {
			t.Fatal(err)
		}
		disk := filepath.Join(owner, filepath.FromSlash(p))
		if trashID != nil {
			rel, _ := filepath.Rel(s.Root, s.trashDisk(owner, *trashID, *original))
			disk = filepath.Join(rel, filepath.FromSlash(strings.TrimPrefix(p, *original)))
		}
		out[disk] = sum
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// The picture holds every file of the data root, versions and trash
// included, as links to the same bytes, and nothing of .cloudos/tmp; the
// database snapshot indexes exactly those files.
func TestFreezeTakesAConsistentPicture(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	fillStore(t, s)
	before := hashTree(t, s.Root)

	fz, err := s.Freeze(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer fz.Release(ctx) //nolint:errcheck
	t.Logf("locks held %v for %d owners, %d files", fz.LockHeld, fz.Owners, fz.Files)
	if fz.Owners != 2 {
		t.Errorf("%d owners locked, want 2", fz.Owners)
	}

	pic := hashTree(t, fz.Dir)
	want := hashTree(t, s.Root, filepath.Join(".cloudos", "tmp"), filepath.Join(".cloudos", "snapshots"))
	if fmt.Sprint(pic) != fmt.Sprint(want) {
		t.Errorf("the picture\n%v\nwant the data root without tmp\n%v", pic, want)
	}
	if _, ok := pic[filepath.Join(".cloudos", "tmp", "put-123")]; ok {
		t.Error("the picture holds a temporary file")
	}
	if int64(len(pic)) != fz.Files {
		t.Errorf("Files = %d, the picture has %d", fz.Files, len(pic))
	}
	var versions, trashed int
	for p := range pic {
		switch {
		case strings.HasPrefix(p, filepath.Join(".cloudos", "versions")):
			versions++
		case strings.HasPrefix(p, filepath.Join(".cloudos", "trash")):
			trashed++
		}
	}
	if versions != 1 || trashed != 1 {
		t.Errorf("the picture has %d versions and %d trashed files, want 1 and 1", versions, trashed)
	}
	// Links, not copies: the same inode as the stored file.
	a, _ := os.Stat(filepath.Join(s.Root, "enzo", "Photos", "2024", "plage.jpg"))
	b, _ := os.Stat(filepath.Join(fz.Dir, "enzo", "Photos", "2024", "plage.jpg"))
	if !os.SameFile(a, b) {
		t.Error("the picture copied a file instead of linking it")
	}

	rows := snapshotRows(t, s, fz.SnapshotID)
	if int64(len(rows)) != fz.Indexed {
		t.Errorf("Indexed = %d, the snapshot has %d file rows", fz.Indexed, len(rows))
	}
	for disk, sum := range rows {
		if pic[disk] != fmt.Sprintf("%x", sum) {
			t.Errorf("the snapshot indexes %s with %x, the picture has %q", disk, sum, pic[disk])
		}
	}

	if err := fz.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(fz.Dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the picture is still there after Release: %v", err)
	}
	if after := hashTree(t, s.Root); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Errorf("the data root changed across Freeze and Release:\n%v\nwas\n%v", after, before)
	}
	if rep, err := s.Verify(ctx); err != nil || len(rep.Problems) > 0 {
		t.Errorf("verify after Release: %v %v", rep.Problems, err)
	}
}

// A write that comes while the links are made waits for the locks, then
// succeeds; it is in neither the picture nor the database snapshot.
func TestFreezeHoldsWritesThenLetsThemThrough(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	fillStore(t, s)

	written := make(chan error, 1)
	freezeLinked = func() {
		go func() {
			_, err := s.Put(ctx, "enzo", "pendant.txt", strings.NewReader("écrit pendant la photo"), NoExpectations())
			written <- err
		}()
		// The write is waiting for enzo's lock, not failing.
		deadline := time.Now().Add(10 * time.Second)
		for {
			var waiting int
			if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`).Scan(&waiting); err != nil {
				t.Error(err)
				return
			}
			if waiting > 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Error("the write never waited for the lock")
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		select {
		case err := <-written:
			t.Errorf("the write went through while the links were made: %v", err)
		default:
		}
	}
	defer func() { freezeLinked = nil }()

	fz, err := s.Freeze(ctx)
	freezeLinked = nil
	if err != nil {
		t.Fatal(err)
	}
	defer fz.Release(ctx) //nolint:errcheck
	select {
	case err := <-written:
		if err != nil {
			t.Fatalf("the write failed after waiting: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the write never went through after Freeze")
	}
	if _, err := os.Lstat(filepath.Join(fz.Dir, "enzo", "pendant.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("the write that waited is in the picture")
	}
	if _, ok := snapshotRows(t, s, fz.SnapshotID)[filepath.Join("enzo", "pendant.txt")]; ok {
		t.Error("the write that waited is in the database snapshot")
	}
	if _, err := s.Lookup(ctx, "enzo", "pendant.txt"); err != nil {
		t.Errorf("the write is not stored: %v", err)
	}
}

// A picture left by a run that stopped before Release is cleared by the next
// Freeze; one Freeze at a time.
func TestFreezeClearsALeftoverPicture(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	fillStore(t, s)

	fz, err := s.Freeze(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Freeze(ctx); !errors.Is(err, ErrFrozen) {
		t.Errorf("a second Freeze while the first holds: %v, want ErrFrozen", err)
	}
	// The process stops: its connection goes, the links stay.
	fz.abort()
	stale := filepath.Join(fz.Dir, "enzo", "gone.txt")
	if err := os.WriteFile(stale, []byte("from an older picture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Trash(ctx, "enzo", "Photos/2024/plage.jpg"); err != nil {
		t.Fatal(err)
	}

	fz, err = s.Freeze(ctx)
	if err != nil {
		t.Fatalf("Freeze after a leftover: %v", err)
	}
	defer fz.Release(ctx) //nolint:errcheck
	if _, err := os.Lstat(stale); !errors.Is(err, fs.ErrNotExist) {
		t.Error("the leftover picture's file is still there")
	}
	pic := hashTree(t, fz.Dir)
	want := hashTree(t, s.Root, filepath.Join(".cloudos", "tmp"), filepath.Join(".cloudos", "snapshots"))
	if fmt.Sprint(pic) != fmt.Sprint(want) {
		t.Errorf("the new picture\n%v\nwant\n%v", pic, want)
	}
}

// The picture's folder is only ever erased where it should be: a symbolic
// link put in its place is refused, and what it points at stays.
func TestRemoveSnapshotRefusesALink(t *testing.T) {
	s := newStore(t)
	put(t, s, "enzo", "a.txt", "précieux")
	if err := os.MkdirAll(filepath.Join(s.Root, ".cloudos"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(s.Root, "enzo"), s.snapshotsDir()); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".", filepath.Join(s.Root, "enzo", "current")); err != nil {
		t.Fatal(err)
	}
	if err := s.removeSnapshot(); err == nil {
		t.Error("removeSnapshot went through a symbolic link")
	}
	if b, err := os.ReadFile(filepath.Join(s.Root, "enzo", "a.txt")); err != nil || !bytes.Equal(b, []byte("précieux")) {
		t.Errorf("the stored file after a refused removal: %q, %v", b, err)
	}
	if _, err := s.Freeze(context.Background()); err == nil {
		t.Error("Freeze went on over a snapshot folder that is a link")
	}
}

// How long the writes wait grows with the number of files: measured here
// for a thousand, it stays well under a second on a laptop.
func TestFreezeLockTime(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	for i := range 1000 {
		dir := filepath.Join(s.Root, "enzo", fmt.Sprintf("dossier-%02d", i%20))
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%04d.txt", i)), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fz, err := s.Freeze(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer fz.Release(ctx) //nolint:errcheck
	t.Logf("1000 files: locks held %v", fz.LockHeld)
	if fz.Files != 1000 {
		t.Errorf("%d files linked, want 1000", fz.Files)
	}
	if fz.LockHeld > 10*time.Second {
		t.Errorf("locks held %v for 1000 files", fz.LockHeld)
	}
}
