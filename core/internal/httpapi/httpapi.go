// Package httpapi serves the core's HTTP API and, when given, the built web front.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/C2-POLY/cloud-os/core/internal/activity"
	"github.com/C2-POLY/cloud-os/core/internal/albums"
	"github.com/C2-POLY/cloud-os/core/internal/auth"
	"github.com/C2-POLY/cloud-os/core/internal/davfs"
	"github.com/C2-POLY/cloud-os/core/internal/db"
	"github.com/C2-POLY/cloud-os/core/internal/doccat"
	"github.com/C2-POLY/cloud-os/core/internal/photovec"
	"github.com/C2-POLY/cloud-os/core/internal/share"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/tasks"
	"github.com/C2-POLY/cloud-os/core/internal/textemb"
	"golang.org/x/net/webdav"
)

// WorkerWindow is how recent a heartbeat must be for a worker to count as present.
const WorkerWindow = 30 * time.Second

// HealthStore is what the health endpoint reads from the database.
type HealthStore interface {
	Ping(ctx context.Context) error
	MigrationVersion(ctx context.Context) (int64, error)
	RecentWorkers(ctx context.Context, window time.Duration) ([]db.Worker, error)
	TaskStats(ctx context.Context) (tasks.Stats, error)
}

// Health is the body of GET /api/health.
type Health struct {
	Version   string       `json:"version"`
	DB        string       `json:"db"`
	Migration *int64       `json:"migration,omitempty"`
	Workers   []db.Worker  `json:"workers,omitempty"`
	Tasks     *tasks.Stats `json:"tasks,omitempty"`
}

// Config is what the core's routes need.
type Config struct {
	Version string
	Health  HealthStore
	Auth    *auth.Service
	Files   *storage.Store
	Shares  *share.Service
	Albums  *albums.Service
	// Photos searches photos by what they show; nil answers 503.
	Photos *photovec.Searcher
	// Documents searches documents by meaning; nil answers 503.
	Documents *textemb.Searcher
	// Categories lists the documents by category and keeps the members' choices.
	Categories *doccat.Store
	// MapsDir holds the .pmtiles file of the Photos map; "" when none.
	MapsDir   string
	Activity  *activity.Log // nil: no journal (tests that do not need it)
	MaxUpload int64         // bytes; 0 means only the free space limits an upload
	StaticDir string        // the built web front, served at / when not empty
}

// access says who may call a route.
type access int

const (
	public access = iota // anyone: health, setup, login, and the guest's read-only routes of a share link
	member               // any signed-in account
	admin                // signed-in admins only
)

type route struct {
	pattern string
	access  access
	handler http.HandlerFunc
}

// routes lists every API route with who may call it. Anything not public goes
// through RequireSession; the access test walks this list.
func routes(c Config) []route {
	a := &accountsAPI{auth: c.Auth}
	f := &filesAPI{store: c.Files}
	u := &uploadsAPI{store: c.Files, maxSize: c.MaxUpload}
	t := &trashAPI{store: c.Files}
	s := &sharesAPI{shares: c.Shares, activity: c.Activity}
	d := &desktopAPI{files: c.Files, activity: c.Activity}
	m := &mapAPI{dir: c.MapsDir, files: c.Files}
	al := &albumsAPI{albums: c.Albums}
	ps := &photoSearchAPI{photos: c.Photos}
	sem := &semanticAPI{docs: c.Documents}
	dc := &documentsAPI{docs: c.Categories}
	return []route{
		{"GET /api/health", public, healthHandler(c.Version, c.Health)},
		{"GET /api/setup", public, a.setupNeeded},
		{"POST /api/setup", public, a.setup},
		{"POST /api/login", public, a.login},
		{"POST /api/logout", public, a.logout},
		{"GET /api/me", member, a.me},
		{"GET /api/accounts", admin, a.list},
		{"POST /api/accounts", admin, a.create},
		{"PATCH /api/accounts/{name}", admin, a.update},
		{"GET /api/files", member, f.list},
		{"POST /api/files/folders", member, f.mkdir},
		{"POST /api/files/move", member, f.move},
		{"GET /api/files/content", member, f.download},
		{"GET /api/files/thumb", member, f.thumb},
		{"GET /api/files/meta", member, f.meta},
		{"GET /api/search", member, f.search},
		{"GET /api/search/content", member, f.searchContent},
		{"GET /api/search/photos", member, ps.search},
		{"GET /api/search/semantic", member, sem.search},
		{"GET /api/documents/categories", member, dc.categories},
		{"GET /api/documents", member, dc.list},
		{"PUT /api/documents/category", member, dc.set},
		{"POST /api/files/known", member, f.known},
		{"POST /api/files/dedupe", member, f.dedupe},
		{"GET /api/files/recent", member, d.recent},
		{"GET /api/activity", member, d.events},
		{"GET /api/timeline", member, d.timeline},
		{"GET /api/timeline/months", member, d.timelineMonths},
		{"PUT /api/files/content", member, f.upload},
		{"OPTIONS /api/uploads", member, u.options},
		{"POST /api/uploads", member, u.create},
		{"HEAD /api/uploads/{id}", member, u.head},
		{"PATCH /api/uploads/{id}", member, u.patch},
		{"DELETE /api/uploads/{id}", member, u.terminate},
		{"POST /api/files/trash", member, t.trash},
		{"GET /api/files/versions", member, f.versions},
		{"GET /api/files/versions/{id}/content", member, f.downloadVersion},
		{"POST /api/files/versions/{id}/restore", member, f.restoreVersion},
		{"GET /api/trash", member, t.list},
		{"POST /api/trash/{id}/restore", member, t.restore},
		{"POST /api/trash/{id}/purge", member, t.purge},
		{"POST /api/trash/empty", member, t.empty},
		{"GET /api/map", member, m.info},
		{"GET /api/map/tiles.pmtiles", member, m.tileFile},
		{"GET /api/map/points", member, m.points},
		{"GET /api/albums", member, al.list},
		{"POST /api/albums", member, al.create},
		{"GET /api/albums/{id}", member, al.get},
		{"PATCH /api/albums/{id}", member, al.rename},
		{"DELETE /api/albums/{id}", member, al.remove},
		{"GET /api/albums/{id}/items", member, al.items},
		{"POST /api/albums/{id}/items", member, al.change(true)},
		{"POST /api/albums/{id}/items/remove", member, al.change(false)},
		{"POST /api/shares", member, s.create},
		{"GET /api/shares", member, s.list},
		{"DELETE /api/shares/{id}", member, s.revoke},
		// A guest reads through a link without an account: these only ever
		// read, and unlock only checks a password. Nothing here writes,
		// renames or deletes.
		{"GET /api/s/{token}", public, s.info},
		{"POST /api/s/{token}/unlock", public, s.unlock},
		{"GET /api/s/{token}/files", public, s.files},
		{"GET /api/s/{token}/content", public, s.content},
		{"GET /api/s/{token}/zip", public, s.zip},
	}
}

