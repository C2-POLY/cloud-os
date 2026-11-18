package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/C2-POLY/cloud-os/core/internal/auth"
	"github.com/C2-POLY/cloud-os/core/internal/embed"
	"github.com/C2-POLY/cloud-os/core/internal/photovec"
	"github.com/C2-POLY/cloud-os/core/internal/share"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/testdb"
)

// newPhotoSearchAPI is an API whose photo search asks the worker at workerURL.
func newPhotoSearchAPI(t *testing.T, workerURL string) (enzo, johan *client, pool *pgxpool.Pool) {
	t.Helper()
	pool, _ = testdb.Migrated(t)
	files := &storage.Store{Root: t.TempDir(), Pool: pool}
	h := NewHandler(Config{
		Version: "test",
		Health:  &fakeStore{},
		Auth:    auth.NewService(pool),
		Files:   files,
		Shares:  share.NewService(pool, files),
		Photos:  &photovec.Searcher{Pool: pool, Embed: &embed.Client{URL: workerURL}},
	})
	enzo = &client{t: t, h: h}
	enzo.do("POST", "/api/setup", `{"name":"enzo","password":"`+pw+`"}`)
	enzo.do("POST", "/api/accounts", `{"name":"johan","password":"`+pw+`"}`)
	johan = &client{t: t, h: h}
	johan.do("POST", "/api/login", `{"name":"johan","password":"`+pw+`"}`)
	return enzo, johan, pool
}

// worker answers every query with the unit vector along the first axis.
func worker(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v := make([]float32, 768)
		v[0] = 1
		_ = json.NewEncoder(w).Encode(map[string]any{"model": photovec.Model, "version": "v1", "vectors": [][]float32{v}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPhotoSearchReturnsTheAccountsPhotosWithTheirScore(t *testing.T) {
	enzo, johan, pool := newPhotoSearchAPI(t, worker(t).URL)
	for _, p := range []string{"Plage/chien.jpg", "Maison/chat.jpg"} {
		if rec := enzo.upload(p, "photo "+p, nil); rec.Code != http.StatusCreated {
			t.Fatalf("upload %s: %d", p, rec.Code)
		}
	}
	// The vectors the worker would write: the dog is the query's, the cat is not.
	for p, lead := range map[string]string{"Plage/chien.jpg": "1", "Maison/chat.jpg": "0"} {
		sum := sha256.Sum256([]byte("photo " + p))
		if _, err := pool.Exec(context.Background(), `INSERT INTO photo_vectors_siglip2_b16_224 (sha256, model, version, source, embedding)
			VALUES ($1, $2, 'v1', 'image', ('[' || $3 || ',0.5' || repeat(',0', 766) || ']')::vector)`, sum[:], photovec.Model, lead); err != nil {
			t.Fatal(err)
		}
	}

	rec := enzo.get("/api/search/photos?q=chien%20%C3%A0%20la%20plage", nil)
	var hits []PhotoHit
	if err := json.Unmarshal(rec.Body.Bytes(), &hits); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("search = %d %s", rec.Code, rec.Body)
	}
	if len(hits) != 1 || hits[0].Path != "Plage/chien.jpg" || hits[0].Name != "chien.jpg" || hits[0].MIME != "image/jpeg" ||
		hits[0].SHA256 == "" || hits[0].Score < 0.89 || hits[0].Score > 0.9 {
		t.Errorf("hits = %+v", hits)
	}
	if rec := johan.get("/api/search/photos?q=chien", nil); rec.Code != http.StatusOK || rec.Body.String() != "[]\n" {
		t.Errorf("johan's search = %d %s", rec.Code, rec.Body)
	}
	if rec := enzo.get("/api/search/photos?q=", nil); rec.Code != http.StatusOK || rec.Body.String() != "[]\n" {
		t.Errorf("empty search = %d %s", rec.Code, rec.Body)
	}
}

func TestPhotoSearchAnswers503WithoutAWorker(t *testing.T) {
	stopped := httptest.NewServer(http.NotFoundHandler())
	stopped.Close()
	enzo, _, _ := newPhotoSearchAPI(t, stopped.URL)
	if rec := enzo.get("/api/search/photos?q=plage", nil); rec.Code != http.StatusServiceUnavailable || rec.Body.String() != "{\"error\":\"photo search unavailable\"}\n" {
		t.Errorf("search = %d %s", rec.Code, rec.Body)
	}
	// A core built without the photo search answers the same.
	_, h := newAPI(t)
	c := &client{t: t, h: h}
	c.do("POST", "/api/setup", `{"name":"enzo","password":"`+pw+`"}`)
	if rec := c.get("/api/search/photos?q=plage", nil); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("search without Photos = %d %s", rec.Code, rec.Body)
	}
}
