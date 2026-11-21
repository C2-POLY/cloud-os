package edge

import (
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestParseHeaderV1(t *testing.T) {
	good := map[string]string{
		"PROXY TCP4 100.101.1.2 100.100.9.9 51234 445\r\n":             "100.101.1.2:51234",
		"PROXY TCP6 fd7a:115c:a1e0::1 fd7a:115c:a1e0::2 51234 445\r\n": "[fd7a:115c:a1e0::1]:51234",
		"PROXY TCP4 255.255.255.255 255.255.255.255 65535 65535\r\n":   "255.255.255.255:65535",
	}
	for line, want := range good {
		a, err := ParseHeaderV1(line)
		if want == "" {
			if err == nil {
				t.Errorf("%q accepted", line)
			}
			continue
		}
		if err != nil || a.String() != want {
			t.Errorf("%q: %v %v, want %s", line, a, err, want)
		}
	}
	if a, err := ParseHeaderV1("PROXY UNKNOWN\r\n"); err != nil || a != nil {
		t.Errorf("UNKNOWN: %v %v", a, err)
	}
	bad := []string{
		"PROXY TCP4 100.101.1.2 100.100.9.9 51234 445\n",
		"PROXY TCP4 100.101.1.2 100.100.9.9 51234\r\n",
		"PROXY TCP4 fd7a::1 100.100.9.9 51234 445\r\n",
		"PROXY TCP6 100.101.1.2 100.100.9.9 51234 445\r\n",
		"PROXY TCP4 100.101.1.2 100.100.9.9 65536 445\r\n",
		"PROXY TCP4 100.101.1.2 100.100.9.9 0123 445\r\n",
		"PROXY TCP4 100.101.1.2 nope 51234 445\r\n",
		"PROXY UDP4 100.101.1.2 100.100.9.9 51234 445\r\n",
		"PROXY  TCP4 100.101.1.2 100.100.9.9 51234 445\r\n",
		"proxy TCP4 100.101.1.2 100.100.9.9 51234 445\r\n",
		"\x00\x00\x00\x45\xfeSMB",
		"PROXY TCP4 " + strings.Repeat("1", 120) + "\r\n",
	}
	for _, line := range bad {
		if a, err := ParseHeaderV1(line); err == nil {
			t.Errorf("%q accepted as %v", line, a)
		}
	}
}

// listen starts a ProxyListener on loopback; trusted says whether loopback is
// the proxy.
func listen(t *testing.T, trusted bool, timeout time.Duration) *ProxyListener {
	t.Helper()
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	list := "10.255.255.1"
	if trusted {
		list = "127.0.0.1"
	}
	pl := newProxyListener(inner, mustProxies(t, list), nil, timeout)
	t.Cleanup(func() { pl.Close() })
	return pl
}

// accept returns the next connection, or nil after a while.
func accept(pl *ProxyListener) net.Conn {
	got := make(chan net.Conn, 1)
	go func() {
		c, err := pl.Accept()
		if err != nil {
			c = nil
		}
		got <- c
	}()
	select {
	case c := <-got:
		return c
	case <-time.After(2 * time.Second):
		return nil
	}
}

func dialSend(t *testing.T, addr net.Addr, s string) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if s != "" {
		if _, err := io.WriteString(c, s); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func TestProxyListenerNamesTheClientOfTheTrustedProxy(t *testing.T) {
	pl := listen(t, true, 300*time.Millisecond)
	dialSend(t, pl.Addr(), "PROXY TCP4 100.101.1.2 100.100.9.9 51234 445\r\nSMB")
	c := accept(pl)
	if c == nil {
		t.Fatal("no connection")
	}
	defer c.Close()
	if got := c.RemoteAddr().String(); got != "100.101.1.2:51234" {
		t.Errorf("RemoteAddr %s", got)
	}
	if _, ok := c.RemoteAddr().(*net.TCPAddr); !ok {
		t.Errorf("RemoteAddr is a %T, smbsrv wants a *net.TCPAddr", c.RemoteAddr())
	}
	// What follows the header is left for the server.
	buf := make([]byte, 3)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "SMB" {
		t.Errorf("after the header: %q %v", buf, err)
	}
}

func TestProxyListenerRefusesTheTrustedProxyWithoutAHeader(t *testing.T) {
	pl := listen(t, true, 300*time.Millisecond)
	client := dialSend(t, pl.Addr(), "\x00\x00\x00\x45\xfeSMB\r\n")
	if c := accept(pl); c != nil {
		t.Fatalf("a connection without a header came through from %s", c.RemoteAddr())
	}
	// The listener closed it.
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := client.Read(make([]byte, 1)); err == nil {
		t.Error("the connection is still open")
	}
}

func TestProxyListenerBelievesNoOtherClient(t *testing.T) {
	pl := listen(t, false, 300*time.Millisecond)
	dialSend(t, pl.Addr(), "PROXY TCP4 100.101.1.2 100.100.9.9 51234 445\r\n")
	c := accept(pl)
	if c == nil {
		t.Fatal("no connection")
	}
	defer c.Close()
	if got := c.RemoteAddr().String(); !strings.HasPrefix(got, "127.0.0.1:") {
		t.Errorf("RemoteAddr %s: a header from an untrusted client was believed", got)
	}
}

func TestProxyListenerSlowHeaderHoldsNoOneUp(t *testing.T) {
	pl := listen(t, true, 5*time.Second)
	dialSend(t, pl.Addr(), "PROXY TCP4 100.") // never finishes
	dialSend(t, pl.Addr(), "PROXY TCP4 100.101.1.3 100.100.9.9 4000 445\r\n")
	c := accept(pl)
	if c == nil {
		t.Fatal("the second connection waited behind the first")
	}
	defer c.Close()
	if got := c.RemoteAddr().String(); got != "100.101.1.3:4000" {
		t.Errorf("RemoteAddr %s", got)
	}
}

func TestProxyListenerClose(t *testing.T) {
	pl := listen(t, true, 300*time.Millisecond)
	pl.Close()
	if _, err := pl.Accept(); err == nil {
		t.Error("Accept after Close")
	}
	if _, err := pl.Accept(); err == nil {
		t.Error("second Accept after Close")
	}
}
