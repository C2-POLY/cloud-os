package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/C2-POLY/cloud-os/core/internal/auth"
	"github.com/C2-POLY/cloud-os/core/internal/encoder"
	"github.com/C2-POLY/cloud-os/core/internal/share"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/testdb"
	"github.com/C2-POLY/cloud-os/core/internal/textemb"
)

// fakeEncoder answers like the worker's encoder: a unit vector along the
// first dimension for any query, or err; it keeps the queries it was asked.
type fakeEncoder struct {
	model   string
	err     error
	queries []string
}

func (f *fakeEncoder) Encode(_ context.Context, model, text string) (encoder.Vector, error) {
	f.queries = append(f.queries, model+": "+text)
	if f.err != nil {
		return encoder.Vector{}, f.err
	}
	v := make([]float32, textemb.Dims)
	v[0] = 1
	m := f.model
	if m == "" {
		m = model
	}
	return encoder.Vector{Model: m, Version: "v1", Values: v}, nil
}

func newSemanticAPI(t *testing.T, enc QueryEncoder) (enzo, johan *client, files *storage.Store, pool *pgxpool.Pool) {
	t.Helper()
	pool, _ = testdb.Migrated(t)
	files = &storage.Store{Root: t.TempDir(), Pool: pool}
	h := NewHandler(Config{
		Version: "test", Health: &fakeStore{}, Auth: auth.NewService(pool),
		Files: files, Shares: share.NewService(pool, files), Encoder: enc,
	})
	enzo = &client{t: t, h: h}
	enzo.do("POST", "/api/setup", `{"name":"enzo","password":"`+pw+`"}`)
	enzo.do("POST", "/api/accounts", `{"name":"johan","password":"`+pw+`"}`)
	johan = &client{t: t, h: h}
	johan.do("POST", "/api/login", `{"name":"johan","password":"`+pw+`"}`)
	return enzo, johan, files, pool
}

// document stores a file of owner, its text and one window whose vector
// makes cosine with the first axis.
func document(t *testing.T, files *storage.Store, owner, path, text string, cosine float64) {
	t.Helper()
	ctx := context.Background()
	if _, err := files.Put(ctx, owner, path, strings.NewReader(text), storage.NoExpectations()); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(text))
	if _, err := files.Pool.Exec(ctx,
		`INSERT INTO document_text (sha256, source, text, chars, truncated, has_text) VALUES ($1, 'text', $2, $3, false, true)
		 ON CONFLICT DO NOTHING`, sum[:], text, len([]rune(text))); err != nil {
		t.Fatal(err)
	}
	v := make([]float32, textemb.Dims)
	v[0], v[1] = float32(cosine), float32(math.Sqrt(1-cosine*cosine))
	if _, err := files.Pool.Exec(ctx,
		`INSERT INTO `+textemb.Table+` (sha256, chunk, start_char, end_char, model, version, embedding)
		 VALUES ($1, 0, 0, $2, $3, 'v1', $4::halfvec) ON CONFLICT DO NOTHING`,
		sum[:], len([]rune(text)), textemb.Model, encoder.Vector{Values: v}.Literal()); err != nil {
		t.Fatal(err)
	}
}

func semantic(t *testing.T, c *client, q string) (int, []SemanticHit) {
	t.Helper()
	rec := c.get("/api/search/semantic?q="+url.QueryEscape(q), nil)
	var hits []SemanticHit
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &hits); err != nil {
			t.Fatal(err)
		}
	}
	return rec.Code, hits
}