// NewHandler returns the core's routes.
func NewHandler(c Config) http.Handler {
	mux := http.NewServeMux()
	for _, rt := range routes(c) {
		var h http.Handler = rt.handler
		switch rt.access {
		case admin:
			h = c.Auth.RequireSession(auth.RequireAdmin(h))
		case member:
			h = c.Auth.RequireSession(h)
		}
		mux.Handle(rt.pattern, h)
	}
	if c.Auth != nil && c.Files != nil {
		mux.Handle("/dav/", davHandler(c))
		mux.Handle("/dav", http.RedirectHandler("/dav/", http.StatusMovedPermanently))
	}
	mux.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such route"})
	}))
	if c.StaticDir != "" {
		mux.Handle("/", spa(c.StaticDir))
	}
	return auth.RequireJSON(mux)
}

func healthHandler(version string, store HealthStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		down := Health{Version: version, DB: "down"}
		if err := store.Ping(ctx); err != nil {
			slog.Warn("health: database unreachable", "err", err)
			writeJSON(w, http.StatusServiceUnavailable, down)
			return
		}
		migration, err := store.MigrationVersion(ctx)
		if err != nil {
			slog.Warn("health: migration version", "err", err)
			writeJSON(w, http.StatusServiceUnavailable, down)
			return
		}
		workers, err := store.RecentWorkers(ctx, WorkerWindow)
		if err != nil {
			slog.Warn("health: workers", "err", err)
			writeJSON(w, http.StatusServiceUnavailable, down)
			return
		}
		stats, err := store.TaskStats(ctx)
		if err != nil {
			slog.Warn("health: tasks", "err", err)
			writeJSON(w, http.StatusServiceUnavailable, down)
			return
		}
		writeJSON(w, http.StatusOK, Health{Version: version, DB: "ok", Migration: &migration, Workers: workers, Tasks: &stats})
	}
}

// spa serves files from dir and answers index.html for client-side routes.
// Under /assets/ and /map/ (the build's files, the map's fonts and
// pictograms) a missing file is a 404: index.html there would be read as a
// script, a font or an image.
func spa(dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/s/") {
			guestHeaders(w) // the guest page of a share link carries its token
		}
		path := filepath.Join(dir, filepath.Clean("/"+r.URL.Path))
		if info, err := os.Stat(path); err != nil || info.IsDir() && r.URL.Path != "/" {
			if strings.HasPrefix(r.URL.Path, "/assets/") || strings.HasPrefix(r.URL.Path, "/map/") {
				http.NotFound(w, r)
				return
			}
			http.ServeFile(w, r, filepath.Join(dir, "index.html"))
			return
		}
		files.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("write response", "err", err)
	}
}

// davHandler serves each account's space over WebDAV at /dav/, for the Finder
// and the Explorer, behind HTTP Basic with the account's password.
func davHandler(c Config) http.Handler {
	h := &webdav.Handler{
		Prefix:     "/dav",
		FileSystem: &davfs.FS{Store: c.Files, AppleFiles: &davfs.AppleDouble{Root: c.Files.Root}},
		LockSystem: webdav.NewMemLS(),
		Logger: func(r *http.Request, err error) {
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				slog.Warn("webdav", "method", r.Method, "path", r.URL.Path, "err", err)
			}
		},
	}
	return c.Auth.NewBasicAuth("Cloud OS").Require(h)
}
