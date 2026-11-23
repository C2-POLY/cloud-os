package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	neturl "net/url"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	// CLOUDOS_BACKUP_AT is a time of the box's zone (TZ), whatever the image holds.
	_ "time/tzdata"

	"github.com/C2-POLY/cloud-os/core/internal/backup"
	"github.com/C2-POLY/cloud-os/core/internal/db"
	"github.com/C2-POLY/cloud-os/core/internal/migrate"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
)

const backupUsage = `usage: core backup daemon|now|status|check|restore-test|restore [RUN]|init|remote-init`

// backupPoolMaxConns keeps the backup service, with pg_dump's connection,
// under its role's limit of 8 connections (migration 0028).
const backupPoolMaxConns = 3

// runBackup is `core backup …`, the backup service (docs/architecture/backup.md).
// It applies no migration: the core does.
func runBackup(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(backupUsage)
	}
	cmd, args := args[0], args[1:]
	s := &backup.Service{
		Repo:     os.Getenv("RESTIC_REPOSITORY"),
		Password: os.Getenv("RESTIC_PASSWORD"),
		Remote:   os.Getenv("CLOUDOS_BACKUP_REMOTE"),
		Dir:      envOr("CLOUDOS_BACKUP_DIR", "/backup"),
	}
	switch cmd {
	case "init":
		return s.Init(ctx)
	case "remote-init":
		return s.InitRemote(ctx)
	case "daemon", "now", "status", "check", "restore-test", "restore":
	default:
		return errors.New(backupUsage)
	}

	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return errors.New("DATABASE_URL is not set")
	}
	// pg_dump and pg_restore get the address on their command line, which any
	// process of the host can read: the password goes through PGPASSWORD.
	if u, err := neturl.Parse(url); err != nil {
		return errors.New("DATABASE_URL is not a URL")
	} else if _, set := u.User.Password(); set {
		return errors.New("DATABASE_URL carries a password: pass it in PGPASSWORD instead")
	}
	pool, err := db.Open(ctx, poolURL(url, backupPoolMaxConns))
	if err != nil {
		return err
	}
	defer pool.Close()
	s.Pool = pool
	s.DatabaseURL = url
	s.Files = &storage.Store{Root: envOr("CLOUDOS_DATA_DIR", "/data"), Pool: pool}

	if cmd == "restore" {
		run := int64(0)
		if len(args) > 0 {
			if run, err = strconv.ParseInt(args[0], 10, 64); err != nil || run <= 0 {
				return errors.New("usage: core backup restore [RUN] (a run id from core backup status)")
			}
		}
		return s.Restore(ctx, run, out)
	}

	// Every other command runs as the backup's own role, on a migrated database.
	if err := backup.CheckRole(ctx, pool); err != nil {
		return err
	}
	provider, err := migrate.NewProvider(db.SQL(pool))
	if err != nil {
		return err
	}
	if pending, err := provider.HasPending(ctx); err != nil {
		return err
	} else if pending {
		return errors.New("the database is not migrated yet: the core applies the migrations, start it first")
	}

	switch cmd {
	case "daemon":
		hour, minute, err := backup.ParseAt(envOr("CLOUDOS_BACKUP_AT", "03:00"))
		if err != nil {
			return err
		}
		s.LowPriority = true
		slog.Info("backup service started", "at", fmt.Sprintf("%02d:%02d", hour, minute), "zone", time.Local.String(),
			"repository", s.Repo, "remote", s.Remote != "", "version", version)
		return s.Daemon(ctx, hour, minute)
	case "now":
		id, err := s.Backup(ctx)
		if err != nil {
			return fmt.Errorf("backup run %d failed: %w", id, err)
		}
		fmt.Fprintf(out, "backup run %d done\n", id)
		return nil
	case "check":
		return s.Check(ctx, time.Now())
	case "restore-test":
		return s.RestoreTest(ctx)
	default: // status
		return backupStatus(ctx, s, out)
	}
}

// backupStatus prints the last runs of backup_runs, newest first.
func backupStatus(ctx context.Context, s *backup.Service, out io.Writer) error {
	runs, err := backup.Runs(ctx, s.Pool, 20)
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		fmt.Fprintln(out, "no backup has run yet")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "RUN\tKIND\tSTARTED\tTOOK\tRESULT\tDETAIL")
	for _, r := range runs {
		took, result := "", "running or cut off"
		if r.FinishedAt != nil {
			took = r.FinishedAt.Sub(r.StartedAt).Round(time.Second).String()
		}
		if r.OK != nil && *r.OK {
			result = "ok"
		} else if r.OK != nil {
			result = "FAILED"
		}
		detail, _ := json.Marshal(r.Detail)
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.Kind, r.StartedAt.Local().Format("2006-01-02 15:04"), took, result, detail)
		if r.Error != nil {
			fmt.Fprintf(w, "\t\t\t\terror:\t%s\n", strings.ReplaceAll(*r.Error, "\n", " "))
		}
	}
	return w.Flush()
}
