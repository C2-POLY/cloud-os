package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/C2-POLY/cloud-os/core/internal/auth"
	"github.com/C2-POLY/cloud-os/core/internal/doccat"
	"github.com/C2-POLY/cloud-os/core/internal/share"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/testdb"
)

// newDocumentsAPI is an API with two accounts, enzo (admin) and johan.
func newDocumentsAPI(t *testing.T) (enzo, johan *client, files *storage.Store) {
	t.Helper()
	pool, _ := testdb.Migrated(t)
	files = &storage.Store{Root: t.TempDir(), Pool: pool}
	h := NewHandler(Config{
		Version: "test", Health: &fakeStore{}, Auth: auth.NewService(pool),
		Files: files, Shares: share.NewService(pool, files), Categories: &doccat.Store{Pool: pool},
	})
	enzo = &client{t: t, h: h}
	enzo.do("POST", "/api/setup", `{"name":"enzo","password":"`+pw+`"}`)
	enzo.do("POST", "/api/accounts", `{"name":"johan","password":"`+pw+`"}`)
	johan = &client{t: t, h: h}
	johan.do("POST", "/api/login", `{"name":"johan","password":"`+pw+`"}`)
	return enzo, johan, files
}

// classified stores a document of owner as the workers leave it: its text
// and its automatic category ("" for none yet).
func classified(t *testing.T, files *storage.Store, owner, path, text, category string, at time.Time) {
	t.Helper()
	ctx := context.Background()
	opt := storage.NoExpectations()
	opt.ModTime = at
	if _, err := files.Put(ctx, owner, path, strings.NewReader(text), opt); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(text))
	if _, err := files.Pool.Exec(ctx,
		`INSERT INTO document_text (sha256, source, text, chars, truncated, has_text) VALUES ($1, 'text', $2, $3, false, true)
		 ON CONFLICT DO NOTHING`, sum[:], text, len([]rune(text))); err != nil {
		t.Fatal(err)
	}
	if category == "" {
		return
	}
	if _, err := files.Pool.Exec(ctx,
		`INSERT INTO document_categories (sha256, category, score, margin, version) VALUES ($1, $2, 0.4, 0.1, 'c1')
		 ON CONFLICT DO NOTHING`, sum[:], category); err != nil {
		t.Fatal(err)
	}
}

func decode[T any](t *testing.T, body string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return v
}

func TestDocumentsByCategory(t *testing.T) {
	enzo, johan, files := newDocumentsAPI(t)
	day := time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)
	classified(t, files, "enzo", "Factures/edf.pdf", "Facture d'électricité", "factures", day)
	classified(t, files, "enzo", "Factures/gaz.pdf", "Facture de gaz", "factures", day.Add(time.Hour))
	classified(t, files, "enzo", "Logement/bail.pdf", "Contrat de location", "contrats", day)
	classified(t, files, "enzo", "Nouveau/scan.pdf", "Pas encore classé", "", day)
	classified(t, files, "johan", "secret.pdf", "La facture de Johan", "factures", day)

	rec := enzo.do("GET", "/api/documents/categories", "")
	sum := decode[CategoriesSummary](t, rec.Body.String())
	if rec.Code != http.StatusOK || len(sum.Categories) != len(doccat.Categories) || sum.Pending != 1 {
		t.Fatalf("categories: %d %s", rec.Code, rec.Body)
	}
	if sum.Categories[0] != (doccat.Count{Category: "factures", Count: 2}) || sum.Categories[1] != (doccat.Count{Category: "contrats", Count: 1}) {
		t.Errorf("counts = %+v", sum.Categories)
	}
	if !strings.Contains(rec.Body.String(), `{"id":"factures","count":2}`) {
		t.Errorf("the JSON of a count: %s", rec.Body)
	}

	// A page of one, then the next from the cursor.
	rec = enzo.do("GET", "/api/documents?category=factures&limit=1", "")
	page := decode[DocumentPage](t, rec.Body.String())
	if rec.Code != http.StatusOK || len(page.Items) != 1 || page.Items[0].Path != "Factures/gaz.pdf" || page.Next == nil {
		t.Fatalf("first page: %d %s", rec.Code, rec.Body)
	}
	if it := page.Items[0]; it.Category != "factures" || it.Auto == nil || *it.Auto != "factures" || it.Corrected || it.Name != "gaz.pdf" {
		t.Errorf("item = %+v", it)
	}
	next := "/api/documents?category=factures&limit=1&before_at=" + url.QueryEscape(page.Next.At.Format(time.RFC3339Nano)) +
		"&before_path=" + url.QueryEscape(page.Next.Path)
	rec = enzo.do("GET", next, "")
	page = decode[DocumentPage](t, rec.Body.String())
	if len(page.Items) != 1 || page.Items[0].Path != "Factures/edf.pdf" {
		t.Errorf("second page: %s", rec.Body)
	}

	// Each account sees its own documents only.
	rec = johan.do("GET", "/api/documents?category=factures", "")
	if page := decode[DocumentPage](t, rec.Body.String()); len(page.Items) != 1 || page.Items[0].Path != "secret.pdf" || page.Next != nil {
		t.Errorf("johan: %s", rec.Body)
	}

	for _, bad := range []string{"/api/documents?category=inconnue", "/api/documents", "/api/documents?category=factures&before_at=hier"} {
		if rec := enzo.do("GET", bad, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", bad, rec.Code)
		}
	}
}

