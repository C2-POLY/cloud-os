package httpapi

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/C2-POLY/cloud-os/core/internal/auth"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
)

// filesAPI serves the signed-in account's own space: owner is always the
// session's account, never a parameter.
type filesAPI struct {
	store *storage.Store
}

// Entry is one file or folder in a listing.
type Entry struct {
	Path       string    `json:"path"`
	Name       string    `json:"name"`
	Kind       string    `json:"kind"`
	Size       int64     `json:"size"`
	MIME       string    `json:"mime"`
	SHA256     string    `json:"sha256,omitempty"`
	ModifiedAt time.Time `json:"modified_at"`
}

func entryOf(f storage.File) Entry {
	name := f.Path
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '/' {
			name = name[i+1:]
			break
		}
	}
	e := Entry{Path: f.Path, Name: name, Kind: f.Kind, Size: f.Size, MIME: f.MIME, ModifiedAt: f.ModifiedAt}
	if f.SHA256 != nil {
		e.SHA256 = hex.EncodeToString(f.SHA256)
	}
	return e
}

func owner(r *http.Request) string {
	a, _ := auth.AccountFrom(r.Context())
	return a.Name
}

// failFiles maps a storage error to its status; anything unexpected is logged and hidden.
func failFiles(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrInvalidPath):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	case errors.Is(err, storage.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such file or folder"})
	case errors.Is(err, storage.ErrExists):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case errors.Is(err, storage.ErrMismatch):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
	default:
		fail(w, err)
	}
}

func (a *filesAPI) list(w http.ResponseWriter, r *http.Request) {
	files, err := a.store.List(r.Context(), owner(r), r.URL.Query().Get("path"))
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

func (a *filesAPI) mkdir(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	f, err := a.store.Mkdir(r.Context(), owner(r), body.Path)
	if err != nil {
		failFiles(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, entryOf(f))
}

func (a *filesAPI) move(w http.ResponseWriter, r *http.Request) {
	var body struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	f, err := a.store.Move(r.Context(), owner(r), body.From, body.To)
	if err != nil {
		failFiles(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entryOf(f))
}

// download streams a file; http.ServeContent answers Range, If-Range,
// If-Modified-Since and If-None-Match.
func (a *filesAPI) download(w http.ResponseWriter, r *http.Request) {
	file, f, err := a.store.Open(r.Context(), owner(r), r.URL.Query().Get("path"))
	if errors.Is(err, storage.ErrNotFound) || err == nil && f.IsDir() {
		if file != nil {
			file.Close()
		}
		failFiles(w, storage.ErrNotFound)
		return
	}
	if err != nil {
		failFiles(w, err)
		return
	}
	defer file.Close()
	name := entryOf(f).Name
	w.Header().Set("Content-Type", f.MIME)
	w.Header().Set("ETag", `"`+hex.EncodeToString(f.SHA256)+`"`)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s", urlPathEscape(name)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, name, f.ModifiedAt, file)
}

// upload stores the request body at path. Content-Length, when sent, and
// X-Content-SHA256 (hex), when sent, must match the bytes received.
func (a *filesAPI) upload(w http.ResponseWriter, r *http.Request) {
	opt := storage.NoExpectations()
	if r.ContentLength >= 0 {
		opt.ExpectSize = r.ContentLength
	}
	if h := r.Header.Get("X-Content-SHA256"); h != "" {
		sum, err := hex.DecodeString(h)
		if err != nil || len(sum) != 32 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "X-Content-SHA256 must be 64 hex characters"})
			return
		}
		opt.ExpectSHA256 = sum
	}
	if h := r.Header.Get("X-Modified-At"); h != "" {
		sec, err := strconv.ParseInt(h, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "X-Modified-At must be Unix seconds"})
			return
		}
		opt.ModTime = time.Unix(sec, 0)
	}
	f, err := a.store.Put(r.Context(), owner(r), r.URL.Query().Get("path"), r.Body, opt)
	if err != nil {
		failFiles(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, entryOf(f))
}

// urlPathEscape escapes a file name for the RFC 5987 filename* parameter.
func urlPathEscape(s string) string {
	const hexDigits = "0123456789ABCDEF"
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~' {
			out = append(out, c)
		} else {
			out = append(out, '%', hexDigits[c>>4], hexDigits[c&15])
		}
	}
	return string(out)
}
