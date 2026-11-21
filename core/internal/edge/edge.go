// Package edge reads what the box's front door says about a request: the
// client's address, whether it came over HTTPS, and whether it came from the
// internet through Tailscale Funnel (docs/architecture/box.md, « Accès »).
//
// The front door is the tailscale service: `tailscale serve` ends HTTPS with
// the box's ts.net certificate and relays to the core over the Compose
// network, setting X-Forwarded-For and X-Forwarded-Proto. The core believes
// those headers only from a trusted proxy (CLOUDOS_TRUSTED_PROXIES): any
// other client could write them.
package edge

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"path"
	"strings"
)

// FunnelHeader is set by `tailscale serve` on every request that came from
// the internet through Funnel; it removes the one a client sends.
const FunnelHeader = "Tailscale-Funnel-Request"

// FunnelPrefixes are the only paths a Funnel request reaches: the guest's
// page of a share link, its API, and the built web front it loads.
var FunnelPrefixes = []string{"/s/", "/api/s/", "/assets/"}

// Config is how the core reads its front door. The zero value trusts no
// proxy and has no public HTTPS address.
type Config struct {
	// Proxies are the addresses whose X-Forwarded-For and X-Forwarded-Proto
	// the core believes; nil believes none.
	Proxies *Proxies
	// PublicHTTPS is true when the box's public address, CLOUDOS_PUBLIC_URL,
	// is https: its cookies are then always Secure.
	PublicHTTPS bool
}

type ctxKey struct{}

type info struct {
	ip     string
	secure bool
}

// Handler puts what the request's front door says in its context, for
// ClientIP and Secure, and keeps a Funnel request to the share links: any
// other path answers 404.
//
// The guard holds whoever sends the Funnel header: a client that sends it
// itself only shuts itself out. It is the second barrier, after the paths of
// box/tailscale/serve-funnel.json.
func (c Config) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(FunnelHeader) != "" && !FunnelAllowed(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, c.read(r))))
	})
}

// FunnelAllowed says whether a Funnel request may reach p: only below one of
// FunnelPrefixes, once p is cleaned, so that "/s/../api/me" stays out.
func FunnelAllowed(p string) bool {
	if p == "" || p[0] != '/' {
		return false
	}
	clean := path.Clean(p)
	for _, prefix := range FunnelPrefixes {
		if strings.HasPrefix(clean, prefix) {
			return true
		}
	}
	return false
}

func (c Config) read(r *http.Request) info {
	peer := remoteHost(r)
	in := info{ip: peer, secure: r.TLS != nil || c.PublicHTTPS}
	addr, err := netip.ParseAddr(peer)
	if err != nil || !c.Proxies.Trusted(addr) {
		return in
	}
	// `tailscale serve` sets X-Forwarded-For to one address. Were there a
	// list, the last entry is the one the trusted proxy added.
	if v := r.Header.Values("X-Forwarded-For"); len(v) > 0 {
		last := v[len(v)-1]
		if i := strings.LastIndexByte(last, ','); i >= 0 {
			last = last[i+1:]
		}
		if a, err := netip.ParseAddr(strings.TrimSpace(last)); err == nil {
			in.ip = a.Unmap().String()
		}
	}
	if strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		in.secure = true
	}
	return in
}

// ClientIP is the address of the request's client: the one the trusted proxy
// forwarded, or the connection's. The attempt limits of logins and share
// passwords count by it.
func ClientIP(r *http.Request) string {
	if in, ok := r.Context().Value(ctxKey{}).(info); ok {
		return in.ip
	}
	return remoteHost(r)
}

// Secure says whether the cookies of the request's answer must be Secure: the
// box is reached over HTTPS, as its public address says or as the trusted
// proxy says. A header from any other client never decides it.
func Secure(r *http.Request) bool {
	if in, ok := r.Context().Value(ctxKey{}).(info); ok {
		return in.secure
	}
	return r.TLS != nil
}

func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if a, err := netip.ParseAddr(host); err == nil {
		return a.Unmap().String()
	}
	return host
}
