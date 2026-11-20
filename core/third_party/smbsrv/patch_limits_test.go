package smbsrv

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tobychui/smbsrv/internal/wire"
)

/*
	Regression tests of PATCHES.md, 4 (what a client can make the server hold)
	and 5 (a panic ends one connection only). Each one fails on the upstream
	code: requests in flight past the limit, a read buffer the size of the
	request, a process that dies.
*/

// readCountingShare is the test drive with reads that take time and are counted:
// how many run at once, and how many bytes they were given to fill.
type readCountingShare struct {
	*testShare
	delay time.Duration

	mu        sync.Mutex
	running   int
	peak      int
	bytes     int64
	peakBytes int64
	largest   int
}

func (s *readCountingShare) Open(p string, flag int) (File, error) {
	f, err := s.testShare.Open(p, flag)
	if err != nil {
		return nil, err
	}
	return &readCountingFile{File: f, share: s}, nil
}

type readCountingFile struct {
	File
	share *readCountingShare
}

func (f *readCountingFile) ReadAt(b []byte, off int64) (int, error) {
	s := f.share
	s.mu.Lock()
	s.running++
	s.bytes += int64(len(b))
	s.peak = max(s.peak, s.running)
	s.peakBytes = max(s.peakBytes, s.bytes)
	s.largest = max(s.largest, len(b))
	s.mu.Unlock()
	time.Sleep(s.delay)
	n, err := f.File.ReadAt(b, off)
	s.mu.Lock()
	s.running--
	s.bytes -= int64(len(b))
	s.mu.Unlock()
	return n, err
}

func (s *readCountingShare) stats() (peak int, peakBytes int64, largest int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peak, s.peakBytes, s.largest
}

// readCountingHarness serves a readCountingShare. PendingAfter is short, so slow
// reads go async, which is how the limit of requests in flight was bypassed.
func readCountingHarness(t *testing.T, delay time.Duration) (*harness, *readCountingShare) {
	t.Helper()
	h := newHarness(t, func(c *Config) { c.PendingAfter = 20 * time.Millisecond })
	cs := &readCountingShare{testShare: h.share, delay: delay}
	h.backend.shares = []Share{cs}
	return h, cs
}

// grow asks for credits with echoes until the client may have want requests
// of one credit in flight.
func (c *rawClient) grow(want int) {
	c.t.Helper()
	have := 1
	for have < want {
		h := wire.Header{Command: wire.CmdEcho, Credits: 64, CreditCharge: 1, MessageID: c.mid, SessionID: c.session, TreeID: c.tree}
		c.mid++
		msg := make([]byte, wire.HeaderSize+4)
		h.Put(msg)
		binary.LittleEndian.PutUint16(msg[wire.HeaderSize:], 4)
		c.signer.Sign(msg)
		c.writeFrame(msg)
		r := parseReplies(c.t, c.readFrame())[0]
		if r.hdr.Status != 0 {
			c.t.Fatalf("echo: %x", r.hdr.Status)
		}
		have += int(r.hdr.Credits) - 1
	}
}

// sendCharged writes one signed request costing charge credits, without waiting.
func (c *rawClient) sendCharged(cmd uint16, body []byte, charge uint16) {
	c.t.Helper()
	h := wire.Header{Command: cmd, Credits: charge, CreditCharge: charge, MessageID: c.mid, SessionID: c.session, TreeID: c.tree}
	c.mid += uint64(charge)
	msg := make([]byte, wire.HeaderSize+len(body))
	h.Put(msg)
	copy(msg[wire.HeaderSize:], body)
	c.signer.Sign(msg)
	c.writeFrame(msg)
}

// finals reads replies until n final ones came, and returns them.
func (c *rawClient) finals(n int, wait time.Duration) []rawReply {
	c.t.Helper()
	out := []rawReply{}
	deadline := time.Now().Add(wait)
	for len(out) < n {
		if time.Now().After(deadline) {
			c.t.Fatalf("%d of %d replies after %s", len(out), n, wait)
		}
		for _, r := range parseReplies(c.t, c.readFrame()) {
			if r.hdr.Status != wire.StatusPending {
				out = append(out, r)
			}
		}
	}
	return out
}

