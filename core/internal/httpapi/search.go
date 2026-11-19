package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/C2-POLY/cloud-os/core/internal/search"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
)

// SearchHit is a file of Demander's list (GET /api/search/all): its entry,
// its score in this list, how it was found, and the words found in its text
// (snippet) or else its passage nearest in meaning (excerpt).
type SearchHit struct {
	Entry
	Score   float64               `json:"score"`
	FoundBy []string              `json:"found_by"`
	Snippet []storage.SnippetPart `json:"snippet,omitempty"`
	Excerpt string                `json:"excerpt,omitempty"`
}

// SearchAnswer is the body of GET /api/search/all (shared/api-types.ts).
type SearchAnswer struct {
	Results []SearchHit `json:"results"`
	// Partial: the search by meaning did not answer, the results come from
	// the names and the words only.
	Partial bool `json:"partial"`
}

type searchAPI struct {
	search *search.Searcher
}

// all answers Demander with one list, best first: the account's files found
// by name, by the words of their text, by its meaning and by what their
// photo shows (search.Searcher). ?q=chien à la plage&limit=30, narrowed by
// type=docs|photos and by from=2025-01-01&to=2025-12-31 (days included, in
// the browser's time zone tz). 400 for another type or a bad date. The query
// is never logged.
func (a *searchAPI) all(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	filter := storage.Filter{Type: storage.FileType(query.Get("type")), Zone: timeZoneOf(r)}
	if filter.Type != storage.AnyType && filter.Type != storage.TypeDocs && filter.Type != storage.TypePhotos {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "type must be docs or photos"})
		return
	}
	var err error
	for _, d := range []struct {
		name string
		to   *time.Time
	}{{"from", &filter.From}, {"to", &filter.To}} {
		if v := query.Get(d.name); v != "" {
			if *d.to, err = time.Parse(time.DateOnly, v); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": d.name + " must be a date, 2025-12-31"})
				return
			}
		}
	}
	if !filter.From.IsZero() && !filter.To.IsZero() && filter.To.Before(filter.From) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "to comes before from"})
		return
	}
	limit, _ := strconv.Atoi(query.Get("limit"))
	res, err := a.search.Search(r.Context(), owner(r), search.Query{Q: query.Get("q"), Filter: filter, Limit: limit})
	if err != nil {
		failFiles(w, err)
		return
	}
	out := SearchAnswer{Results: make([]SearchHit, len(res.Hits)), Partial: res.Partial}
	for i, h := range res.Hits {
		out.Results[i] = SearchHit{Entry: entryOf(h.File), Score: h.Score, FoundBy: h.FoundBy, Snippet: h.Snippet, Excerpt: h.Excerpt}
	}
	writeJSON(w, http.StatusOK, out)
}
