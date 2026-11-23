package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// scratchName is the restore test's folder on the backup disk.
const scratchName = "restore-test"

// testPort is the throwaway server's port, on its own socket only.
const testPort = "5499"

func (s *Service) scratch() string { return filepath.Join(s.Dir, scratchName) }

// RestoreTest checks, without touching the box, that the newest complete run
// restores: it extracts its database dump, restores it into a throwaway
// Postgres started here (initdb on the backup disk, a socket and no port),
// checks the number of indexed files, then extracts Sample files of that run
// picked at random in the restored index and compares their SHA-256 with it.
func (s *Service) RestoreTest(ctx context.Context) error {
	id, err := s.begin(ctx, "restore_test")
	if err != nil {
		return err
	}
	detail := map[string]any{}
	err = s.restoreTest(ctx, detail)
	s.end(ctx, id, detail, err)
	return err
}

func (s *Service) restoreTest(ctx context.Context, detail map[string]any) (err error) {
	if s.Dir == "" {
		return errors.New("the backup folder is not set")
	}
	if err := s.checkRepo(ctx, s.Repo); err != nil {
		return err
	}
	pair, err := s.complete(ctx, s.Repo, 0)
	if err != nil {
		return err
	}
	detail["run"] = pair.Run
	detail["db_snapshot"] = pair.DB.ID
	detail["files_snapshot"] = pair.Files.ID

	// The run's own count of indexed files, taken in the same snapshot.
	var want *int64
	if err := s.Pool.QueryRow(ctx, `SELECT (detail ->> 'indexed_files')::bigint FROM backup_runs WHERE id = $1`, pair.Run).
		Scan(&want); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}

	// A test left by a stop: its server is gone with the container.
	if err := s.removeScratch(); err != nil {
		return err
	}
	if err := os.MkdirAll(s.scratch(), 0o700); err != nil {
		return err
	}
	defer func() {
		if rerr := s.removeScratch(); rerr != nil {
			err = errors.Join(err, rerr)
		}
	}()

	pg, err := s.startTestServer(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, pg.stop()) }()

	conn, err := pgx.Connect(ctx, pg.url("postgres"))
	if err != nil {
		return err
	}
	_, err = conn.Exec(ctx, `CREATE DATABASE restoretest`)
	conn.Close(ctx)
	if err != nil {
		return err
	}
	// Ownership and rights name the box's roles, which this server lacks.
	if err := s.restoreDump(ctx, pair, pg.url("restoretest"), "--no-owner", "--no-acl"); err != nil {
		return err
	}

	db, err := pgx.Connect(ctx, pg.url("restoretest"))
	if err != nil {
		return err
	}
	defer db.Close(context.WithoutCancel(ctx))
	var restored int64
	if err := db.QueryRow(ctx, `SELECT count(*) FROM files WHERE kind = 'file'`).Scan(&restored); err != nil {
		return err
	}
	detail["indexed_files"] = restored
	if want != nil && *want != restored {
		return fmt.Errorf("the restored index has %d files, the run counted %d", restored, *want)
	}

	rows, err := db.Query(ctx, `SELECT owner, path, sha256 FROM files
		WHERE kind = 'file' AND trash_id IS NULL ORDER BY random() LIMIT $1`, s.sample())
	if err != nil {
		return err
	}
	type pick struct {
		owner, path string
		sum         []byte
	}
	picks, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (pick, error) {
		var p pick
		return p, r.Scan(&p.owner, &p.path, &p.sum)
	})
	if err != nil {
		return err
	}
	var mismatches []string
	for _, p := range picks {
		got, err := s.dumpHash(ctx, pair.Files.ID, path.Join(pair.FilesRoot(), p.owner, p.path))
		switch {
		case err != nil:
			mismatches = append(mismatches, fmt.Sprintf("%s/%s: %v", p.owner, p.path, err))
		case !bytes.Equal(got, p.sum):
			mismatches = append(mismatches, fmt.Sprintf("%s/%s: sha256 %x, the index says %x", p.owner, p.path, got, p.sum))
		}
	}
	detail["sample"] = len(picks)
	if len(mismatches) > 0 {
		detail["mismatches"] = mismatches[:min(len(mismatches), 20)]
		return fmt.Errorf("%d of %d sampled files differ from the index: %s", len(mismatches), len(picks), mismatches[0])
	}
	return nil
}

