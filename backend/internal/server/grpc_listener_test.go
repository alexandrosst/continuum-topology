package server

import (
	"net"
	"sync"
	"syscall"
	"testing"
	"time"

	"ikhnos/internal/agents"
	"ikhnos/internal/pki"
)

// scriptedListener answers Accept with the errors it is given, then blocks until it is closed.
type scriptedListener struct {
	net.Listener
	mu     sync.Mutex
	errs   []error
	closed chan struct{}
	once   sync.Once
}

func newScriptedListener(t *testing.T, errs ...error) *scriptedListener {
	return &scriptedListener{Listener: listenLocal(t), errs: errs, closed: make(chan struct{})}
}

func (l *scriptedListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	if len(l.errs) > 0 {
		err := l.errs[0]
		l.errs = l.errs[1:]
		l.mu.Unlock()
		return nil, err
	}
	l.mu.Unlock()
	<-l.closed
	return nil, net.ErrClosed
}

func (l *scriptedListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return l.Listener.Close()
}

func serveScripted(t *testing.T, errs ...error) (*agents.GRPCServer, *scriptedListener, chan error) {
	t.Helper()
	e := newEnv(t)
	srv := e.base.Agents.NewGRPC(pki.NewServerCerts(e.core.CA, []string{"127.0.0.1"}), &agents.BaseAgentService{C: e.base.Agents})
	l := newScriptedListener(t, errs...)
	done := make(chan error, 1)
	go func() { done <- srv.Serve(l) }()
	t.Cleanup(func() { srv.Stop() })
	return srv, l, done
}

// A file-descriptor limit or an aborted connection is a moment, not the end of the listener: the agents must still be able to
// connect once it has passed.
func TestTheAgentListenerOutlivesAnAcceptThatFailedForAMoment(t *testing.T) {
	emfile := &net.OpError{Op: "accept", Net: "tcp", Err: syscall.EMFILE}
	srv, l, done := serveScripted(t, emfile, emfile, &net.OpError{Op: "accept", Net: "tcp", Err: syscall.ECONNABORTED})
	deadline := time.Now().Add(5 * time.Second)
	for {
		l.mu.Lock()
		left := len(l.errs)
		l.mu.Unlock()
		if left == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the listener did not get past the errors")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case err := <-done:
		t.Fatalf("Serve returned %v after errors that pass", err)
	case <-time.After(200 * time.Millisecond):
	}
	if !srv.Serving() {
		t.Error("the server stopped reporting that it is serving")
	}
}

// A listener that fails for good takes the server out of rotation: /readyz reads Serving, and must not say yes for a listener
// that is gone.
func TestTheServerStopsReportingServingWhenItsListenerFailsForGood(t *testing.T) {
	srv, _, done := serveScripted(t, &net.OpError{Op: "accept", Net: "tcp", Err: syscall.EBADF})
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Serve ended without saying why")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not end on a listener that failed for good")
	}
	if srv.Serving() {
		t.Error("the server still reports that it is serving after its listener failed")
	}
}
