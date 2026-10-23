package testkit

import (
	"context"
	"crypto/tls"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"

	"google.golang.org/grpc"
)

// FakeAgent is a scripted agent on a real mutual-TLS stream to a real server. The real agent (internal/agent) is the thing the
// end-to-end tests run, and it cannot be told to misbehave: to be late, to say the wrong thing, to vanish, to come back. This one does
// only what the test tells it, so a rule of the protocol can be checked in the exact situation that breaks it. It keeps what it was
// sent, answers nothing by itself (no heartbeat, no echo), and the test says each step.
//
// Faults it can make, each one a call: Cancel (the stream ends with no goodbye), Connect again (a new stream from the same
// identity), Beat with any digest (an echo that is late, from an older Config, or from one the server never sent), say nothing at all
// (the default), Send any message at all (a second hello, a sync before the hello, a flood), Replay one it sent before, SendReordered
// in an order of the test's choosing, and Ended to learn what ended the stream. A stream that was replaced by a Connect is kept by a
// test that wants it (its Stream, Incoming and Cancel are fields), and can be sent on after it has been replaced.
//
// What it cannot do is look inside the server: whether the server has noticed the stream end, which Config it believes is in force.
// That is the package's own, which embeds a FakeAgent and adds it.
type FakeAgent struct {
	T     *testing.T
	Ctx   context.Context
	ID    string
	Key   any
	Leaf  []byte
	Hello *continuumv1.Hello
	// Dial opens a connection to the server with the certificate given as the client's.
	Dial func(cert *tls.Certificate) *grpc.ClientConn

	Cancel   context.CancelFunc // ends the current stream
	Stream   continuumv1.AgentService_ConnectClient
	Incoming chan Recvd
	Configs  []*continuumv1.Config       // every Config received on any stream, in order
	Sent     []*continuumv1.AgentMessage // everything sent with Send, Beat, Replay and SendReordered, on any stream, in order
}

// Recvd is one thing the server said, or the error that ended the stream.
type Recvd struct {
	M   *continuumv1.ServerMessage
	Err error
}

// Connect opens a stream, says hello and waits for the Config the server sends in answer.
func (f *FakeAgent) Connect() *continuumv1.Config {
	f.T.Helper()
	cert := &tls.Certificate{Certificate: [][]byte{f.Leaf}, PrivateKey: f.Key}
	c := continuumv1.NewAgentServiceClient(f.Dial(cert))
	sctx, cancel := context.WithCancel(f.Ctx)
	s, err := c.Connect(sctx)
	if err != nil {
		cancel()
		f.T.Fatalf("connect: %v", err)
	}
	f.Cancel, f.Stream = cancel, s
	f.T.Cleanup(cancel)
	in := make(chan Recvd, 64)
	f.Incoming = in
	go func() {
		for {
			m, err := s.Recv()
			in <- Recvd{m, err}
			if err != nil {
				return
			}
		}
	}()
	if err := s.Send(&continuumv1.AgentMessage{Msg: &continuumv1.AgentMessage_Hello{Hello: f.Hello}}); err != nil {
		f.T.Fatalf("hello: %v", err)
	}
	cfg := f.NextConfig(5 * time.Second)
	if cfg == nil {
		f.T.Fatal("the server sent no Config after hello")
	}
	return cfg
}

// NextConfig waits up to d for the next Config on the current stream and returns nil if none comes. Other messages are skipped.
func (f *FakeAgent) NextConfig(d time.Duration) *continuumv1.Config {
	f.T.Helper()
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case in := <-f.Incoming:
			if in.Err != nil {
				return nil
			}
			if c := in.M.GetConfig(); c != nil {
				f.Configs = append(f.Configs, c)
				return c
			}
		case <-timer.C:
			return nil
		}
	}
}

// Beat sends a heartbeat that names digest as the Config in force ("" says nothing, as an older agent does).
func (f *FakeAgent) Beat(digest string) {
	f.T.Helper()
	m := &continuumv1.AgentMessage{Msg: &continuumv1.AgentMessage_Heartbeat{Heartbeat: &continuumv1.Heartbeat{AppliedConfigDigest: digest}}}
	if err := f.Send(m); err != nil {
		f.T.Fatalf("heartbeat: %v", err)
	}
}

// Send sends any message on the current stream and remembers it. The error is the test's to judge: a stream the server has ended
// refuses the send, and a test of what happens after that wants to see it.
func (f *FakeAgent) Send(m *continuumv1.AgentMessage) error {
	f.Sent = append(f.Sent, m)
	return f.Stream.Send(m)
}

// Replay sends again, unchanged, the i-th message sent so far (a negative i counts from the last), on the current stream, which may be
// a newer one than it first went on.
func (f *FakeAgent) Replay(i int) error {
	f.T.Helper()
	if i < 0 {
		i += len(f.Sent)
	}
	if i < 0 || i >= len(f.Sent) {
		f.T.Fatalf("there is no message %d to replay among the %d sent", i, len(f.Sent))
	}
	return f.Stream.Send(f.Sent[i])
}

// SendReordered sends msgs in the order given by order, which are indexes into msgs (so 2, 0, 1 sends the third first). A stream is
// ordered, so this is how an agent that numbered its messages one way and sent them another looks to the server.
func (f *FakeAgent) SendReordered(msgs []*continuumv1.AgentMessage, order ...int) error {
	f.T.Helper()
	for _, i := range order {
		if err := f.Send(msgs[i]); err != nil {
			return err
		}
	}
	return nil
}

// Ended waits up to d for the current stream to end and returns what ended it, or nil if it is still open. Whatever the server said
// on the way is read and dropped.
func (f *FakeAgent) Ended(d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case in := <-f.Incoming:
			if in.Err != nil {
				return in.Err
			}
		case <-timer.C:
			return nil
		}
	}
}

// WaitUntil polls until cond is true. The server handles a message on its own goroutine, so a test that sent one has to wait for it.
func (f *FakeAgent) WaitUntil(what string, cond func() bool) {
	f.T.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			f.T.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
