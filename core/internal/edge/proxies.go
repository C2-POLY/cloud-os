package edge

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// RefreshEvery is how often Watch resolves the proxies' host names again.
const RefreshEvery = 30 * time.Second

// Proxies is the set of addresses whose forwarding headers the core believes,
// read from CLOUDOS_TRUSTED_PROXIES: a comma-separated list of addresses,
// prefixes ("172.18.0.0/16") and host names. A host name is a Compose service,
// "tailscale": it is resolved at start and again by Watch, since a container
// may get another address when it restarts without the core. A nil *Proxies
// trusts no one.
type Proxies struct {
	prefixes []netip.Prefix
	hosts    []string
	lookup   func(ctx context.Context, host string) ([]netip.Addr, error)

	mu       sync.RWMutex
	resolved map[string][]netip.Addr
}

// ParseProxies reads a CLOUDOS_TRUSTED_PROXIES value; "" trusts no one.
func ParseProxies(list string) (*Proxies, error) {
	p := &Proxies{lookup: lookupIP, resolved: map[string][]netip.Addr{}}
	for _, item := range strings.Split(list, ",") {
		item = strings.TrimSpace(item)
		switch {
		case item == "":
		case strings.Contains(item, "/"):
			pfx, err := netip.ParsePrefix(item)
			if err != nil {
				return nil, fmt.Errorf("trusted proxy %q: %w", item, err)
			}
			p.prefixes = append(p.prefixes, pfx.Masked())
		default:
			if a, err := netip.ParseAddr(item); err == nil {
				a = a.Unmap()
				p.prefixes = append(p.prefixes, netip.PrefixFrom(a, a.BitLen()))
			} else if validHost(item) {
				p.hosts = append(p.hosts, item)
			} else {
				return nil, fmt.Errorf("trusted proxy %q: neither an address, a prefix nor a host name", item)
			}
		}
	}
	return p, nil
}

func lookupIP(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

func validHost(s string) bool {
	if len(s) > 253 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '.' || r == '_') {
			return false
		}
	}
	return true
}

// Refresh resolves the host names again. A name that does not resolve keeps
// its last addresses: the proxy may only be restarting.
func (p *Proxies) Refresh(ctx context.Context) {
	if p == nil {
		return
	}
	for _, host := range p.hosts {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		addrs, err := p.lookup(ctx, host)
		cancel()
		if err != nil {
			slog.Debug("trusted proxy: lookup", "host", host, "err", err)
			continue
		}
		for i := range addrs {
			addrs[i] = addrs[i].Unmap()
		}
		p.mu.Lock()
		p.resolved[host] = addrs
		p.mu.Unlock()
	}
}

// Watch resolves the host names now, then every interval until ctx ends.
func (p *Proxies) Watch(ctx context.Context, every time.Duration) {
	if p == nil || len(p.hosts) == 0 {
		return
	}
	p.Refresh(ctx)
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				p.Refresh(ctx)
			}
		}
	}()
}

// Trusted says whether a is one of the proxies.
func (p *Proxies) Trusted(a netip.Addr) bool {
	if p == nil || !a.IsValid() {
		return false
	}
	a = a.Unmap()
	for _, pfx := range p.prefixes {
		if pfx.Contains(a) {
			return true
		}
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, addrs := range p.resolved {
		for _, b := range addrs {
			if a == b {
				return true
			}
		}
	}
	return false
}

// TrustedAddr is Trusted for a net.Addr, a connection's remote address.
func (p *Proxies) TrustedAddr(addr net.Addr) bool {
	if tcp, ok := addr.(*net.TCPAddr); ok {
		a, ok := netip.AddrFromSlice(tcp.IP)
		return ok && p.Trusted(a)
	}
	ap, err := netip.ParseAddrPort(addr.String())
	return err == nil && p.Trusted(ap.Addr())
}