// dumpHash extracts file from snapshot and returns its SHA-256.
func (s *Service) dumpHash(ctx context.Context, snapshot, file string) ([]byte, error) {
	h := sha256.New()
	cmd := s.command(ctx, s.resticEnv(s.Repo), "restic", "--retry-lock", "10m", "dump", snapshot, file)
	cmd.Stdout = h
	if _, err := run(cmd); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

// restoreDump pipes the run's database dump from the repository into
// pg_restore on url, in one transaction: nothing is left half restored.
func (s *Service) restoreDump(ctx context.Context, pair Pair, url string, extra ...string) error {
	dump := s.command(ctx, s.resticEnv(s.Repo), "restic", "--retry-lock", "10m", "dump", pair.DB.ID, "/"+DumpName)
	args := append([]string{"--exit-on-error", "--single-transaction", "--dbname=" + url}, extra...)
	restore := s.command(ctx, nil, "pg_restore", args...)
	pipe, err := dump.StdoutPipe()
	if err != nil {
		return err
	}
	restore.Stdin = pipe
	dumpErr := &tail{max: 2048}
	dump.Stderr = dumpErr
	if err := dump.Start(); err != nil {
		return err
	}
	_, rerr := run(restore)
	werr := dump.Wait()
	// pg_restore failing first cuts the pipe: its own error is the cause.
	if rerr != nil {
		return fmt.Errorf("restore the database: %w", rerr)
	}
	if werr != nil {
		return fmt.Errorf("extract the database dump: %w: %s", werr, strings.TrimSpace(dumpErr.String()))
	}
	return nil
}

// testServer is a throwaway Postgres: its data under the scratch folder, its
// socket in the system's temporary folder. A Unix socket cannot be made on
// every disk (exFAT, a Mac's shared folder), and its path must stay under
// about 100 bytes, which a deep backup folder would pass.
type testServer struct {
	s       *Service
	data    string
	sockets string
}

func (pg *testServer) url(db string) string {
	return "host=" + pg.sockets + " port=" + testPort + " user=postgres dbname=" + db + " sslmode=disable"
}

// startTestServer creates and starts a Postgres of the image's version,
// listening on a socket of its own only.
func (s *Service) startTestServer(ctx context.Context) (*testServer, error) {
	pg := &testServer{s: s, data: filepath.Join(s.scratch(), "pg"), sockets: filepath.Join(os.TempDir(), "cloudos-restore-test")}
	if err := os.MkdirAll(pg.sockets, 0o700); err != nil {
		return nil, err
	}
	if _, err := run(s.command(ctx, nil, "initdb", "--pgdata="+pg.data, "--username=postgres", "--auth=trust",
		"--encoding=UTF8", "--locale=C.UTF-8", "--no-sync", "--no-instructions")); err != nil {
		return nil, fmt.Errorf("create the test database server: %w", err)
	}
	opts := "-c listen_addresses='' -c unix_socket_directories=" + pg.sockets + " -p " + testPort +
		" -c fsync=off -c full_page_writes=off -c max_wal_size=1GB"
	logFile := filepath.Join(s.scratch(), "pg.log")
	if _, err := run(s.command(ctx, nil, "pg_ctl", "--pgdata="+pg.data, "--wait", "--timeout=120",
		"--log="+logFile, "--options="+opts, "start")); err != nil {
		// The server's own reason is in its log, which the scratch folder's
		// removal takes away: keep its end in the error.
		if b, rerr := os.ReadFile(logFile); rerr == nil && len(b) > 0 {
			if len(b) > 1500 {
				b = b[len(b)-1500:]
			}
			return nil, fmt.Errorf("start the test database server: %w; its log ends with: %s", err, strings.TrimSpace(string(b)))
		}
		return nil, fmt.Errorf("start the test database server: %w", err)
	}
	return pg, nil
}

// stop stops the server, at once: it holds nothing to keep.
func (pg *testServer) stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "pg_ctl", "--pgdata="+pg.data, "--mode=immediate", "--wait", "stop")
	if _, err := run(cmd); err != nil {
		return fmt.Errorf("stop the test database server: %w", err)
	}
	return nil
}

// removeScratch erases the restore test's folder, strictly
// <backup dir>/restore-test: a throwaway database server and its dump, never
// a user's file. It refuses when the backup folder is not set, or when that
// folder is or lies inside the data root, or is a symbolic link.
func (s *Service) removeScratch() error {
	if s.Dir == "" || !filepath.IsAbs(s.Dir) {
		return fmt.Errorf("refusing to erase the restore test's folder: the backup folder %q is not an absolute path", s.Dir)
	}
	dir := s.scratch()
	if filepath.Base(dir) != scratchName || filepath.Dir(dir) != filepath.Clean(s.Dir) {
		return fmt.Errorf("refusing to erase %s: not the restore test's folder", dir)
	}
	if s.Files == nil || s.Files.Root == "" {
		return fmt.Errorf("refusing to erase %s: the data root is not known", dir)
	}
	// Checked even when the folder is absent: RestoreTest creates it next.
	if err := s.apartFromData(dir); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("refusing to erase %s: not a folder", dir)
	}
	return os.RemoveAll(dir)
}

// apartFromData checks, on the disk rather than by path, that dir is the
// restore test's folder of a backup folder and holds no user file: the backup
// folder holds a restic repository and is no data root, and neither dir nor
// the data root lies inside the other. Paths alone cannot tell: in the
// container the same host folder can be mounted at /data and at /backup.
func (s *Service) apartFromData(dir string) error {
	if fi, err := os.Lstat(filepath.Join(s.Dir, "restic", "config")); err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("refusing to erase %s: %s holds no restic repository", dir, s.Dir)
	}
	for _, marker := range []string{filepath.Join(s.Dir, ".cloudos"), filepath.Join(dir, ".cloudos")} {
		if _, err := os.Lstat(marker); !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("refusing to erase %s: %s looks like a data root", dir, filepath.Dir(marker))
		}
	}
	root, err := os.Stat(s.Files.Root)
	if err != nil {
		return fmt.Errorf("refusing to erase %s: cannot see the data root: %w", dir, err)
	}
	if within(dir, root) {
		return fmt.Errorf("refusing to erase %s: it is or lies in the data root", dir)
	}
	scratch, err := os.Stat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if within(s.Files.Root, scratch) {
		return fmt.Errorf("refusing to erase %s: the data root lies in it", dir)
	}
	return nil
}

// within reports whether path, or one of the folders above it, is the folder
// that target describes.
func within(path string, target fs.FileInfo) bool {
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		if fi, err := os.Stat(p); err == nil && os.SameFile(fi, target) {
			return true
		}
		if p == filepath.Dir(p) {
			return false
		}
	}
}
