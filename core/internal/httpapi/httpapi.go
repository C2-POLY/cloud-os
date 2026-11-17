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

// NewHandler returns the core's routes. staticDir, when not empty, is served at /.
func NewHandler(version string, store HealthStore, staticDir string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", healthHandler(version, store))
	if staticDir != "" {
		mux.Handle("/", spa(staticDir))
	}
	return mux
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
