package textemb_test

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
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/C2-POLY/cloud-os/core/internal/doctext"
	"github.com/C2-POLY/cloud-os/core/internal/embed"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/tasks"
	"github.com/C2-POLY/cloud-os/core/internal/testdb"
	"github.com/C2-POLY/cloud-os/core/internal/textemb"
)

type env struct {
	ctx   context.Context
	pool  *pgxpool.Pool
	store *storage.Store
}

func newEnv(t *testing.T) env {
	t.Helper()
	pool, _ := testdb.Migrated(t)
	return env{ctx: context.Background(), pool: pool, store: &storage.Store{Root: t.TempDir(), Pool: pool}}
}

func sum(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

// put stores a file whose content is content.
func (e env) put(t *testing.T, owner, path, content string) {
	t.Helper()
	if _, err := e.store.Put(e.ctx, owner, path, strings.NewReader(content), storage.NoExpectations()); err != nil {
		t.Fatal(err)
	}
}

// addText writes what the text task writes for content: text, with words or not.
func (e env) addText(t *testing.T, content, text string, hasText bool) {
	t.Helper()
	if _, err := e.pool.Exec(e.ctx,
		`INSERT INTO document_text (sha256, source, text, chars, truncated, has_text) VALUES ($1, 'text', $2, $3, false, $4)`,
		sum(content), text, len([]rune(text)), hasText); err != nil {
		t.Fatal(err)
	}
}

// axis is the unit vector along dimensions dims, weighted by weights: what a
// model would give, made by hand.
func axis(weights map[int]float64) []float32 {
	v := make([]float32, textemb.Dims)
	var norm float64
	for _, w := range weights {
		norm += w * w
	}
	for d, w := range weights {
		v[d] = float32(w / math.Sqrt(norm))
	}
	return v
}

// addWindow writes what the textemb task writes for one window of content.
func (e env) addWindow(t *testing.T, content string, chunk, start, end int, v []float32) {
	t.Helper()
	if _, err := e.pool.Exec(e.ctx,
		`INSERT INTO `+textemb.Table+` (sha256, chunk, start_char, end_char, model, version, embedding)
		 VALUES ($1, $2, $3, $4, $5, 'v1', $6::halfvec)`,
		sum(content), chunk, start, end, textemb.Model, embed.Vector{Values: v}.Literal()); err != nil {
		t.Fatal(err)
	}
}

// fakeWorker answers POST /embed like the worker's query endpoint: the
// vector and version it is set to, for any query; it keeps the queries.
type fakeWorker struct {
	mu      sync.Mutex
	vector  []float32
	version string
	queries []string
}

func (w *fakeWorker) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	var req struct {
		Model string   `json:"model"`
		Texts []string `json:"texts"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.queries = append(w.queries, req.Texts...)
	_ = json.NewEncoder(rw).Encode(map[string]any{"model": req.Model, "version": w.version, "vectors": [][]float32{w.vector}})
}

// searcher is a Searcher whose queries all come back as v, version "v1".
func (e env) searcher(t *testing.T, v []float32) (*textemb.Searcher, *fakeWorker) {
	t.Helper()
	w := &fakeWorker{vector: v, version: "v1"}
	srv := httptest.NewServer(w)
	t.Cleanup(srv.Close)
	return &textemb.Searcher{Pool: e.pool, Embed: &embed.Client{URL: srv.URL}}, w
}

func paths(hits []textemb.Hit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.File.Owner + ":" + h.File.Path
	}
	return out
}

const (
	lease  = "Contrat de location. Appartement de trois pièces à Nantes. Dépôt de garantie : 850 €."
	salary = "Bulletin de paie de mars"
	car    = "Certificat d'immatriculation"
)

// corpus stores three documents for enzo: the lease, whose second window
// is the nearest to the query along axis 0; the payslip; the car papers.
func (e env) corpus(t *testing.T) {
	t.Helper()
	e.put(t, "enzo", "Logement/bail.pdf", lease)
	e.put(t, "enzo", "Travail/paie.pdf", salary)
	e.put(t, "enzo", "Voiture/carte-grise.pdf", car)
	e.addText(t, lease, lease, true)
	e.addText(t, salary, salary, true)
	e.addText(t, car, car, true)
	e.addWindow(t, lease, 0, 0, 40, axis(map[int]float64{0: 1, 1: 1}))
	e.addWindow(t, lease, 1, 21, len([]rune(lease)), axis(map[int]float64{0: 3, 1: 1}))
	e.addWindow(t, salary, 0, 0, len(salary), axis(map[int]float64{0: 1, 2: 2}))
	e.addWindow(t, car, 0, 0, len(car), axis(map[int]float64{3: 1}))
}

func TestSearchRanksTheAccountsDocumentsByTheirBestWindow(t *testing.T) {
	e := newEnv(t)
	e.corpus(t)
	s, w := e.searcher(t, axis(map[int]float64{0: 1}))
	hits, err := s.Search(e.ctx, "enzo", "  contrat   de l'appart ", 0, storage.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(w.queries, []string{"contrat de l'appart"}) {
		t.Errorf("the workers were asked %q", w.queries)
	}
	want := []string{"enzo:Logement/bail.pdf", "enzo:Travail/paie.pdf", "enzo:Voiture/carte-grise.pdf"}
	if got := paths(hits); !slices.Equal(got, want) {
		t.Fatalf("hits %v, want %v", got, want)
	}
	best := hits[0]
	// cos(axis 0, (3, 1)/√10) = 3/√10.
	if math.Abs(best.Score-3/math.Sqrt(10)) > 1e-3 || best.Start != 21 || best.End != len([]rune(lease)) {
		t.Errorf("best = %.4f [%d, %d)", best.Score, best.Start, best.End)
	}
	if best.Excerpt != "Appartement de trois pièces à Nantes. Dépôt de garantie : 850 €." {
		t.Errorf("excerpt %q", best.Excerpt)
	}
	if math.Abs(hits[2].Score) > 1e-3 {
		t.Errorf("unrelated score %.4f, want 0", hits[2].Score)
	}

	// limit, and its bounds.
	if hits, err := s.Search(e.ctx, "enzo", "contrat", 1, storage.Filter{}); err != nil || len(hits) != 1 {
		t.Errorf("limit 1: %v, %v", paths(hits), err)
	}
	if hits, err := s.Search(e.ctx, "enzo", "contrat", 1000, storage.Filter{}); err != nil || len(hits) != 3 {
		t.Errorf("limit 1000: %v, %v", paths(hits), err)
	}

	// An empty query asks nothing; a long one is cut to what the workers read.
	w.queries = nil
	if hits, err := s.Search(e.ctx, "enzo", " \t ", 0, storage.Filter{}); err != nil || hits == nil || len(hits) != 0 {
		t.Errorf("empty query: %v, %v", hits, err)
	}
	if _, err := s.Search(e.ctx, "enzo", strings.Repeat("é", textemb.MaxQueryChars+5), 0, storage.Filter{}); err != nil {
		t.Fatal(err)
	}
	if len(w.queries) != 1 || len([]rune(w.queries[0])) != textemb.MaxQueryChars {
		t.Errorf("asked %d queries, the last of %d characters", len(w.queries), len([]rune(w.queries[len(w.queries)-1])))
	}
}

func TestSearchComparesOnlyVectorsOfTheQuerysVersion(t *testing.T) {
	e := newEnv(t)
	e.corpus(t) // version v1
	// The workers now serve v2: the lease is the only document already
	// encoded again, the others wait for their task.
	if _, err := e.pool.Exec(e.ctx, `UPDATE `+textemb.Table+` SET version = 'v2' WHERE sha256 = $1`, sum(lease)); err != nil {
		t.Fatal(err)
	}
	s, w := e.searcher(t, axis(map[int]float64{0: 1}))
	w.version = "v2"
	hits, err := s.Search(e.ctx, "enzo", "contrat", 0, storage.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := paths(hits), []string{"enzo:Logement/bail.pdf"}; !slices.Equal(got, want) {
		t.Errorf("hits %v, want %v", got, want)
	}
}

func TestSearchIsUnavailableWithoutTheWorkersVector(t *testing.T) {
	e := newEnv(t)
	e.corpus(t)
	short, _ := e.searcher(t, []float32{1, 0})
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	for name, s := range map[string]*textemb.Searcher{
		"another dimension": short,
		"no worker":         {Pool: e.pool, Embed: &embed.Client{URL: down.URL}},
		"no worker URL":     {Pool: e.pool, Embed: &embed.Client{}},
	} {
		if _, err := s.Search(e.ctx, "enzo", "contrat", 0, storage.Filter{}); !errors.Is(err, embed.ErrUnavailable) {
			t.Errorf("%s: %v, want ErrUnavailable", name, err)
		}
	}
}

func TestSearchShowsOnlyTheAccountsLiveFiles(t *testing.T) {
	e := newEnv(t)
	e.corpus(t)
	// Léa has her own copy of the lease (same content, same vectors), and a
	// document of her own; Enzo puts his payslip in the trash.
	e.put(t, "lea", "Mes papiers/bail.pdf", lease)
	e.put(t, "lea", "impots.pdf", "Avis d'impôt")
	e.addText(t, "Avis d'impôt", "Avis d'impôt", true)
	e.addWindow(t, "Avis d'impôt", 0, 0, 12, axis(map[int]float64{0: 1}))
	if _, err := e.store.Trash(e.ctx, "enzo", "Travail/paie.pdf"); err != nil {
		t.Fatal(err)
	}

	s, _ := e.searcher(t, axis(map[int]float64{0: 1}))
	enzo, err := s.Search(e.ctx, "enzo", "contrat", 0, storage.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := paths(enzo), []string{"enzo:Logement/bail.pdf", "enzo:Voiture/carte-grise.pdf"}; !slices.Equal(got, want) {
		t.Errorf("enzo sees %v, want %v", got, want)
	}
	lea, err := s.Search(e.ctx, "lea", "contrat", 0, storage.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := paths(lea), []string{"lea:impots.pdf", "lea:Mes papiers/bail.pdf"}; !slices.Equal(got, want) {
		t.Errorf("léa sees %v, want %v", got, want)
	}
	if hits, err := s.Search(e.ctx, "nobody", "contrat", 0, storage.Filter{}); err != nil || len(hits) != 0 {
		t.Errorf("an account without files: %v, %v", paths(hits), err)
	}
	if _, err := s.Search(e.ctx, "../enzo", "contrat", 0, storage.Filter{}); err == nil {
		t.Error("a bad owner name was accepted")
	}
}

func TestBackfillQueuesLiveDocumentsWithTextAndNoVectors(t *testing.T) {
	e := newEnv(t)
	e.corpus(t) // three documents with vectors
	e.put(t, "enzo", "nouveau.txt", "pas encore vectorisé")
	e.addText(t, "pas encore vectorisé", "pas encore vectorisé", true)
	e.put(t, "enzo", "scan.pdf", "scan")
	e.addText(t, "scan", "", false) // waits for its OCR
	e.put(t, "enzo", "jete.txt", "jeté")
	e.addText(t, "jeté", "jeté", true)
	if _, err := e.store.Trash(e.ctx, "enzo", "jete.txt"); err != nil {
		t.Fatal(err)
	}
	e.addText(t, "orphelin", "plus aucun fichier", true)
	if _, err := e.pool.Exec(e.ctx, `INSERT INTO embedding_models (name, version, dims) VALUES ($1, 'v1', 768)`, textemb.Model); err != nil {
		t.Fatal(err)
	}

	q := &tasks.Queue{Pool: e.pool}
	if n, err := textemb.Backfill(e.ctx, q); err != nil || n != 1 {
		t.Fatalf("backfill = %d, %v; want 1", n, err)
	}
	var payload string
	var priority int
	var key string
	if err := e.pool.QueryRow(e.ctx, `SELECT payload->>'sha256', priority, dedupe_key FROM tasks WHERE kind = $1`, textemb.Kind).Scan(&payload, &priority, &key); err != nil {
		t.Fatal(err)
	}
	want := hex.EncodeToString(sum("pas encore vectorisé"))
	if payload != want || priority != tasks.PriorityBackfill || key != "textemb:"+want {
		t.Errorf("queued %s at %d (%s)", payload, priority, key)
	}
	// Already pending: nothing more.
	if n, err := textemb.Backfill(e.ctx, q); err != nil || n != 0 {
		t.Errorf("backfill again = %d, %v", n, err)
	}
}

// queued lists the contents whose vectors are queued, by their hex SHA-256.
func (e env) queued(t *testing.T) []string {
	t.Helper()
	rows, err := e.pool.Query(e.ctx, `SELECT payload->>'sha256' FROM tasks WHERE kind = $1 AND state = 'queued' AND priority = $2 ORDER BY 1`, textemb.Kind, tasks.PriorityBackfill)
	if err != nil {
		t.Fatal(err)
	}
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

func TestSyncQueuesEveryDocumentAgainOnceForANewVersion(t *testing.T) {
	e := newEnv(t)
	e.corpus(t) // three documents with vectors of v1
	q := &tasks.Queue{Pool: e.pool}
	// No worker has declared the model yet: nothing to do.
	if n, err := textemb.Sync(e.ctx, q); err != nil || n != 0 {
		t.Fatalf("sync before any worker = %d, %v", n, err)
	}
	// A worker declares v1, the version of every vector: nothing to encode,
	// and the version is marked done.
	declare := func(version string) {
		t.Helper()
		if _, err := e.pool.Exec(e.ctx, `INSERT INTO embedding_models (name, version, dims) VALUES ($1, $2, 768)
			ON CONFLICT (name) DO UPDATE SET version = EXCLUDED.version, requeued_at = NULL`, textemb.Model, version); err != nil {
			t.Fatal(err)
		}
	}
	declare("v1")
	if n, err := textemb.Sync(e.ctx, q); err != nil || n != 0 {
		t.Fatalf("sync at v1 = %d, %v", n, err)
	}
	if _, due, err := embed.Due(e.ctx, e.pool, textemb.Model); err != nil || due {
		t.Fatalf("v1 still due: %v, %v", due, err)
	}

	// Another cutting or model: v2. Every live document is queued, once.
	declare("v2")
	if n, err := textemb.Sync(e.ctx, q); err != nil || n != 3 {
		t.Fatalf("sync at v2 = %d, %v; want 3", n, err)
	}
	want := []string{hex.EncodeToString(sum(lease)), hex.EncodeToString(sum(salary)), hex.EncodeToString(sum(car))}
	slices.Sort(want)
	if got := e.queued(t); !slices.Equal(got, want) {
		t.Errorf("queued %v, want %v", got, want)
	}
	if n, err := textemb.Sync(e.ctx, q); err != nil || n != 0 {
		t.Errorf("sync again = %d, %v", n, err)
	}
}

func TestPruningTheTextPrunesItsVectors(t *testing.T) {
	e := newEnv(t)
	e.corpus(t)
	if _, err := e.store.Trash(e.ctx, "enzo", "Travail/paie.pdf"); err != nil {
		t.Fatal(err)
	}
	// What a purge leaves: no file has the car papers any more.
	if _, err := e.pool.Exec(e.ctx, `DELETE FROM files WHERE path = 'Voiture/carte-grise.pdf'`); err != nil {
		t.Fatal(err)
	}
	if _, err := doctext.Prune(e.ctx, e.pool); err != nil {
		t.Fatal(err)
	}
	for content, want := range map[string]int{lease: 2, salary: 1, car: 0} {
		var n int
		if err := e.pool.QueryRow(e.ctx, `SELECT count(*) FROM `+textemb.Table+` WHERE sha256 = $1`, sum(content)).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != want {
			t.Errorf("%.20q: %d windows kept, want %d", content, n, want)
		}
	}
}
