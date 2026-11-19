package search_test

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/C2-POLY/cloud-os/core/internal/embed"
	"github.com/C2-POLY/cloud-os/core/internal/photovec"
	"github.com/C2-POLY/cloud-os/core/internal/search"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/testdb"
	"github.com/C2-POLY/cloud-os/core/internal/textemb"
)

// axisWorker answers POST /embed with the unit vector of the first axis, for
// any query and model, after delay. It keeps the models it was asked for.
type axisWorker struct {
	mu     sync.Mutex
	delay  time.Duration
	models []string
}

func (w *axisWorker) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	var req struct {
		Model string `json:"model"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	w.mu.Lock()
	w.models = append(w.models, req.Model)
	delay := w.delay
	w.mu.Unlock()
	select {
	case <-time.After(delay):
	case <-r.Context().Done():
		return
	}
	v := make([]float32, 768)
	v[0] = 1
	_ = json.NewEncoder(rw).Encode(map[string]any{"model": req.Model, "version": "v1", "vectors": [][]float32{v}})
}

func (w *axisWorker) asked() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.models)
}

type env struct {
	ctx    context.Context
	pool   *pgxpool.Pool
	store  *storage.Store
	worker *axisWorker
	s      *search.Searcher
}

func newEnv(t *testing.T) env {
	t.Helper()
	pool, _ := testdb.Migrated(t)
	w := &axisWorker{}
	srv := httptest.NewServer(w)
	t.Cleanup(srv.Close)
	workers := &embed.Client{URL: srv.URL}
	store := &storage.Store{Root: t.TempDir(), Pool: pool}
	return env{
		ctx: context.Background(), pool: pool, store: store, worker: w,
		s: &search.Searcher{Files: store, Docs: &textemb.Searcher{Pool: pool, Embed: workers}, Photos: &photovec.Searcher{Pool: pool, Embed: workers}},
	}
}

// at is a unit vector whose cosine with the first axis is cosine.
func at(cosine float64) string {
	v := make([]float32, 768)
	v[0], v[1] = float32(cosine), float32(math.Sqrt(1-cosine*cosine))
	return embed.Vector{Values: v}.Literal()
}

// doc stores a document of owner, its text, and one window at cosine.
func (e env) doc(t *testing.T, owner, path, text string, cosine float64) storage.File {
	t.Helper()
	f, err := e.store.Put(e.ctx, owner, path, strings.NewReader(text), storage.NoExpectations())
	if err != nil {
		t.Fatal(err)
	}
	exec(t, e.pool, `INSERT INTO document_text (sha256, source, text, chars, truncated, has_text) VALUES ($1, 'text', $2, $3, false, true)
		ON CONFLICT DO NOTHING`, f.SHA256, text, len(text))
	exec(t, e.pool, `INSERT INTO `+textemb.Table+` (sha256, chunk, start_char, end_char, model, version, embedding)
		VALUES ($1, 0, 0, $2, $3, 'v1', $4::halfvec) ON CONFLICT DO NOTHING`, f.SHA256, len(text), textemb.Model, at(cosine))
	return f
}

// photo stores a JPEG of owner whose vector is at cosine.
func (e env) photo(t *testing.T, owner, path string, cosine float64) storage.File {
	t.Helper()
	jpeg := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, []byte(path)...) // a JPEG's magic, a content of its own
	f, err := e.store.Put(e.ctx, owner, path, strings.NewReader(string(jpeg)), storage.NoExpectations())
	if err != nil {
		t.Fatal(err)
	}
	exec(t, e.pool, `INSERT INTO `+photoTable+` (sha256, model, version, source, embedding) VALUES ($1, $2, 'v1', 'image', $3::vector)`,
		f.SHA256, photovec.Model, at(cosine))
	return f
}

// mkdir makes a folder of enzo.
func (e env) mkdir(t *testing.T, path string) {
	t.Helper()
	if _, err := e.store.Mkdir(e.ctx, "enzo", path); err != nil {
		t.Fatal(err)
	}
}

func names(r search.Result) []string {
	out := make([]string, len(r.Hits))
	for i, h := range r.Hits {
		out[i] = h.File.Path
	}
	return out
}

func TestAnEmptyQueryAsksNoOne(t *testing.T) {
	e := newEnv(t)
	e.doc(t, "enzo", "a.txt", "contrat", 0.5)
	for _, q := range []string{"", "  \t "} {
		r, err := e.s.Search(e.ctx, "enzo", search.Query{Q: q})
		if err != nil || r.Partial || r.Hits == nil || len(r.Hits) != 0 {
			t.Errorf("%q: %+v, %v", q, r, err)
		}
	}
	if got := e.worker.asked(); len(got) != 0 {
		t.Errorf("the workers were asked %v", got)
	}
	if _, err := e.s.Search(e.ctx, "../enzo", search.Query{Q: "contrat"}); err == nil {
		t.Error("a bad owner searched")
	}
}

func TestTheFourSearchesMakeOneList(t *testing.T) {
	e := newEnv(t)
	e.doc(t, "enzo", "Documents/bail.txt", "Contrat de location de l'appartement", 0.45)
	e.doc(t, "enzo", "Documents/facture.txt", "Facture d'électricité", 0.1)
	e.photo(t, "enzo", "Photos/chien.jpg", 0.1)
	e.photo(t, "enzo", "Photos/loin.jpg", 0.061)
	e.mkdir(t, "Contrats")
	r, err := e.s.Search(e.ctx, "enzo", search.Query{Q: "contrat"})
	if err != nil || r.Partial {
		t.Fatal(r, err)
	}
	// The lease by its words and its meaning (score 1), the folder by its
	// name (0.9), the photo by what it shows (0.41: 0.037 over the mean of
	// the two photos started from 10 at MinScore); the far photo and the
	// unrelated invoice are not listed.
	want := []string{"Documents/bail.txt", "Contrats", "Photos/chien.jpg"}
	if got := names(r); !slices.Equal(got, want) {
		t.Errorf("list %v, want %v", got, want)
	}
	if h := r.Hits[0]; !slices.Equal(h.FoundBy, []string{search.ByText, search.ByMeaning}) || len(h.Snippet) == 0 {
		t.Errorf("lease %+v", h)
	}
	if got := e.worker.asked(); !slices.Contains(got, textemb.Model) || !slices.Contains(got, photovec.Model) {
		t.Errorf("asked %v", got)
	}
	if r, err := e.s.Search(e.ctx, "enzo", search.Query{Q: "contrat", Limit: 1}); err != nil || len(r.Hits) != 1 {
		t.Errorf("limit 1: %v, %v", names(r), err)
	}
}

func TestOnlyTheAccountsLiveFiles(t *testing.T) {
	e := newEnv(t)
	e.doc(t, "enzo", "bail.txt", "Contrat de location", 0.45)
	e.doc(t, "johan", "bail-johan.txt", "Contrat de location", 0.45)
	e.photo(t, "johan", "chien.jpg", 0.13)
	e.doc(t, "enzo", "vieux-bail.txt", "Ancien contrat", 0.45)
	if _, err := e.store.Trash(e.ctx, "enzo", "vieux-bail.txt"); err != nil {
		t.Fatal(err)
	}
	r, err := e.s.Search(e.ctx, "enzo", search.Query{Q: "contrat"})
	if err != nil || !slices.Equal(names(r), []string{"bail.txt"}) {
		t.Errorf("enzo: %v, %v", names(r), err)
	}
}

func TestASlowOrMissingWorkerLeavesNamesAndWords(t *testing.T) {
	e := newEnv(t)
	e.doc(t, "enzo", "bail.txt", "Contrat de location", 0.45)
	e.photo(t, "enzo", "chien.jpg", 0.13)
	e.worker.delay = time.Second
	e.s.Budget = 100 * time.Millisecond
	start := time.Now()
	r, err := e.s.Search(e.ctx, "enzo", search.Query{Q: "contrat"})
	if took := time.Since(start); took > 700*time.Millisecond {
		t.Errorf("took %v with a budget of 100 ms", took)
	}
	if err != nil || !r.Partial || !slices.Equal(names(r), []string{"bail.txt"}) || !slices.Equal(r.Hits[0].FoundBy, []string{search.ByText}) {
		t.Errorf("slow worker: %+v, %v", r, err)
	}

	none := &search.Searcher{Files: e.store}
	r, err = none.Search(e.ctx, "enzo", search.Query{Q: "contrat"})
	if err != nil || !r.Partial || !slices.Equal(names(r), []string{"bail.txt"}) {
		t.Errorf("no worker: %+v, %v", r, err)
	}
	// Without photos to look at, a missing photo search is not a miss.
	docsOnly := &search.Searcher{Files: e.store, Docs: e.s.Docs}
	e.worker.delay = 0
	r, err = docsOnly.Search(e.ctx, "enzo", search.Query{Q: "contrat", Filter: storage.Filter{Type: storage.TypeDocs}})
	if err != nil || r.Partial {
		t.Errorf("documents only: %+v, %v", r, err)
	}
}

func TestFilters(t *testing.T) {
	e := newEnv(t)
	bail := e.doc(t, "enzo", "Documents/bail contrat.txt", "Contrat de location", 0.45)
	chien := e.photo(t, "enzo", "Photos/chien contrat.jpg", 0.13)
	plage := e.photo(t, "enzo", "Photos/plage contrat.jpg", 0.12)
	e.mkdir(t, "Contrats")
	// The lease was modified on 2025-12-31 at 23:30 UTC, 2026-01-01 in Paris; the
	// dog was taken on 2024-07-14 by the camera's clock, the beach has no date
	// taken and was modified in 2026.
	exec(t, e.pool, `UPDATE files SET modified_at = '2025-12-31 23:30:00+00' WHERE id = $1`, bail.ID)
	exec(t, e.pool, `UPDATE files SET modified_at = '2026-03-01 12:00:00+00' WHERE id IN ($1, $2)`, chien.ID, plage.ID)
	exec(t, e.pool, `INSERT INTO media_meta (sha256, taken_at) VALUES ($1, '2024-07-14 18:03:22')`, chien.SHA256)

	day := func(s string) time.Time { d, _ := time.Parse(time.DateOnly, s); return d }
	for _, c := range []struct {
		name   string
		filter storage.Filter
		want   []string
	}{
		{"documents", storage.Filter{Type: storage.TypeDocs}, []string{"Documents/bail contrat.txt"}},
		{"photos", storage.Filter{Type: storage.TypePhotos}, []string{"Photos/chien contrat.jpg", "Photos/plage contrat.jpg"}},
		{"2025 in UTC", storage.Filter{From: day("2025-01-01"), To: day("2025-12-31")}, []string{"Documents/bail contrat.txt"}},
		{"2026 in Paris", storage.Filter{From: day("2026-01-01"), Zone: "Europe/Paris"}, []string{"Documents/bail contrat.txt", "Photos/plage contrat.jpg"}},
		{"taken in July 2024", storage.Filter{From: day("2024-07-14"), To: day("2024-07-14")}, []string{"Photos/chien contrat.jpg"}},
		{"photos before 2025", storage.Filter{Type: storage.TypePhotos, To: day("2024-12-31")}, []string{"Photos/chien contrat.jpg"}},
	} {
		r, err := e.s.Search(e.ctx, "enzo", search.Query{Q: "contrat", Filter: c.filter})
		got := names(r)
		slices.Sort(got)
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, %v; want %v", c.name, got, err, c.want)
		}
	}
	// A search for documents does not ask for the vector of photos.
	e.worker.models = nil
	if _, err := e.s.Search(e.ctx, "enzo", search.Query{Q: "contrat", Filter: storage.Filter{Type: storage.TypeDocs}}); err != nil {
		t.Fatal(err)
	}
	if got := e.worker.asked(); !slices.Equal(got, []string{textemb.Model}) {
		t.Errorf("documents only asked %v", got)
	}
}
