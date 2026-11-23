// Package backup saves the box every night into an encrypted restic
// repository on a USB disk, checks it every week, and restores it onto a new
// box (docs/architecture/backup.md).
//
// A run takes a consistent picture with storage's Freeze: the database is
// dumped from the exported snapshot, the files are read from their hard links
// under .cloudos/snapshots/current, then the links go (Release). Nothing here
// writes the data root but storage itself; the one folder this package erases
// is its own scratch on the backup disk (removeScratch).
package backup

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/C2-POLY/cloud-os/core/internal/storage"
)

// DBRole is the backup service's database role (migration 0028).
const DBRole = "cloudos_backup"

// Host is the host name of every snapshot: a container's own name changes
// at each start, and restic finds the previous snapshot (for an incremental
// run) and applies the retention by host.
const Host = "cloudos"

// DumpName is the database dump's name in the repository.
const DumpName = "cloudos.dump"

// Keep is the retention: how many daily, weekly and monthly snapshots stay.
type Keep struct{ Daily, Weekly, Monthly int }

// DefaultKeep is 7 days, 4 weeks and 12 months.
var DefaultKeep = Keep{Daily: 7, Weekly: 4, Monthly: 12}

// DefaultSample is how many files the restore test compares with the index.
const DefaultSample = 200

// ErrNoRepository is a backup folder without a restic repository: the disk
// is not plugged in, or the repository was never created (scripts/box
// backup-init). A run never creates one.
var ErrNoRepository = errors.New("no restic repository")

// Service runs the backups. Its database role is DBRole, except for Restore,
// which writes a new database.
type Service struct {
	Pool  *pgxpool.Pool
	Files *storage.Store
	// DatabaseURL is what pg_dump and pg_restore connect to (the password in
	// PGPASSWORD, inherited).
	DatabaseURL string
	// Repo and Password are the repository on the backup disk
	// (RESTIC_REPOSITORY, RESTIC_PASSWORD).
	Repo, Password string
	// Remote, when set, is a second repository each run is copied to,
	// encrypted with the same key (CLOUDOS_BACKUP_REMOTE). Off by default.
	Remote string
	// Dir is the backup disk's folder; the restore test works under
	// Dir/restore-test.
	Dir string
	// Keep is the retention; zero means DefaultKeep.
	Keep Keep
	// Sample is how many files the restore test compares; zero means DefaultSample.
	Sample int
	// LowPriority runs restic, pg_dump and the test server under nice and
	// ionice (the nightly daemon).
	LowPriority bool
	// Env is added to every command's environment (tests: RESTIC_CACHE_DIR).
	Env []string
	Log *slog.Logger
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *Service) keep() Keep {
	if s.Keep == (Keep{}) {
		return DefaultKeep
	}
	return s.Keep
}

func (s *Service) sample() int {
	if s.Sample > 0 {
		return s.Sample
	}
	return DefaultSample
}

// command builds name with args, under nice and ionice when LowPriority.
// Cancelling ctx interrupts it as Ctrl-C would, so restic removes its lock.
func (s *Service) command(ctx context.Context, env []string, name string, args ...string) *exec.Cmd {
	base := append(os.Environ(), s.Env...)
	if name != "restic" {
		base = withoutResticSecrets(base)
	}
	if s.LowPriority {
		args = append([]string{"-n", "10", "ionice", "-c", "2", "-n", "7", name}, args...)
		name = "nice"
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(base, env...)
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = time.Minute
	return cmd
}

// resticEnv is the environment of a restic command on repo.
// withoutResticSecrets drops the repository's key and the remote's
// credentials from an environment, for the commands that are not restic:
// pg_restore, initdb and the test server have no use for them.
func withoutResticSecrets(env []string) []string {
	kept := make([]string, 0, len(env))
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(key, "RESTIC_") && key != "RESTIC_CACHE_DIR" || strings.HasPrefix(key, "AWS_") ||
			key == "CLOUDOS_BACKUP_REMOTE" || key == "CLOUDOS_BACKUP_DB_PASSWORD" {
			continue
		}
		kept = append(kept, kv)
	}
	return kept
}

func (s *Service) resticEnv(repo string) []string {
	return []string{"RESTIC_REPOSITORY=" + repo, "RESTIC_PASSWORD=" + s.Password}
}

// restic runs restic on repo and returns its standard output. Every command
// waits up to 10 minutes for a lock held by another run.
func (s *Service) restic(ctx context.Context, repo string, args ...string) ([]byte, error) {
	cmd := s.command(ctx, s.resticEnv(repo), "restic", append([]string{"--retry-lock", "10m"}, args...)...)
	return run(cmd)
}

// run runs cmd and returns its standard output; an error carries the end of
// its standard error.
func run(cmd *exec.Cmd) ([]byte, error) {
	var out bytes.Buffer
	errTail := &tail{max: 2048}
	if cmd.Stdout == nil {
		cmd.Stdout = &out
	}
	cmd.Stderr = errTail
	if err := cmd.Run(); err != nil {
		return out.Bytes(), &cmdError{name: cmdName(cmd), err: err, stderr: scrub(strings.TrimSpace(errTail.String()), cmd.Env)}
	}
	return out.Bytes(), nil
}

