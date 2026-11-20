package edge

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

const proxyAddr = "172.18.0.9"

func mustProxies(t *testing.T, list string) *Proxies {
	t.Helper()
	p, err := ParseProxies(list)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// seen runs one request through the config's handler and returns what the
// next handler read of it, or the status when the handler stopped it.
func seen(t *testing.T, c Config, r *http.Request) (ip string, secure bool, status int) {
	t.Helper()
	rec := httptest.NewRecorder()
	c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, secure = ClientIP(r), Secure(r)
	})).ServeHTTP(rec, r)
	return ip, secure, rec.Code
}

func request(remote string, headers ...string) *http.Request {
	r := httptest.NewRequest("GET", "/api/me", nil)
	r.RemoteAddr = remote
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Add(headers[i], headers[i+1])
	}
	return r
}

func TestClientIPBelievesOnlyTheTrustedProxy(t *testing.T) {
	c := Config{Proxies: mustProxies(t, proxyAddr)}
	cases := []struct {
		name string
		r    *http.Request
		want string
	}{
		{"behind the proxy", request(proxyAddr+":40000", "X-Forwarded-For", "100.101.1.2"), "100.101.1.2"},
		{"the proxy itself", request(proxyAddr + ":40000"), proxyAddr},
		{"a list: the proxy's entry is the last", request(proxyAddr+":40000", "X-Forwarded-For", "6.6.6.6, 100.101.1.2"), "100.101.1.2"},
		{"two headers: the last", request(proxyAddr+":40000", "X-Forwarded-For", "6.6.6.6", "X-Forwarded-For", "100.101.1.2"), "100.101.1.2"},
		{"not an address", request(proxyAddr+":40000", "X-Forwarded-For", "evil"), proxyAddr},
		{"IPv6 behind the proxy", request(proxyAddr+":40000", "X-Forwarded-For", "fd7a:115c:a1e0::1"), "fd7a:115c:a1e0::1"},
		{"any other client", request("192.168.1.20:5000", "X-Forwarded-For", "10.9.9.9"), "192.168.1.20"},
		{"any other client, IPv6", request("[2001:db8::1]:5000", "X-Forwarded-For", "10.9.9.9"), "2001:db8::1"},
	}
	for _, tc := range cases {
		if ip, _, _ := seen(t, c, tc.r); ip != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, ip, tc.want)
		}
	}
	// Without the middleware, the connection's address.
	if ip := ClientIP(request("192.168.1.20:5000", "X-Forwarded-For", "10.9.9.9")); ip != "192.168.1.20" {
		t.Errorf("without the handler: %q", ip)
	}
	// No trusted proxy: no header is believed.
	if ip, _, _ := seen(t, Config{}, request(proxyAddr+":40000", "X-Forwarded-For", "10.9.9.9")); ip != proxyAddr {
		t.Errorf("no proxies: %q", ip)
	}
}

func TestSecureFollowsThePublicURLNotAnyClient(t *testing.T) {
	trusted := mustProxies(t, proxyAddr)
	cases := []struct {
		name string
		c    Config
		r    *http.Request
		want bool
	}{
		{"plain http", Config{Proxies: trusted}, request("192.168.1.20:5000"), false},
		{"a client's header", Config{Proxies: trusted}, request("192.168.1.20:5000", "X-Forwarded-Proto", "https"), false},
		{"the trusted proxy's header", Config{Proxies: trusted}, request(proxyAddr+":40000", "X-Forwarded-Proto", "https"), true},
		{"the trusted proxy over http", Config{Proxies: trusted}, request(proxyAddr+":40000", "X-Forwarded-Proto", "http"), false},
		{"an https public address", Config{PublicHTTPS: true}, request("192.168.1.20:5000"), true},
	}
	for _, tc := range cases {
		if _, secure, _ := seen(t, tc.c, tc.r); secure != tc.want {
			t.Errorf("%s: Secure %v, want %v", tc.name, secure, tc.want)
		}
	}
	if Secure(request("192.168.1.20:5000", "X-Forwarded-Proto", "https")) {
		t.Error("without the handler, a client's header made it Secure")
	}
}

