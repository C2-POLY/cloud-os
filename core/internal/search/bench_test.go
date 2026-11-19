package search_test

// The benchmark of Demander's one list (#72), through GET /api/search/all: the
// corpus of the model benchmark (worker/tests/fixtures/bench, 30 documents of
// a household and 17 photos), with the vectors the worker's models make of it
// (testdata/bench.json.gz, written by worker/bench/golden.py; the worker's
// test_golden.py says when they are stale). A fake worker answers POST /embed
// with the vectors of the queries.

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/C2-POLY/cloud-os/core/internal/auth"
	"github.com/C2-POLY/cloud-os/core/internal/embed"
	"github.com/C2-POLY/cloud-os/core/internal/httpapi"
	"github.com/C2-POLY/cloud-os/core/internal/photovec"
	"github.com/C2-POLY/cloud-os/core/internal/share"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/testdb"
	"github.com/C2-POLY/cloud-os/core/internal/textemb"
)

const photoTable = "photo_vectors_siglip2_b16_224" // core/migrations/0020_photo_vectors.sql

// benchPhotos is the benchmark's photos folder: the files are stored as they are.
const benchPhotos = "../../../worker/tests/fixtures/bench/photos"

type golden struct {
	Models map[string]string `json:"models"`
	Docs   []struct {
		Name    string `json:"name"`
		Text    string `json:"text"`
		Windows []struct {
			Start  int    `json:"start"`
			End    int    `json:"end"`
			Vector string `json:"vector"`
		} `json:"windows"`
	} `json:"docs"`
	Photos []struct {
		Name   string `json:"name"`
		Vector string `json:"vector"`
	} `json:"photos"`
	Queries []benchQuery `json:"queries"`
}

type benchQuery struct {
	Kind    string            `json:"kind"` // text, image or mixed
	Q       string            `json:"q"`
	Expect  []string          `json:"expect"` // text and image: any of them first
	Photos  []string          `json:"photos"` // mixed: one of them and one of docs in the first 8
	Docs    []string          `json:"docs"`
	Vectors map[string]string `json:"vectors"` // by model
}

func loadGolden(t *testing.T) golden {
	t.Helper()
	raw, err := os.ReadFile("testdata/bench.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	var g golden
	if err := json.NewDecoder(zr).Decode(&g); err != nil {
		t.Fatal(err)
	}
	return g
}

// half decodes base64 of little-endian float16 values.
func half(t *testing.T, s string) []float32 {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]float32, len(b)/2)
	for i := range out {
		h := binary.LittleEndian.Uint16(b[2*i:])
		sign, exp, frac := uint32(h>>15), int(h>>10&0x1f), float64(h&0x3ff)
		var v float64
		switch exp {
		case 0:
			v = frac / 1024 * math.Pow(2, -14)
		case 0x1f:
			v = math.Inf(1)
		default:
			v = (1 + frac/1024) * math.Pow(2, float64(exp-15))
		}
		if sign == 1 {
			v = -v
		}
		out[i] = float32(v)
	}
	return out
}

// queryWorker answers POST /embed with the golden vector of the query for the
// model, or 503 when down, as a worker without its models.
type queryWorker struct {
	t       *testing.T
	models  map[string]string
	vectors map[[2]string][]float32 // (model, query)
	down    atomic.Bool
}