func TestCorrectingADocumentsCategory(t *testing.T) {
	enzo, johan, files := newDocumentsAPI(t)
	day := time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)
	classified(t, files, "enzo", "Factures/edf.pdf", "Facture d'électricité", "factures", day)
	if _, err := files.Put(context.Background(), "enzo", "Photos/plage.jpg", strings.NewReader("jpeg"), storage.NoExpectations()); err != nil {
		t.Fatal(err)
	}

	rec := enzo.do("PUT", "/api/documents/category", `{"path":"Factures/edf.pdf","category":"contrats"}`)
	got := decode[DocumentEntry](t, rec.Body.String())
	if rec.Code != http.StatusOK || got.Category != "contrats" || got.Auto == nil || *got.Auto != "factures" || !got.Corrected {
		t.Fatalf("set: %d %s", rec.Code, rec.Body)
	}
	rec = enzo.do("GET", "/api/documents?category=contrats", "")
	if page := decode[DocumentPage](t, rec.Body.String()); len(page.Items) != 1 || !page.Items[0].Corrected {
		t.Errorf("contrats after the correction: %s", rec.Body)
	}
	// null: back to the automatic category.
	rec = enzo.do("PUT", "/api/documents/category", `{"path":"Factures/edf.pdf","category":null}`)
	if got := decode[DocumentEntry](t, rec.Body.String()); rec.Code != http.StatusOK || got.Category != "factures" || got.Corrected {
		t.Errorf("reset: %d %s", rec.Code, rec.Body)
	}

	for _, c := range []struct {
		who  *client
		body string
		want int
	}{
		{enzo, `{"path":"Factures/edf.pdf","category":"inconnue"}`, http.StatusBadRequest},
		{enzo, `{"path":"Factures/edf.pdf","category":"factures","extra":1}`, http.StatusBadRequest},
		{enzo, `{"path":"Photos/plage.jpg","category":"factures"}`, http.StatusNotFound},
		{enzo, `{"path":"absent.pdf","category":"factures"}`, http.StatusNotFound},
		{johan, `{"path":"Factures/edf.pdf","category":"factures"}`, http.StatusNotFound},
	} {
		if rec := c.who.do("PUT", "/api/documents/category", c.body); rec.Code != c.want {
			t.Errorf("%s: %d %s, want %d", c.body, rec.Code, rec.Body, c.want)
		}
	}
	// A state-changing request must be JSON (cross-site request forgery).
	if rec := enzo.do("PUT", "/api/documents/category", ""); rec.Code == http.StatusOK {
		t.Errorf("a PUT without a JSON body was accepted")
	}
}
