package meta_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/C2-POLY/cloud-os/core/internal/meta"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/tasks"
	"github.com/C2-POLY/cloud-os/core/internal/testdb"
)

// Enough of each format for content sniffing; the workers read the real thing.
const (
	jpeg = "\xff\xd8\xff\xe0\x00\x10JFIF\x00 a photo"
	png  = "\x89PNG\r\n\x1a\n a scan"
)

type env struct {
	ctx   context.Context
	pool  *pgxpool.Pool
	queue *tasks.Queue
	store *storage.Store
}

// newEnv returns a store whose writes queue metadata, as the core's.
func newEnv(t *testing.T) env {
	t.Helper()
	pool, _ := testdb.Migrated(t)
	e := env{ctx: context.Background(), pool: pool, queue: &tasks.Queue{Pool: pool}, store: &storage.Store{Root: t.TempDir(), Pool: pool}}
	e.store.AfterStore = func(ctx context.Context, f storage.File, head []byte) {
		if _, err := meta.Enqueue(ctx, e.queue, f, head, tasks.PriorityNew); err != nil {
			t.Error(err)
		}
	}
	return e
}

func (e env) put(t *testing.T, path, content string) {
	t.Helper()
	if _, err := e.store.Put(e.ctx, "enzo", path, strings.NewReader(content), storage.NoExpectations()); err != nil {
		t.Fatal(err)
	}
}

func (e env) queued(t *testing.T) []string {
	t.Helper()
	rows, err := e.pool.Query(e.ctx, `SELECT payload->>'sha256' || '/' || priority FROM tasks WHERE kind = 'meta' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func sum(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

// addMeta writes what a worker writes for a photo without EXIF.
func (e env) addMeta(t *testing.T, content string) {
	t.Helper()
	if _, err := e.pool.Exec(e.ctx, `INSERT INTO media_meta (sha256, width, height) VALUES ($1, 2, 1)`, sum(content)); err != nil {
		t.Fatal(err)
	}
}

func TestStoringAnImageQueuesItsMetadataOnce(t *testing.T) {
	e := newEnv(t)
	e.put(t, "a.jpg", jpeg)
	e.put(t, "notes.txt", "hello")
	e.put(t, "scan", png)           // no extension: recognised by its content
	e.put(t, "copy of a.jpg", jpeg) // same content: same metadata
	want := []string{hex.EncodeToString(sum(jpeg)) + "/10", hex.EncodeToString(sum(png)) + "/10"}
	if got := e.queued(t); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("queued %v, want %v", got, want)
	}

	// A content that already has its metadata queues nothing.
	e.addMeta(t, jpeg+"!")
	e.put(t, "b.jpg", jpeg+"!")
	if got := e.queued(t); len(got) != 2 {
		t.Errorf("queued %v", got)
	}
}

func TestBackfillQueuesLiveImagesWithoutMetadata(t *testing.T) {
	e := newEnv(t)
	e.store.AfterStore = nil // files stored before metadata was read
	e.put(t, "a.jpg", jpeg)
	e.put(t, "b.png", png)
	e.put(t, "done.jpg", jpeg+"done")
	e.put(t, "gone.jpg", jpeg+"gone")
	e.put(t, "notes.txt", "hello")
	e.addMeta(t, jpeg+"done")
	if _, err := e.store.Trash(e.ctx, "enzo", "gone.jpg"); err != nil {
		t.Fatal(err)
	}
	n, err := meta.Backfill(e.ctx, e.queue)
	if err != nil || n != 2 {
		t.Fatalf("backfill = %d, %v", n, err)
	}
	for _, q := range e.queued(t) {
		if !strings.HasSuffix(q, "/0") {
			t.Errorf("%s is not at backfill priority", q)
		}
	}
	if n, _ := meta.Backfill(e.ctx, e.queue); n != 0 {
		t.Errorf("second backfill queued %d", n)
	}
}

func TestPruneKeepsMetadataOfStoredContents(t *testing.T) {
	e := newEnv(t)
	e.put(t, "kept.jpg", jpeg)
	e.put(t, "trashed.jpg", jpeg+"t")
	e.put(t, "deleted.jpg", jpeg+"d")
	for _, c := range []string{jpeg, jpeg + "t", jpeg + "d", jpeg + "never stored"} {
		e.addMeta(t, c)
	}
	if _, err := e.store.Trash(e.ctx, "enzo", "trashed.jpg"); err != nil {
		t.Fatal(err)
	}
	// What a purge leaves: no row for the content any more.
	if _, err := e.pool.Exec(e.ctx, `DELETE FROM files WHERE path = 'deleted.jpg'`); err != nil {
		t.Fatal(err)
	}
	if n, err := meta.Prune(e.ctx, e.pool); err != nil || n != 2 {
		t.Fatalf("prune = %d, %v", n, err)
	}
	for c, want := range map[string]bool{jpeg: true, jpeg + "t": true, jpeg + "d": false} {
		_, err := meta.Get(e.ctx, e.pool, sum(c))
		if got := err == nil; got != want {
			t.Errorf("%q kept = %v (%v), want %v", c, got, err, want)
		}
	}
}

func TestGetReturnsWhatTheWorkerWrote(t *testing.T) {
	e := newEnv(t)
	if _, err := meta.Get(e.ctx, e.pool, sum("nothing")); !errors.Is(err, meta.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if _, err := e.pool.Exec(e.ctx,
		`INSERT INTO media_meta (sha256, taken_at, taken_offset, latitude, longitude, make, model, width, height, orientation)
		 VALUES ($1, '2024-07-14 18:03:22', 120, 48.8584, 2.2945, 'Apple', 'iPhone 15', 3024, 4032, 6)`, sum(jpeg)); err != nil {
		t.Fatal(err)
	}
	m, err := meta.Get(e.ctx, e.pool, sum(jpeg))
	if err != nil {
		t.Fatal(err)
	}
	if m.TakenAt == nil || !m.TakenAt.Equal(time.Date(2024, 7, 14, 18, 3, 22, 0, time.UTC)) || *m.TakenOffset != 120 {
		t.Errorf("taken %v %v", m.TakenAt, m.TakenOffset)
	}
	if *m.Latitude != 48.8584 || *m.Longitude != 2.2945 || *m.Make != "Apple" || *m.Model != "iPhone 15" || m.Lens != nil {
		t.Errorf("place and camera %+v", m)
	}
	if *m.Width != 3024 || *m.Height != 4032 || *m.Orientation != 6 {
		t.Errorf("dimensions %+v", m)
	}
}
