package smbsrv

import (
	"fmt"
	"runtime/debug"
)

/*
	guard.go

	A panic in a handler, or in the Backend it calls, ends the connection it
	came from and nothing else (PATCHES.md, 5). Every goroutine the server
	starts defers one of these.
*/

// guard is deferred at the top of a connection's goroutines: a panic is
// logged with its stack and the connection is closed.
func (c *conn) guard(where string) {
	if v := recover(); v != nil {
		c.panicked(where, v)
	}
}

// panicked logs a recovered panic and closes the connection.
func (c *conn) panicked(where string, v any) {
	c.srv.cfg.Logger.Error("smbsrv: panic, closing the connection",
		"component", "smbsrv", "where", where, "remote", c.remote,
		"panic", fmt.Sprint(v), "stack", string(debug.Stack()))
	c.shutdown()
}

// spawn runs fn on its own goroutine under the connection's guard.
func (c *conn) spawn(where string, fn func()) {
	go func() {
		defer c.guard(where)
		fn()
	}()
}

// guard is deferred at the top of the server's own goroutines (lease and
// notification polling, durable handle timers): a panic is logged and that
// goroutine ends, the server goes on.
func (s *Server) guard(where string) {
	if v := recover(); v != nil {
		s.guardValue(where, v)
	}
}

// guardValue logs a panic the server recovered.
func (s *Server) guardValue(where string, v any) {
	s.cfg.Logger.Error("smbsrv: panic",
		"component", "smbsrv", "where", where,
		"panic", fmt.Sprint(v), "stack", string(debug.Stack()))
}

// spawn runs fn on its own goroutine under the server's guard.
func (s *Server) spawn(where string, fn func()) {
	go func() {
		defer s.guard(where)
		fn()
	}()
}
