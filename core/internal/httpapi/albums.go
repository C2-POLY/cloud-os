package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/C2-POLY/cloud-os/core/internal/albums"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
)

// albumsAPI serves the account's albums (#39). An album only refers to files:
// nothing here moves, copies or deletes one.
type albumsAPI struct {
	albums *albums.Service
}

// AlbumEntry is an album as its list shows it.
type AlbumEntry struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	Count     int       `json:"count"`
	Cover     *Entry    `json:"cover"` // the newest live file, for the thumbnail; null when empty
}

func albumEntryOf(a albums.Album) AlbumEntry {
	e := AlbumEntry{ID: a.ID, Name: a.Name, CreatedAt: a.CreatedAt, Count: a.Count}
	if a.Cover != nil {
		c := entryOf(*a.Cover)
		e.Cover = &c
	}
	return e
}

// failAlbums maps an album error to its status, then storage's.
func failAlbums(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, albums.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, albums.ErrExists):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case errors.Is(err, albums.ErrInvalid):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	default:
		failFiles(w, err)
	}
}

func albumID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": albums.ErrNotFound.Error()})
		return 0, false
	}
	return id, true
}

func (a *albumsAPI) list(w http.ResponseWriter, r *http.Request) {
	all, err := a.albums.List(r.Context(), owner(r))
	if err != nil {
		failAlbums(w, err)
		return
	}
	out := make([]AlbumEntry, len(all))
	for i, al := range all {
		out[i] = albumEntryOf(al)
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *albumsAPI) create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	al, err := a.albums.Create(r.Context(), owner(r), body.Name)
	if err != nil {
		failAlbums(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, albumEntryOf(al))
}

func (a *albumsAPI) get(w http.ResponseWriter, r *http.Request) {
	id, ok := albumID(w, r)
	if !ok {
		return
	}
	al, err := a.albums.Get(r.Context(), owner(r), id)
	if err != nil {
		failAlbums(w, err)
		return
	}
	writeJSON(w, http.StatusOK, albumEntryOf(al))
}

func (a *albumsAPI) rename(w http.ResponseWriter, r *http.Request) {
	id, ok := albumID(w, r)
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if err := a.albums.Rename(r.Context(), owner(r), id, body.Name); err != nil {
		failAlbums(w, err)
		return
	}
	a.get(w, r)
}

// remove deletes the album and its references, never a file.
func (a *albumsAPI) remove(w http.ResponseWriter, r *http.Request) {
	id, ok := albumID(w, r)
	if !ok {
		return
	}
	if err := a.albums.Delete(r.Context(), owner(r), id); err != nil {
		failAlbums(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// items lists the album's live files, newest first, as the timeline does.
func (a *albumsAPI) items(w http.ResponseWriter, r *http.Request) {
	id, ok := albumID(w, r)
	if !ok {
		return
	}
	writeTimelinePage(w, r, func(after *storage.TimelineCursor, limit int) ([]storage.TimelineItem, error) {
		return a.albums.Items(r.Context(), owner(r), timeZoneOf(r), id, after, limit)
	})
}

func (a *albumsAPI) change(add bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := albumID(w, r)
		if !ok {
			return
		}
		var body struct {
			Paths []string `json:"paths"`
		}
		if !readJSON(w, r, &body) {
			return
		}
		do := a.albums.Remove
		if add {
			do = a.albums.Add
		}
		if err := do(r.Context(), owner(r), id, body.Paths); err != nil {
			failAlbums(w, err)
			return
		}
		a.get(w, r)
	}
}
