// Command core is the Cloud OS core.
//
//	core                     migrate the database and serve the API (same as `core serve`)
//	core import OWNER DIR    copy DIR into OWNER's space; DIR is only read
//	core verify              check every stored file against the index; exit 1 on any difference
//	core reset-password NAME set a new password read from stdin and end the account's sessions
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"

	"github.com/C2-POLY/cloud-os/core/internal/auth"
	"github.com/C2-POLY/cloud-os/core/internal/db"
	"github.com/C2-POLY/cloud-os/core/internal/httpapi"
	"github.com/C2-POLY/cloud-os/core/internal/migrate"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
)

// version is set at build time from the VERSION file (-ldflags "-X main.version=…").
var version = "dev"

// errDiffers makes `core verify` exit 1 without logging it as a crash.
var errDiffers = errors.New("the index and the disk differ")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err := run(ctx, os.Args[1:], os.Stdout)
	switch {
	case errors.Is(err, errDiffers):
		os.Exit(1)
	case err != nil:
		slog.Error("core stopped", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	cmd := "serve"
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "serve":
		return withCore(ctx, func(c core) error { return serve(ctx, c) })
	case "import":
		if len(args) != 2 {
			return errors.New("usage: core import OWNER DIR")
		}
		return withCore(ctx, func(c core) error { return importDir(ctx, c, args[0], args[1], out) })
	case "verify":
		return withCore(ctx, func(c core) error { return verify(ctx, c, out) })
	case "reset-password":
		if len(args) != 1 {
			return errors.New("usage: core reset-password NAME (the new password is read from stdin)")
		}
		return withCore(ctx, func(c core) error { return resetPassword(ctx, c, args[0], os.Stdin, out) })
	default:
		return fmt.Errorf("unknown command %q (serve, import, verify)", cmd)
	}
}

type core struct {
	pool     *pgxpool.Pool
	provider *goose.Provider
	files    *storage.Store
}

// withCore connects to the database, applies the migrations and opens the file store.
func withCore(ctx context.Context, fn func(core) error) error {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return errors.New("DATABASE_URL is not set")
	}
	pool, err := db.Open(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()

	provider, err := migrate.NewProvider(db.SQL(pool))
	if err != nil {
		return err
	}
	results, err := provider.Up(ctx)
	if err != nil {
		return err
	}
	for _, r := range results {
		slog.Info("migration applied", "version", r.Source.Version, "file", r.Source.Path, "duration", r.Duration)
	}
	files := &storage.Store{
		Root: envOr("CLOUDOS_DATA_DIR", "/data"), Pool: pool, TrashTTL: trashTTL(),
		VersionsKeep: envInt("CLOUDOS_VERSIONS_KEEP"), VersionsMaxAge: time.Duration(envInt("CLOUDOS_VERSIONS_DAYS")) * 24 * time.Hour,
	}
	return fn(core{pool: pool, provider: provider, files: files})
}

