package smbsrv

import (
	"net"
	"testing"
	"time"

	"github.com/tobychui/smbsrv/internal/auth"
)

// renamingListener hands out connections whose RemoteAddr is another
// client's, as a listener that reads a proxy's PROXY header does.
type renamingListener struct {
	net.Listener
	remote net.Addr
}

type renamedConn struct {
	net.Conn
	remote net.Addr
}

func (c renamedConn) RemoteAddr() net.Addr { return c.remote }

func (l renamingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return renamedConn{Conn: c, remote: l.remote}, nil
}

// PATCHES.md, 7: the server serves a listener it is given, and the address
// its connections name is the one the Backend and the limits see. Upstream
// had no Listener and always listened on Address itself.
func TestPatchServesTheGivenListener(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	client := &net.TCPAddr{IP: net.ParseIP("100.101.102.103"), Port: 51234}
	share := &testShare{name: "data", root: t.TempDir(), denyWrite: map[string]bool{}, denyRead: map[string]bool{}, spaceLeft: -1}
	b := &lookupBackend{testBackend: &testBackend{shares: []Share{share}}, hash: auth.NTHash(testPassword)}
	srv, err := New(Config{Address: "127.0.0.1:1", Listener: renamingListener{Listener: inner, remote: client}, Backend: b, LoginDelay: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	if got := srv.Addr().String(); got != inner.Addr().String() {
		t.Fatalf("Addr %s, want the given listener's %s", got, inner.Addr())
	}
	h := &harness{srv: srv, backend: b.testBackend, share: share, addr: inner.Addr().String()}
	if _, err := dial(t, h, testUserName, testPassword); err != nil {
		t.Fatalf("login: %v", err)
	}
	b.mu.Lock()
	remotes := append([]string{}, b.remotes...)
	b.mu.Unlock()
	if len(remotes) != 1 || remotes[0] != "100.101.102.103:51234" {
		t.Errorf("LookupHash got %q, want the named client", remotes)
	}
}
