package embed_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/C2-POLY/cloud-os/core/internal/embed"
	"github.com/C2-POLY/cloud-os/core/internal/testdb"
)

func TestQueryAsksTheWorkerForTheVector(t *testing.T) {
	var got struct {
		Model string   `json:"model"`
		Texts []string `json:"texts"`
	}
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/embed" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("%s %s %q", r.Method, r.URL.Path, r.Header.Get("Content-Type"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{"model":"siglip","version":"v2","vectors":[[0.6,-0.8]]}`))
	}))
	defer worker.Close()

	v, err := (&embed.Client{URL: worker.URL + "/"}).Query(context.Background(), "siglip", "chien à la plage")
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "siglip" || len(got.Texts) != 1 || got.Texts[0] != "chien à la plage" {
		t.Errorf("request = %+v", got)
	}
	if v.Model != "siglip" || v.Version != "v2" || len(v.Values) != 2 || v.Values[0] != 0.6 || v.Values[1] != -0.8 {
		t.Errorf("vector = %+v", v)
	}
}

func TestQueryIsUnavailableWhenTheWorkerCannotAnswer(t *testing.T) {
	answer := func(status int, body string) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer slow.Close()
	down := answer(http.StatusOK, "")
	down.Close()
	cases := map[string]*embed.Client{
		"no worker":       {},
		"stopped worker":  {URL: down.URL},
		"model missing":   {URL: answer(http.StatusServiceUnavailable, `{"error":"model files missing"}`).URL},
		"unknown model":   {URL: answer(http.StatusNotFound, `{"error":"no model"}`).URL},
		"not JSON":        {URL: answer(http.StatusOK, `<html>`).URL},
		"another model":   {URL: answer(http.StatusOK, `{"model":"clip","version":"v1","vectors":[[1]]}`).URL},
		"no version":      {URL: answer(http.StatusOK, `{"model":"siglip","vectors":[[1]]}`).URL},
		"no vector":       {URL: answer(http.StatusOK, `{"model":"siglip","version":"v1","vectors":[]}`).URL},
		"too slow":        {URL: slow.URL, HTTP: &http.Client{Timeout: 50 * time.Millisecond}},
		"an empty vector": {URL: answer(http.StatusOK, `{"model":"siglip","version":"v1","vectors":[[]]}`).URL},
	}
	for name, c := range cases {
		if _, err := c.Query(context.Background(), "siglip", "plage"); !errors.Is(err, embed.ErrUnavailable) {
			t.Errorf("%s: %v, want ErrUnavailable", name, err)
		}
	}
	var nilClient *embed.Client
	if _, err := nilClient.Query(context.Background(), "siglip", "plage"); !errors.Is(err, embed.ErrUnavailable) {
		t.Errorf("nil client: %v", err)
	}
}

func TestLiteralIsPgvectorText(t *testing.T) {
	v := embed.Vector{Values: []float32{0.5, -1, 1e-7, 0.1}}
	if got := v.Literal(); got != "[0.5,-1,1e-07,0.1]" {
		t.Errorf("literal = %s", got)
	}
	if got := (embed.Vector{}).Literal(); got != "[]" {
		t.Errorf("empty literal = %s", got)
	}
}

func TestDueOncePerDeclaredVersion(t *testing.T) {
	ctx := context.Background()
	pool, _ := testdb.Migrated(t)
	due := func(want string) {
		t.Helper()
		v, ok, err := embed.Due(ctx, pool, "siglip")
		if err != nil || ok != (want != "") || v != want {
			t.Errorf("due = %q %v %v, want %q", v, ok, err, want)
		}
	}
	due("") // no worker declared the model yet
	if _, err := pool.Exec(ctx, `INSERT INTO embedding_models (name, version, dims) VALUES ('siglip', 'v1', 768)`); err != nil {
		t.Fatal(err)
	}
	due("v1")
	if err := embed.MarkRequeued(ctx, pool, "siglip", "v1"); err != nil {
		t.Fatal(err)
	}
	due("")
	// A worker with a new version, as worker/src/worker/vectors.py declares it.
	if _, err := pool.Exec(ctx, `UPDATE embedding_models SET version = 'v2', requeued_at = NULL WHERE name = 'siglip'`); err != nil {
		t.Fatal(err)
	}
	due("v2")
	// Requeuing for a version that is no longer current leaves the new one due.
	if err := embed.MarkRequeued(ctx, pool, "siglip", "v1"); err != nil {
		t.Fatal(err)
	}
	due("v2")
}
