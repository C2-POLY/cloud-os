package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
)

// roles are the box's own database roles, created by migrations 0026 and
// 0028 with these attributes. A role belongs to the Postgres server, not to
// the database, so a dump does not hold them; the rights the dump grants
// them need them first. TestRestoreKnowsEveryRole keeps this list in step
// with the migrations. Their passwords are set by the core at its start.
var roles = []struct{ name, create, after string }{
	{"cloudos_smb", `CREATE ROLE cloudos_smb NOLOGIN NOINHERIT CONNECTION LIMIT 8`, ""},
	{"cloudos_backup", `CREATE ROLE cloudos_backup NOLOGIN INHERIT CONNECTION LIMIT 8`, `GRANT pg_read_all_data TO cloudos_backup`},
}

// Restore puts a complete run (the newest when run is 0) back onto a new
// box: its files into the data root, which must be empty, then its database
// into the box's database, which must have no table yet. It connects as the
// database's owner, not as DBRole. It never writes over anything: a folder
// or a database already in use is refused. `core verify` then checks every
// file against the restored index.
func (s *Service) Restore(ctx context.Context, run int64, out io.Writer) error {
	if err := s.checkRepo(ctx, s.Repo); err != nil {
		return err
	}
	root := s.Files.Root
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("the data folder %s: %w", root, err)
	}
	for _, e := range entries {
		if e.Name() != "lost+found" {
			return fmt.Errorf("the data folder %s is not empty (%s): a restore only goes onto a new box", root, e.Name())
		}
	}
	var tables int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_tables WHERE schemaname = 'public'`).Scan(&tables); err != nil {
		return err
	}
	if tables > 0 {
		return errors.New("the database already has tables: a restore only goes into a new database")
	}
	pair, err := s.complete(ctx, s.Repo, run)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "restoring run %d of %s (database %s, files %s)\n",
		pair.Run, pair.Files.Time.Local().Format("2006-01-02 15:04"), pair.DB.ID[:8], pair.Files.ID[:8])

	// The files first, the longer part: the database stays empty if they fail.
	if _, err := s.restic(ctx, s.Repo, "restore", pair.Files.ID+":"+pair.FilesRoot(), "--target", root); err != nil {
		return fmt.Errorf("restore the files: %w", err)
	}
	fmt.Fprintln(out, "files restored into", root)

	for _, r := range roles {
		var exists bool
		if err := s.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, r.name).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			if _, err := s.Pool.Exec(ctx, r.create); err != nil {
				return fmt.Errorf("create the role %s: %w", r.name, err)
			}
		}
		if r.after != "" {
			if _, err := s.Pool.Exec(ctx, r.after); err != nil {
				return fmt.Errorf("role %s: %w", r.name, err)
			}
		}
	}
	// The objects go to the role restoring them, the database's owner.
	if err := s.restoreDump(ctx, pair, s.DatabaseURL, "--no-owner"); err != nil {
		return err
	}
	var files int64
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM files WHERE kind = 'file'`).Scan(&files); err != nil {
		return err
	}
	fmt.Fprintf(out, "database restored: %d indexed files\n", files)
	return nil
}
