// Command core is the Cloud OS core.
//
//	core                     migrate the database and serve the API (same as `core serve`)
//	core import OWNER DIR    copy DIR into OWNER's space; DIR is only read
//	core verify              check every stored file against the index; exit 1 on any difference
//	core reset-password NAME set a new password read from stdin and end the account's sessions
//	core selftest [N]        queue N selftest tasks and wait for the workers to run them
//	core photovec backfill   queue the vector of every stored photo and video that has none
//	core textemb backfill    queue the vectors of every stored document with text that has none
//	core doccat backfill     queue the category of every stored document whose vectors have none of the workers' version
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

	"github.com/C2-POLY/cloud-os/core/internal/activity"
	"github.com/C2-POLY/cloud-os/core/internal/albums"
	"github.com/C2-POLY/cloud-os/core/internal/auth"
	"github.com/C2-POLY/cloud-os/core/internal/db"
	"github.com/C2-POLY/cloud-os/core/internal/doccat"
	"github.com/C2-POLY/cloud-os/core/internal/doctext"
	"github.com/C2-POLY/cloud-os/core/internal/embed"
	"github.com/C2-POLY/cloud-os/core/internal/httpapi"
	"github.com/C2-POLY/cloud-os/core/internal/meta"
	"github.com/C2-POLY/cloud-os/core/internal/migrate"
	"github.com/C2-POLY/cloud-os/core/internal/ocr"
	"github.com/C2-POLY/cloud-os/core/internal/photovec"
	"github.com/C2-POLY/cloud-os/core/internal/share"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/tasks"
	"github.com/C2-POLY/cloud-os/core/internal/textemb"
	"github.com/C2-POLY/cloud-os/core/internal/thumbs"
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
	case "selftest":
		n := 10
		if len(args) > 0 {
			if v, err := strconv.Atoi(args[0]); err == nil && v > 0 {
				n = v
			}
		}
		return withCore(ctx, func(c core) error { return selftest(ctx, c, n, out) })
	case "thumbs":
		if len(args) != 1 || args[0] != "backfill" {
			return errors.New("usage: core thumbs backfill")
		}
		return withCore(ctx, func(c core) error {
			n, err := thumbs.Backfill(ctx, &tasks.Queue{Pool: c.pool})
			fmt.Fprintf(out, "%d thumbnails queued\n", n)
			return err
		})
	case "meta":
		if len(args) != 1 || args[0] != "backfill" {
			return errors.New("usage: core meta backfill")
		}
		return withCore(ctx, func(c core) error {
			n, err := meta.Backfill(ctx, &tasks.Queue{Pool: c.pool})
			fmt.Fprintf(out, "%d metadata tasks queued\n", n)
			return err
		})
	case "text":
		if len(args) != 1 || args[0] != "backfill" {
			return errors.New("usage: core text backfill")
		}
		return withCore(ctx, func(c core) error {
			n, err := doctext.Backfill(ctx, &tasks.Queue{Pool: c.pool})
			fmt.Fprintf(out, "%d text extractions queued\n", n)
			return err
		})
	case "ocr":
		if len(args) != 1 || args[0] != "backfill" {
			return errors.New("usage: core ocr backfill")
		}
		return withCore(ctx, func(c core) error {
			n, err := ocr.Backfill(ctx, &tasks.Queue{Pool: c.pool})
			fmt.Fprintf(out, "%d OCR readings queued\n", n)
			return err
		})
	case "photovec":
		if len(args) != 1 || args[0] != "backfill" {
			return errors.New("usage: core photovec backfill")
		}
		return withCore(ctx, func(c core) error {
			n, err := photovec.Backfill(ctx, &tasks.Queue{Pool: c.pool})
			fmt.Fprintf(out, "%d photo vectors queued\n", n)
			return err
		})
	case "textemb":
		if len(args) != 1 || args[0] != "backfill" {
			return errors.New("usage: core textemb backfill")
		}
		return withCore(ctx, func(c core) error {
			n, err := textemb.Backfill(ctx, &tasks.Queue{Pool: c.pool})
			fmt.Fprintf(out, "%d document vectors queued\n", n)
			return err
		})
	case "doccat":
		if len(args) != 1 || args[0] != "backfill" {
			return errors.New("usage: core doccat backfill")
		}
		return withCore(ctx, func(c core) error {
			n, err := doccat.Backfill(ctx, &tasks.Queue{Pool: c.pool})
			fmt.Fprintf(out, "%d document classifications queued\n", n)
			return err
		})
	case "reset-password":
		if len(args) != 1 {
			return errors.New("usage: core reset-password NAME (the new password is read from stdin)")
		}
		return withCore(ctx, func(c core) error { return resetPassword(ctx, c, args[0], os.Stdin, out) })
	default:
		return fmt.Errorf("unknown command %q (serve, import, verify, selftest, thumbs, meta, text, ocr, photovec, textemb, doccat, reset-password)", cmd)
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
	files.AfterStore = queueBackgroundWork(&tasks.Queue{Pool: pool}, tasks.PriorityNew)
	journal := &activity.Log{Pool: pool}
	files.Notify = journal.Record
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
	shares := share.NewService(c.pool, c.files)
	if n, err := shares.Clean(ctx); err != nil {
		return fmt.Errorf("clean share links: %w", err)
	} else if n > 0 {
		slog.Info("deleted expired share links", "count", n)
	}
	go cleanUploadsEvery(ctx, c.files, shares, time.Hour)
	go reapTasksEvery(ctx, &tasks.Queue{Pool: c.pool}, 30*time.Second)
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
	// The workers' query endpoint, for the searches by meaning.
	workers := &embed.Client{URL: os.Getenv("WORKER_URL")}
	srv := &http.Server{
		Addr: addr,
		Handler: httpapi.NewHandler(httpapi.Config{
			Version:    version,
			Health:     &db.Store{Pool: c.pool, Migrations: c.provider},
			Auth:       auth.NewService(c.pool),
			Files:      c.files,
			Shares:     shares,
			MapsDir:    os.Getenv("MAPS_DIR"),
			Albums:     &albums.Service{Pool: c.pool, Files: c.files},
			Photos:     &photovec.Searcher{Pool: c.pool, Embed: workers},
			Documents:  &textemb.Searcher{Pool: c.pool, Embed: workers},
			Categories: &doccat.Store{Pool: c.pool},
			Activity:   &activity.Log{Pool: c.pool},
			MaxUpload:  maxUpload(),
			StaticDir:  os.Getenv("STATIC_DIR"),
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
	// A whole library at once is catch-up work: it must not delay new files.
	c.files.AfterStore = queueBackgroundWork(&tasks.Queue{Pool: c.pool}, tasks.PriorityBackfill)
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

// queueBackgroundWork is the store's AfterStore hook: it queues the thumbnail,
// the metadata and the vector of a new image or video content, the text of a
// new document, and the OCR of a new image (the workers queue the vectors of
// a document once they have its text). Best effort: a task lost here is
// caught up by `core thumbs backfill`, `core meta backfill`, `core text
// backfill`, `core ocr backfill` and `core photovec backfill`.
func queueBackgroundWork(q *tasks.Queue, priority int) func(context.Context, storage.File, []byte) {
	return func(ctx context.Context, f storage.File, head []byte) {
		ctx = context.WithoutCancel(ctx)
		if _, err := thumbs.Enqueue(ctx, q, f, head, priority); err != nil {
			slog.Error("queue thumbnail", "owner", f.Owner, "path", f.Path, "err", err)
		}
		if _, err := meta.Enqueue(ctx, q, f, head, priority); err != nil {
			slog.Error("queue metadata", "owner", f.Owner, "path", f.Path, "err", err)
		}
		if _, err := doctext.Enqueue(ctx, q, f, priority); err != nil {
			slog.Error("queue text", "owner", f.Owner, "path", f.Path, "err", err)
		}
		if _, err := ocr.Enqueue(ctx, q, f, head, priority); err != nil {
			slog.Error("queue OCR", "owner", f.Owner, "path", f.Path, "err", err)
		}
		if _, err := photovec.Enqueue(ctx, q, f, head, priority); err != nil {
			slog.Error("queue photo vector", "owner", f.Owner, "path", f.Path, "err", err)
		}
	}
}

// reapTasksEvery gives back the tasks of dead workers, prunes old done tasks
// and what no file has any more, and queues every photo and document again
// when the workers declare a new model version, or a new version of the
// classifier of documents.
func reapTasksEvery(ctx context.Context, q *tasks.Queue, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := q.Reap(ctx); err != nil {
				slog.Error("reap tasks", "err", err)
			} else if n > 0 {
				slog.Warn("tasks of a stopped worker queued again", "count", n)
			}
			if _, err := q.Prune(ctx); err != nil {
				slog.Error("prune tasks", "err", err)
			}
			if _, err := thumbs.Prune(ctx, q.Pool); err != nil {
				slog.Error("prune thumbnails", "err", err)
			}
			if _, err := meta.Prune(ctx, q.Pool); err != nil {
				slog.Error("prune metadata", "err", err)
			}
			if _, err := doctext.Prune(ctx, q.Pool); err != nil {
				slog.Error("prune document text", "err", err)
			}
			if _, err := photovec.Prune(ctx, q.Pool); err != nil {
				slog.Error("prune photo vectors", "err", err)
			}
			if n, err := photovec.Sync(ctx, q); err != nil {
				slog.Error("queue photo vectors for the model", "err", err)
			} else if n > 0 {
				slog.Info("photo vectors queued for the workers' model", "model", photovec.Model, "count", n)
			}
			if n, err := textemb.Sync(ctx, q); err != nil {
				slog.Error("queue document vectors for the model", "err", err)
			} else if n > 0 {
				slog.Info("document vectors queued for the workers' model", "model", textemb.Model, "count", n)
			}
			if n, err := doccat.Sync(ctx, q); err != nil {
				slog.Error("queue document categories for the classifier", "err", err)
			} else if n > 0 {
				slog.Info("document categories queued for the workers' classifier", "count", n)
			}
			if _, err := (&activity.Log{Pool: q.Pool}).Prune(ctx); err != nil {
				slog.Error("prune activity", "err", err)
			}
		}
	}
}

// selftest queues n selftest tasks and waits until the workers have run them:
// the end-to-end proof that the core and the workers share the queue.
func selftest(ctx context.Context, c core, n int, out io.Writer) error {
	q := &tasks.Queue{Pool: c.pool}
	ids := make([]int64, 0, n)
	for i := range n {
		id, _, err := q.Enqueue(ctx, "selftest", map[string]any{"n": i}, tasks.EnqueueOptions{Priority: tasks.PriorityNew})
		if err != nil {
			return err
		}
		ids = append(ids, id)
	}
	fmt.Fprintf(out, "queued %d selftest tasks, waiting for the workers…\n", n)
	deadline := time.Now().Add(2 * time.Minute)
	for {
		done, failed := 0, 0
		for _, id := range ids {
			t, err := q.Get(ctx, id)
			if err != nil {
				return err
			}
			switch t.State {
			case "done":
				done++
			case "failed":
				failed++
			}
		}
		if done+failed == n {
			fmt.Fprintf(out, "done %d, failed %d\n", done, failed)
			if failed > 0 {
				return errors.New("some selftest tasks failed")
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out: %d of %d done", done, n)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// cleanUploadsEvery ends abandoned uploads, purges expired trash items, prunes
// old versions and deletes long-expired share links until ctx is done.
func cleanUploadsEvery(ctx context.Context, files *storage.Store, shares *share.Service, every time.Duration) {
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
			if n, err := shares.Clean(ctx); err != nil {
				slog.Error("clean share links", "err", err)
			} else if n > 0 {
				slog.Info("deleted expired share links", "count", n)
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
