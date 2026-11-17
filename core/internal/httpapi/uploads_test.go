package httpapi

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/C2-POLY/cloud-os/core/internal/auth"
)

// tus sends a tus request as tus-js-client does, with the Cloud OS client header.
func (c *client) tus(method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	c.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set(auth.ClientHeader, "tus-js-client")
	if method == "PATCH" {
		req.Header.Set("Content-Type", "application/offset+octet-stream")
	}
	for k, v := range headers {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	return rec
}

func meta(kv ...string) string {
	var parts []string
	for i := 0; i < len(kv); i += 2 {
		parts = append(parts, kv[i]+" "+base64.StdEncoding.EncodeToString([]byte(kv[i+1])))
	}
	return strings.Join(parts, ",")
}

func TestTusUpload(t *testing.T) {
	enzo, johan := newFilesAPI(t)

	opts := enzo.tus("OPTIONS", "/api/uploads", "", nil)
	if opts.Code != http.StatusNoContent || opts.Header().Get("Tus-Version") != "1.0.0" ||
		opts.Header().Get("Tus-Extension") != "creation,expiration,termination" {
		t.Errorf("OPTIONS: %d %v", opts.Code, opts.Header())
	}

	created := enzo.tus("POST", "/api/uploads", "", map[string]string{
		"Upload-Length":   "10",
		"Upload-Metadata": meta("path", "Vidéos/clip.mp4", "filename", "clip.mp4"),
	})
	loc := created.Header().Get("Location")
	if created.Code != http.StatusCreated || !strings.HasPrefix(loc, "/api/uploads/") ||
		created.Header().Get("Upload-Expires") == "" || created.Header().Get("Tus-Resumable") != "1.0.0" {
		t.Fatalf("create: %d %v %s", created.Code, created.Header(), created.Body)
	}

	if rec := enzo.tus("PATCH", loc, "01234", map[string]string{"Upload-Offset": "0"}); rec.Code != http.StatusNoContent || rec.Header().Get("Upload-Offset") != "5" {
		t.Fatalf("chunk 1: %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	if rec := enzo.tus("PATCH", loc, "xyz", map[string]string{"Upload-Offset": "3"}); rec.Code != http.StatusConflict {
		t.Errorf("wrong offset: %d", rec.Code)
	}
	if rec := enzo.tus("PATCH", loc, "56789", map[string]string{"Upload-Offset": "5", "Content-Type": "application/octet-stream"}); rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("wrong content type: %d", rec.Code)
	}
	if rec := johan.tus("HEAD", loc, "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("johan sees enzo's upload: %d", rec.Code)
	}
	head := enzo.tus("HEAD", loc, "", nil)
	if head.Code != http.StatusOK || head.Header().Get("Upload-Offset") != "5" || head.Header().Get("Upload-Length") != "10" ||
		head.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("HEAD: %d %v", head.Code, head.Header())
	}
	if rec := enzo.tus("PATCH", loc, "56789", map[string]string{"Upload-Offset": "5"}); rec.Code != http.StatusNoContent || rec.Header().Get("Upload-Offset") != "10" {
		t.Fatalf("last chunk: %d %s", rec.Code, rec.Body)
	}
	if rec := enzo.get("/api/files/content?path=Vid%C3%A9os/clip.mp4", nil); rec.Body.String() != "0123456789" {
		t.Errorf("stored %q", rec.Body)
	}
	if rec := enzo.tus("HEAD", loc, "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("finished upload still there: %d", rec.Code)
	}
}

func TestTusRefusals(t *testing.T) {
	enzo, _ := newFilesAPI(t)
	enzo.upload("pris.txt", "here", nil)

	for name, c := range map[string]struct {
		headers map[string]string
		want    int
	}{
		"no Tus-Resumable": {map[string]string{"Tus-Resumable": "", "Upload-Length": "1", "Upload-Metadata": meta("path", "a")}, http.StatusPreconditionFailed},
		"no client header": {map[string]string{auth.ClientHeader: "", "Upload-Length": "1", "Upload-Metadata": meta("path", "a")}, http.StatusUnsupportedMediaType},
		"no length":        {map[string]string{"Upload-Metadata": meta("path", "a")}, http.StatusBadRequest},
		"no path":          {map[string]string{"Upload-Length": "1", "Upload-Metadata": meta("filename", "a")}, http.StatusBadRequest},
		"name taken":       {map[string]string{"Upload-Length": "1", "Upload-Metadata": meta("path", "pris.txt")}, http.StatusConflict},
		"bad path":         {map[string]string{"Upload-Length": "1", "Upload-Metadata": meta("path", "../x")}, http.StatusBadRequest},
		"bad sha256":       {map[string]string{"Upload-Length": "1", "Upload-Metadata": meta("path", "a", "sha256", "zz")}, http.StatusBadRequest},
	} {
		if rec := enzo.tus("POST", "/api/uploads", "", c.headers); rec.Code != c.want {
			t.Errorf("%s: %d, want %d (%s)", name, rec.Code, c.want, rec.Body)
		}
	}

	// An empty file is stored at creation.
	if rec := enzo.tus("POST", "/api/uploads", "", map[string]string{"Upload-Length": "0", "Upload-Metadata": meta("path", "vide.txt")}); rec.Code != http.StatusCreated {
		t.Fatalf("empty upload: %d %s", rec.Code, rec.Body)
	}
	if rec := enzo.get("/api/files/content?path=vide.txt", nil); rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("empty file: %d %q", rec.Code, rec.Body)
	}

	// Termination.
	loc := enzo.tus("POST", "/api/uploads", "", map[string]string{"Upload-Length": "5", "Upload-Metadata": meta("path", "annule.bin")}).Header().Get("Location")
	if rec := enzo.tus("DELETE", loc, "", nil); rec.Code != http.StatusNoContent {
		t.Errorf("terminate: %d %s", rec.Code, rec.Body)
	}
	if rec := enzo.tus("HEAD", loc, "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("after terminate: %d", rec.Code)
	}
}
