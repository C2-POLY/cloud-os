package edge

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

// HeaderTimeout is how long a trusted proxy has to send its PROXY header.
const HeaderTimeout = 5 * time.Second

// maxHeaderV1 is the longest PROXY protocol v1 line, CRLF included.
const maxHeaderV1 = 107

// ProxyListener wraps a TCP listener whose trusted proxies send the PROXY
// protocol, version 1 (`tailscale serve` with "ProxyProtocol": 1 on a TCP
// forward): a connection from a trusted proxy must start with the header, and
// its RemoteAddr becomes the client the header names. A connection from
// anyone else is left as it is, header or not: only the proxy is believed.
//
// Headers are read off the accept loop, so a slow proxy connection never
// holds up the others.
type ProxyListener struct {
	net.Listener
	proxies *Proxies
	timeout time.Duration
	logger  *slog.Logger

	ready chan net.Conn
	errc  chan error
	done  chan struct{}
	once  sync.Once
}

// NewProxyListener starts accepting on l.
func NewProxyListener(l net.Listener, proxies *Proxies, logger *slog.Logger) *ProxyListener {
	return newProxyListener(l, proxies, logger, HeaderTimeout)
}

func newProxyListener(l net.Listener, proxies *Proxies, logger *slog.Logger, timeout time.Duration) *ProxyListener {
	if logger == nil {
		logger = slog.Default()
	}
	pl := &ProxyListener{
		Listener: l, proxies: proxies, timeout: timeout, logger: logger,
		ready: make(chan net.Conn), errc: make(chan error, 1), done: make(chan struct{}),
	}
	go pl.loop()
	return pl
}

func (pl *ProxyListener) loop() {
	for {
		c, err := pl.Listener.Accept()
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			pl.errc <- err
			return
		}
		if !pl.proxies.TrustedAddr(c.RemoteAddr()) {
			pl.hand(c)
			continue
		}
		// smbsrv sets these on a bare *net.TCPConn only.
		if tcp, ok := c.(*net.TCPConn); ok {
			_ = tcp.SetNoDelay(true)
			_ = tcp.SetKeepAlive(true)
			_ = tcp.SetKeepAlivePeriod(30 * time.Second)
		}
		go func() {
			pc, err := readHeader(c, pl.timeout)
			if err != nil {
				pl.logger.Warn("PROXY header refused", "proxy", c.RemoteAddr().String(), "err", err)
				c.Close()
				return
			}
			pl.hand(pc)
		}()
	}
}

func (pl *ProxyListener) hand(c net.Conn) {
	select {
	case pl.ready <- c:
	case <-pl.done:
		c.Close()
	}
}

// Accept returns the next connection, its header read when it came from a
// trusted proxy.
func (pl *ProxyListener) Accept() (net.Conn, error) {
	select {
	case c := <-pl.ready:
		return c, nil
	case err := <-pl.errc:
		pl.errc <- err // every later Accept fails the same way
		return nil, err
	case <-pl.done:
		return nil, net.ErrClosed
	}
}

// Close stops listening.
func (pl *ProxyListener) Close() error {
	pl.once.Do(func() { close(pl.done) })
	return pl.Listener.Close()
}

// proxiedConn is a connection whose client is the one its PROXY header named.
type proxiedConn struct {
	net.Conn
	remote net.Addr
}

func (c *proxiedConn) RemoteAddr() net.Addr { return c.remote }

// readHeader reads the PROXY v1 line that starts c, one byte at a time so
// nothing after it is consumed.
func readHeader(c net.Conn, timeout time.Duration) (net.Conn, error) {
	if err := c.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}
	line := make([]byte, 0, maxHeaderV1)
	b := make([]byte, 1)
	for {
		if _, err := c.Read(b); err != nil {
			return nil, fmt.Errorf("read: %w", err)
		}
		line = append(line, b[0])
		if b[0] == '\n' {
			break
		}
		if len(line) >= maxHeaderV1 {
			return nil, errors.New("no PROXY v1 line")
		}
	}
	if err := c.SetReadDeadline(time.Time{}); err != nil {
		return nil, err
	}
	remote, err := ParseHeaderV1(string(line))
	if err != nil {
		return nil, err
	}
	if remote == nil { // UNKNOWN: the proxy's own address stays
		return c, nil
	}
	return &proxiedConn{Conn: c, remote: remote}, nil
}

// ParseHeaderV1 reads a PROXY protocol v1 line, "PROXY TCP4 <src> <dst>
// <sport> <dport>\r\n", and returns the source. "PROXY UNKNOWN…" returns nil.
func ParseHeaderV1(line string) (*net.TCPAddr, error) {
	if len(line) > maxHeaderV1 {
		return nil, errors.New("PROXY line too long")
	}
	body, ok := strings.CutSuffix(line, "\r\n")
	if !ok {
		return nil, errors.New("PROXY line does not end with CRLF")
	}
	f := strings.Split(body, " ")
	if len(f) < 2 || f[0] != "PROXY" {
		return nil, errors.New("not a PROXY line")
	}
	if f[1] == "UNKNOWN" {
		return nil, nil
	}
	if len(f) != 6 || (f[1] != "TCP4" && f[1] != "TCP6") {
		return nil, fmt.Errorf("bad PROXY line %q", body)
	}
	src, err := netip.ParseAddr(f[2])
	if err != nil {
		return nil, fmt.Errorf("PROXY source: %w", err)
	}
	if _, err := netip.ParseAddr(f[3]); err != nil {
		return nil, fmt.Errorf("PROXY destination: %w", err)
	}
	if src.Is4() != (f[1] == "TCP4") || src.Zone() != "" {
		return nil, fmt.Errorf("PROXY source %s is not %s", src, f[1])
	}
	port, err := strconv.ParseUint(f[4], 10, 16)
	if err != nil || (len(f[4]) > 1 && f[4][0] == '0') {
		return nil, fmt.Errorf("PROXY source port %q", f[4])
	}
	if _, err := strconv.ParseUint(f[5], 10, 16); err != nil {
		return nil, fmt.Errorf("PROXY destination port %q", f[5])
	}
	return net.TCPAddrFromAddrPort(netip.AddrPortFrom(src, uint16(port))), nil
}
