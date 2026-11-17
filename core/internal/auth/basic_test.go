package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBasicAuthCachesAndStillRefusesADisabledAccount(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	if _, err := s.Setup(ctx, "enzo", "", pw); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, "johan", "", pw, RoleMember); err != nil {
		t.Fatal(err)
	}
	b := s.NewBasicAuth("test")
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	b.now = func() time.Time { return now }
	h := b.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, _ := AccountFrom(r.Context())
		w.Write([]byte(a.Name)) //nolint:errcheck
	}))
	call := func(name, password string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("PROPFIND", "/dav/", nil)
		req.SetBasicAuth(name, password)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	if rec := call("johan", pw); rec.Code != http.StatusOK || rec.Body.String() != "johan" {
		t.Fatalf("first call: %d %s", rec.Code, rec.Body)
	}
	if len(b.seen) != 1 {
		t.Fatalf("not cached: %d entries", len(b.seen))
	}
	// The cache key is a hash: the password itself is not kept.
	for k := range b.seen {
		if string(k[:]) == pw {
			t.Fatal("password kept in clear")
		}
	}
	// A wrong password never matches the cached entry.
	if rec := call("johan", pw+"x"); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong password with a cached entry: %d", rec.Code)
	}
	// Disabling the account cuts it off at once, cache or not.
	yes := true
	if _, err := s.Update(ctx, "johan", nil, &yes); err != nil {
		t.Fatal(err)
	}
	if rec := call("johan", pw); rec.Code != http.StatusUnauthorized {
		t.Errorf("disabled account still served: %d", rec.Code)
	}
	// An entry expires after BasicTTL.
	call("enzo", pw)
	now = now.Add(BasicTTL + time.Second)
	if b.trusted(credentialKey("enzo", pw)) {
		t.Error("entry trusted after BasicTTL")
	}
}
