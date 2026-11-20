package smbsrv

import (
	"sync"
	"sync/atomic"
)

/*
	limits.go

	What a client can make the server hold in memory (PATCHES.md, 4). The
	frames read off a connection and the responses of READs waiting to be
	written are counted in bytes per connection, one budget for each
	direction. A request waits for room rather than allocating past it, so a
	connection cannot run the process out of memory by asking for many large
	reads at once. There is no budget for the whole server: a client that
	stops reading its responses would hold it, and stall every other client,
	until the write deadline. What many connections hold together is left to
	the limits of the process (MaxConns, MaxPerIP, and the host's memory
	limit).
*/

// Memory budgets. A read of maxTransact still runs alone on a connection that
// holds nothing else.
const (
	maxConnInbound  = 32 << 20 //Frames of one connection being handled
	maxConnOutbound = 32 << 20 //READ data of one connection not yet written
	maxPendingAsync = 1024     //Requests of one connection answered STATUS_PENDING and not yet finished
)

// memLimit is one direction's budget, in bytes per connection.
type memLimit struct {
	mu      sync.Mutex
	perConn int64
	used    int64         //By every connection, for the tests
	wake    chan struct{} //Closed and replaced whenever bytes come back
}

func newMemLimit(perConn int64) *memLimit {
	return &memLimit{perConn: perConn}
}

// memUse is one connection's share of a memLimit.
type memUse struct {
	l      *memLimit
	used   int64
	closed bool
}

func (l *memLimit) conn() *memUse { return &memUse{l: l} }

// wakeLocked lets every waiter look again.
func (l *memLimit) wakeLocked() {
	if l.wake != nil {
		close(l.wake)
		l.wake = nil
	}
}

// acquire takes n bytes, waiting until they fit or done closes, and returns
// the function that gives them back; nil means the connection is ending. A
// charge larger than the budget is let through when the connection holds
// nothing, so no single request can wait forever.
func (u *memUse) acquire(n int64, done <-chan struct{}) func() {
	l := u.l
	for {
		l.mu.Lock()
		if u.closed {
			l.mu.Unlock()
			return nil
		}
		if u.used == 0 || u.used+n <= l.perConn {
			u.used += n
			l.used += n
			l.mu.Unlock()
			var once sync.Once
			return func() { once.Do(func() { u.release(n) }) }
		}
		if l.wake == nil {
			l.wake = make(chan struct{})
		}
		wake := l.wake
		l.mu.Unlock()
		select {
		case <-wake:
		case <-done:
			return nil
		}
	}
}

// tryAcquire takes n bytes only if they fit now, without the exception for
// an empty budget.
func (u *memUse) tryAcquire(n int64) func() {
	l := u.l
	l.mu.Lock()
	defer l.mu.Unlock()
	if u.closed || u.used+n > l.perConn {
		return nil
	}
	u.used += n
	l.used += n
	var once sync.Once
	return func() { once.Do(func() { u.release(n) }) }
}

func (u *memUse) release(n int64) {
	l := u.l
	l.mu.Lock()
	defer l.mu.Unlock()
	if u.closed {
		//Given back in one go when the connection ended
		return
	}
	u.used -= n
	l.used -= n
	l.wakeLocked()
}

// close gives back whatever the connection still holds, such as frames that
// were queued but never written. Later acquires fail, later releases do nothing.
func (u *memUse) close() {
	l := u.l
	l.mu.Lock()
	defer l.mu.Unlock()
	if u.closed {
		return
	}
	u.closed = true
	l.used -= u.used
	u.used = 0
	l.wakeLocked()
}

// frameHold is what one frame keeps while its requests run: the bytes of the
// frame and, once it took one, its place among the requests in flight. A
// request that goes on after its interim response keeps both until it ends,
// so the limit of maxInFlight counts it.
type frameHold struct {
	refs    atomic.Int32
	release func() //The frame's bytes
	slot    chan struct{}
}

func newFrameHold(release func()) *frameHold {
	h := &frameHold{release: release}
	h.refs.Store(1)
	return h
}

func (h *frameHold) retain() {
	if h != nil {
		h.refs.Add(1)
	}
}

func (h *frameHold) done() {
	if h == nil || h.refs.Add(-1) != 0 {
		return
	}
	if h.slot != nil {
		<-h.slot
	}
	if h.release != nil {
		h.release()
	}
}
