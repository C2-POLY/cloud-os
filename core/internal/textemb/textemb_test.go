package textemb_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/C2-POLY/cloud-os/core/internal/doctext"
	"github.com/C2-POLY/cloud-os/core/internal/encoder"
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
		sum(content), chunk, start, end, textemb.Model, encoder.Vector{Values: v}.Literal()); err != nil {
		t.Fatal(err)
	}
}

func query(v []float32) encoder.Vector {
	return encoder.Vector{Model: textemb.Model, Version: "v1", Values: v}
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
	hits, err := textemb.Search(e.ctx, e.pool, "enzo", query(axis(map[int]float64{0: 1})), 0)
	if err != nil {
		t.Fatal(err)
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
	if hits, err := textemb.Search(e.ctx, e.pool, "enzo", query(axis(map[int]float64{0: 1})), 1); err != nil || len(hits) != 1 {
		t.Errorf("limit 1: %v, %v", paths(hits), err)
	}
	if hits, err := textemb.Search(e.ctx, e.pool, "enzo", query(axis(map[int]float64{0: 1})), 1000); err != nil || len(hits) != 3 {
		t.Errorf("limit 1000: %v, %v", paths(hits), err)
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

	q := query(axis(map[int]float64{0: 1}))
	enzo, err := textemb.Search(e.ctx, e.pool, "enzo", q, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := paths(enzo), []string{"enzo:Logement/bail.pdf", "enzo:Voiture/carte-grise.pdf"}; !slices.Equal(got, want) {
		t.Errorf("enzo sees %v, want %v", got, want)
	}
	lea, err := textemb.Search(e.ctx, e.pool, "lea", q, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := paths(lea), []string{"lea:impots.pdf", "lea:Mes papiers/bail.pdf"}; !slices.Equal(got, want) {
		t.Errorf("léa sees %v, want %v", got, want)
	}
	if hits, err := textemb.Search(e.ctx, e.pool, "nobody", q, 0); err != nil || len(hits) != 0 {
		t.Errorf("an account without files: %v, %v", paths(hits), err)
	}
	if _, err := textemb.Search(e.ctx, e.pool, "../enzo", q, 0); err == nil {
		t.Error("a bad owner name was accepted")
	}
}

func TestSearchRefusesAnotherModelsVector(t *testing.T) {
	e := newEnv(t)
	for _, v := range []encoder.Vector{
		{Model: "siglip2-base-patch16-224", Values: axis(map[int]float64{0: 1})},
		{Model: textemb.Model, Values: []float32{1, 0}},
	} {
		if _, err := textemb.Search(e.ctx, e.pool, "enzo", v, 0); !errors.Is(err, textemb.ErrInvalid) {
			t.Errorf("%s, %d values: %v, want ErrInvalid", v.Model, len(v.Values), err)
		}
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
