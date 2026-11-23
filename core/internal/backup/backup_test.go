package backup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/C2-POLY/cloud-os/core/internal/db"
	"github.com/C2-POLY/cloud-os/core/internal/migrate"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/testdb"
)

// requireTools skips the test without restic, pg_dump and a Postgres server
// of the database's version: `make test-backup` runs it in the backup image,
// which has them (CLOUDOS_REQUIRE_BACKUP_TOOLS=1 there turns a skip into a failure).
func requireTools(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"restic", "pg_dump", "pg_restore", "initdb", "pg_ctl"} {
		if _, err := exec.LookPath(bin); err != nil {
			if os.Getenv("CLOUDOS_REQUIRE_BACKUP_TOOLS") == "1" {
				t.Fatalf("%s is not on the PATH", bin)
			}
			t.Skipf("%s is not on the PATH: run make test-backup, which runs these tests in the backup image", bin)
		}
	}
}

// newService gives a service on a migrated throwaway database, an empty data
// root, and a new repository in a backup folder of its own.
func newService(t *testing.T) *Service {
	t.Helper()
	requireTools(t)
	url := testdb.URL(t)
	pool, err := db.Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	p, err := migrate.NewProvider(db.SQL(pool))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	s := &Service{
		Pool: pool, Files: &storage.Store{Root: t.TempDir(), Pool: pool}, DatabaseURL: url,
		Repo: filepath.Join(dir, "restic"), Password: "test-key-" + t.Name(), Dir: dir,
		Env: []string{"RESTIC_CACHE_DIR=" + t.TempDir()},
	}
	if err := s.Init(context.Background()); err != nil {
		t.Fatalf("init the repository: %v", err)
	}
	return s
}

func put(t *testing.T, s *Service, owner, p string, content []byte) {
	t.Helper()
	if _, err := s.Files.Put(context.Background(), owner, p, strings.NewReader(string(content)), storage.NoExpectations()); err != nil {
		t.Fatalf("put %s: %v", p, err)
	}
}

