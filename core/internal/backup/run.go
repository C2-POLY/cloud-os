package backup

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Backup saves the box once: a consistent picture (storage's Freeze), the
// database dumped from its snapshot into the repository, the files read
// from the picture, the picture's links removed (Release), then the
// retention. Both snapshots carry the tag run:<id>, id being the run's row in
// backup_runs, which says how it went. When Remote is set, the repository is
// then copied there (a row of its own, kind copy).
func (s *Service) Backup(ctx context.Context) (int64, error) {
	id, err := s.begin(ctx, "backup")
	if err != nil {
		return 0, err
	}
	detail := map[string]any{}
	err = s.backup(ctx, id, detail)
	s.end(ctx, id, detail, err)
	if err != nil {
		return id, err
	}
	if s.Remote != "" {
		if err := s.Copy(ctx); err != nil {
			return id, err
		}
	}
	return id, nil
}

func (s *Service) backup(ctx context.Context, id int64, detail map[string]any) (err error) {
	if err := s.checkRepo(ctx, s.Repo); err != nil {
		return err
	}
	run := "run:" + strconv.FormatInt(id, 10)
	start := time.Now()

	fz, err := s.Files.Freeze(ctx)
	if err != nil {
		return fmt.Errorf("freeze the data: %w", err)
	}
	released := false
	defer func() {
		if !released {
			err = errors.Join(err, fz.Release(ctx))
		}
	}()
	detail["owners"] = fz.Owners
	detail["files"] = fz.Files
	detail["bytes"] = fz.Bytes
	detail["indexed_files"] = fz.Indexed
	detail["lock_ms"] = fz.LockHeld.Milliseconds()

	// The database, from the snapshot of the instant the files were linked.
	// restic stores nothing if pg_dump fails.
	out, err := s.restic(ctx, s.Repo, "backup", "--json", "--host", Host, "--tag", run, "--tag", "db",
		"--stdin-filename", DumpName, "--stdin-from-command", "--",
		"pg_dump", "--format=custom", "--snapshot="+fz.SnapshotID, "--dbname="+s.DatabaseURL)
	if err != nil {
		return fmt.Errorf("back up the database: %w", err)
	}
	db, err := parseSummary(out)
	if err != nil {
		return fmt.Errorf("back up the database: %w", err)
	}
	detail["db_snapshot"] = db.SnapshotID
	detail["db_bytes"] = db.TotalBytes
	if err := fz.EndSnapshot(ctx); err != nil {
		return err
	}

	// The files. Linking changes a file's ctime, never its content (storage
	// never modifies a stored file in place): --ignore-ctime keeps the run
	// incremental.
	out, err = s.restic(ctx, s.Repo, "backup", "--json", "--host", Host, "--tag", run, "--tag", "files",
		"--ignore-ctime", fz.Dir)
	if err != nil {
		// A dump without its files would pair the retention badly: it goes.
		if _, ferr := s.restic(ctx, s.Repo, "forget", db.SnapshotID); ferr == nil {
			detail["discarded_db_snapshot"] = db.SnapshotID
		}
		if exitCode(err) == 3 {
			return fmt.Errorf("back up the files: some files could not be read: %w", err)
		}
		return fmt.Errorf("back up the files: %w", err)
	}
	files, err := parseSummary(out)
	if err != nil {
		return fmt.Errorf("back up the files: %w", err)
	}
	detail["files_snapshot"] = files.SnapshotID
	detail["files_new"] = files.FilesNew
	detail["files_changed"] = files.FilesChanged
	detail["files_unmodified"] = files.FilesUnmodified
	detail["data_added"] = db.DataAdded + files.DataAdded

	released = true
	if err := fz.Release(ctx); err != nil {
		return fmt.Errorf("release the snapshot: %w", err)
	}
	if err := s.forget(ctx, s.Repo); err != nil {
		return err
	}
	detail["duration_ms"] = time.Since(start).Milliseconds()
	s.log().Info("backup done", "run", id, "files", fz.Files, "new", files.FilesNew, "changed", files.FilesChanged,
		"added", db.DataAdded+files.DataAdded, "lock", fz.LockHeld)
	return nil
}

// forget applies the retention to repo and prunes what no snapshot uses.
// The database dumps and the files are kept apart (restic groups them by
// path), each 7 days, 4 weeks and 12 months.
func (s *Service) forget(ctx context.Context, repo string) error {
	k := s.keep()
	_, err := s.restic(ctx, repo, "forget", "--host", Host,
		"--keep-daily", strconv.Itoa(k.Daily), "--keep-weekly", strconv.Itoa(k.Weekly), "--keep-monthly", strconv.Itoa(k.Monthly),
		"--prune")
	if err != nil {
		return fmt.Errorf("apply the retention: %w", err)
	}
	return nil
}

// Copy copies the snapshots the remote repository lacks there, encrypted
// before they leave, then applies the retention there too.
func (s *Service) Copy(ctx context.Context) error {
	id, err := s.begin(ctx, "copy")
	if err != nil {
		return err
	}
	detail := map[string]any{"remote": redact(s.Remote)}
	err = s.copy(ctx)
	s.end(ctx, id, detail, err)
	return err
}

func (s *Service) copy(ctx context.Context) error {
	if err := s.checkRepo(ctx, s.Remote); err != nil {
		return fmt.Errorf("remote: %w", err)
	}
	cmd := s.command(ctx, append(s.resticEnv(s.Remote), "RESTIC_FROM_PASSWORD="+s.Password), "restic",
		"--retry-lock", "10m", "copy", "--from-repo", s.Repo, "--host", Host)
	if _, err := run(cmd); err != nil {
		return fmt.Errorf("copy to the remote repository: %w", err)
	}
	return s.forget(ctx, s.Remote)
}

// InitRemote creates the remote repository with the local one's chunker
// parameters, so a copy deduplicates there as here, and its key.
func (s *Service) InitRemote(ctx context.Context) error {
	if s.Remote == "" {
		return errors.New("CLOUDOS_BACKUP_REMOTE is not set")
	}
	if err := s.checkRepo(ctx, s.Repo); err != nil {
		return err
	}
	cmd := s.command(ctx, append(s.resticEnv(s.Remote), "RESTIC_FROM_PASSWORD="+s.Password), "restic",
		"init", "--from-repo", s.Repo, "--copy-chunker-params")
	_, err := run(cmd)
	return err
}

// Init creates the repository on the backup disk. It refuses one that exists.
func (s *Service) Init(ctx context.Context) error {
	if s.Password == "" {
		return errors.New("RESTIC_PASSWORD is not set")
	}
	if err := s.checkRepo(ctx, s.Repo); err == nil {
		return fmt.Errorf("%s already holds a restic repository", s.Repo)
	} else if !errors.Is(err, ErrNoRepository) {
		return err
	}
	_, err := s.restic(ctx, s.Repo, "init")
	return err
}

// Check reads back a twelfth of the repository's data, a different one each
// week: the whole repository in 12 weeks.
func (s *Service) Check(ctx context.Context, now time.Time) error {
	id, err := s.begin(ctx, "check")
	if err != nil {
		return err
	}
	_, week := now.ISOWeek()
	subset := fmt.Sprintf("%d/12", week%12+1)
	detail := map[string]any{"subset": subset}
	err = s.checkRepo(ctx, s.Repo)
	if err == nil {
		_, err = s.restic(ctx, s.Repo, "check", "--read-data-subset="+subset)
	}
	s.end(ctx, id, detail, err)
	return err
}

// redact drops the user and password of a remote's address, if it has any.
func redact(remote string) string {
	for _, prefix := range []string{"s3:", "rest:", "sftp:", "b2:", "azure:", "gs:", "swift:", "rclone:"} {
		if rest, ok := strings.CutPrefix(remote, prefix); ok {
			if at := strings.LastIndex(rest, "@"); at >= 0 {
				if scheme := strings.Index(rest, "://"); scheme >= 0 && scheme < at {
					return prefix + rest[:scheme+3] + "…@" + rest[at+1:]
				}
				return prefix + "…@" + rest[at+1:]
			}
			return remote
		}
	}
	return remote
}
