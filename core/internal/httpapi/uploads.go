package httpapi

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/C2-POLY/cloud-os/core/internal/storage"
)

// tus 1.0 (https://tus.io/protocols/resumable-upload): core protocol and the
// creation, expiration and termination extensions.
const (
	tusVersion    = "1.0.0"
	tusExtensions = "creation,expiration,termination"
	tusChunkType  = "application/offset+octet-stream"
)

type uploadsAPI struct {
	store   *storage.Store
	maxSize int64 // 0: only the free space limits an upload
}

func tusHeaders(w http.ResponseWriter) {
	w.Header().Set("Tus-Resumable", tusVersion)
	w.Header().Set("Cache-Control", "no-store")
}

// tusRequest checks the Tus-Resumable header; it answers 412 itself and returns false otherwise.
func tusRequest(w http.ResponseWriter, r *http.Request) bool {
	tusHeaders(w)
	if r.Header.Get("Tus-Resumable") != tusVersion {
		w.Header().Set("Tus-Version", tusVersion)
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{"error": "Tus-Resumable: 1.0.0 is required"})
		return false
	}
	return true
}

// tusFail maps a storage error to the status tus clients expect.
func tusFail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrOffset):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case errors.Is(err, storage.ErrBusy):
		writeJSON(w, http.StatusLocked, map[string]string{"error": err.Error()})
	case errors.Is(err, storage.ErrTooLarge):
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": err.Error()})
	default:
		failFiles(w, err)
	}
}

func expires(t time.Time) string { return t.UTC().Format(http.TimeFormat) }

func (a *uploadsAPI) options(w http.ResponseWriter, _ *http.Request) {
	tusHeaders(w)
	w.Header().Set("Tus-Version", tusVersion)
	w.Header().Set("Tus-Extension", tusExtensions)
	if a.maxSize > 0 {
		w.Header().Set("Tus-Max-Size", strconv.FormatInt(a.maxSize, 10))
	}
	w.WriteHeader(http.StatusNoContent)
}

// parseMetadata reads Upload-Metadata: comma-separated "key base64value" pairs.
func parseMetadata(h string) (map[string]string, bool) {
	meta := map[string]string{}
	for _, pair := range strings.Split(h, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		key, b64, _ := strings.Cut(pair, " ")
		val, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, false
		}
		meta[key] = string(val)
	}
	return meta, true
}

// create starts an upload. Upload-Metadata must give "path"; "sha256" (hex),
// "mtime" (Unix seconds) and "replace" ("1": replace an existing file, whose
// content is kept as a version) are optional.
func (a *uploadsAPI) create(w http.ResponseWriter, r *http.Request) {
	if !tusRequest(w, r) {
		return
	}
	length, err := strconv.ParseInt(r.Header.Get("Upload-Length"), 10, 64)
	if err != nil || length < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Upload-Length must be a byte count (deferred length is not supported)"})
		return
	}
	meta, ok := parseMetadata(r.Header.Get("Upload-Metadata"))
	if !ok || meta["path"] == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Upload-Metadata must give the path, base64-encoded"})
		return
	}
	var sum []byte
	if h := meta["sha256"]; h != "" {
		if sum, err = hex.DecodeString(h); err != nil || len(sum) != 32 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sha256 must be 64 hex characters"})
			return
		}
	}
	var mod *time.Time
	if h := meta["mtime"]; h != "" {
		sec, err := strconv.ParseInt(h, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mtime must be Unix seconds"})
			return
		}
		t := time.Unix(sec, 0)
		mod = &t
	}

	u, err := a.store.CreateUpload(r.Context(), owner(r), meta["path"], length, sum, mod, a.maxSize, meta["replace"] == "1")
	if err != nil {
		tusFail(w, err)
		return
	}
	if length == 0 {
		if _, err := a.store.FinishEmptyUpload(r.Context(), u); err != nil {
			tusFail(w, err)
			return
		}
	}
	w.Header().Set("Location", "/api/uploads/"+u.ID)
	w.Header().Set("Upload-Expires", expires(u.ExpiresAt))
	w.WriteHeader(http.StatusCreated)
}

func (a *uploadsAPI) head(w http.ResponseWriter, r *http.Request) {
	if !tusRequest(w, r) {
		return
	}
	u, err := a.store.GetUpload(r.Context(), owner(r), r.PathValue("id"))
	if err != nil {
		tusFail(w, err)
		return
	}
	w.Header().Set("Upload-Offset", strconv.FormatInt(u.Offset, 10))
	w.Header().Set("Upload-Length", strconv.FormatInt(u.Length, 10))
	w.Header().Set("Upload-Expires", expires(u.ExpiresAt))
	w.WriteHeader(http.StatusOK)
}

func (a *uploadsAPI) patch(w http.ResponseWriter, r *http.Request) {
	if !tusRequest(w, r) {
		return
	}
	if r.Header.Get("Content-Type") != tusChunkType {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "Content-Type must be " + tusChunkType})
		return
	}
	offset, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
	if err != nil || offset < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Upload-Offset must be a byte count"})
		return
	}
	u, _, err := a.store.AppendUpload(r.Context(), owner(r), r.PathValue("id"), offset, r.Body)
	if u.ID != "" {
		w.Header().Set("Upload-Offset", strconv.FormatInt(u.Offset, 10))
		w.Header().Set("Upload-Expires", expires(u.ExpiresAt))
	}
	if err != nil {
		tusFail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *uploadsAPI) terminate(w http.ResponseWriter, r *http.Request) {
	if !tusRequest(w, r) {
		return
	}
	if err := a.store.DeleteUpload(r.Context(), owner(r), r.PathValue("id")); err != nil {
		tusFail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