func (w *queryWorker) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	var req struct {
		Model string   `json:"model"`
		Texts []string `json:"texts"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Texts) != 1 {
		http.Error(rw, "bad request", http.StatusBadRequest)
		return
	}
	if w.down.Load() {
		http.Error(rw, `{"error":"model files missing"}`, http.StatusServiceUnavailable)
		return
	}
	v, ok := w.vectors[[2]string{req.Model, req.Texts[0]}]
	if !ok {
		w.t.Errorf("no golden vector for %s %q", req.Model, req.Texts[0])
		http.Error(rw, "unknown query", http.StatusNotFound)
		return
	}
	_ = json.NewEncoder(rw).Encode(map[string]any{"model": req.Model, "version": w.models[req.Model], "vectors": [][]float32{v}})
}

// benchAPI stores the corpus for enzo, with the texts the text task would
// write and the golden vectors, and serves the API with a session for enzo.
type benchAPI struct {
	g      golden
	photos map[string]bool // the names of the photos
	worker *queryWorker
	http   *http.Client
	base   string
}

func newBenchAPI(t *testing.T) *benchAPI {
	t.Helper()
	ctx := context.Background()
	g := loadGolden(t)
	pool, _ := testdb.Migrated(t)
	files := &storage.Store{Root: t.TempDir(), Pool: pool}
	for _, d := range g.Docs {
		f, err := files.Put(ctx, "enzo", "Documents/"+d.Name+".txt", strings.NewReader(d.Text), storage.NoExpectations())
		if err != nil {
			t.Fatal(err)
		}
		exec(t, pool, `INSERT INTO document_text (sha256, source, text, chars, truncated, has_text) VALUES ($1, 'text', $2, $3, false, true)`,
			f.SHA256, d.Text, len([]rune(d.Text)))
		for i, w := range d.Windows {
			exec(t, pool, `INSERT INTO `+textemb.Table+` (sha256, chunk, start_char, end_char, model, version, embedding)
				VALUES ($1, $2, $3, $4, $5, $6, $7::halfvec)`,
				f.SHA256, i, w.Start, w.End, textemb.Model, g.Models[textemb.Model], embed.Vector{Values: half(t, w.Vector)}.Literal())
		}
	}
	for _, p := range g.Photos {
		data, err := os.ReadFile(path.Join(benchPhotos, p.Name+".jpg"))
		if err != nil {
			t.Fatal(err)
		}
		f, err := files.Put(ctx, "enzo", "Photos/"+p.Name+".jpg", bytes.NewReader(data), storage.NoExpectations())
		if err != nil {
			t.Fatal(err)
		}
		if f.MIME != "image/jpeg" {
			t.Fatalf("%s stored as %s", p.Name, f.MIME)
		}
		exec(t, pool, `INSERT INTO `+photoTable+` (sha256, model, version, source, embedding) VALUES ($1, $2, $3, 'image', $4::vector)`,
			f.SHA256, photovec.Model, g.Models[photovec.Model], embed.Vector{Values: half(t, p.Vector)}.Literal())
	}

	w := &queryWorker{t: t, models: g.Models, vectors: map[[2]string][]float32{}}
	for _, q := range g.Queries {
		for model, v := range q.Vectors {
			w.vectors[[2]string{model, q.Q}] = half(t, v)
		}
	}
	workerSrv := httptest.NewServer(w)
	t.Cleanup(workerSrv.Close)
	workers := &embed.Client{URL: workerSrv.URL}
	h := httpapi.NewHandler(httpapi.Config{
		Version: "test", Auth: auth.NewService(pool), Files: files, Shares: share.NewService(pool, files),
		Photos: &photovec.Searcher{Pool: pool, Embed: workers}, Documents: &textemb.Searcher{Pool: pool, Embed: workers},
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	api := &benchAPI{g: g, photos: map[string]bool{}, worker: w, http: &http.Client{Jar: jar}, base: srv.URL}
	for _, p := range g.Photos {
		api.photos[p.Name] = true
	}
	res, err := api.http.Post(srv.URL+"/api/setup", "application/json", strings.NewReader(`{"name":"enzo","password":"un mot de passe assez long"}`))
	if err != nil || res.StatusCode != http.StatusCreated {
		t.Fatalf("setup: %v %v", res.Status, err)
	}
	res.Body.Close()
	return api
}

func exec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

// answer is the body of GET /api/search/all, and the results named as the
// benchmark names them: the file name without its extension.
type answer struct {
	httpapi.SearchAnswer
	names []string
}

func (a *benchAPI) search(t *testing.T, q, extra string) answer {
	t.Helper()
	res, err := a.http.Get(a.base + "/api/search/all?q=" + url.QueryEscape(q) + extra)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("%q: %s", q, res.Status)
	}
	var out answer
	if err := json.NewDecoder(res.Body).Decode(&out.SearchAnswer); err != nil {
		t.Fatal(err)
	}
	for _, h := range out.Results {
		out.names = append(out.names, strings.TrimSuffix(h.Name, path.Ext(h.Name)))
	}
	return out
}

// photo tells a name of the benchmark is a photo's.
func (a *benchAPI) photo(name string) bool { return a.photos[name] }

// firstOf is the place of the first of names in got, or -1.
func firstOf(got, names []string) int {
	return slices.IndexFunc(got, func(n string) bool { return slices.Contains(names, n) })
}

func TestTheBenchmarkThroughTheFusedRoute(t *testing.T) {
	api := newBenchAPI(t)

	// The two queries of the issue.
	lease := api.search(t, "contrat de l'appart", "")
	if lease.Partial || len(lease.names) == 0 || lease.names[0] != "bail-appartement" {
		t.Fatalf("contrat de l'appart: %v (partial %v), want the lease first", lease.names, lease.Partial)
	}
	if i := slices.IndexFunc(lease.names, api.photo); i >= 0 {
		t.Errorf("contrat de l'appart lists the photo %s: %v", lease.names[i], lease.names)
	}
	dogs := api.search(t, "chien à la plage", "")
	if top := slices.Sorted(slices.Values(dogs.names[:min(3, len(dogs.names))])); !slices.Equal(top, []string{"chien-plage-1", "chien-plage-2", "chien-plage-3"}) {
		t.Fatalf("chien à la plage: %v, want its three photos first", dogs.names)
	}

	// Every query of the benchmark: the answer first, and for a document no
	// photo before it.
	first := map[string]int{}
	total := map[string]int{}
	others := map[string]int{} // photos listed for a document, documents for a photo
	var missed []string
	for _, q := range api.g.Queries {
		got := api.search(t, q.Q, "")
		if got.Partial {
			t.Errorf("%q: partial with the workers up", q.Q)
		}
		t.Logf("%-6s %-40q %v", q.Kind, q.Q, got.names[:min(6, len(got.names))])
		switch q.Kind {
		case "text", "image":
			total[q.Kind]++
			for _, n := range got.names {
				if api.photo(n) == (q.Kind == "text") {
					others[q.Kind]++
				}
			}
			at := firstOf(got.names, q.Expect)
			if at == 0 {
				first[q.Kind]++
			} else {
				missed = append(missed, q.Q)
			}
			if q.Kind == "text" && at >= 0 {
				if i := slices.IndexFunc(got.names[:at], api.photo); i >= 0 {
					t.Errorf("%q: the photo %s comes before its document: %v", q.Q, got.names[i], got.names)
				}
			}
		case "mixed":
			top := got.names[:min(8, len(got.names))]
			if firstOf(top, q.Photos) < 0 || firstOf(top, q.Docs) < 0 {
				t.Errorf("%q: %v, want photos and documents in the first 8", q.Q, top)
			}
		case "partial", "typo":
			// The document first, found in its text by the word completed or corrected.
			by := map[string]string{"partial": "prefix", "typo": "fuzzy"}[q.Kind]
			if firstOf(got.names, q.Expect) != 0 || !slices.Contains(got.Results[0].FoundBy, by) {
				t.Errorf("%s %q: %v, first found by %v; want %v first, found by %s",
					q.Kind, q.Q, got.names, got.Results[0].FoundBy, q.Expect, by)
			}
		}
	}
	t.Logf("first: %d/%d documents, %d/%d photos; missed %q", first["text"], total["text"], first["image"], total["image"], missed)
	t.Logf("listed besides: %d photos for the %d document queries, %d documents for the %d photo queries",
		others["text"], total["text"], others["image"], total["image"])
	// Arctic alone finds 32 of the 33 documents first (search-ai.md): the
	// fusion keeps them, and finds every photo first.
	if first["text"] < 32 || first["image"] != total["image"] {
		t.Errorf("first: %d/%d documents (want 32 at least), %d/%d photos: missed %q",
			first["text"], total["text"], first["image"], total["image"], missed)
	}
}

func TestTheBenchmarkWithoutTheWorkers(t *testing.T) {
	api := newBenchAPI(t)
	api.worker.down.Store(true)
	got := api.search(t, "contrat de l'appart", "")
	if !got.Partial {
		t.Error("not partial without the workers")
	}
	// The words still find the lease, and nothing found by meaning comes.
	if !slices.Contains(got.names, "bail-appartement") {
		t.Errorf("contrat de l'appart without the workers: %v", got.names)
	}
	for _, h := range got.Results {
		if slices.Contains(h.FoundBy, "meaning") || slices.Contains(h.FoundBy, "image") {
			t.Errorf("%s found by %v without the workers", h.Path, h.FoundBy)
		}
	}
	first, total := 0, 0
	for _, q := range api.g.Queries {
		if q.Kind != "text" {
			continue
		}
		total++
		if got := api.search(t, q.Q, ""); firstOf(got.names, q.Expect) == 0 {
			first++
		}
	}
	t.Logf("without the workers: %d/%d documents first", first, total)
	if first < total/2 {
		t.Errorf("without the workers: %d/%d documents first", first, total)
	}
}

func TestTheBenchmarkByType(t *testing.T) {
	api := newBenchAPI(t)
	photos := api.search(t, "chien", "&type=photos")
	docs := api.search(t, "chien", "&type=docs")
	if len(photos.names) == 0 || len(docs.names) == 0 {
		t.Fatalf("chien: photos %v, docs %v", photos.names, docs.names)
	}
	for _, n := range photos.names {
		if !api.photo(n) {
			t.Errorf("type=photos lists %s", n)
		}
	}
	for _, n := range docs.names {
		if api.photo(n) {
			t.Errorf("type=docs lists %s", n)
		}
	}
	if docs.names[0] != "facture-veterinaire" {
		t.Errorf("chien in the documents: %v", docs.names)
	}
}