func TestFunnelReachesOnlyTheShareLinks(t *testing.T) {
	c := Config{Proxies: mustProxies(t, proxyAddr)}
	pass := []string{"/s/AbC_-", "/s/x/", "/api/s/x", "/api/s/x/unlock", "/api/s/x/files", "/api/s/x/content", "/assets/index-abc.js", "/s/a/../b"}
	stop := []string{
		"/", "/index.html", "/s", "/s/", "/api/s/", "/assets/",
		"/api/login", "/api/setup", "/api/me", "/api/files", "/api/files/content", "/api/health", "/api/config",
		"/dav/", "/dav", "/map/fonts/x", "/s/../api/me", "/assets/../api/me", "/api/s/../../api/files",
		"/S/x", "//s/x/../../api/me",
	}
	for _, from := range []string{proxyAddr + ":40000", "203.0.113.5:7000"} {
		for _, p := range pass {
			r := request(from, FunnelHeader, "?1")
			r.URL.Path = p
			if _, _, code := seen(t, c, r); code != http.StatusOK {
				t.Errorf("Funnel from %s to %s: %d, want it to pass", from, p, code)
			}
		}
		for _, p := range stop {
			r := request(from, FunnelHeader, "?1")
			r.URL.Path = p
			if _, _, code := seen(t, c, r); code != http.StatusNotFound {
				t.Errorf("Funnel from %s to %s: %d, want 404", from, p, code)
			}
		}
	}
	// Without the header, the guard lets everything through.
	if _, _, code := seen(t, c, request(proxyAddr+":40000")); code != http.StatusOK {
		t.Errorf("tailnet request to /api/me: %d", code)
	}
}

func TestProxiesParse(t *testing.T) {
	p := mustProxies(t, " 10.0.0.0/8 , 172.18.0.9,::ffff:192.168.5.5, fd00::1, tailscale ")
	for _, a := range []string{"10.1.2.3", "172.18.0.9", "192.168.5.5", "fd00::1", "::ffff:10.0.0.1"} {
		if !p.Trusted(netip.MustParseAddr(a)) {
			t.Errorf("%s not trusted", a)
		}
	}
	for _, a := range []string{"172.18.0.10", "192.168.5.6", "fd00::2", "100.64.0.1"} {
		if p.Trusted(netip.MustParseAddr(a)) {
			t.Errorf("%s trusted", a)
		}
	}
	if len(p.hosts) != 1 || p.hosts[0] != "tailscale" {
		t.Errorf("hosts %q", p.hosts)
	}
	for _, bad := range []string{"10.0.0.0/33", "tail scale", "http://tailscale", "1.2.3.4:80"} {
		if _, err := ParseProxies(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	empty := mustProxies(t, "")
	if empty.Trusted(netip.MustParseAddr("127.0.0.1")) {
		t.Error("an empty list trusts")
	}
	var none *Proxies
	if none.Trusted(netip.MustParseAddr("127.0.0.1")) {
		t.Error("nil trusts")
	}
}

func TestProxiesResolveNamesAgain(t *testing.T) {
	p := mustProxies(t, "tailscale")
	answers := map[string][]netip.Addr{"tailscale": {netip.MustParseAddr("172.18.0.9")}}
	var fail error
	p.lookup = func(_ context.Context, host string) ([]netip.Addr, error) {
		if fail != nil {
			return nil, fail
		}
		return append([]netip.Addr{}, answers[host]...), nil
	}
	if p.Trusted(netip.MustParseAddr("172.18.0.9")) {
		t.Fatal("trusted before any lookup")
	}
	p.Refresh(context.Background())
	if !p.Trusted(netip.MustParseAddr("172.18.0.9")) {
		t.Fatal("not trusted after the lookup")
	}
	// The container restarts with another address.
	answers["tailscale"] = []netip.Addr{netip.MustParseAddr("172.18.0.12")}
	p.Refresh(context.Background())
	if p.Trusted(netip.MustParseAddr("172.18.0.9")) || !p.Trusted(netip.MustParseAddr("172.18.0.12")) {
		t.Fatal("the old address is still trusted, or the new one is not")
	}
	// A failed lookup keeps the last addresses.
	fail = errors.New("no such host")
	p.Refresh(context.Background())
	if !p.Trusted(netip.MustParseAddr("172.18.0.12")) {
		t.Fatal("a failed lookup dropped the proxy")
	}
}