func serve(ctx context.Context, c core) error {
	// Only the server cleans up: an import running beside it may be writing there.
	n, err := c.files.CleanTmp()
	if err != nil {
		return fmt.Errorf("clean temporary files: %w", err)
	}
	if n > 0 {
		slog.Warn("removed temporary files left by interrupted writes", "count", n)
	}
	if n, err := c.files.CleanUploads(ctx); err != nil {
		return fmt.Errorf("clean uploads: %w", err)
	} else if n > 0 {
		slog.Info("removed expired or orphan uploads", "count", n)
	}
	go cleanUploadsEvery(ctx, c.files, time.Hour)
	if n, err := c.files.PurgeExpired(ctx); err != nil {
		return fmt.Errorf("purge the trash: %w", err)
	} else if n > 0 {
		slog.Info("purged expired trash items", "count", n)
	}
	if n, err := c.files.PruneVersions(ctx); err != nil {
		return fmt.Errorf("prune versions: %w", err)
	} else if n > 0 {
		slog.Info("pruned old versions", "count", n)
	}
	finished, dropped, err := c.files.RecoverMoves(ctx)
	if err != nil {
		return fmt.Errorf("recover moves: %w", err)
	}
	if finished+dropped > 0 {
		slog.Warn("settled moves left by a crash", "finished", finished, "dropped", dropped)
	}

	addr := envOr("CORE_ADDR", ":8080")
	srv := &http.Server{
		Addr: addr,
		Handler: httpapi.NewHandler(httpapi.Config{
			Version:   version,
			Health:    &db.Store{Pool: c.pool, Migrations: c.provider},
			Auth:      auth.NewService(c.pool),
			Files:     c.files,
			MaxUpload: maxUpload(),
			StaticDir: os.Getenv("STATIC_DIR"),
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		slog.Info("core listening", "addr", addr, "version", version, "data", c.files.Root)
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	}
}

func importDir(ctx context.Context, c core, owner, dir string, out io.Writer) error {
	rep, err := c.files.Import(ctx, owner, dir)
	fmt.Fprintf(out, "imported %d, already on disk and now indexed %d, already present %d, skipped %d\n",
		len(rep.Imported), len(rep.Adopted), len(rep.Present), len(rep.Skipped))
	for _, p := range rep.Skipped {
		fmt.Fprintf(out, "skipped  %s: %s\n", p.Path, p.Note)
	}
	return err
}

func verify(ctx context.Context, c core, out io.Writer) error {
	rep, err := c.files.Verify(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "ok %d, problems %d\n", rep.OK, len(rep.Problems))
	for _, p := range rep.Problems {
		fmt.Fprintf(out, "%-9s %s/%s", p.Kind, p.Owner, p.Path)
		if p.Note != "" {
			fmt.Fprintf(out, ": %s", p.Note)
		}
		fmt.Fprintln(out)
	}
	if !rep.Clean() {
		return errDiffers
	}
	return nil
}

func resetPassword(ctx context.Context, c core, name string, in io.Reader, out io.Writer) error {
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if err := auth.NewService(c.pool).ResetPassword(ctx, name, strings.TrimRight(line, "\r\n")); err != nil {
		return err
	}
	fmt.Fprintf(out, "password of %s changed, its sessions ended\n", name)
	return nil
}

// cleanUploadsEvery ends abandoned uploads and purges expired trash items until ctx is done.
func cleanUploadsEvery(ctx context.Context, files *storage.Store, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := files.CleanUploads(ctx); err != nil {
				slog.Error("clean uploads", "err", err)
			} else if n > 0 {
				slog.Info("removed expired or orphan uploads", "count", n)
			}
			if n, err := files.PurgeExpired(ctx); err != nil {
				slog.Error("purge the trash", "err", err)
			} else if n > 0 {
				slog.Info("purged expired trash items", "count", n)
			}
			if n, err := files.PruneVersions(ctx); err != nil {
				slog.Error("prune versions", "err", err)
			} else if n > 0 {
				slog.Info("pruned old versions", "count", n)
			}
		}
	}
}

// trashTTL reads CLOUDOS_TRASH_DAYS; unset or invalid means storage.DefaultTrashTTL.
func trashTTL() time.Duration {
	days, err := strconv.Atoi(os.Getenv("CLOUDOS_TRASH_DAYS"))
	if err != nil || days <= 0 {
		return storage.DefaultTrashTTL
	}
	return time.Duration(days) * 24 * time.Hour
}

// envInt reads a positive integer variable; unset or invalid means 0 (the default applies).
func envInt(key string) int {
	n, err := strconv.Atoi(os.Getenv(key))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// maxUpload reads CLOUDOS_MAX_UPLOAD (bytes); 0 or unset means no limit but the free space.
func maxUpload() int64 {
	n, err := strconv.ParseInt(os.Getenv("CLOUDOS_MAX_UPLOAD"), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
