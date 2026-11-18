package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/C2-POLY/cloud-os/core/internal/activity"
	"github.com/C2-POLY/cloud-os/core/internal/auth"
	"github.com/C2-POLY/cloud-os/core/internal/share"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/testdb"
	"github.com/jackc/pgx/v5/pgxpool"
)

// newMapAPI is newFilesAPIPool with a maps folder.
func newMapAPI(t *testing.T, mapsDir string) (enzo, johan *client, pool *pgxpool.Pool) {
	t.Helper()
	pool, _ = testdb.Migrated(t)
	files := &storage.Store{Root: t.TempDir(), Pool: pool}
	h := NewHandler(Config{
		Version:  "test",
		Health:   &fakeStore{},
		Auth:     auth.NewService(pool),
		Files:    files,
		Shares:   share.NewService(pool, files),
		Activity: &activity.Log{Pool: pool},
		MapsDir:  mapsDir,
	})
	enzo = &client{t: t, h: h}
	enzo.do("POST", "/api/setup", `{"name":"enzo","password":"`+pw+`"}`)
	enzo.do("POST", "/api/accounts", `{"name":"johan","password":"`+pw+`"}`)
	johan = &client{t: t, h: h}
	johan.do("POST", "/api/login", `{"name":"johan","password":"`+pw+`"}`)
	return enzo, johan, pool
}

func TestMapSaysWhenNoTilesAreInstalled(t *testing.T) {
	for _, dir := range []string{"", t.TempDir()} {
		enzo, _, _ := newMapAPI(t, dir)
		rec := enzo.get("/api/map", nil)
		if rec.Code != http.StatusOK || rec.Body.String() != "{\"tiles\":false}\n" {
			t.Errorf("dir %q: map = %d %s", dir, rec.Code, rec.Body)
		}
		if rec := enzo.get("/api/map/tiles.pmtiles", nil); rec.Code != http.StatusNotFound {
			t.Errorf("dir %q: tiles = %d, want 404", dir, rec.Code)
		}
	}
}

func TestMapServesTheTilesByRange(t *testing.T) {
	dir := t.TempDir()
	content := []byte("PMTiles\x03 and the rest of an archive")
	if err := os.WriteFile(filepath.Join(dir, "europe.pmtiles"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	enzo, _, _ := newMapAPI(t, dir)
	if rec := enzo.get("/api/map", nil); rec.Body.String() != "{\"tiles\":true,\"name\":\"europe.pmtiles\"}\n" {
		t.Errorf("map = %s", rec.Body)
	}
	rec := enzo.get("/api/map/tiles.pmtiles", map[string]string{"Range": "bytes=0-6"})
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "PMTiles" || rec.Header().Get("Content-Range") != "bytes 0-6/"+strconv.Itoa(len(content)) {
		t.Errorf("range = %d %q %q", rec.Code, rec.Body, rec.Header().Get("Content-Range"))
	}
	if anon := (&client{t: t, h: enzo.h}).get("/api/map/tiles.pmtiles", nil); anon.Code != http.StatusUnauthorized {
		t.Errorf("without a session: %d, want 401", anon.Code)
	}
}

func TestMapPointsAreTheAccountsLivePhotosWithAPosition(t *testing.T) {
	enzo, johan, pool := newMapAPI(t, "")
	ctx := context.Background()
	for _, p := range []string{"Photos/tour.jpg", "Photos/sans-gps.jpg", "Photos/jetee.jpg"} {
		if rec := enzo.upload(p, "image "+p, nil); rec.Code != http.StatusCreated {
			t.Fatalf("upload %s: %d", p, rec.Code)
		}
	}
	at := func(p string, lat, lon *float64) {
		sum := sha256.Sum256([]byte("image " + p))
		if _, err := pool.Exec(ctx, `INSERT INTO media_meta (sha256, taken_at, latitude, longitude) VALUES ($1, '2024-07-14 18:03:22', $2, $3)`, sum[:], lat, lon); err != nil {
			t.Fatal(err)
		}
	}
	lat, lon := 48.8584, 2.2945
	at("Photos/tour.jpg", &lat, &lon)
	at("Photos/sans-gps.jpg", nil, nil)
	at("Photos/jetee.jpg", &lat, &lon)
	if rec := enzo.do("POST", "/api/files/trash", `{"path":"Photos/jetee.jpg"}`); rec.Code != http.StatusOK {
		t.Fatalf("trash: %d", rec.Code)
	}
	rec := enzo.get("/api/map/points?tz=Europe/Paris", nil)
	var points []MapPoint
	if err := json.Unmarshal(rec.Body.Bytes(), &points); err != nil || len(points) != 1 {
		t.Fatalf("points = %d %s", rec.Code, rec.Body)
	}
	if p := points[0]; p.Path != "Photos/tour.jpg" || p.Lat != lat || p.Lon != lon || p.At != "2024-07-14T18:03:22" || !p.Taken {
		t.Errorf("point = %+v", p)
	}
	if rec := johan.get("/api/map/points", nil); rec.Body.String() != "[]\n" {
		t.Errorf("johan's points = %s", rec.Body)
	}
}
