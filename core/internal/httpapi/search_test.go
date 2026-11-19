package httpapi

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
)

func searchAll(t *testing.T, c *client, query string) (int, SearchAnswer) {
	t.Helper()
	rec := c.get("/api/search/all?"+query, nil)
	var out SearchAnswer
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
	}
	return rec.Code, out
}

func TestSearchAllAnswersOneList(t *testing.T) {
	w := &textWorker{}
	enzo, johan, files := newSemanticAPI(t, w)
	document(t, files, "enzo", "Logement/bail.pdf", "Contrat de location de l'appartement", 0.9)
	document(t, files, "johan", "secret.pdf", "Le contrat de Johan", 1)

	code, got := searchAll(t, enzo, "q="+url.QueryEscape("contrat de l'appart"))
	if code != http.StatusOK || len(got.Results) != 1 {
		t.Fatalf("enzo: %d %+v", code, got)
	}
	// Found by its words and its meaning; no photo search is configured, so
	// the answer says it misses a search by meaning.
	h := got.Results[0]
	if h.Path != "Logement/bail.pdf" || h.Name != "bail.pdf" || h.Score <= 0.9 || len(h.Snippet) == 0 || h.Excerpt != "" ||
		len(h.FoundBy) != 2 || h.FoundBy[0] != "text" || h.FoundBy[1] != "meaning" || !got.Partial {
		t.Errorf("hit %+v, partial %v", h, got.Partial)
	}
	if _, got := searchAll(t, johan, "q=contrat"); len(got.Results) != 1 || got.Results[0].Path != "secret.pdf" {
		t.Errorf("johan: %+v", got)
	}
	if code, got := searchAll(t, enzo, "q="); code != http.StatusOK || got.Results == nil || len(got.Results) != 0 {
		t.Errorf("empty query: %d %+v", code, got)
	}
	if code, got := searchAll(t, enzo, "q=contrat&type=photos&from=2020-01-01&to=2030-12-31&tz=Europe/Paris"); code != http.StatusOK || len(got.Results) != 0 {
		t.Errorf("photos only: %d %+v", code, got)
	}

	// Without the workers, the words still answer.
	w.down = true
	if code, got := searchAll(t, enzo, "q=contrat"); code != http.StatusOK || !got.Partial || len(got.Results) != 1 ||
		len(got.Results[0].FoundBy) != 1 || got.Results[0].FoundBy[0] != "text" {
		t.Errorf("workers down: %d %+v", code, got)
	}
}

func TestSearchAllRefusesBadFilters(t *testing.T) {
	enzo, _, _ := newSemanticAPI(t, nil)
	for _, q := range []string{
		"q=a&type=videos",
		"q=a&from=hier",
		"q=a&to=2025-13-01",
		"q=a&from=2025-02-01&to=2025-01-31",
	} {
		if code, _ := searchAll(t, enzo, q); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", q, code)
		}
	}
	if code, _ := searchAll(t, enzo, "q=a&from=2025-01-31&to=2025-01-31&tz=Nowhere/Land"); code != http.StatusOK {
		t.Errorf("an unknown time zone is UTC: %d", code)
	}
	if code, _ := searchAll(t, &client{t: t, h: enzo.h}, "q=a"); code != http.StatusUnauthorized {
		t.Errorf("without a session: %d", code)
	}
}