// scrub takes the secrets of a command's environment out of its error output,
// which ends up in backup_runs and the logs: restic names the repository it
// could not open, and a remote's address may carry a user and password.
func scrub(text string, env []string) string {
	for _, kv := range env {
		key, value, _ := strings.Cut(kv, "=")
		if len(value) < 4 {
			continue
		}
		switch key {
		case "RESTIC_REPOSITORY", "RESTIC_FROM_REPOSITORY":
			if r := redact(value); r != value {
				text = strings.ReplaceAll(text, value, r)
			}
		case "RESTIC_PASSWORD", "RESTIC_FROM_PASSWORD", "PGPASSWORD", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN":
			text = strings.ReplaceAll(text, value, "…")
		}
	}
	return text
}

// cmdName is the program cmd runs, past nice and ionice.
func cmdName(cmd *exec.Cmd) string {
	args := cmd.Args
	if len(args) > 8 && args[0] == "nice" {
		args = args[7:]
	}
	if len(args) > 1 && args[0] == "restic" {
		for _, a := range args[1:] {
			if !strings.HasPrefix(a, "-") && a != "10m" {
				return "restic " + a
			}
		}
	}
	return args[0]
}

type cmdError struct {
	name, stderr string
	err          error
}

func (e *cmdError) Error() string {
	if e.stderr == "" {
		return fmt.Sprintf("%s: %v", e.name, e.err)
	}
	return fmt.Sprintf("%s: %v: %s", e.name, e.err, e.stderr)
}

func (e *cmdError) Unwrap() error { return e.err }

// exitCode is the exit status of the command behind err, or -1.
func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// tail keeps the last max bytes written to it.
type tail struct {
	buf []byte
	max int
}

func (t *tail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tail) String() string { return string(t.buf) }

// checkRepo fails with ErrNoRepository when repo holds no restic repository.
func (s *Service) checkRepo(ctx context.Context, repo string) error {
	if s.Password == "" {
		return errors.New("RESTIC_PASSWORD is not set: run scripts/box backup-init")
	}
	if repo == "" {
		return errors.New("RESTIC_REPOSITORY is not set")
	}
	_, err := s.restic(ctx, repo, "cat", "config")
	if err == nil {
		return nil
	}
	// restic exits 10 when the repository does not exist (0.17 and later)
	if exitCode(err) == 10 {
		return fmt.Errorf("%w at %s: is the backup disk plugged in? (%v)", ErrNoRepository, redact(repo), err)
	}
	return fmt.Errorf("open the repository %s: %w", redact(repo), err)
}

// Snapshot is a restic snapshot, as `restic snapshots --json` lists it.
type Snapshot struct {
	ID    string    `json:"id"`
	Time  time.Time `json:"time"`
	Paths []string  `json:"paths"`
	Tags  []string  `json:"tags"`
}

func (sn Snapshot) has(tag string) bool {
	for _, t := range sn.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

// runID is the id of the run that made sn (its tag run:<id>), or 0.
func (sn Snapshot) runID() int64 {
	for _, t := range sn.Tags {
		if v, ok := strings.CutPrefix(t, "run:"); ok {
			if id, err := strconv.ParseInt(v, 10, 64); err == nil {
				return id
			}
		}
	}
	return 0
}

// snapshots lists the box's snapshots in repo, oldest first.
func (s *Service) snapshots(ctx context.Context, repo string) ([]Snapshot, error) {
	out, err := s.restic(ctx, repo, "snapshots", "--json", "--host", Host)
	if err != nil {
		return nil, err
	}
	var list []Snapshot
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, fmt.Errorf("read restic's snapshot list: %w", err)
	}
	return list, nil
}

// Pair is a complete run in the repository: its database dump and its files.
type Pair struct {
	Run       int64
	DB, Files Snapshot
}

// FilesRoot is the folder the files were read from, inside the snapshot.
func (p Pair) FilesRoot() string {
	if len(p.Files.Paths) == 0 {
		return ""
	}
	return p.Files.Paths[0]
}

// complete returns the newest run with both snapshots, or the run id when
// not 0.
func (s *Service) complete(ctx context.Context, repo string, id int64) (Pair, error) {
	list, err := s.snapshots(ctx, repo)
	if err != nil {
		return Pair{}, err
	}
	runs := map[int64]*Pair{}
	for _, sn := range list {
		r := sn.runID()
		if r == 0 || (id != 0 && r != id) {
			continue
		}
		p := runs[r]
		if p == nil {
			p = &Pair{Run: r}
			runs[r] = p
		}
		switch {
		case sn.has("db"):
			p.DB = sn
		case sn.has("files"):
			p.Files = sn
		}
	}
	var best *Pair
	for _, p := range runs {
		if p.DB.ID == "" || p.Files.ID == "" {
			continue
		}
		if best == nil || p.Files.Time.After(best.Files.Time) {
			best = p
		}
	}
	if best == nil {
		if id != 0 {
			return Pair{}, fmt.Errorf("run %d has no complete backup (database and files) in %s", id, redact(repo))
		}
		return Pair{}, fmt.Errorf("no complete backup (database and files) in %s", redact(repo))
	}
	return *best, nil
}

