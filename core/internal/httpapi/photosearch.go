package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/C2-POLY/cloud-os/core/internal/embed"
	"github.com/C2-POLY/cloud-os/core/internal/photovec"
)

// PhotoHit is one result of GET /api/search/photos: a photo or video of the
// account, and its cosine similarity to the query (higher is closer).
type PhotoHit struct {
	Entry
	Score float64 `json:"score"`
}

type photoSearchAPI struct {
	photos *photovec.Searcher
}

// search finds the account's photos and videos by what they show:
// ?q=chien à la plage&limit=30, best first. 503 when the workers cannot
// encode the query (stopped, starting, or no model): the search by name
// still works.
func (a *photoSearchAPI) search(w http.ResponseWriter, r *http.Request) {
	if a.photos == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "photo search unavailable"})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	hits, err := a.photos.Search(r.Context(), owner(r), r.URL.Query().Get("q"), limit)
	if errors.Is(err, embed.ErrUnavailable) {
		slog.Warn("photo search", "err", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "photo search unavailable"})
		return
	}
	if err != nil {
		failFiles(w, err)
		return
	}
	out := make([]PhotoHit, len(hits))
	for i, h := range hits {
		out[i] = PhotoHit{Entry: entryOf(h.File), Score: h.Score}
	}
	writeJSON(w, http.StatusOK, out)
}
