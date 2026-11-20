package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The smb service is off unless CLOUDOS_SMB=1, whatever else is set.
func TestSMBNeedsToBeTurnedOn(t *testing.T) {
	for _, v := range []string{"", "0", "true", "yes"} {
		t.Setenv("CLOUDOS_SMB", v)
		t.Setenv("DATABASE_URL", "postgres://nowhere.invalid/x")
		err := run(context.Background(), []string{"smb"}, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "CLOUDOS_SMB=1") {
			t.Errorf("CLOUDOS_SMB=%q: %v", v, err)
		}
	}
}

func TestHealthCheck(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	if err := checkHealth(context.Background(), ":"+port); err != nil {
		t.Errorf("a healthy core: %v", err)
	}
	status = http.StatusServiceUnavailable
	if err := checkHealth(context.Background(), ":"+port); err == nil {
		t.Error("a core whose database is down passed")
	}
	srv.Close()
	if err := checkHealth(context.Background(), ":"+port); err == nil {
		t.Error("a core that does not answer passed")
	}
}
