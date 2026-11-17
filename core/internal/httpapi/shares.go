package httpapi

import (
	"archive/zip"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/C2-POLY/cloud-os/core/internal/activity"
	"github.com/C2-POLY/cloud-os/core/internal/auth"
	"github.com/C2-POLY/cloud-os/core/internal/share"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
)

// GrantCookie holds the access a guest opened with the password of a link. Its
// path is the link's own API path, so each link has its own.
const GrantCookie = "cloudos_share"

// sharesAPI serves the owner's links (member routes) and the guest's read-only
// view through a link (public routes under /api/s/{token}).
type sharesAPI struct {
	shares   *share.Service
	activity *activity.Log
}

// failShares maps a share or storage error to its status.
func failShares(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, share.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": share.ErrNotFound.Error()})
	case errors.Is(err, share.ErrPasswordRequired):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": share.ErrPasswordRequired.Error()})
	case errors.Is(err, share.ErrWrongPassword):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": share.ErrWrongPassword.Error()})
	case errors.Is(err, share.ErrInvalid):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	case errors.Is(err, auth.ErrThrottled):
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": err.Error()})
	default:
		failFiles(w, err)
	}
}

// ShareEntry is one link in its owner's list.
type ShareEntry struct {
	share.Listed
	Name string `json:"name"`
}

func shareEntryOf(l share.Listed) ShareEntry {
	return ShareEntry{Listed: l, Name: entryOf(storage.File{Path: l.Path}).Name}
}

func (a *sharesAPI) create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path      string    `json:"path"`
		ExpiresAt time.Time `json:"expires_at"`
		Password  string    `json:"password"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	sh, token, err := a.shares.Create(r.Context(), owner(r), body.Path, body.ExpiresAt, body.Password)
	if err != nil {
		failShares(w, err)
		return
	}
	f, err := a.shares.Files.Lookup(r.Context(), sh.Owner, body.Path)
	if err != nil {
		failShares(w, err)
		return
	}
	entry := shareEntryOf(share.Listed{Share: sh, Path: f.Path, Kind: f.Kind, Size: f.Size})
	writeJSON(w, http.StatusCreated, map[string]any{"share": entry, "token": token})
}

func (a *sharesAPI) list(w http.ResponseWriter, r *http.Request) {
	links, err := a.shares.List(r.Context(), owner(r))
	if err != nil {
		failShares(w, err)
		return
	}
	out := make([]ShareEntry, len(links))
	for i, l := range links {
		out[i] = shareEntryOf(l)
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *sharesAPI) revoke(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		failShares(w, share.ErrNotFound)
		return
	}
	if err := a.shares.Revoke(r.Context(), owner(r), id); err != nil {
		failShares(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// guestHeaders keep a link's token out of other sites' logs and search
// engines, and its answers out of every cache, so a revoked link serves nothing.
func guestHeaders(w http.ResponseWriter) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("Cache-Control", "no-store")
}

// guest resolves the link of the request and checks its password access. It
// answers the error itself and returns false when the guest may not read.
func (a *sharesAPI) guest(w http.ResponseWriter, r *http.Request) (share.Share, storage.File, bool) {
	guestHeaders(w)
	if a.shares == nil {
		failShares(w, share.ErrNotFound)
		return share.Share{}, storage.File{}, false
	}
	sh, item, err := a.shares.Resolve(r.Context(), r.PathValue("token"))
	if err == nil {
		grant := ""
		if c, cerr := r.Cookie(GrantCookie); cerr == nil {
			grant = c.Value
		}
		err = a.shares.Check(r.Context(), sh, grant)
	}
	if err != nil {
		failShares(w, err)
		return share.Share{}, storage.File{}, false
	}
	return sh, item, true
}

// GuestItem is what a guest sees of the shared item.
type GuestItem struct {
	Name       string    `json:"name"`
	Kind       string    `json:"kind"`
	Size       int64     `json:"size"`
	MIME       string    `json:"mime"`
	ModifiedAt time.Time `json:"modified_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

func (a *sharesAPI) info(w http.ResponseWriter, r *http.Request) {
	sh, item, ok := a.guest(w, r)
	if !ok {
		return
	}
	// The guest's page asks for this once per visit: one "opened" in the owner's journal.
	a.activity.Record(r.Context(), sh.Owner, activity.ShareOpened, item.Path)
	e := entryOf(item)
	writeJSON(w, http.StatusOK, GuestItem{Name: e.Name, Kind: e.Kind, Size: e.Size, MIME: e.MIME, ModifiedAt: e.ModifiedAt, ExpiresAt: sh.ExpiresAt})
}