func TestSemanticSearchReturnsTheAccountsNearestDocuments(t *testing.T) {
	enc := &fakeEncoder{}
	enzo, johan, files, _ := newSemanticAPI(t, enc)
	document(t, files, "enzo", "Logement/bail.pdf", "Contrat de location de l'appartement", 0.9)
	document(t, files, "enzo", "Travail/paie.pdf", "Bulletin de paie", 0.4)
	document(t, files, "enzo", "Voiture/carte-grise.pdf", "Certificat d'immatriculation", 0.1)
	document(t, files, "johan", "secret.pdf", "Le contrat de Johan", 1)

	code, hits := semantic(t, enzo, "contrat de l'appart")
	if code != http.StatusOK || len(hits) != 3 {
		t.Fatalf("enzo: %d, %d hits", code, len(hits))
	}
	first := hits[0]
	if first.Entry.Path != "Logement/bail.pdf" || first.Entry.Kind != "file" || first.Excerpt != "Contrat de location de l'appartement" ||
		first.Start != 0 || first.End != len("Contrat de location de l'appartement") || first.Score < 0.89 || first.Score > 0.91 {
		t.Errorf("first hit %+v", first)
	}
	if hits[1].Entry.Path != "Travail/paie.pdf" || hits[2].Entry.Path != "Voiture/carte-grise.pdf" {
		t.Errorf("order %s, %s", hits[1].Entry.Path, hits[2].Entry.Path)
	}
	if len(enc.queries) != 1 || enc.queries[0] != textemb.Model+": contrat de l'appart" {
		t.Errorf("asked the encoder %v", enc.queries)
	}
	// Johan sees his own document only, enzo never sees Johan's.
	if _, hits := semantic(t, johan, "contrat"); len(hits) != 1 || hits[0].Entry.Path != "secret.pdf" {
		t.Errorf("johan sees %+v", hits)
	}

	// The trash takes a document out; limit cuts the list.
	if rec := enzo.do("POST", "/api/files/trash", `{"path":"Logement/bail.pdf"}`); rec.Code != http.StatusOK {
		t.Fatalf("trash: %d %s", rec.Code, rec.Body)
	}
	if _, hits := semantic(t, enzo, "contrat de l'appart"); len(hits) != 2 || hits[0].Entry.Path != "Travail/paie.pdf" {
		t.Errorf("after the trash: %+v", hits)
	}
	if rec := enzo.get("/api/search/semantic?q=contrat&limit=1", nil); !strings.HasPrefix(rec.Body.String(), `[{"entry":{"path":"Travail/paie.pdf"`) || strings.Count(rec.Body.String(), `"entry"`) != 1 {
		t.Errorf("limit 1: %s", rec.Body)
	}
}

func TestSemanticSearchInputsAndFailures(t *testing.T) {
	enc := &fakeEncoder{}
	enzo, _, _, _ := newSemanticAPI(t, enc)
	if code, hits := semantic(t, enzo, "   "); code != http.StatusOK || hits == nil || len(hits) != 0 {
		t.Errorf("empty query: %d %v", code, hits)
	}
	if code, _ := semantic(t, enzo, strings.Repeat("é", textemb.MaxQueryChars+1)); code != http.StatusBadRequest {
		t.Errorf("long query: %d, want 400", code)
	}
	if code, hits := semantic(t, enzo, strings.Repeat("é", textemb.MaxQueryChars)); code != http.StatusOK || len(hits) != 0 {
		t.Errorf("longest query: %d %v", code, hits)
	}
	if len(enc.queries) != 1 {
		t.Errorf("asked the encoder %d times, want 1", len(enc.queries))
	}
	if anon := (&client{t: t, h: enzo.h}).get("/api/search/semantic?q=bail", nil); anon.Code != http.StatusUnauthorized {
		t.Errorf("without a session: %d, want 401", anon.Code)
	}

	enc.err = errors.Join(encoder.ErrUnavailable, errors.New("connection refused"))
	if rec := enzo.get("/api/search/semantic?q=bail", nil); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "unavailable") {
		t.Errorf("worker down: %d %s", rec.Code, rec.Body)
	}
	enc.err, enc.model = nil, "siglip2-base-patch16-224"
	if code, _ := semantic(t, enzo, "bail"); code != http.StatusServiceUnavailable {
		t.Errorf("another model: %d, want 503", code)
	}

	none, _, _, _ := newSemanticAPI(t, nil)
	if code, _ := semantic(t, none, "bail"); code != http.StatusServiceUnavailable {
		t.Errorf("no encoder: %d, want 503", code)
	}
}
