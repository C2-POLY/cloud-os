package httpapi

import (
	"net/http"
	"path"
	"strconv"
	"time"

	"github.com/C2-POLY/cloud-os/core/internal/storage"
)

// TrashEntry is one item of the trash, as the web shows it.
type TrashEntry struct {
	ID        int64     `json:"id"`
	Path      string    `json:"path"` // where it was
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	Size      int64     `json:"size"`
	DeletedAt time.Time `json:"deleted_at"`
	ExpiresAt time.Time `json:"expires_at"`
	// Reason is why the box put it there itself: "smb_not_saved" for an SMB
	// write it could not store. Absent for a deletion.
	Reason string `json:"reason,omitempty"`
}

func trashEntryOf(t storage.TrashItem) TrashEntry {
	return TrashEntry{ID: t.ID, Path: t.Path, Name: path.Base(t.Path), Kind: t.Kind, Size: t.Size, DeletedAt: t.DeletedAt, ExpiresAt: t.ExpiresAt, Reason: t.Reason}
}

type trashAPI struct {
	store *storage.Store
}

// trash moves a file or folder of the signed-in account to its trash.
func (a *trashAPI) trash(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	item, err := a.store.Trash(r.Context(), owner(r), body.Path)
	if err != nil {
		failFiles(w, err)
		return
	}
	writeJSON(w, http.StatusOK, trashEntryOf(item))
}

func (a *trashAPI) list(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListTrash(r.Context(), owner(r))
	if err != nil {
		failFiles(w, err)
		return
	}
	out := make([]TrashEntry, len(items))
	for i, it := range items {
		out[i] = trashEntryOf(it)
	}
	writeJSON(w, http.StatusOK, out)
}

func trashID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such trash item"})
		return 0, false
	}
	return id, true
}

func (a *trashAPI) restore(w http.ResponseWriter, r *http.Request) {
	id, ok := trashID(w, r)
	if !ok {
		return
	}
	f, err := a.store.Restore(r.Context(), owner(r), id)
	if err != nil {
		failFiles(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entryOf(f))
}

// confirmed reads {"confirm": true}; anything else is refused with 400, so an
// erasing request cannot be sent by mistake.
func confirmed(w http.ResponseWriter, r *http.Request) bool {
	var body struct {
		Confirm bool `json:"confirm"`
	}
	if !readJSON(w, r, &body) {
		return false
	}
	if !body.Confirm {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `erasing for good needs {"confirm": true}`})
		return false
	}
	return true
}

func (a *trashAPI) purge(w http.ResponseWriter, r *http.Request) {
	id, ok := trashID(w, r)
	if !ok || !confirmed(w, r) {
		return
	}
	if err := a.store.Purge(r.Context(), owner(r), id); err != nil {
		failFiles(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *trashAPI) empty(w http.ResponseWriter, r *http.Request) {
	if !confirmed(w, r) {
		return
	}
	n, err := a.store.EmptyTrash(r.Context(), owner(r))
	if err != nil {
		failFiles(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"purged": n})
}
