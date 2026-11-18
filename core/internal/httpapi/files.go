package httpapi

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/C2-POLY/cloud-os/core/internal/auth"
	mediameta "github.com/C2-POLY/cloud-os/core/internal/meta"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/thumbs"
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
	case errors.Is(err, storage.ErrInvalidPath), errors.Is(err, storage.ErrInvalidRequest):
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

// thumb serves the thumbnail of a file, 404 while it has none (not an image,
// or not made yet). The ETag is the content's hash; the web front adds
// ?v=<sha256> to the URL so that a new version is never served from cache.
func (a *filesAPI) thumb(w http.ResponseWriter, r *http.Request) {
	f, err := a.store.Lookup(r.Context(), owner(r), r.URL.Query().Get("path"))
	if err == nil && f.IsDir() {
		err = storage.ErrNotFound
	}
	if err != nil {
		failFiles(w, err)
		return
	}
	t, err := thumbs.Get(r.Context(), a.store.Pool, f.SHA256)
	if errors.Is(err, thumbs.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no thumbnail"})
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", t.MIME)
	w.Header().Set("ETag", `"`+hex.EncodeToString(f.SHA256)+`"`)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "", t.CreatedAt, bytes.NewReader(t.Bytes))
}

// search finds the account's files and folders by name and path (q), at most
// limit of them (storage.DefaultSearchLimit, storage.MaxSearchLimit).
func (a *filesAPI) search(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	files, err := a.store.Search(r.Context(), owner(r), r.URL.Query().Get("q"), limit)
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

// metaJSON is the metadata of a file as the API returns it. TakenAt is the
// wall-clock time the photo was taken, without a zone ("2024-07-14T18:03:22");
// TakenOffset is its offset from UTC ("+02:00") when the camera wrote one.
type metaJSON struct {
	TakenAt     *string  `json:"taken_at"`
	TakenOffset *string  `json:"taken_offset"`
	Latitude    *float64 `json:"latitude"`
	Longitude   *float64 `json:"longitude"`
	Make        *string  `json:"make"`
	Model       *string  `json:"model"`
	Lens        *string  `json:"lens"`
	Width       *int     `json:"width"`
	Height      *int     `json:"height"`
	Orientation *int     `json:"orientation"`
	DurationMS  *int64   `json:"duration_ms"` // a video's length
}

// meta returns the metadata the workers read from a file (date taken, GPS,
// camera, dimensions, a video's length), 404 while it has none (not a photo
// or a video, or not read yet).
func (a *filesAPI) meta(w http.ResponseWriter, r *http.Request) {
	f, err := a.store.Lookup(r.Context(), owner(r), r.URL.Query().Get("path"))
	if err == nil && f.IsDir() {
		err = storage.ErrNotFound
	}
	if err != nil {
		failFiles(w, err)
		return
	}
	m, err := mediameta.Get(r.Context(), a.store.Pool, f.SHA256)
	if errors.Is(err, mediameta.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no metadata"})
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	out := metaJSON{
		Latitude: m.Latitude, Longitude: m.Longitude, Make: m.Make, Model: m.Model, Lens: m.Lens,
		Width: m.Width, Height: m.Height, Orientation: m.Orientation, DurationMS: m.DurationMS,
	}
	if m.TakenAt != nil {
		s := m.TakenAt.Format("2006-01-02T15:04:05")
		out.TakenAt = &s
	}
	if m.TakenOffset != nil {
		s := time.Unix(0, 0).In(time.FixedZone("", *m.TakenOffset*60)).Format("-07:00")
		out.TakenOffset = &s
	}
	writeJSON(w, http.StatusOK, out)
}

// known answers which of the given SHA-256 (hex) the account already has, so
// that a client backing up a phone skips them at once.
func (a *filesAPI) known(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SHA256 []string `json:"sha256"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	sums := make([][]byte, 0, len(body.SHA256))
	for _, h := range body.SHA256 {
		sum, err := hex.DecodeString(h)
		if err != nil || len(sum) != 32 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sha256 must be 64 hex characters"})
			return
		}
		sums = append(sums, sum)
	}
	found, err := a.store.Known(r.Context(), owner(r), sums)
	if err != nil {
		failFiles(w, err)
		return
	}
	out := make([]string, len(found))
	for i, sum := range found {
		out[i] = hex.EncodeToString(sum)
	}
	writeJSON(w, http.StatusOK, map[string][]string{"known": out})
}

// dedupe stores at path a content the account already has, without its
// bytes: 201 copied, 200 when path already holds it, 404 when the account
// does not have it (the client then sends it), 409 when path holds another.
func (a *filesAPI) dedupe(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path       string `json:"path"`
		SHA256     string `json:"sha256"`
		ModifiedAt int64  `json:"modified_at"` // Unix seconds; 0 means now
	}
	if !readJSON(w, r, &body) {
		return
	}
	sum, err := hex.DecodeString(body.SHA256)
	if err != nil || len(sum) != 32 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sha256 must be 64 hex characters"})
		return
	}
	var mod time.Time
	if body.ModifiedAt > 0 {
		mod = time.Unix(body.ModifiedAt, 0)
	}
	f, result, err := a.store.Dedupe(r.Context(), owner(r), body.Path, sum, mod)
	if errors.Is(err, storage.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown content: send the file"})
		return
	}
	if err != nil {
		failFiles(w, err)
		return
	}
	status := http.StatusCreated
	if result == storage.DedupePresent {
		status = http.StatusOK
	}
	writeJSON(w, status, entryOf(f))
}

// upload stores the request body at path. Content-Length, when sent, and
// X-Content-SHA256 (hex), when sent, must match the bytes received. With
// replace=1, an existing file is replaced and its content kept as a version.
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
	opt.Replace = r.URL.Query().Get("replace") == "1"
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

// VersionEntry is one previous content of a file.
type VersionEntry struct {
	ID         int64     `json:"id"`
	Size       int64     `json:"size"`
	MIME       string    `json:"mime"`
	SHA256     string    `json:"sha256"`
	ModifiedAt time.Time `json:"modified_at"` // when this content was last modified
	ReplacedAt time.Time `json:"replaced_at"`
}

func versionEntryOf(v storage.Version) VersionEntry {
	return VersionEntry{ID: v.ID, Size: v.Size, MIME: v.MIME, SHA256: hex.EncodeToString(v.SHA256), ModifiedAt: v.ModifiedAt, ReplacedAt: v.CreatedAt}
}

func (a *filesAPI) versions(w http.ResponseWriter, r *http.Request) {
	versions, err := a.store.ListVersions(r.Context(), owner(r), r.URL.Query().Get("path"))
	if err != nil {
		failFiles(w, err)
		return
	}
	out := make([]VersionEntry, len(versions))
	for i, v := range versions {
		out[i] = versionEntryOf(v)
	}
	writeJSON(w, http.StatusOK, out)
}

func versionID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such version"})
		return 0, false
	}
	return id, true
}

// downloadVersion streams a previous content, named after the file and its date.
func (a *filesAPI) downloadVersion(w http.ResponseWriter, r *http.Request) {
	id, ok := versionID(w, r)
	if !ok {
		return
	}
	file, v, err := a.store.OpenVersion(r.Context(), owner(r), id)
	if err != nil {
		failFiles(w, err)
		return
	}
	defer file.Close()
	name := fmt.Sprintf("version-%d", v.ID)
	if r.URL.Query().Get("name") != "" {
		name = r.URL.Query().Get("name")
	}
	w.Header().Set("Content-Type", v.MIME)
	w.Header().Set("ETag", `"`+hex.EncodeToString(v.SHA256)+`"`)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s", urlPathEscape(name)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, name, v.ModifiedAt, file)
}

func (a *filesAPI) restoreVersion(w http.ResponseWriter, r *http.Request) {
	id, ok := versionID(w, r)
	if !ok {
		return
	}
	f, err := a.store.RestoreVersion(r.Context(), owner(r), id)
	if err != nil {
		failFiles(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entryOf(f))
}
