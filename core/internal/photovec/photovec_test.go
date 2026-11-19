package photovec_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/C2-POLY/cloud-os/core/internal/embed"
	"github.com/C2-POLY/cloud-os/core/internal/photovec"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/tasks"
	"github.com/C2-POLY/cloud-os/core/internal/testdb"
)

// Enough of each format for content sniffing; the workers read the real thing.
const (
	jpeg = "\xff\xd8\xff\xe0\x00\x10JFIF\x00 a photo"
	png  = "\x89PNG\r\n\x1a\n a screenshot"
	heic = "\x00\x00\x00\x18ftypheic\x00\x00\x00\x00mif1heic an iPhone photo"
	mp4  = "\x00\x00\x00\x18ftypisom\x00\x00\x02\x00isomiso2 a video"
	mov  = "\x00\x00\x00\x14ftypqt  \x00\x00\x02\x00qt   an iPhone video"
)

type env struct {
	ctx   context.Context
	pool  *pgxpool.Pool
	queue *tasks.Queue
	store *storage.Store
}

// newEnv returns a store whose writes queue photo vectors, as the core's.
func newEnv(t *testing.T) env {
	t.Helper()
	pool, _ := testdb.Migrated(t)
	e := env{ctx: context.Background(), pool: pool, queue: &tasks.Queue{Pool: pool}, store: &storage.Store{Root: t.TempDir(), Pool: pool}}
	e.store.AfterStore = func(ctx context.Context, f storage.File, head []byte) {
		if _, err := photovec.Enqueue(ctx, e.queue, f, head, tasks.PriorityNew); err != nil {
			t.Error(err)
		}
	}
	return e
}

