package smbsrv

import (
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hirochachacha/go-smb2"

	"github.com/tobychui/smbsrv/internal/auth"
)

// lookupBackend records the address LookupHash is given, and can answer a
// hash of any length.
type lookupBackend struct {
	*testBackend
	mu      sync.Mutex
	remotes []string
	hash    []byte
}

func (b *lookupBackend) LookupHash(username, remote string) (string, []byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.remotes = append(b.remotes, remote)
	if !strings.EqualFold(username, testUserName) {
		return "", nil, false
	}
	return testUserName, b.hash, true
}

func lookupHarness(t *testing.T, hash []byte) (*harness, *lookupBackend) {
	t.Helper()
	share := &testShare{name: "data", root: t.TempDir(), denyWrite: map[string]bool{}, denyRead: map[string]bool{}, spaceLeft: -1}
	b := &lookupBackend{testBackend: &testBackend{shares: []Share{share}}, hash: hash}
	srv, err := New(Config{Address: "127.0.0.1:0", Backend: b, LoginDelay: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	return &harness{srv: srv, backend: b.testBackend, share: share, addr: srv.Addr().String()}, b
}

// PATCHES.md, 6: LookupHash gets the client's ip:port, the address AuthLog
// gets, so a backend can limit attempts per address.
func TestPatchLookupHashGetsTheAddress(t *testing.T) {
	h, b := lookupHarness(t, auth.NTHash(testPassword))
	if _, err := dial(t, h, testUserName, testPassword); err != nil {
		t.Fatalf("login: %v", err)
	}
	b.mu.Lock()
	remotes := append([]string{}, b.remotes...)
	b.mu.Unlock()
	h.backend.mu.Lock()
	last := h.backend.lastRemote
	h.backend.mu.Unlock()
	if len(remotes) != 1 || !strings.HasPrefix(remotes[0], "127.0.0.1:") || remotes[0] != last {
		t.Errorf("LookupHash got %q, AuthLog %q", remotes, last)
	}
}

// PATCHES.md, 6: a hash that is not 16 bytes logs nobody in, whatever the
// backend says. Upstream, a client that knew the short hash (an empty one is
// known to everyone) logged in with it.
func TestPatchShortHashLogsNobodyIn(t *testing.T) {
	for _, hash := range [][]byte{{1, 2, 3}, make([]byte, 17)} {
		h, _ := lookupHarness(t, hash)
		nc, err := net.Dial("tcp", h.addr)
		if err != nil {
			t.Fatal(err)
		}
		d := &smb2.Dialer{Initiator: &smb2.NTLMInitiator{User: testUserName, Hash: hash}}
		if s, err := d.Dial(nc); err == nil {
			s.Logoff()
			t.Errorf("a hash of %d bytes let its holder in", len(hash))
		}
		nc.Close()
	}
}
