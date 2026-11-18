package encoder_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/C2-POLY/cloud-os/core/internal/encoder"
)

func TestEncodeAsksTheWorkerForTheModelsVector(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/encode" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("%s %s %s", r.Method, r.URL.Path, r.Header.Get("Content-Type"))
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Error(err)
		}
		_, _ = io.WriteString(w, `{"model":"arctic-embed-m-v2.0","version":"03d923bb1850-w512-o64-n64","vector":[0.6,-0.8,0]}`)
	}))
	defer srv.Close()

	c := &encoder.Client{URL: srv.URL + "/"}
	v, err := c.Encode(context.Background(), "arctic-embed-m-v2.0", "contrat de l'appart")
	if err != nil {
		t.Fatal(err)
	}
	if got["model"] != "arctic-embed-m-v2.0" || got["text"] != "contrat de l'appart" {
		t.Errorf("sent %v", got)
	}
	if v.Model != "arctic-embed-m-v2.0" || v.Version != "03d923bb1850-w512-o64-n64" || len(v.Values) != 3 {
		t.Errorf("got %+v", v)
	}
	if lit := v.Literal(); lit != "[0.6,-0.8,0]" {
		t.Errorf("literal %s", lit)
	}
}

func TestEveryFailureIsUnavailableAndNeverCarriesTheQuery(t *testing.T) {
	const secret = "facture du docteur Martin"
	answers := map[string]http.HandlerFunc{
		"error status": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":"the query could not be encoded"}`)
		},
		"model not served": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		},
		"not JSON": func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "<html>") },
		"another model": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"model":"siglip2-base-patch16-224","version":"v","vector":[1]}`)
		},
		"no vector": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"model":"arctic-embed-m-v2.0","version":"v","vector":[]}`)
		},
		"too slow": func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-time.After(time.Second):
			}
		},
	}
	for name, h := range answers {
		srv := httptest.NewServer(h)
		c := &encoder.Client{URL: srv.URL, HTTP: &http.Client{Timeout: 200 * time.Millisecond}}
		_, err := c.Encode(context.Background(), "arctic-embed-m-v2.0", secret)
		if !errors.Is(err, encoder.ErrUnavailable) {
			t.Errorf("%s: %v, want ErrUnavailable", name, err)
		} else if strings.Contains(err.Error(), "Martin") {
			t.Errorf("%s: the error carries the query: %v", name, err)
		}
		srv.Close()
	}

	// No worker at all.
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	if _, err := (&encoder.Client{URL: url}).Encode(context.Background(), "arctic-embed-m-v2.0", secret); !errors.Is(err, encoder.ErrUnavailable) {
		t.Errorf("no worker: %v", err)
	}
}
