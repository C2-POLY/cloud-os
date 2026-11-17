// Package httpapi serves the core's HTTP API and, when given, the built web front.
package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/C2-POLY/cloud-os/core/internal/auth"
	"github.com/C2-POLY/cloud-os/core/internal/db"
)

// WorkerWindow is how recent a heartbeat must be for a worker to count as present.
const WorkerWindow = 30 * time.Second

// HealthStore is what the health endpoint reads from the database.
type HealthStore interface {
	Ping(ctx context.Context) error
	MigrationVersion(ctx context.Context) (int64, error)
	RecentWorkers(ctx context.Context, window time.Duration) ([]db.Worker, error)
}

// Health is the body of GET /api/health.
type Health struct {
	Version   string      `json:"version"`
	DB        string      `json:"db"`
	Migration *int64      `json:"migration,omitempty"`
	Workers   []db.Worker `json:"workers,omitempty"`
}

// Config is what the core's routes need.
type Config struct {
	Version   string
	Health    HealthStore
	Auth      *auth.Service
	StaticDir string // the built web front, served at / when not empty
}

// access says who may call a route.
type access int

const (
	public access = iota // anyone: health, setup, login, and later share links
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
		writeJSON(w, http.StatusOK, Health{Version: version, DB: "ok", Migration: &migration, Workers: workers})
	}
}

// spa serves files from dir and falls back to index.html for client-side routes.
func spa(dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := filepath.Join(dir, filepath.Clean("/"+r.URL.Path))
		if info, err := os.Stat(path); err != nil || info.IsDir() && r.URL.Path != "/" {
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
