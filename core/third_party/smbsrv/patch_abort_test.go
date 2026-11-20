package smbsrv

import (
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/hirochachacha/go-smb2"

	"github.com/tobychui/smbsrv/internal/wire"
)

// abortShare is a drive whose files are staged: it records, per path, whether
// an open ended with Close (stored) or Abort (dropped).
type abortShare struct {
	*testShare
	mu     sync.Mutex
	closed map[string]int
	abort  map[string]int
}

type abortFile struct {
	File
	share *abortShare
	path  string
}

func (s *abortShare) Open(p string, flag int) (File, error) {
	f, err := s.testShare.Open(p, flag)
	if err != nil {
		return nil, err
	}
	return &abortFile{File: f, share: s, path: p}, nil
}

func (f *abortFile) Close() error {
	f.share.mu.Lock()
	f.share.closed[f.path]++
	f.share.mu.Unlock()
	return f.File.Close()
}

func (f *abortFile) Abort() error {
	f.share.mu.Lock()
	f.share.abort[f.path]++
	f.share.mu.Unlock()
	return f.File.Close()
}

func (s *abortShare) counts(p string) (closed, aborted int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed[p], s.abort[p]
}

func abortHarness(t *testing.T) (*harness, *abortShare) {
	t.Helper()
	ts := &testShare{name: "data", root: t.TempDir(), denyWrite: map[string]bool{}, denyRead: map[string]bool{}, spaceLeft: -1}
	share := &abortShare{testShare: ts, closed: map[string]int{}, abort: map[string]int{}}
	backend := &testBackend{shares: []Share{share}}
	srv, err := New(Config{Address: "127.0.0.1:0", Backend: backend, LoginDelay: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	return &harness{srv: srv, backend: backend, share: ts, addr: srv.Addr().String()}, share
}

// waitCounts waits for the server to end the open of p, which it does on its
// own goroutines once a connection is gone.
func waitCounts(t *testing.T, s *abortShare, p string, closed, aborted int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, a := s.counts(p)
		if c == closed && a == aborted {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: Close %d and Abort %d times, want %d and %d", p, c, a, closed, aborted)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// PATCHES.md, 7: a CLOSE stores a staged file, and only a CLOSE does. A
// connection cut, a server stopping, or a close while the file waits to be
// deleted abort it. Upstream called Close in every case, which stored the
// half of a copy whose connection had dropped.
func TestPatchOnlyACloseStores(t *testing.T) {
	t.Run("close", func(t *testing.T) {
		h, s := abortHarness(t)
		fs := mount(t, h)
		f, err := fs.Create("a.txt")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte("hello")); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		waitCounts(t, s, "a.txt", 1, 0)
	})

	t.Run("connection cut", func(t *testing.T) {
		h, s := abortHarness(t)
		nc, err := net.Dial("tcp", h.addr)
		if err != nil {
			t.Fatal(err)
		}
		d := &smb2.Dialer{Initiator: &smb2.NTLMInitiator{User: testUserName, Password: testPassword}}
		sess, err := d.Dial(nc)
		if err != nil {
			t.Fatal(err)
		}
		fs, err := sess.Mount("data")
		if err != nil {
			t.Fatal(err)
		}
		f, err := fs.OpenFile("half.bin", os.O_RDWR|os.O_CREATE, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(make([]byte, 4096)); err != nil {
			t.Fatal(err)
		}
		nc.Close()
		waitCounts(t, s, "half.bin", 0, 1)
	})

	t.Run("server stops", func(t *testing.T) {
		h, s := abortHarness(t)
		fs := mount(t, h)
		f, err := fs.Create("b.txt")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte("partial")); err != nil {
			t.Fatal(err)
		}
		h.srv.Close()
		waitCounts(t, s, "b.txt", 0, 1)
	})

	t.Run("delete pending", func(t *testing.T) {
		h, s := abortHarness(t)
		c := newRawClient(t, h)
		rw := createOpts{access: wire.FileReadData | wire.FileWriteData | wire.AccessDelete, share: shareAll, disposition: wire.DispositionOpenIf}
		id := mustCreate(t, c, "c.txt", rw)
		if r := c.call(wire.CmdWrite, writeBodyOf(id, 0, []byte("doomed"))); r.hdr.Status != 0 {
			t.Fatalf("write: %x", r.hdr.Status)
		}
		// The file is marked for deletion while still open
		if r := c.call(wire.CmdSetInfo, setInfoBody(id, wire.FileDispositionInformation, []byte{1})); r.hdr.Status != 0 {
			t.Fatalf("disposition: %x", r.hdr.Status)
		}
		if st := c.close(id); st != 0 {
			t.Fatalf("close: %x", st)
		}
		waitCounts(t, s, "c.txt", 0, 1)
		if _, err := os.Stat(s.full("c.txt")); !os.IsNotExist(err) {
			t.Errorf("the deleted file is still there: %v", err)
		}
	})
}
