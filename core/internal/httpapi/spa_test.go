package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestSPAServesRoutesButNotMissingAssets(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{"index.html": "<html>", "map/fonts/a.pbf": "glyphs"} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := spa(dir)
	for _, c := range []struct {
		path string
		code int
		body string
	}{
		{"/photos/carte", http.StatusOK, "<html>"},
		{"/map/fonts/a.pbf", http.StatusOK, "glyphs"},
		{"/map/fonts/missing.pbf", http.StatusNotFound, ""},
		{"/assets/gone-1234.js", http.StatusNotFound, ""},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", c.path, nil))
		if rec.Code != c.code || (c.body != "" && rec.Body.String() != c.body) {
			t.Errorf("%s = %d %q, want %d %q", c.path, rec.Code, rec.Body, c.code, c.body)
		}
	}
}
