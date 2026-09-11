// Package fakereplicatest runs fake replicas that can die the way a process
// does, for testing what the router and the harness do when one does.
//
// It lives beside the fake rather than in each test package because what it
// imitates — every connection cut at once and the port refusing new ones, then
// the same port answering again once the process is restarted — is exactly the
// shape a copy would get subtly wrong, and a kill that let in-flight requests
// finish would be testing a drain.
package fakereplicatest

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// Mortal is a replica server that can be killed and revived on its address.
type Mortal struct {
	handler http.Handler
	addr    string

	mu  sync.Mutex
	srv *httptest.Server
}

// Start serves handler on a loopback port until the test ends.
func Start(t testing.TB, handler http.Handler) *Mortal {
	t.Helper()
	m := &Mortal{handler: handler, srv: httptest.NewServer(handler)}
	m.addr = m.srv.Listener.Addr().String()
	t.Cleanup(m.Kill)
	return m
}

// URL is the replica's base URL. It survives a kill and a revival, as a real
// replica's does: the port is the replica's identity in the fleet spec.
func (m *Mortal) URL() string { return "http://" + m.addr }

// Kill does to the server what kill -9 does to a replica process, as its
// clients see it: the port stops accepting connections, and every connection
// already open is cut mid-exchange, whatever was on it. Nothing in flight is
// allowed to finish. Killing a dead replica does nothing.
func (m *Mortal) Kill() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.srv == nil {
		return
	}
	// The listener first, so nothing reconnects in the moment between cutting the
	// connections and closing the server.
	m.srv.Listener.Close()
	m.srv.CloseClientConnections()
	// Close waits for the handlers, which return promptly: the fake watches its
	// request's context, and a cut connection cancels it.
	m.srv.Close()
	m.srv = nil
}

// Revive serves the handler on the same address again, as a replica restarted on
// its own port does. Reviving a live replica does nothing.
func (m *Mortal) Revive() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.srv != nil {
		return nil
	}
	ln, err := net.Listen("tcp", m.addr)
	if err != nil {
		return fmt.Errorf("fakereplicatest: revive on %s: %w", m.addr, err)
	}
	srv := httptest.NewUnstartedServer(m.handler)
	srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	m.srv = srv
	return nil
}
