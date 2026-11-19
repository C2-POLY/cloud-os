package search_test

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/C2-POLY/cloud-os/core/internal/embed"
	"github.com/C2-POLY/cloud-os/core/internal/photovec"
	"github.com/C2-POLY/cloud-os/core/internal/search"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/testdb"
	"github.com/C2-POLY/cloud-os/core/internal/textemb"
)

// TestSpeedOn10000Files measures Demander's list on a synthetic library of
// 10 000 files: 6 000 photos (a third with a date taken) and 4 000 documents
// of 300 to 900 words each from the benchmark's vocabulary, one to three
// windows each, random vectors. The workers answer after a delay, 20 ms by
// default (CLOUDOS_SEARCH_BENCH_EMBED_MS; Arctic takes 16 ms on the Mac,
// about 100 ms estimated on a Raspberry Pi 5). Run apart, it takes a minute
// to fill:
//
//	CLOUDOS_SEARCH_BENCH=1 make test-core
//
// or go test -run TestSpeedOn10000Files -v ./internal/search with
// CLOUDOS_TEST_DATABASE_URL set.
func TestSpeedOn10000Files(t *testing.T) {
	if os.Getenv("CLOUDOS_SEARCH_BENCH") == "" {
		t.Skip("set CLOUDOS_SEARCH_BENCH=1 to measure the search on 10 000 files")
	}
	delay := 20 * time.Millisecond
	if ms, err := strconv.Atoi(os.Getenv("CLOUDOS_SEARCH_BENCH_EMBED_MS")); err == nil {
		delay = time.Duration(ms) * time.Millisecond
	}
	ctx := context.Background()
	pool, _ := testdb.Migrated(t)

	var words []string
	docs, _ := filepath.Glob("../../../worker/tests/fixtures/bench/docs/*.txt")
	for _, p := range docs {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range strings.FieldsFunc(string(b), func(r rune) bool { return !unicode.IsLetter(r) }) {
			if len([]rune(w)) >= 3 {
				words = append(words, strings.ToLower(w))
			}
		}
	}
	start := time.Now()
	for _, sql := range []string{
		`INSERT INTO files (owner, path, kind, size, mime, modified_at) VALUES
		 ('enzo', 'Photos', 'dir', 0, 'inode/directory', now()), ('enzo', 'Documents', 'dir', 0, 'inode/directory', now())`,
		`INSERT INTO files (owner, path, kind, size, mime, sha256, modified_at)
		 SELECT 'enzo', 'Photos/IMG_' || g || '.jpg', 'file', 2000000, 'image/jpeg', sha256(('photo ' || g)::bytea),
		        now() - make_interval(hours => g) FROM generate_series(1, 6000) g`,
		`INSERT INTO files (owner, path, kind, size, mime, sha256, modified_at)
		 SELECT 'enzo', 'Documents/document ' || g || '.pdf', 'file', 100000, 'application/pdf', sha256(('doc ' || g)::bytea),
		        now() - make_interval(hours => 3 * g) FROM generate_series(1, 4000) g`,
		`INSERT INTO media_meta (sha256, taken_at)
		 SELECT sha256(('photo ' || g)::bytea), '2020-01-01'::timestamp + make_interval(days => g) FROM generate_series(1, 6000, 3) g`,
		`INSERT INTO document_text (sha256, source, text, chars, truncated, has_text)
		 SELECT sha256(('doc ' || g)::bytea), 'pdf', x.text, length(x.text), false, true
		 FROM generate_series(1, 4000) g,
		      LATERAL (SELECT string_agg(($1::text[])[1 + floor(random() * array_length($1::text[], 1))::int], ' ') AS text
		               FROM generate_series(1, 300 + g % 600) WHERE g > 0) x`,
		`INSERT INTO ` + textemb.Table + ` (sha256, chunk, start_char, end_char, model, version, embedding)
		 SELECT sha256(('doc ' || g)::bytea), c, c * 1000, c * 1000 + 1000, '` + textemb.Model + `', 'v1',
		        (SELECT array_agg(random() - 0.5)::real[] FROM generate_series(1, 768) WHERE g + c >= 0)::halfvec
		 FROM generate_series(1, 4000) g, generate_series(0, g % 3) c`,
		`INSERT INTO ` + photoTable + ` (sha256, model, version, source, embedding)
		 SELECT sha256(('photo ' || g)::bytea), '` + photovec.Model + `', 'v1', 'image',
		        (SELECT array_agg(random() - 0.5)::real[] FROM generate_series(1, 768) WHERE g >= 0)::vector
		 FROM generate_series(1, 6000) g`,
		`ANALYZE`,
	} {
		args := []any{}
		if strings.Contains(sql, "$1") {
			args = append(args, words)
		}
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%v\n%s", err, sql)
		}
	}
	t.Logf("10 000 files filled in %v", time.Since(start).Round(time.Second))

	w := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		time.Sleep(delay)
		v := make([]float32, 768)
		for i := range v {
			v[i] = rand.Float32() - 0.5
		}
		_ = json.NewEncoder(rw).Encode(map[string]any{"model": req.Model, "version": "v1", "vectors": [][]float32{v}})
	}))
	t.Cleanup(w.Close)
	workers := &embed.Client{URL: w.URL}
	s := &search.Searcher{
		Files: &storage.Store{Root: t.TempDir(), Pool: pool},
		Docs:  &textemb.Searcher{Pool: pool, Embed: workers}, Photos: &photovec.Searcher{Pool: pool, Embed: workers},
	}

	day := func(s string) time.Time { d, _ := time.Parse(time.DateOnly, s); return d }
	filters := map[string]storage.Filter{
		"":                  {},
		"photos":            {Type: storage.TypePhotos},
		"documents of 2025": {Type: storage.TypeDocs, From: day("2025-01-01"), To: day("2025-12-31"), Zone: "Europe/Paris"},
		"taken in 2021":     {From: day("2021-01-01"), To: day("2021-12-31")},
	}
	queries := []string{"contrat de l'appart", "facture d'électricité", "chien à la plage", "relevé de compte de janvier",
		"montant du dépôt de garantie", "assurance", "bail", "vacances au bord de la mer", "ordonnance pour l'angine", "loyer avril",
		"quitance de loyer", "asurance habitation", "électrici", "kinési", "Warolo"}
	for name, f := range filters {
		var took []time.Duration
		results := 0
		for range 3 {
			for _, q := range queries {
				t0 := time.Now()
				r, err := s.Search(ctx, "enzo", search.Query{Q: q, Filter: f})
				took = append(took, time.Since(t0))
				if err != nil || r.Partial {
					t.Fatalf("%q %s: partial %v, %v", q, name, r.Partial, err)
				}
				results += len(r.Hits)
			}
		}
		slices.Sort(took)
		p50, p95 := took[len(took)/2], took[len(took)*95/100]
		t.Logf("filter %-18q %d searches, %d results on average: p50 %v, p95 %v, max %v", name, len(took), results/len(took),
			p50.Round(time.Millisecond), p95.Round(time.Millisecond), took[len(took)-1].Round(time.Millisecond))
		if p95 > time.Second {
			t.Errorf("filter %q: p95 %v, over a second", name, p95)
		}
	}
}