func (e env) put(t *testing.T, owner, path, content string) storage.File {
	t.Helper()
	f, err := e.store.Put(e.ctx, owner, path, strings.NewReader(content), storage.NoExpectations())
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (e env) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.pool.Exec(e.ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
}

// queued lists the pending photovec tasks as "<sha256>/<priority>".
func (e env) queued(t *testing.T) []string {
	t.Helper()
	rows, err := e.pool.Query(e.ctx, `SELECT payload->>'sha256' || '/' || priority FROM tasks WHERE kind = 'photovec' AND state = 'queued' ORDER BY id`)
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

// declare records the model version, as a starting worker does.
func (e env) declare(t *testing.T, version string) {
	t.Helper()
	e.exec(t, `INSERT INTO embedding_models (name, version, dims) VALUES ($1, $2, 768)
		ON CONFLICT (name) DO UPDATE SET version = EXCLUDED.version, requeued_at = NULL`, photovec.Model, version)
}

// vec is a 768-dimension vector with the given leading values.
func vec(values ...float32) []float32 {
	v := make([]float32, 768)
	copy(v, values)
	return v
}

// addVector stores the vector of a content, as the worker writes it.
func (e env) addVector(t *testing.T, content, version string, v []float32) {
	t.Helper()
	sum := sha256.Sum256([]byte(content))
	e.exec(t, `INSERT INTO photo_vectors_siglip2_b16_224 (sha256, model, version, source, embedding)
		VALUES ($1, $2, $3, 'image', $4::vector)
		ON CONFLICT (sha256) DO UPDATE SET version = EXCLUDED.version, embedding = EXCLUDED.embedding`,
		sum[:], photovec.Model, version, embed.Vector{Values: v}.Literal())
}

func TestStoringAPhotoOrAVideoQueuesItsVectorOnce(t *testing.T) {
	e := newEnv(t)
	e.put(t, "enzo", "a.jpg", jpeg)
	e.put(t, "enzo", "notes.txt", "hello")
	e.put(t, "enzo", "screenshot", png)      // no extension: recognised by its content
	e.put(t, "enzo", "IMG_0001.HEIC", heic)  // iPhone photo, by its extension
	e.put(t, "enzo", "IMG_0002.MOV", mov)    // iPhone video
	e.put(t, "enzo", "clip.mp4", mp4)        // MP4
	e.put(t, "enzo", "copy of a.jpg", jpeg)  // same content: same vector
	e.put(t, "johan", "from enzo.jpg", jpeg) // another account, same content
	e.put(t, "enzo", "Documents/x.pdf", "%PDF-1.7 not a photo")
	want := []string{hexsum(jpeg) + "/10", hexsum(png) + "/10", hexsum(heic) + "/10", hexsum(mov) + "/10", hexsum(mp4) + "/10"}
	if got := e.queued(t); !slices.Equal(got, want) {
		t.Errorf("queued %v, want %v", got, want)
	}
}

func TestAVectorOfTheDeclaredVersionQueuesNothing(t *testing.T) {
	e := newEnv(t)
	e.declare(t, "v2")
	e.addVector(t, jpeg+"current", "v2", vec(1))
	e.addVector(t, jpeg+"old", "v1", vec(1))
	e.put(t, "enzo", "current.jpg", jpeg+"current")
	e.put(t, "enzo", "old.jpg", jpeg+"old") // an older version is encoded again
	if got := e.queued(t); !slices.Equal(got, []string{hexsum(jpeg+"old") + "/10"}) {
		t.Errorf("queued %v", got)
	}
}

func TestBackfillQueuesLivePhotosWithoutAVectorOfTheVersion(t *testing.T) {
	e := newEnv(t)
	e.store.AfterStore = nil // files stored before the workers made vectors
	e.declare(t, "v2")
	e.put(t, "enzo", "none.jpg", jpeg)
	e.put(t, "enzo", "old.jpg", jpeg+"old")
	e.put(t, "enzo", "current.jpg", jpeg+"current")
	e.put(t, "enzo", "iPhone/IMG_0002.MOV", mov)
	e.put(t, "enzo", "gone.jpg", jpeg+"gone")
	e.put(t, "enzo", "notes.txt", "hello")
	e.addVector(t, jpeg+"old", "v1", vec(1))
	e.addVector(t, jpeg+"current", "v2", vec(1))
	if _, err := e.store.Trash(e.ctx, "enzo", "gone.jpg"); err != nil {
		t.Fatal(err)
	}
	n, err := photovec.Backfill(e.ctx, e.queue)
	if err != nil || n != 3 {
		t.Fatalf("backfill = %d, %v", n, err)
	}
	got := e.queued(t)
	slices.Sort(got)
	want := []string{hexsum(jpeg) + "/0", hexsum(jpeg+"old") + "/0", hexsum(mov) + "/0"}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("queued %v, want %v", got, want)
	}
	if n, _ := photovec.Backfill(e.ctx, e.queue); n != 0 {
		t.Errorf("second backfill queued %d", n)
	}
}

func TestSyncQueuesEveryPhotoOncePerDeclaredVersion(t *testing.T) {
	e := newEnv(t)
	e.store.AfterStore = nil // photos stored before this core
	e.put(t, "enzo", "a.jpg", jpeg)
	e.put(t, "johan", "b.heic", heic)
	sync := func(want int) {
		t.Helper()
		if n, err := photovec.Sync(e.ctx, e.queue); err != nil || n != want {
			t.Errorf("sync = %d, %v, want %d", n, err, want)
		}
	}

	sync(0) // no worker has declared the model yet
	if got := e.queued(t); len(got) != 0 {
		t.Errorf("queued %v before any worker", got)
	}
	e.declare(t, "v1") // the first worker starts
	sync(2)
	sync(0) // once per version: a restart queues nothing more
	for _, q := range e.queued(t) {
		if !strings.HasSuffix(q, "/0") {
			t.Errorf("%s is not at backfill priority", q)
		}
	}

	// The workers encode both, then a new model version arrives.
	e.exec(t, `UPDATE tasks SET state = 'done' WHERE kind = 'photovec'`)
	e.addVector(t, jpeg, "v1", vec(1))
	e.addVector(t, heic, "v1", vec(1))
	sync(0)
	e.declare(t, "v2")
	sync(2)
	sync(0)
}

func TestPruneKeepsVectorsOfStoredContents(t *testing.T) {
	e := newEnv(t)
	e.put(t, "enzo", "kept.jpg", jpeg)
	e.put(t, "enzo", "trashed.jpg", jpeg+"t")
	e.put(t, "enzo", "deleted.jpg", jpeg+"d")
	for _, c := range []string{jpeg, jpeg + "t", jpeg + "d", jpeg + "never stored"} {
		e.addVector(t, c, "v1", vec(1))
	}
	if _, err := e.store.Trash(e.ctx, "enzo", "trashed.jpg"); err != nil {
		t.Fatal(err)
	}
	// What a purge leaves: no row for the content any more.
	e.exec(t, `DELETE FROM files WHERE path = 'deleted.jpg'`)
	if n, err := photovec.Prune(e.ctx, e.pool); err != nil || n != 2 {
		t.Fatalf("prune = %d, %v", n, err)
	}
	for c, want := range map[string]bool{jpeg: true, jpeg + "t": true, jpeg + "d": false} {
		sum := sha256.Sum256([]byte(c))
		var kept bool
		if err := e.pool.QueryRow(e.ctx, `SELECT EXISTS (SELECT 1 FROM photo_vectors_siglip2_b16_224 WHERE sha256 = $1)`, sum[:]).Scan(&kept); err != nil {
			t.Fatal(err)
		}
		if kept != want {
			t.Errorf("%q kept = %v, want %v", c, kept, want)
		}
	}
}

// fakeWorker answers POST /embed with the vector of the query, as the
// worker's endpoint does, and counts the queries.
type fakeWorker struct {
	*httptest.Server
	queries atomic.Int32
	last    atomic.Value
}

func newFakeWorker(t *testing.T, version string, v []float32) *fakeWorker {
	t.Helper()
	w := &fakeWorker{}
	w.Server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string   `json:"model"`
			Texts []string `json:"texts"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Model != photovec.Model || len(req.Texts) != 1 {
			http.Error(rw, "bad request", http.StatusBadRequest)
			return
		}
		w.queries.Add(1)
		w.last.Store(req.Texts[0])
		_ = json.NewEncoder(rw).Encode(map[string]any{"model": req.Model, "version": version, "vectors": [][]float32{v}})
	}))
	t.Cleanup(w.Close)
	return w
}

func paths(hits []photovec.Hit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.Owner + "/" + h.Path
	}
	return out
}

func TestSearchFindsTheAccountsLivePhotosClosestFirst(t *testing.T) {
	e := newEnv(t)
	e.store.AfterStore = nil
	worker := newFakeWorker(t, "v2", vec(1))
	s := &photovec.Searcher{Pool: e.pool, Embed: &embed.Client{URL: worker.URL}}

	e.put(t, "enzo", "Plage/chien.jpg", jpeg+"dog on the beach")
	e.put(t, "enzo", "Plage/copie.jpg", jpeg+"dog on the beach") // same content, both found
	e.put(t, "enzo", "Plage/sable.mp4", mp4+"sand")
	e.put(t, "enzo", "Maison/chat.jpg", jpeg+"cat")
	e.put(t, "enzo", "Corbeille/chien.jpg", jpeg+"trashed dog")
	e.put(t, "enzo", "Ancien/chien.jpg", jpeg+"old version")
	e.put(t, "johan", "Vacances/chien.jpg", jpeg+"johan's dog")
	e.put(t, "johan", "Vacances/sable.mp4", mp4+"sand") // johan has the same video
	e.addVector(t, jpeg+"dog on the beach", "v2", vec(1))
	e.addVector(t, mp4+"sand", "v2", vec(0.8, 0.6))
	e.addVector(t, jpeg+"cat", "v2", vec(0, 1))                // unrelated: under MinScore
	e.addVector(t, jpeg+"trashed dog", "v2", vec(1))           // in the trash: never found
	e.addVector(t, jpeg+"old version", "v1", vec(1))           // another model version: never compared
	e.addVector(t, jpeg+"johan's dog", "v2", vec(0.99, 0.141)) // johan's: never in enzo's results
	if _, err := e.store.Trash(e.ctx, "enzo", "Corbeille/chien.jpg"); err != nil {
		t.Fatal(err)
	}

	hits, err := s.Search(e.ctx, "enzo", "  chien   à la plage ", 0, storage.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := paths(hits), []string{"enzo/Plage/chien.jpg", "enzo/Plage/copie.jpg", "enzo/Plage/sable.mp4"}; !slices.Equal(got, want) {
		t.Errorf("enzo found %v, want %v", got, want)
	}
	if len(hits) == 3 && (math.Abs(hits[0].Score-1) > 1e-6 || math.Abs(hits[2].Score-0.8) > 1e-6) {
		t.Errorf("scores %v %v %v", hits[0].Score, hits[1].Score, hits[2].Score)
	}
	// The lift is over the mean of enzo's live contents of the version, the
	// cat under MinScore included, each content once, started from 10 photos
	// at MinScore: (1 + 0.8 + 0 + 10 × 0.06) / 13.
	mean := (1 + 0.8 + 0 + 10*photovec.MinScore) / 13
	if len(hits) == 3 && (math.Abs(hits[0].Lift-(1-mean)) > 1e-6 || math.Abs(hits[1].Lift-(1-mean)) > 1e-6 || math.Abs(hits[2].Lift-(0.8-mean)) > 1e-6) {
		t.Errorf("lifts %v %v %v, want %v %v %v", hits[0].Lift, hits[1].Lift, hits[2].Lift, 1-mean, 1-mean, 0.8-mean)
	}
	if len(hits) > 0 && (hits[0].MIME != "image/jpeg" || hex.EncodeToString(hits[0].SHA256) != hexsum(jpeg+"dog on the beach") || hits[0].Kind != "file") {
		t.Errorf("hit = %+v", hits[0].File)
	}
	if q, _ := worker.last.Load().(string); q != "chien à la plage" {
		t.Errorf("worker asked %q", q)
	}

	hits, err = s.Search(e.ctx, "johan", "chien à la plage", 0, storage.Filter{})
	if got, want := paths(hits), []string{"johan/Vacances/chien.jpg", "johan/Vacances/sable.mp4"}; err != nil || !slices.Equal(got, want) {
		t.Errorf("johan found %v (%v), want %v", got, err, want)
	}
	// johan's mean is his own: his dog and the video, never enzo's photos.
	if len(hits) == 2 {
		mean := (hits[0].Score + hits[1].Score + 10*photovec.MinScore) / 12
		if math.Abs(hits[0].Lift-(hits[0].Score-mean)) > 1e-6 || math.Abs(hits[1].Lift-(hits[1].Score-mean)) > 1e-6 {
			t.Errorf("johan's lifts %v %v, want over his mean %v", hits[0].Lift, hits[1].Lift, mean)
		}
	}

	if hits, err := s.Search(e.ctx, "enzo", "chien", 1, storage.Filter{}); err != nil || len(hits) != 1 {
		t.Errorf("limit 1: %v %v", paths(hits), err)
	}
}

func TestSearchOfNothingAsksNoWorker(t *testing.T) {
	e := newEnv(t)
	worker := newFakeWorker(t, "v1", vec(1))
	s := &photovec.Searcher{Pool: e.pool, Embed: &embed.Client{URL: worker.URL}}
	hits, err := s.Search(e.ctx, "enzo", "   ", 0, storage.Filter{})
	if err != nil || hits == nil || len(hits) != 0 || worker.queries.Load() != 0 {
		t.Errorf("empty search = %v, %v, %d queries", hits, err, worker.queries.Load())
	}
	// A long query is cut, whole characters, before the worker reads it.
	if _, err := s.Search(e.ctx, "enzo", strings.Repeat("é", 500), 0, storage.Filter{}); err != nil {
		t.Fatal(err)
	}
	if q, _ := worker.last.Load().(string); utf8.RuneCountInString(q) != 200 || !utf8.ValidString(q) {
		t.Errorf("worker asked %d characters", utf8.RuneCountInString(q))
	}
	if _, err := s.Search(e.ctx, "../enzo", "plage", 0, storage.Filter{}); !errors.Is(err, storage.ErrInvalidPath) {
		t.Errorf("bad owner: %v, want ErrInvalidPath", err)
	}
}

func TestSearchIsUnavailableWithoutAWorker(t *testing.T) {
	e := newEnv(t)
	for _, c := range []*embed.Client{nil, {}, {URL: "http://127.0.0.1:1"}} {
		s := &photovec.Searcher{Pool: e.pool, Embed: c}
		if _, err := s.Search(e.ctx, "enzo", "plage", 0, storage.Filter{}); !errors.Is(err, embed.ErrUnavailable) {
			t.Errorf("client %+v: %v, want ErrUnavailable", c, err)
		}
	}
}