// unlock checks the password of a link and gives the guest its access cookie.
func (a *sharesAPI) unlock(w http.ResponseWriter, r *http.Request) {
	guestHeaders(w)
	var body struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if a.shares == nil {
		failShares(w, share.ErrNotFound)
		return
	}
	token := r.PathValue("token")
	sh, _, err := a.shares.Resolve(r.Context(), token)
	if err != nil {
		failShares(w, err)
		return
	}
	grant, expires, err := a.shares.Unlock(r.Context(), sh, body.Password, clientIP(r))
	if err != nil {
		failShares(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     GrantCookie,
		Value:    grant,
		Path:     "/api/s/" + token,
		MaxAge:   max(1, int(time.Until(expires).Seconds())),
		HttpOnly: true,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteStrictMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

// guestEntry is an Entry whose path is relative to the shared item, so the
// guest never learns where the item sits in its owner's space.
func guestEntry(item, f storage.File) Entry {
	e := entryOf(f)
	e.Path = share.Relative(item, f.Path)
	return e
}

func (a *sharesAPI) files(w http.ResponseWriter, r *http.Request) {
	_, item, ok := a.guest(w, r)
	if !ok {
		return
	}
	dir, err := share.Within(item, r.URL.Query().Get("path"))
	if err != nil {
		failShares(w, err)
		return
	}
	files, err := a.shares.Files.List(r.Context(), item.Owner, dir)
	if err != nil {
		failShares(w, err)
		return
	}
	entries := make([]Entry, len(files))
	for i, f := range files {
		entries[i] = guestEntry(item, f)
	}
	writeJSON(w, http.StatusOK, entries)
}

// attachment marks a download: always saved, never shown in the page, and
// sandboxed if a browser opens it anyway, so a shared HTML file runs nothing
// on the box's origin.
func attachment(w http.ResponseWriter, name, mime string) {
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s", urlPathEscape(name)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox")
}

func (a *sharesAPI) content(w http.ResponseWriter, r *http.Request) {
	_, item, ok := a.guest(w, r)
	if !ok {
		return
	}
	p, err := share.Within(item, r.URL.Query().Get("path"))
	if err != nil {
		failShares(w, err)
		return
	}
	f, err := a.shares.Files.Lookup(r.Context(), item.Owner, p)
	if err == nil && f.IsDir() {
		err = storage.ErrNotFound
	}
	if err != nil {
		failShares(w, err)
		return
	}
	file, err := a.shares.Files.OpenFile(f)
	if err != nil {
		failShares(w, err)
		return
	}
	defer file.Close()
	name := entryOf(f).Name
	attachment(w, name, f.MIME)
	w.Header().Set("ETag", `"`+hex.EncodeToString(f.SHA256)+`"`)
	http.ServeContent(w, r, name, f.ModifiedAt, file)
}

// zip streams a folder of the subtree as a zip, without compression (photos
// and videos already are) and without a temporary file.
func (a *sharesAPI) zip(w http.ResponseWriter, r *http.Request) {
	_, item, ok := a.guest(w, r)
	if !ok {
		return
	}
	dir, err := share.Within(item, r.URL.Query().Get("path"))
	if err != nil {
		failShares(w, err)
		return
	}
	files, err := a.shares.Files.Tree(r.Context(), item.Owner, dir)
	if err != nil {
		failShares(w, err)
		return
	}
	folder := entryOf(storage.File{Path: dir}).Name
	attachment(w, folder+".zip", "application/zip")
	zw := zip.NewWriter(w)
	for _, f := range files {
		src, err := a.shares.Files.OpenFile(f)
		if errors.Is(err, fs.ErrNotExist) {
			continue // moved or trashed since the listing
		}
		if err == nil {
			err = addToZip(zw, src, f, folder+"/"+f.Path[len(dir)+1:])
		}
		if err != nil {
			// The headers are gone: cut the archive short so the guest sees a broken download.
			slog.Warn("share zip", "owner", item.Owner, "path", f.Path, "err", err)
			return
		}
	}
	if err := zw.Close(); err != nil {
		slog.Warn("share zip", "owner", item.Owner, "err", err)
	}
}

func addToZip(zw *zip.Writer, src *os.File, f storage.File, name string) error {
	defer src.Close()
	dst, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store, Modified: f.ModifiedAt})
	if err != nil {
		return err
	}
	_, err = io.Copy(dst, src)
	return err
}
