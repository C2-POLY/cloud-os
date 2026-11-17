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
	groups, err := a.activity.Groups(r.Context(), owner(r), days, timeZoneOf(r), 0)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, groups)
}

// timeZoneOf is the browser's IANA time zone (tz), or UTC when it is missing
// or unknown to this box.
func timeZoneOf(r *http.Request) string {
	tz := r.URL.Query().Get("tz")
	if _, err := time.LoadLocation(tz); err != nil || tz == "" || tz == "Local" {
		return "UTC"
	}
	return tz
}

// TimelineEntry is a file on the timeline: its Entry and the date it sits at.
type TimelineEntry struct {
	Entry
	At    string `json:"at"`    // wall-clock date, "2024-07-14T18:03:22", no zone
	Taken bool   `json:"taken"` // At is when the photo was taken
}

// TimelinePage is one page of the timeline and where the next one starts.
type TimelinePage struct {
	Items []TimelineEntry `json:"items"`
	Next  *TimelineCursor `json:"next"`
}

// TimelineCursor is sent back as before_at and before_path for the next page.
type TimelineCursor struct {
	At   string `json:"at"`
	Path string `json:"path"`
}

const wallClock = "2006-01-02T15:04:05.999999"

func timelineFilter(r *http.Request) storage.TimelineFilter {
	switch f := storage.TimelineFilter(r.URL.Query().Get("filter")); f {
	case storage.TimelineMedia, storage.TimelineDocs:
		return f
	}
	return storage.TimelineAll
}

// timeline lists the account's files newest first, photos at the date they
// were taken, others at their last modification, a page at a time.
func (a *desktopAPI) timeline(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var after *storage.TimelineCursor
	if s := q.Get("before_at"); s != "" {
		at, err := time.Parse(wallClock, s)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "before_at must be a date like 2024-07-14T18:03:22"})
			return
		}
		after = &storage.TimelineCursor{At: at, Path: q.Get("before_path")}
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = storage.DefaultTimelinePage
	}
	items, err := a.files.Timeline(r.Context(), owner(r), timeZoneOf(r), timelineFilter(r), after, limit)
	if err != nil {
		failFiles(w, err)
		return
	}
	page := TimelinePage{Items: make([]TimelineEntry, len(items))}
	for i, it := range items {
		page.Items[i] = TimelineEntry{Entry: entryOf(it.File), At: it.At.Format(wallClock), Taken: it.Taken}
	}
	if n := len(items); n == min(limit, storage.MaxTimelinePage) {
		page.Next = &TimelineCursor{At: items[n-1].At.Format(wallClock), Path: items[n-1].Path}
	}
	writeJSON(w, http.StatusOK, page)
}

// timelineMonths counts the account's files per month, for the timeline's strip.
func (a *desktopAPI) timelineMonths(w http.ResponseWriter, r *http.Request) {
	months, err := a.files.TimelineMonths(r.Context(), owner(r), timeZoneOf(r), timelineFilter(r))
	if err != nil {
		failFiles(w, err)
		return
	}
	writeJSON(w, http.StatusOK, months)
}