func writeTestFile(t *testing.T, h *harness, name string, size int64) {
	t.Helper()
	f, err := os.Create(filepath.Join(h.share.root, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
}

// PATCHES.md, 4: requests answered STATUS_PENDING keep their place among the
// maxInFlight requests running, instead of handing it back at the interim
// response. Upstream had all 300 reads running in the backend at once.
func TestPatchPendingRequestsCountInFlight(t *testing.T) {
	h, cs := readCountingHarness(t, 80*time.Millisecond)
	writeTestFile(t, h, "f.bin", 1<<20)
	c := newRawClient(t, h)
	id, st := c.create("f.bin", createOpts{access: wire.FileReadData, share: 7, disposition: wire.DispositionOpen})
	if st != 0 {
		t.Fatalf("create: %x", st)
	}
	const reads = 300
	c.grow(reads + 1)
	for i := 0; i < reads; i++ {
		c.sendCharged(wire.CmdRead, readBodyOf(id, uint64(i%16)*4096, 4096), 1)
	}
	for _, r := range c.finals(reads, 60*time.Second) {
		if r.hdr.Status != 0 {
			t.Fatalf("read: %x", r.hdr.Status)
		}
	}
	if peak, _, _ := cs.stats(); peak > maxInFlight {
		t.Errorf("%d reads ran in the backend at once, the limit is %d", peak, maxInFlight)
	}
}

// PATCHES.md, 4: a READ allocates what the file can give, not what the
// client asked for, and still sees bytes the file gained after the open.
func TestPatchReadAllocatesWhatTheFileHas(t *testing.T) {
	h, cs := readCountingHarness(t, 0)
	path := filepath.Join(h.share.root, "small.txt")
	if err := os.WriteFile(path, []byte("ten bytes!"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := newRawClient(t, h)
	id, st := c.create("small.txt", createOpts{access: wire.FileReadData, share: 7, disposition: wire.DispositionOpen})
	if st != 0 {
		t.Fatalf("create: %x", st)
	}
	c.grow(300)
	read := func(off uint64, length uint32) []byte {
		t.Helper()
		c.sendCharged(wire.CmdRead, readBodyOf(id, off, length), uint16((length-1)/65536+1))
		r := c.finals(1, 10*time.Second)[0]
		if r.hdr.Status == wire.StatusEndOfFile {
			return nil
		}
		if r.hdr.Status != 0 {
			t.Fatalf("read: %x", r.hdr.Status)
		}
		n := binary.LittleEndian.Uint32(r.body[4:])
		return r.body[16 : 16+n]
	}
	if got := read(0, maxTransact); string(got) != "ten bytes!" {
		t.Fatalf("read %q", got)
	}
	if _, _, largest := cs.stats(); largest > 64<<10 {
		t.Errorf("a read of a 10 byte file was given a buffer of %d bytes", largest)
	}
	if got := read(1<<20, maxTransact); got != nil {
		t.Errorf("a read past the end returned %d bytes", len(got))
	}

	// The file grows after the open: its new bytes still come back.
	more := bytes.Repeat([]byte("x"), 300<<10)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(more); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if got := read(0, 1<<20); len(got) != 10+len(more) {
		t.Errorf("after growing, a read returned %d bytes, want %d", len(got), 10+len(more))
	}
	if got := read(5, 1<<20); len(got) != 5+len(more) {
		t.Errorf("after growing, a read at 5 returned %d bytes, want %d", len(got), 5+len(more))
	}
	// An offset past what int64 holds is refused.
	c.sendCharged(wire.CmdRead, readBodyOf(id, 1<<63, 4096), 1)
	if r := c.finals(1, 5*time.Second)[0]; r.hdr.Status != wire.StatusInvalidParameter {
		t.Errorf("a read at 2^63: %x", r.hdr.Status)
	}
}

// PATCHES.md, 4: the data of READs not yet written is capped per connection.
// Upstream gave a backend 12 buffers of 8 MiB at once.
func TestPatchReadDataIsCappedPerConnection(t *testing.T) {
	h, cs := readCountingHarness(t, 150*time.Millisecond)
	writeTestFile(t, h, "big.bin", 16<<20)
	c := newRawClient(t, h)
	id, st := c.create("big.bin", createOpts{access: wire.FileReadData, share: 7, disposition: wire.DispositionOpen})
	if st != 0 {
		t.Fatalf("create: %x", st)
	}
	const reads = 12
	const charge = maxTransact / 65536
	c.grow(reads*charge + 1)
	for i := 0; i < reads; i++ {
		c.sendCharged(wire.CmdRead, readBodyOf(id, uint64(i%2)*maxTransact, maxTransact), charge)
	}
	for _, r := range c.finals(reads, 60*time.Second) {
		if r.hdr.Status != 0 {
			t.Fatalf("read: %x", r.hdr.Status)
		}
	}
	if _, peakBytes, _ := cs.stats(); peakBytes > maxConnOutbound {
		t.Errorf("the backend filled %d MiB of reads at once, the cap per connection is %d MiB", peakBytes>>20, maxConnOutbound>>20)
	}
}

// PATCHES.md, 4: a connection has at most maxPendingAsync requests waiting
// for a change or a lock; the next ones are refused.
func TestPatchPendingRequestsAreCapped(t *testing.T) {
	h := newHarness(t)
	if err := os.Mkdir(filepath.Join(h.share.root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := newRawClient(t, h)
	id, st := c.create("dir", createOpts{access: wire.FileReadData, share: 7, disposition: wire.DispositionOpen, options: wire.OptionDirectory})
	if st != 0 {
		t.Fatalf("create: %x", st)
	}
	const notifies = maxPendingAsync + 40
	c.grow(notifies + 1)
	for i := 0; i < notifies; i++ {
		c.sendCharged(wire.CmdChangeNotify, notifyBody(id), 1)
	}
	pending, refused := 0, 0
	for pending+refused < notifies {
		for _, r := range parseReplies(t, c.readFrame()) {
			switch r.hdr.Status {
			case wire.StatusPending:
				pending++
			case wire.StatusInsufficientResources:
				refused++
			default:
				t.Fatalf("change notify: %x", r.hdr.Status)
			}
		}
	}
	if pending != maxPendingAsync || refused != notifies-maxPendingAsync {
		t.Errorf("%d pending and %d refused, want %d and %d", pending, refused, maxPendingAsync, notifies-maxPendingAsync)
	}
}

func TestMemLimit(t *testing.T) {
	l := newMemLimit(10)
	a, b := l.conn(), l.conn()
	done := make(chan struct{})
	r1 := a.acquire(8, done)
	if r1 == nil {
		t.Fatal("8 of 10 refused")
	}
	if a.tryAcquire(3) != nil {
		t.Error("11 of 10 bytes on one connection")
	}
	if r := b.acquire(10, done); r == nil {
		t.Error("another connection has a budget of its own")
	} else {
		r()
	}
	// A waiter gets its bytes once they come back.
	got := make(chan func())
	go func() { got <- a.acquire(4, done) }()
	select {
	case <-got:
		t.Fatal("4 more bytes did not wait")
	case <-time.After(50 * time.Millisecond):
	}
	r1()
	r1() // twice is once
	r3 := <-got
	if r3 == nil {
		t.Fatal("the waiter was refused")
	}
	r3()
	// More than the budget runs alone.
	big := a.acquire(40, done)
	if big == nil {
		t.Fatal("a charge larger than the budget never runs, even alone")
	}
	if a.tryAcquire(1) != nil {
		t.Error("a byte beside the large charge")
	}
	// A closed connection gives everything back, and waits no more.
	a.close()
	big()
	if l.used != 0 {
		t.Errorf("%d bytes held after the connection closed", l.used)
	}
	if a.acquire(1, done) != nil {
		t.Error("a closed connection took bytes")
	}
	close(done)
	d := l.conn()
	d.acquire(10, nil)
	if d.acquire(1, done) != nil {
		t.Error("a waiter did not stop when its connection ended")
	}
}

// panickyShare panics on one name, as a Backend with a bug would.
type panickyShare struct{ *testShare }

func (s *panickyShare) Stat(p string) (FileInfo, error) {
	if strings.HasSuffix(p, "boom") {
		panic("the backend broke on " + p)
	}
	return s.testShare.Stat(p)
}

type panickyBackend struct{ *testBackend }

func (b *panickyBackend) AuthLog(username, remote string, ok bool) time.Duration {
	if username == "panic" {
		panic("the backend broke on a login")
	}
	return b.testBackend.AuthLog(username, remote, ok)
}

// PATCHES.md, 5: a panic in a request, or in a login, closes the connection
// it came from; the others and the server go on. Upstream, the process died.
func TestPatchPanicClosesOneConnection(t *testing.T) {
	share := &testShare{name: "data", root: t.TempDir(), denyWrite: map[string]bool{}, denyRead: map[string]bool{}, spaceLeft: -1}
	backend := &panickyBackend{&testBackend{shares: []Share{&panickyShare{share}}}}
	srv, err := New(Config{Address: "127.0.0.1:0", ServerName: "AROZTEST", Backend: backend, LoginDelay: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	h := &harness{srv: srv, addr: srv.Addr().String()}
	if err := os.WriteFile(filepath.Join(share.root, "a.txt"), []byte("still here"), 0o644); err != nil {
		t.Fatal(err)
	}

	bystander := mount(t, h)
	victim := mount(t, h)
	if _, err := victim.Stat("boom"); err == nil {
		t.Fatal("a request whose backend panicked succeeded")
	}
	if _, err := victim.Stat("a.txt"); err == nil {
		t.Error("the connection of the panic is still open")
	}
	// A panic in the login, on the reading goroutine itself.
	if _, err := dial(t, h, "panic", "x"); err == nil {
		t.Error("a login whose backend panicked succeeded")
	}
	if b, err := bystander.ReadFile("a.txt"); err != nil || string(b) != "still here" {
		t.Errorf("another connection: %q, %v", b, err)
	}
	fresh := mount(t, h)
	if b, err := fresh.ReadFile("a.txt"); err != nil || string(b) != "still here" {
		t.Errorf("a new connection: %q, %v", b, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for srv.ConnectionCount() != 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := srv.ConnectionCount(); n != 2 {
		t.Errorf("%d connections, want the bystander and the new one", n)
	}
}

// PATCHES.md, 4: a compound of READs larger than the budget is answered:
// the reads beyond it are refused instead of waiting for room that only the
// compound's own responses would give back. Before, the connection hung.
func TestPatchCompoundOfReadsDoesNotWaitForItself(t *testing.T) {
	h := newHarness(t)
	writeTestFile(t, h, "big.bin", 16<<20)
	c := newRawClient(t, h)
	id, st := c.create("big.bin", createOpts{access: wire.FileReadData, share: 7, disposition: wire.DispositionOpen})
	if st != 0 {
		t.Fatalf("create: %x", st)
	}
	const reads, charge = 6, maxTransact / 65536
	c.grow(reads*charge + 1)
	frame := []byte{}
	for i := 0; i < reads; i++ {
		hd := wire.Header{Command: wire.CmdRead, Credits: charge, CreditCharge: charge, MessageID: c.mid, SessionID: c.session, TreeID: c.tree}
		c.mid += charge
		body := readBodyOf(id, uint64(i%2)*maxTransact, maxTransact)
		m := make([]byte, wire.HeaderSize+len(body))
		hd.Put(m)
		copy(m[wire.HeaderSize:], body)
		if i < reads-1 {
			for len(m)%8 != 0 {
				m = append(m, 0)
			}
			binary.LittleEndian.PutUint32(m[20:], uint32(len(m)))
		}
		c.signer.Sign(m)
		frame = append(frame, m...)
	}
	c.writeFrame(frame)
	replies := parseReplies(t, c.readFrame())
	ok, refused := 0, 0
	for _, r := range replies {
		switch r.hdr.Status {
		case 0:
			ok++
		case wire.StatusInsufficientResources:
			refused++
		default:
			t.Errorf("read: %x", r.hdr.Status)
		}
	}
	if ok != 1 || refused != reads-1 {
		t.Errorf("%d reads answered and %d refused, of %d", ok, refused, reads)
	}
	// The connection still works.
	c.sendCharged(wire.CmdRead, readBodyOf(id, 0, 4096), 1)
	if r := c.finals(1, 5*time.Second)[0]; r.hdr.Status != 0 {
		t.Errorf("a read after the compound: %x", r.hdr.Status)
	}
}

// PATCHES.md, 3: a search pattern longer than a name is refused.
func TestPatchLongPatternIsRefused(t *testing.T) {
	h := newHarness(t)
	if err := os.Mkdir(filepath.Join(h.share.root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.share.root, "dir", "a.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	c := newRawClient(t, h)
	id, st := c.create("dir", createOpts{access: wire.FileReadData, share: 7, disposition: wire.DispositionOpen, options: wire.OptionDirectory})
	if st != 0 {
		t.Fatalf("create: %x", st)
	}
	r := c.call(wire.CmdQueryDirectory, queryDirBody(id, wire.FileDirectoryInformation, wire.RestartScans, strings.Repeat("*", 300)))
	if r.hdr.Status != wire.StatusObjectNameInvalid {
		t.Errorf("a pattern of 300 stars: %x", r.hdr.Status)
	}
	r = c.call(wire.CmdQueryDirectory, queryDirBody(id, wire.FileDirectoryInformation, wire.RestartScans, strings.Repeat("*", 255)))
	if r.hdr.Status != 0 {
		t.Errorf("a pattern of 255 stars: %x", r.hdr.Status)
	}
}
