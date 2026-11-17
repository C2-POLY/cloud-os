package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/C2-POLY/cloud-os/core/internal/activity"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
)

// desktopAPI feeds the desktop's widgets: Reprendre and Ce qui s'est passé.
type desktopAPI struct {
	files    *storage.Store
	activity *activity.Log
}

// recent lists the account's most recently added or modified files.
func (a *desktopAPI) recent(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	files, err := a.files.Recent(r.Context(), owner(r), limit)
	if err != nil {
		failFiles(w, err)
		return
	}
	entries := make([]Entry, len(files))
	for i, f := range files {
		entries[i] = entryOf(f)
	}
	writeJSON(w, http.StatusOK, entries)
}

// events lists what happened in the account over the last days (7 by
// default, 30 at most), grouped by day in the browser's time zone (tz, an
// IANA name; UTC when missing or unknown).
func (a *desktopAPI) events(w http.ResponseWriter, r *http.Request) {
	if a.activity == nil {
		writeJSON(w, http.StatusOK, []activity.Group{})
		return
	}
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 || days > 30 {
		days = 7
	}
	tz := r.URL.Query().Get("tz")
	if _, err := time.LoadLocation(tz); err != nil || tz == "" || tz == "Local" {
		tz = "UTC"
	}
	groups, err := a.activity.Groups(r.Context(), owner(r), days, tz, 0)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, groups)
}