// summary is the last message of `restic backup --json`.
type summary struct {
	FilesNew        int64  `json:"files_new"`
	FilesChanged    int64  `json:"files_changed"`
	FilesUnmodified int64  `json:"files_unmodified"`
	DataAdded       int64  `json:"data_added"`
	TotalFiles      int64  `json:"total_files_processed"`
	TotalBytes      int64  `json:"total_bytes_processed"`
	SnapshotID      string `json:"snapshot_id"`
}

// parseSummary finds the summary among restic's JSON lines.
func parseSummary(out []byte) (summary, error) {
	var sum summary
	found := false
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var msg struct {
			Type string `json:"message_type"`
		}
		if json.Unmarshal(sc.Bytes(), &msg) != nil || msg.Type != "summary" {
			continue
		}
		if err := json.Unmarshal(sc.Bytes(), &sum); err != nil {
			return sum, err
		}
		found = true
	}
	if !found || sum.SnapshotID == "" {
		return sum, errors.New("restic gave no snapshot")
	}
	return sum, nil
}

// begin writes the row of a run that starts and returns its id.
func (s *Service) begin(ctx context.Context, kind string) (int64, error) {
	var id int64
	err := s.Pool.QueryRow(ctx, `INSERT INTO backup_runs (kind) VALUES ($1) RETURNING id`, kind).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("write the run in backup_runs: %w", err)
	}
	return id, nil
}

// end completes the row of a run with its outcome, and the space of the
// backup disk as the run leaves it (disk_free, disk_total), which the box's
// status shows: the core does not mount that disk.
func (s *Service) end(ctx context.Context, id int64, detail map[string]any, runErr error) {
	if total, free, ok := s.diskUsage(); ok {
		if detail == nil {
			detail = map[string]any{}
		}
		detail["disk_free"] = free
		detail["disk_total"] = total
	}
	var msg *string
	if runErr != nil {
		m := runErr.Error()
		if len(m) > 4000 {
			m = m[:4000] + "…"
		}
		msg = &m
	}
	if _, err := s.Pool.Exec(context.WithoutCancel(ctx),
		`UPDATE backup_runs SET finished_at = now(), ok = $2, detail = $3, error = $4 WHERE id = $1`,
		id, runErr == nil, detail, msg); err != nil {
		s.log().Error("backup: complete the run in backup_runs", "id", id, "err", err)
	}
}

// diskUsage measures the disk the repository is on, only while the
// repository is there: with the USB disk unplugged, its mount point is an
// empty folder of the system disk, whose space would be read as the backup's.
func (s *Service) diskUsage() (total, free uint64, ok bool) {
	if s.Repo == "" || !filepath.IsAbs(s.Repo) {
		return 0, 0, false // not a folder (a remote address), or not set
	}
	if _, err := os.Stat(filepath.Join(s.Repo, "config")); err != nil {
		return 0, 0, false
	}
	total, free, err := storage.DiskUsage(s.Repo)
	if err != nil || total == 0 {
		return 0, 0, false
	}
	return total, free, true
}

// Run is one row of backup_runs.
type Run struct {
	ID         int64          `json:"id"`
	Kind       string         `json:"kind"`
	StartedAt  time.Time      `json:"started_at"`
	FinishedAt *time.Time     `json:"finished_at"`
	OK         *bool          `json:"ok"`
	Detail     map[string]any `json:"detail"`
	Error      *string        `json:"error"`
}

// Runs returns the last n rows of backup_runs, newest first.
func Runs(ctx context.Context, pool *pgxpool.Pool, n int) ([]Run, error) {
	rows, err := pool.Query(ctx,
		`SELECT id, kind, started_at, finished_at, ok, detail, error FROM backup_runs ORDER BY id DESC LIMIT $1`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var r Run
		if err := rows.Scan(&r.ID, &r.Kind, &r.StartedAt, &r.FinishedAt, &r.OK, &r.Detail, &r.Error); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CheckRole refuses a database role wider than the backup's own: a
// superuser, or one that may write the index. The backup service reads the
// whole database and writes backup_runs only.
func CheckRole(ctx context.Context, pool *pgxpool.Pool) error {
	var (
		user                        string
		super, createRole, createDB bool
		bypassRLS, writesFiles, all bool
	)
	err := pool.QueryRow(ctx,
		`SELECT rolname, rolsuper, rolcreaterole, rolcreatedb, rolbypassrls,
		        has_table_privilege('files', 'INSERT,UPDATE,DELETE,TRUNCATE'),
		        pg_has_role(current_user, 'pg_write_all_data', 'USAGE')
		   FROM pg_roles WHERE rolname = current_user`).Scan(&user, &super, &createRole, &createDB, &bypassRLS, &writesFiles, &all)
	if err != nil {
		return fmt.Errorf("backup: check the database role: %w", err)
	}
	if super || createRole || createDB || bypassRLS || writesFiles || all {
		return fmt.Errorf("backup: the database role %s has more rights than reading; connect as %s (migration 0028)", user, DBRole)
	}
	return nil
}
