package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/C2-POLY/cloud-os/core/internal/db"
	"github.com/C2-POLY/cloud-os/core/internal/tasks"
)

type fakeStore struct {
	pingErr error
	version int64
	workers []db.Worker
	window  time.Duration
}

func (f *fakeStore) Ping(context.Context) error { return f.pingErr }

func (f *fakeStore) MigrationVersion(context.Context) (int64, error) { return f.version, nil }

func (f *fakeStore) TaskStats(context.Context) (tasks.Stats, error) {
	return tasks.Stats{Queued: 2, Running: 1}, nil
}

func (f *fakeStore) RecentWorkers(_ context.Context, window time.Duration) ([]db.Worker, error) {
	f.window = window
	return f.workers, nil
}

func getHealth(t *testing.T, store HealthStore) (int, Health) {
	t.Helper()
	rec := httptest.NewRecorder()
	NewHandler(Config{Version: "1.2.3", Health: store}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	var body Health
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return rec.Code, body
}

func TestHealthOK(t *testing.T) {
	seen := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	store := &fakeStore{version: 1, workers: []db.Worker{{ID: "worker-a", SeenAt: seen}}}

	code, body := getHealth(t, store)

	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if body.Version != "1.2.3" || body.DB != "ok" {
		t.Errorf("body = %+v", body)
	}
	if body.Migration == nil || *body.Migration != 1 {
		t.Errorf("migration = %v, want 1", body.Migration)
	}
	if len(body.Workers) != 1 || body.Workers[0].ID != "worker-a" || !body.Workers[0].SeenAt.Equal(seen) {
		t.Errorf("workers = %+v", body.Workers)
	}
	if body.Tasks == nil || body.Tasks.Queued != 2 || body.Tasks.Running != 1 {
		t.Errorf("tasks = %+v", body.Tasks)
	}
	if store.window != 30*time.Second {
		t.Errorf("worker window = %v, want 30s", store.window)
	}
}

func TestHealthDatabaseDown(t *testing.T) {
	code, body := getHealth(t, &fakeStore{pingErr: errors.New("connection refused")})

	if code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", code)
	}
	if body.Version != "1.2.3" || body.DB != "down" || body.Migration != nil || body.Workers != nil {
		t.Errorf("body = %+v", body)
	}
}

func TestStaticFallsBackToIndex(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<p>app</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.js"), []byte("js"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(Config{Version: "1.2.3", Health: &fakeStore{}, StaticDir: dir})

	for path, want := range map[string]string{"/": "app", "/photos/2026": "app", "/app.js": "js"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("GET %s = %d %q, want %q", path, rec.Code, rec.Body.String(), want)
		}
	}
}
