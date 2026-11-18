package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/C2-POLY/cloud-os/core/internal/embed"
	"github.com/C2-POLY/cloud-os/core/internal/textemb"
)

// SemanticHit is a document found by meaning: the file, the cosine
// similarity of its best window to the query, that window in characters of
// the document's text, and its first characters (shared/api-types.ts).
type SemanticHit struct {
	Entry
	Score   float64 `json:"score"`
	Start   int     `json:"start"`
	End     int     `json:"end"`
	Excerpt string  `json:"excerpt"`
}

type semanticAPI struct {
	docs *textemb.Searcher
}

// search finds the account's documents nearest in meaning to q, best first:
// ?q=contrat de l'appart&limit=20 (textemb.DefaultLimit, textemb.MaxLimit).
// 503 when the workers cannot encode the query (stopped, starting, or no
// model): the search by name and by words still works. The query is never
// logged.
func (a *semanticAPI) search(w http.ResponseWriter, r *http.Request) {
	unavailable := map[string]string{"error": "search by meaning is unavailable"}
	if a.docs == nil {
		writeJSON(w, http.StatusServiceUnavailable, unavailable)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	hits, err := a.docs.Search(r.Context(), owner(r), r.URL.Query().Get("q"), limit)
	if errors.Is(err, embed.ErrUnavailable) {
		slog.Warn("search by meaning", "err", err)
		writeJSON(w, http.StatusServiceUnavailable, unavailable)
		return
	}
	if err != nil {
		failFiles(w, err)
		return
	}
	out := make([]SemanticHit, len(hits))
	for i, h := range hits {
		out[i] = SemanticHit{Entry: entryOf(h.File), Score: h.Score, Start: h.Start, End: h.End, Excerpt: h.Excerpt}
	}
	writeJSON(w, http.StatusOK, out)
}
