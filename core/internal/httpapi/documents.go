package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/C2-POLY/cloud-os/core/internal/doccat"
)

// documentsAPI serves the app Documents (#73): the account's documents by
// category, and the member's corrections. It only reads files and writes
// categories: nothing here moves, copies or deletes a file.
type documentsAPI struct {
	docs *doccat.Store
}

// DocumentEntry is a document with its category (shared/api-types.ts).
type DocumentEntry struct {
	Entry
	Category  string  `json:"category"`  // the member's choice, else the automatic one
	Auto      *string `json:"auto"`      // the automatic one; null until the workers classify it
	Corrected bool    `json:"corrected"` // the member chose the category
}

// DocumentCursor is sent back as before_at and before_path for the next page.
type DocumentCursor struct {
	At   time.Time `json:"at"`
	Path string    `json:"path"`
}

// DocumentPage is one page of a category and where the next one starts.
type DocumentPage struct {
	Items []DocumentEntry `json:"items"`
	Next  *DocumentCursor `json:"next"`
}

// CategoriesSummary is the number of documents in each category, and of
// documents with text not classified yet.
type CategoriesSummary struct {
	Categories []doccat.Count `json:"categories"`
	Pending    int            `json:"pending"`
}

func documentEntryOf(d doccat.Document) DocumentEntry {
	return DocumentEntry{Entry: entryOf(d.File), Category: d.Category, Auto: d.Auto, Corrected: d.Corrected}
}

// failDocuments maps a category error to its status, then storage's.
func failDocuments(w http.ResponseWriter, err error) {
	if errors.Is(err, doccat.ErrInvalid) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	failFiles(w, err)
}

// categories counts the account's documents in each category.
func (a *documentsAPI) categories(w http.ResponseWriter, r *http.Request) {
	counts, pending, err := a.docs.Summary(r.Context(), owner(r))
	if err != nil {
		failDocuments(w, err)
		return
	}
	writeJSON(w, http.StatusOK, CategoriesSummary{Categories: counts, Pending: pending})
}

// list returns a page of the account's documents in a category, the most
// recently modified first: ?category=factures&limit=60&before_at=&before_path=.
func (a *documentsAPI) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var after *doccat.Cursor
	if s := q.Get("before_at"); s != "" {
		at, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "before_at must be a date like 2024-07-14T18:03:22.123456Z"})
			return
		}
		after = &doccat.Cursor{At: at, Path: q.Get("before_path")}
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = doccat.DefaultLimit
	}
	limit = min(limit, doccat.MaxLimit)
	docs, err := a.docs.List(r.Context(), owner(r), q.Get("category"), after, limit)
	if err != nil {
		failDocuments(w, err)
		return
	}
	page := DocumentPage{Items: make([]DocumentEntry, len(docs))}
	for i, d := range docs {
		page.Items[i] = documentEntryOf(d)
	}
	if n := len(docs); n == limit {
		page.Next = &DocumentCursor{At: docs[n-1].File.ModifiedAt, Path: docs[n-1].File.Path}
	}
	writeJSON(w, http.StatusOK, page)
}

// set records the member's category for one of their documents, or forgets
// it when category is null: {"path": "Factures/edf.pdf", "category": "factures"}.
func (a *documentsAPI) set(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path     string  `json:"path"`
		Category *string `json:"category"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	d, err := a.docs.Set(r.Context(), owner(r), body.Path, body.Category)
	if err != nil {
		failDocuments(w, err)
		return
	}
	writeJSON(w, http.StatusOK, documentEntryOf(d))
}
