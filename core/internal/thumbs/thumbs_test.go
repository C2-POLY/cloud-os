package thumbs_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/tasks"
	"github.com/C2-POLY/cloud-os/core/internal/testdb"
	"github.com/C2-POLY/cloud-os/core/internal/thumbs"
)

// Enough of each format for content sniffing; the workers read the real thing.
const (
	jpeg = "\xff\xd8\xff\xe0\x00\x10JFIF\x00 a photo"
	png  = "\x89PNG\r\n\x1a\n a scan"
	heic = "\x00\x00\x00\x18ftypheic\x00\x00\x00\x00mif1heic an iPhone photo"
	heif = "\x00\x00\x00\x18ftypmif1\x00\x00\x00\x00mif1heic a HEIF photo"
	mp4  = "\x00\x00\x00\x18ftypisom\x00\x00\x02\x00isomiso2 a video"
	mov  = "\x00\x00\x00\x14ftypqt  \x00\x00\x02\x00qt   an iPhone video"
)

type env struct {
	ctx   context.Context
	pool  *pgxpool.Pool
	queue *tasks.Queue
	store *storage.Store
}

// newEnv returns a store whose writes queue thumbnails, as the core's.
func newEnv(t *testing.T) env {
	t.Helper()
	pool, _ := testdb.Migrated(t)
	e := env{ctx: context.Background(), pool: pool, queue: &tasks.Queue{Pool: pool}, store: &storage.Store{Root: t.TempDir(), Pool: pool}}
	e.store.AfterStore = func(ctx context.Context, f storage.File, head []byte) {
		if _, err := thumbs.Enqueue(ctx, e.queue, f, head, tasks.PriorityNew); err != nil {
			t.Error(err)
		}
	}
	return e
}

func (e env) put(t *testing.T, path, content string) storage.File {
	t.Helper()
	f, err := e.store.Put(e.ctx, "enzo", path, strings.NewReader(content), storage.NoExpectations())
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (e env) queued(t *testing.T) []string {
	t.Helper()
	rows, err := e.pool.Query(e.ctx, `SELECT payload->>'sha256' || '/' || priority FROM tasks WHERE kind = 'thumb' ORDER BY id`)
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

func hexsum(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func (e env) addThumb(t *testing.T, content string) {
	t.Helper()
	sum := sha256.Sum256([]byte(content))
	if _, err := e.pool.Exec(e.ctx, `INSERT INTO thumbnails (sha256, size, mime, width, height, bytes) VALUES ($1, $2, 'image/webp', 2, 1, 'webp')`, sum[:], thumbs.Size); err != nil {
		t.Fatal(err)
	}
}

func TestStoringAnImageQueuesItsThumbnailOnce(t *testing.T) {
	e := newEnv(t)
	e.put(t, "a.jpg", jpeg)
	e.put(t, "notes.txt", "hello")
	e.put(t, "scan", png)           // no extension: recognised by its content
	e.put(t, "IMG_0001.HEIC", heic) // iPhone photo, by its extension
	e.put(t, "photo", heif)         // HEIF without extension, by its brand
	e.put(t, "copy of a.jpg", jpeg) // same content: same thumbnail
	want := []string{hexsum(jpeg) + "/10", hexsum(png) + "/10", hexsum(heic) + "/10", hexsum(heif) + "/10"}
	if got := e.queued(t); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("queued %v, want %v", got, want)
	}

	// A content that already has its thumbnail queues nothing.
	e.addThumb(t, jpeg+"!")
	e.put(t, "b.jpg", jpeg+"!")
	if got := e.queued(t); len(got) != 4 {
		t.Errorf("queued %v", got)
	}
}

func TestStoringAVideoQueuesItsThumbnail(t *testing.T) {
	e := newEnv(t)
	e.put(t, "IMG_0002.MOV", mov)   // iPhone video, by its extension
	e.put(t, "clip.mp4", mp4)       // MP4, by its extension
	e.put(t, "from-phone", mov+"!") // QuickTime without extension, by its brand
	e.put(t, "export", mp4+"!")     // MP4 without extension, by its brand
	want := []string{hexsum(mov) + "/10", hexsum(mp4) + "/10", hexsum(mov+"!") + "/10", hexsum(mp4+"!") + "/10"}
	if got := e.queued(t); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("queued %v, want %v", got, want)
	}
}

func TestBackfillQueuesLiveImagesWithoutThumbnail(t *testing.T) {
	e := newEnv(t)
	e.store.AfterStore = nil // files stored before thumbnails existed
	e.put(t, "a.jpg", jpeg)
	e.put(t, "b.png", png)
	e.put(t, "iPhone/IMG_0001.HEIC", heic)
	e.put(t, "iPhone/IMG_0002.MOV", mov)
	e.put(t, "done.jpg", jpeg+"done")
	e.put(t, "gone.jpg", jpeg+"gone")
	e.put(t, "notes.txt", "hello")
	e.addThumb(t, jpeg+"done")
	if _, err := e.store.Trash(e.ctx, "enzo", "gone.jpg"); err != nil {
		t.Fatal(err)
	}
	n, err := thumbs.Backfill(e.ctx, e.queue)
	if err != nil || n != 4 {
		t.Fatalf("backfill = %d, %v", n, err)
	}
	for _, q := range e.queued(t) {
		if !strings.HasSuffix(q, "/0") {
			t.Errorf("%s is not at backfill priority", q)
		}
	}
	if n, _ := thumbs.Backfill(e.ctx, e.queue); n != 0 {
		t.Errorf("second backfill queued %d", n)
	}
}

func TestPruneKeepsThumbnailsOfStoredContents(t *testing.T) {
	e := newEnv(t)
	e.put(t, "kept.jpg", jpeg)
	e.put(t, "trashed.jpg", jpeg+"t")
	e.put(t, "deleted.jpg", jpeg+"d")
	for _, c := range []string{jpeg, jpeg + "t", jpeg + "d", jpeg + "never stored"} {
		e.addThumb(t, c)
	}
	if _, err := e.store.Trash(e.ctx, "enzo", "trashed.jpg"); err != nil {
		t.Fatal(err)
	}
	// What a purge leaves: no row for the content any more.
	if _, err := e.pool.Exec(e.ctx, `DELETE FROM files WHERE path = 'deleted.jpg'`); err != nil {
		t.Fatal(err)
	}
	if n, err := thumbs.Prune(e.ctx, e.pool); err != nil || n != 2 {
		t.Fatalf("prune = %d, %v", n, err)
	}
	for c, want := range map[string]bool{jpeg: true, jpeg + "t": true, jpeg + "d": false} {
		sum := sha256.Sum256([]byte(c))
		_, err := thumbs.Get(e.ctx, e.pool, sum[:])
		if got := err == nil; got != want {
			t.Errorf("%q kept = %v (%v), want %v", c, got, err, want)
		}
	}
	sum := sha256.Sum256([]byte("nothing"))
	if _, err := thumbs.Get(e.ctx, e.pool, sum[:]); !errors.Is(err, thumbs.ErrNotFound) {
		t.Errorf("missing thumbnail: %v", err)
	}
}
