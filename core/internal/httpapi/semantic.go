package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/C2-POLY/cloud-os/core/internal/encoder"
	"github.com/C2-POLY/cloud-os/core/internal/textemb"
)

// QueryEncoder turns a search query into a model's vector: the worker's
// encoder (encoder.Client) in the core, a fake in tests.
type QueryEncoder interface {
	Encode(ctx context.Context, model, text string) (encoder.Vector, error)
}

// SemanticHit is a document found by meaning: the file, the cosine
// similarity of its best window to the query, that window in characters of
// the document's text, and its first characters (shared/api-types.ts).
type SemanticHit struct {
	Entry   Entry   `json:"entry"`
	Score   float64 `json:"score"`
	Start   int     `json:"start"`
	End     int     `json:"end"`
	Excerpt string  `json:"excerpt"`
}

type semanticAPI struct {
	pool    *pgxpool.Pool
	encoder QueryEncoder
}

// search finds the account's documents nearest in meaning to q, best first,
// at most limit of them (textemb.DefaultLimit, textemb.MaxLimit). 503 when
// the worker cannot encode the query; the query is never logged.
func (a *semanticAPI) search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, http.StatusOK, []SemanticHit{})
		return
	}
	if utf8.RuneCountInString(q) > textemb.MaxQueryChars {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the query is too long"})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	unavailable := map[string]string{"error": "search by meaning is unavailable"}
	if a.encoder == nil {
		writeJSON(w, http.StatusServiceUnavailable, unavailable)
		return
	}
	v, err := a.encoder.Encode(r.Context(), textemb.Model, q)
	if err == nil && (v.Model != textemb.Model || len(v.Values) != textemb.Dims) {
		err = textemb.ErrInvalid
	}
	if err != nil {
		slog.Warn("semantic search: encode the query", "err", err)
		writeJSON(w, http.StatusServiceUnavailable, unavailable)
		return
	}
	hits, err := textemb.Search(r.Context(), a.pool, owner(r), v, limit)
	if errors.Is(err, textemb.ErrInvalid) {
		writeJSON(w, http.StatusServiceUnavailable, unavailable)
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]SemanticHit, len(hits))
	for i, h := range hits {
		out[i] = SemanticHit{Entry: entryOf(h.File), Score: h.Score, Start: h.Start, End: h.End, Excerpt: h.Excerpt}
	}
	writeJSON(w, http.StatusOK, out)
}
