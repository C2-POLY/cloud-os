package httpapi

import (
	"net/http"

	"github.com/C2-POLY/cloud-os/core/internal/analysis"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
)

// analysisAPI tells the account where the background analysis of its files
// stands (#82, shared/api-types.ts). It only reads: nothing here queues,
// retries or touches a file. Every answer starts from the account's own file
// rows, so another account that has the same content is never seen.
type analysisAPI struct {
	reader *analysis.Reader
	files  *storage.Store
}

// summary counts the account's files being analysed and those whose analysis
// failed.
func (a *analysisAPI) summary(w http.ResponseWriter, r *http.Request) {
	if a.reader == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the analysis status is unavailable"})
		return
	}
	s, err := a.reader.Summary(r.Context(), owner(r))
	if err != nil {
		failFiles(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s)
}

// file lists the steps of the analysis of one of the account's files, and
// where each stands: ?path=Documents/bail.pdf.
func (a *analysisAPI) file(w http.ResponseWriter, r *http.Request) {
	if a.reader == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the analysis status is unavailable"})
		return
	}
	f, err := a.files.Lookup(r.Context(), owner(r), r.URL.Query().Get("path"))
	if err == nil && f.IsDir() {
		err = storage.ErrNotFound
	}
	if err != nil {
		failFiles(w, err)
		return
	}
	st, err := a.reader.File(r.Context(), f)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}