func random(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// fill stores a few files, some of them large, a version and a trashed one.
func fill(t *testing.T, s *Service) {
	t.Helper()
	ctx := context.Background()
	put(t, s, "enzo", "Photos/vacances.jpg", random(t, 2<<20))
	put(t, s, "enzo", "Documents/contrat.pdf", random(t, 300<<10))
	put(t, s, "enzo", "notes.txt", []byte("à faire"))
	put(t, s, "lea", "devoirs/maths.txt", []byte("exercice 4"))
	put(t, s, "lea", "musique.mp3", random(t, 1<<20))
	if _, err := s.Files.Put(ctx, "enzo", "notes.txt", strings.NewReader("fait"), storage.PutOptions{ExpectSize: -1, Replace: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Files.Trash(ctx, "lea", "musique.mp3"); err != nil {
		t.Fatal(err)
	}
}

// hashTree returns the SHA-256 of every regular file under dir, by path.
func hashTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out[rel] = fmt.Sprintf("%x", sha256.Sum256(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// lastRun reads the newest row of kind.
func lastRun(t *testing.T, pool *pgxpool.Pool, kind string) Run {
	t.Helper()
	runs, err := Runs(context.Background(), pool, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		if r.Kind == kind {
			return r
		}
	}
	t.Fatalf("no %s run in backup_runs", kind)
	return Run{}
}

func num(v any) int64 {
	f, _ := v.(float64)
	return int64(f)
}

// Two runs: the first stores everything, the second almost nothing (the
// hard links change the ctime, never the content). The data root is the
// same after both, the picture gone.
func TestBackupTwiceAddsAlmostNothing(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	fill(t, s)
	before := hashTree(t, s.Files.Root)

	id, err := s.Backup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first := lastRun(t, s.Pool, "backup")
	if first.ID != id || first.OK == nil || !*first.OK {
		t.Fatalf("first run: %+v", first)
	}
	t.Logf("first run: %v", first.Detail)
	if num(first.Detail["data_added"]) < 3<<20 {
		t.Errorf("the first run added %v bytes, want the files' 3 MB at least", first.Detail["data_added"])
	}
	if num(first.Detail["files_new"]) != 6 { // 4 live files, a version, a trashed file
		t.Errorf("the first run stored %v new files, want 6", first.Detail["files_new"])
	}

	if _, err := s.Backup(ctx); err != nil {
		t.Fatal(err)
	}
	second := lastRun(t, s.Pool, "backup")
	t.Logf("second run: %v", second.Detail)
	if num(second.Detail["files_new"]) != 0 || num(second.Detail["files_changed"]) != 0 {
		t.Errorf("the second run re-read files: new %v, changed %v", second.Detail["files_new"], second.Detail["files_changed"])
	}
	if added := num(second.Detail["data_added"]); added > 256<<10 {
		t.Errorf("the second run added %d bytes", added)
	}

	if _, err := os.Lstat(s.Files.SnapshotDir()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the picture is left after the run: %v", err)
	}
	if after := hashTree(t, s.Files.Root); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Errorf("the data root changed across two runs")
	}
	list, err := s.snapshots(ctx, s.Repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 4 {
		t.Errorf("%d snapshots after two runs, want 4", len(list))
	}
	for _, sn := range list {
		if r := sn.runID(); r != first.ID && r != second.ID {
			t.Errorf("snapshot %s has run %d", sn.ID, r)
		}
	}
}

// The retention keeps 7 days, 4 weeks and 12 months. Snapshots on chosen
// dates, at noon UTC: ten days from Monday 21 to Wednesday 30 December 2026,
// the 15th of each month from January to November 2026, and two in 2025.
// Kept: the 7 last days (24 to 30 December); weeks 53 and 52 are among them,
// then 15 November and 15 October make 4 weeks; December, November and
// October are kept, September to January make 12 months. Gone: 21, 22 and 23
// December, and both of 2025: 23 - 5 = 18.
func TestForgetKeepsTheRetention(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	s.Env = append(s.Env, "TZ=UTC")
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	var dates []string
	for d := 21; d <= 30; d++ {
		dates = append(dates, fmt.Sprintf("2026-12-%02d", d))
	}
	for m := 1; m <= 11; m++ {
		dates = append(dates, fmt.Sprintf("2026-%02d-15", m))
	}
	dates = append(dates, "2025-06-15", "2025-11-15")
	for _, d := range dates {
		if _, err := s.restic(ctx, s.Repo, "backup", "--host", Host, "--time", d+" 12:00:00", src); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.forget(ctx, s.Repo); err != nil {
		t.Fatal(err)
	}
	list, err := s.snapshots(ctx, s.Repo)
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, sn := range list {
		kept = append(kept, sn.Time.UTC().Format("2006-01-02"))
	}
	sort.Strings(kept)
	want := []string{
		"2026-01-15", "2026-02-15", "2026-03-15", "2026-04-15", "2026-05-15", "2026-06-15",
		"2026-07-15", "2026-08-15", "2026-09-15", "2026-10-15", "2026-11-15",
		"2026-12-24", "2026-12-25", "2026-12-26", "2026-12-27", "2026-12-28", "2026-12-29", "2026-12-30",
	}
	if fmt.Sprint(kept) != fmt.Sprint(want) {
		t.Errorf("kept\n%v\nwant\n%v", kept, want)
	}
}

// The restore test passes on a healthy repository, and fails when the
// repository is damaged, and when a file differs from the index.
func TestRestoreTest(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	fill(t, s)
	s.Sample = 100 // every live file
	if _, err := s.Backup(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.RestoreTest(ctx); err != nil {
		t.Fatalf("restore test on a healthy repository: %v", err)
	}
	r := lastRun(t, s.Pool, "restore_test")
	t.Logf("restore test: %v", r.Detail)
	if r.OK == nil || !*r.OK || num(r.Detail["sample"]) != 4 || num(r.Detail["indexed_files"]) != 5 {
		t.Errorf("restore test row: %+v", r)
	}
	if _, err := os.Lstat(filepath.Join(s.Dir, scratchName)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the restore test left its folder: %v", err)
	}

	t.Run("a file differs from the index", func(t *testing.T) {
		// The index says another content than the one stored: the next run
		// backs that up, and the restore test must see it.
		if _, err := s.Pool.Exec(ctx, `UPDATE files SET sha256 = sha256('autre chose') WHERE owner = 'enzo' AND path = 'Photos/vacances.jpg'`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Backup(ctx); err != nil {
			t.Fatal(err)
		}
		err := s.RestoreTest(ctx)
		if err == nil || !strings.Contains(err.Error(), "Photos/vacances.jpg") {
			t.Fatalf("restore test with a file that differs: %v", err)
		}
		r := lastRun(t, s.Pool, "restore_test")
		if r.OK == nil || *r.OK || r.Error == nil {
			t.Errorf("row of a failed restore test: %+v", r)
		}
	})

	t.Run("a damaged repository", func(t *testing.T) {
		packs := 0
		err := filepath.WalkDir(filepath.Join(s.Repo, "data"), func(p string, d fs.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() {
				return err
			}
			if err := os.Chmod(p, 0o600); err != nil {
				return err
			}
			f, err := os.OpenFile(p, os.O_RDWR, 0)
			if err != nil {
				return err
			}
			defer f.Close()
			packs++
			_, err = f.WriteAt([]byte("damaged!"), 64)
			return err
		})
		if err != nil || packs == 0 {
			t.Fatalf("damage %d packs: %v", packs, err)
		}
		if err := s.RestoreTest(ctx); err == nil {
			t.Fatal("restore test passed on a damaged repository")
		}
	})
}

// A full restore onto a new box: an empty data root and an empty database
// get every file back, and storage's own check finds no difference.
func TestRestoreOntoANewBox(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	fill(t, s)
	if _, err := s.Backup(ctx); err != nil {
		t.Fatal(err)
	}
	want := hashTree(t, s.Files.Root)

	url := testdb.URL(t)
	pool, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	box := &Service{
		Pool: pool, Files: &storage.Store{Root: t.TempDir(), Pool: pool}, DatabaseURL: url,
		Repo: s.Repo, Password: s.Password, Dir: s.Dir, Env: s.Env,
	}
	var out strings.Builder
	if err := box.Restore(ctx, 0, &out); err != nil {
		t.Fatalf("restore: %v\n%s", err, out.String())
	}
	t.Log(out.String())
	if got := hashTree(t, box.Files.Root); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("restored files\n%v\nwant\n%v", got, want)
	}
	p, err := migrate.NewProvider(db.SQL(pool))
	if err != nil {
		t.Fatal(err)
	}
	if pending, err := p.HasPending(ctx); err != nil || pending {
		t.Errorf("the restored database has pending migrations: %v %v", pending, err)
	}
	rep, err := box.Files.Verify(ctx)
	if err != nil || len(rep.Problems) > 0 || rep.OK == 0 {
		t.Errorf("verify after restore: %d ok, problems %v, %v", rep.OK, rep.Problems, err)
	}
	var smbFiles bool
	if err := pool.QueryRow(ctx, `SELECT has_table_privilege('cloudos_smb', 'files', 'SELECT')`).Scan(&smbFiles); err != nil || !smbFiles {
		t.Errorf("the smb role lost its rights in the restore: %v %v", smbFiles, err)
	}

	// A second restore is refused: nothing is ever written over.
	if err := box.Restore(ctx, 0, &out); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Errorf("a restore over a box in use: %v", err)
	}
}

// With no repository (the disk is not plugged in), a run fails, says so in
// backup_runs, takes no picture and creates no repository.
func TestBackupWithoutTheDisk(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	fill(t, s)
	s.Repo = filepath.Join(s.Dir, "unplugged", "restic")
	_, err := s.Backup(ctx)
	if !errors.Is(err, ErrNoRepository) {
		t.Fatalf("a run without the disk: %v, want ErrNoRepository", err)
	}
	r := lastRun(t, s.Pool, "backup")
	if r.OK == nil || *r.OK || r.Error == nil || !strings.Contains(*r.Error, "plugged in") {
		t.Errorf("row of a run without the disk: %+v", r)
	}
	if _, err := os.Lstat(filepath.Join(s.Dir, "unplugged")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("the run created something where the disk should be")
	}
	if _, err := os.Lstat(s.Files.SnapshotDir()); !errors.Is(err, fs.ErrNotExist) {
		t.Error("the run took a picture with no disk to write it to")
	}
}

// A remote repository gets a copy of every snapshot after each run, under
// the same key, with the same chunker (InitRemote).
func TestCopyToARemote(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	fill(t, s)
	s.Remote = filepath.Join(t.TempDir(), "remote")
	if err := s.InitRemote(ctx); err != nil {
		t.Fatalf("init the remote: %v", err)
	}
	if _, err := s.Backup(ctx); err != nil {
		t.Fatal(err)
	}
	r := lastRun(t, s.Pool, "copy")
	if r.OK == nil || !*r.OK {
		t.Fatalf("copy row: %+v", r)
	}
	local, err := s.snapshots(ctx, s.Repo)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := s.snapshots(ctx, s.Remote)
	if err != nil {
		t.Fatal(err)
	}
	if len(remote) != len(local) || len(remote) != 2 {
		t.Errorf("%d snapshots in the remote, %d here", len(remote), len(local))
	}
}

// The check reads back a twelfth of the data, a different one each week.
func TestCheck(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	fill(t, s)
	if _, err := s.Backup(ctx); err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 11, 22, 3, 0, 0, 0, time.UTC) // a Sunday of ISO week 47
	if err := s.Check(ctx, day); err != nil {
		t.Fatal(err)
	}
	r := lastRun(t, s.Pool, "check")
	if r.OK == nil || !*r.OK || r.Detail["subset"] != "12/12" {
		t.Errorf("check row: %+v", r)
	}
}

// The scratch folder of the restore test is erased there and nowhere else.
func TestRemoveScratchStaysInItsFolder(t *testing.T) {
	// A backup folder as backup-init leaves it, with its repository.
	backupDir := func(t *testing.T) string {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "restic"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "restic", "config"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	// A user's file in <dir>/restore-test, which must survive.
	plant := func(t *testing.T, dir string) string {
		keep := filepath.Join(dir, scratchName, "precious.txt")
		if err := os.MkdirAll(filepath.Dir(keep), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(keep, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		return keep
	}
	refused := func(t *testing.T, s *Service, keep string) {
		t.Helper()
		if err := s.removeScratch(); err == nil {
			t.Errorf("removeScratch with the backup folder %q and the data root %q went through", s.Dir, s.Files.Root)
		}
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("a user's file is gone: %v", err)
		}
	}

	t.Run("not an absolute path", func(t *testing.T) {
		for _, dir := range []string{"", "relative"} {
			if err := (&Service{Dir: dir, Files: &storage.Store{Root: t.TempDir()}}).removeScratch(); err == nil {
				t.Errorf("removeScratch with the backup folder %q went through", dir)
			}
		}
	})
	t.Run("no data root known", func(t *testing.T) {
		dir := backupDir(t)
		refused(t, &Service{Dir: dir, Files: &storage.Store{}}, plant(t, dir))
	})
	t.Run("no repository", func(t *testing.T) {
		dir := t.TempDir()
		refused(t, &Service{Dir: dir, Files: &storage.Store{Root: t.TempDir()}}, plant(t, dir))
	})
	t.Run("the data root itself", func(t *testing.T) {
		// A data root holding a repository, mounted twice: as /data and as
		// /backup in the container, two paths for one folder.
		dir := backupDir(t)
		if err := os.MkdirAll(filepath.Join(dir, ".cloudos"), 0o700); err != nil {
			t.Fatal(err)
		}
		other := filepath.Join(t.TempDir(), "data")
		if err := os.Symlink(dir, other); err != nil {
			t.Fatal(err)
		}
		refused(t, &Service{Dir: dir, Files: &storage.Store{Root: other}}, plant(t, dir))
	})
	t.Run("the same folder under another path", func(t *testing.T) {
		// No .cloudos yet (a new box): only the disk comparison sees it.
		dir := backupDir(t)
		other := filepath.Join(t.TempDir(), "data")
		if err := os.Symlink(filepath.Join(dir, scratchName), other); err != nil {
			t.Fatal(err)
		}
		refused(t, &Service{Dir: dir, Files: &storage.Store{Root: other}}, plant(t, dir))
	})
	t.Run("inside the data root", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, "backup")
		if err := os.MkdirAll(filepath.Join(dir, "restic"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "restic", "config"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(t.TempDir(), "data")
		if err := os.Symlink(root, link); err != nil {
			t.Fatal(err)
		}
		refused(t, &Service{Dir: dir, Files: &storage.Store{Root: link}}, plant(t, dir))
	})
	t.Run("inside the data root, before the restore test's first run", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, "backup")
		if err := os.MkdirAll(filepath.Join(dir, "restic"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "restic", "config"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := (&Service{Dir: dir, Files: &storage.Store{Root: root}}).removeScratch(); err == nil {
			t.Error("the restore test would create its folder in the data root")
		}
	})
	t.Run("the data root inside it", func(t *testing.T) {
		dir := backupDir(t)
		keep := plant(t, dir)
		refused(t, &Service{Dir: dir, Files: &storage.Store{Root: filepath.Dir(keep)}}, keep)
	})
	t.Run("a link in its place", func(t *testing.T) {
		dir := backupDir(t)
		target := t.TempDir()
		keep := filepath.Join(target, "precious.txt")
		if err := os.WriteFile(keep, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, scratchName)); err != nil {
			t.Fatal(err)
		}
		refused(t, &Service{Dir: dir, Files: &storage.Store{Root: t.TempDir()}}, keep)
	})
	t.Run("its own folder", func(t *testing.T) {
		dir := backupDir(t)
		plant(t, dir)
		if err := (&Service{Dir: dir, Files: &storage.Store{Root: t.TempDir()}}).removeScratch(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(filepath.Join(dir, scratchName)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("the restore test's folder is still there: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "restic", "config")); err != nil {
			t.Errorf("the repository is gone: %v", err)
		}
	})
}

// Every role a migration creates is one Restore creates before loading a
// dump that grants it rights.
func TestRestoreKnowsEveryRole(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`CREATE ROLE (\w+)[^;]*`)
	created := map[string]string{}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			created[m[1]] = strings.Join(strings.Fields(m[0]), " ")
		}
	}
	known := map[string]string{}
	for _, r := range roles {
		known[r.name] = r.create
	}
	if fmt.Sprint(created) != fmt.Sprint(known) {
		t.Errorf("the migrations create\n%v\nRestore creates\n%v", created, known)
	}
}

func TestRedact(t *testing.T) {
	for in, want := range map[string]string{
		"s3:https://s3.example.com/bucket":   "s3:https://s3.example.com/bucket",
		"sftp:user@host:/srv/restic":         "sftp:…@host:/srv/restic",
		"rest:https://u:p@host:8000/restic/": "rest:https://…@host:8000/restic/",
		"/backup/remote":                     "/backup/remote",
	} {
		if got := redact(in); got != want {
			t.Errorf("redact(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestErrorsKeepNoSecret(t *testing.T) {
	// A command that, like restic, repeats its repository and could print a
	// key: its error, bound for backup_runs, carries neither.
	remote := "rest:https://user:hunter2-remote@host:8000/restic/"
	s := &Service{Password: "the-repository-key"}
	cmd := s.command(context.Background(), append(s.resticEnv(remote), "AWS_SECRET_ACCESS_KEY=aws-secret-value"),
		"sh", "-c", `echo "Fatal: unable to open $RESTIC_REPOSITORY with $RESTIC_PASSWORD and $AWS_SECRET_ACCESS_KEY" >&2; exit 1`)
	_, err := run(cmd)
	if err == nil {
		t.Fatal("the command did not fail")
	}
	for _, secret := range []string{"hunter2-remote", "the-repository-key", "aws-secret-value"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("the error shows %q: %v", secret, err)
		}
	}
	if !strings.Contains(err.Error(), "rest:https://…@host:8000/restic/") {
		t.Errorf("the error lost the repository's address: %v", err)
	}
}

func TestOnlyResticGetsItsSecrets(t *testing.T) {
	s := &Service{LowPriority: true, Env: []string{"RESTIC_PASSWORD=key", "AWS_SECRET_ACCESS_KEY=aws", "RESTIC_CACHE_DIR=/cache", "PGPASSWORD=pg"}}
	has := func(env []string, kv string) bool { return slices.Contains(env, kv) }
	restic := s.command(context.Background(), nil, "restic", "snapshots").Env
	if !has(restic, "RESTIC_PASSWORD=key") || !has(restic, "AWS_SECRET_ACCESS_KEY=aws") {
		t.Errorf("restic lost its secrets: %v", restic)
	}
	other := s.command(context.Background(), nil, "pg_restore", "--list").Env
	if has(other, "RESTIC_PASSWORD=key") || has(other, "AWS_SECRET_ACCESS_KEY=aws") {
		t.Errorf("pg_restore got restic's secrets: %v", other)
	}
	if !has(other, "PGPASSWORD=pg") || !has(other, "RESTIC_CACHE_DIR=/cache") {
		t.Errorf("pg_restore lost what it may keep: %v", other)
	}
}

func TestCheckRole(t *testing.T) {
	ctx := context.Background()
	owner, _ := testdb.Migrated(t)
	if err := CheckRole(ctx, owner); err == nil {
		t.Error("CheckRole accepted the database's owner")
	}
	// The same database as the backup's role.
	cfg := owner.Config().Copy()
	cfg.MaxConns = 1
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, "SET ROLE "+DBRole)
		return err
	}
	role, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer role.Close()
	if err := CheckRole(ctx, role); err != nil {
		t.Errorf("CheckRole refused %s: %v", DBRole, err)
	}
}

func TestNext(t *testing.T) {
	paris, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ now, want string }{
		{"2026-11-20 17:00", "2026-11-21 03:00"},
		{"2026-11-20 02:59", "2026-11-20 03:00"},
		{"2026-11-20 03:00", "2026-11-21 03:00"},
		{"2026-03-28 12:00", "2026-03-29 03:00"}, // the night the clocks go forward
	} {
		now, _ := time.ParseInLocation("2006-01-02 15:04", c.now, paris)
		if got := next(now, 3, 0).Format("2006-01-02 15:04"); got != c.want {
			t.Errorf("next(%s) = %s, want %s", c.now, got, c.want)
		}
	}
	if _, _, err := ParseAt("3h"); err == nil {
		t.Error("ParseAt accepted 3h")
	}
	if h, m, err := ParseAt("03:30"); err != nil || h != 3 || m != 30 {
		t.Errorf("ParseAt(03:30) = %d, %d, %v", h, m, err)
	}
}
