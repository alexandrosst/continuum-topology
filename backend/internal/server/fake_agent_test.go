package server

import (
	"context"
	"crypto/tls"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/pki"
)

// fakeAgent is a scripted agent on a real mutual-TLS stream to a real server. The real agent (internal/agent) is the thing the
// end-to-end tests run, and it cannot be told to misbehave: to be late, to say the wrong thing, to vanish, to come back. This
// one does only what the test tells it, so a rule of the protocol can be checked in the exact situation that breaks it. It keeps
// what it was sent, answers nothing by itself (no heartbeat, no echo), and the test says each step.
//
// Faults it can make, each one a method: drop (the stream ends with no goodbye), restart (a new stream from the same identity),
// beat with any digest (an echo that is late, from an older Config, or from one the server never sent), say nothing at all (the
// default), and send what it likes after being replaced.
type fakeAgent struct {
	t     *testing.T
	r     *hubRig
	ctx   context.Context
	id    string
	key   any
	leaf  []byte
	hello *continuumv1.Hello

	cancel   context.CancelFunc // ends the current stream
	stream   continuumv1.AgentService_ConnectClient
	incoming chan recvdMsg
	configs  []*continuumv1.Config // every Config received on any stream, in order
}

type recvdMsg struct {
	m   *continuumv1.ServerMessage
	err error
}

// newFakeAgent makes an approved agent and connects it: the hello is sent and the first Config, which the server sends at once, is
// received before it returns.
func (r *hubRig) newFakeAgent(t *testing.T, ctx context.Context) *fakeAgent {
	t.Helper()
	return r.newFakeAgentWith(t, ctx, &continuumv1.Hello{AgentVersion: "fake"})
}

// newFakeAgentWith is newFakeAgent with a hello of the test's choosing.
func (r *hubRig) newFakeAgentWith(t *testing.T, ctx context.Context, hello *continuumv1.Hello) *fakeAgent {
	t.Helper()
	id, key, leaf := r.approvedAgent(t, fp)
	f := &fakeAgent{t: t, r: r, ctx: ctx, id: id, key: key, leaf: leaf, hello: hello}
	f.connect()
	return f
}

// connect opens a stream, says hello and waits for the Config the server sends in answer.
func (f *fakeAgent) connect() *continuumv1.Config {
	f.t.Helper()
	cert := &tls.Certificate{Certificate: [][]byte{f.leaf}, PrivateKey: f.key}
	c := continuumv1.NewAgentServiceClient(f.r.dial(f.t, pki.ClientTLS(f.r.core.CA.Pin(), "127.0.0.1", cert)))
	sctx, cancel := context.WithCancel(f.ctx)
	s, err := c.Connect(sctx)
	if err != nil {
		cancel()
		f.t.Fatalf("connect: %v", err)
	}
	f.cancel, f.stream = cancel, s
	f.t.Cleanup(cancel)
	in := make(chan recvdMsg, 64)
	f.incoming = in
	go func() {
		for {
			m, err := s.Recv()
			in <- recvdMsg{m, err}
			if err != nil {
				return
			}
		}
	}()
	if err := s.Send(&continuumv1.AgentMessage{Msg: &continuumv1.AgentMessage_Hello{Hello: f.hello}}); err != nil {
		f.t.Fatalf("hello: %v", err)
	}
	cfg := f.nextConfig(5 * time.Second)
	if cfg == nil {
		f.t.Fatal("the server sent no Config after hello")
	}
	return cfg
}

// nextConfig waits up to d for the next Config on the current stream and returns nil if none comes. Other messages are skipped.
func (f *fakeAgent) nextConfig(d time.Duration) *continuumv1.Config {
	f.t.Helper()
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case in := <-f.incoming:
			if in.err != nil {
				return nil
			}
			if c := in.m.GetConfig(); c != nil {
				f.configs = append(f.configs, c)
				return c
			}
		case <-timer.C:
			return nil
		}
	}
}

// beat sends a heartbeat that names digest as the Config in force ("" says nothing, as an older agent does).
func (f *fakeAgent) beat(digest string) {
	f.t.Helper()
	m := &continuumv1.AgentMessage{Msg: &continuumv1.AgentMessage_Heartbeat{Heartbeat: &continuumv1.Heartbeat{AppliedConfigDigest: digest}}}
	if err := f.stream.Send(m); err != nil {
		f.t.Fatalf("heartbeat: %v", err)
	}
}

// drop ends the stream the way a crash or a cut network does: nothing is said, the connection just stops.
func (f *fakeAgent) drop() {
	f.t.Helper()
	f.cancel()
	f.waitUntil("the server to notice the stream ended", func() bool {
		f.r.hub.Mu.Lock()
		defer f.r.hub.Mu.Unlock()
		return f.r.hub.Sessions[f.id] == nil
	})
}

// restart is a drop and a new stream with the same identity, which is what a restarted agent pod does.
func (f *fakeAgent) restart() *continuumv1.Config {
	f.t.Helper()
	f.drop()
	return f.connect()
}

// waitUntil polls until cond is true. The server handles a message on its own goroutine, so a test that sent one has to wait for it.
func (f *fakeAgent) waitUntil(what string, cond func() bool) {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			f.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// inForce is the digest the server believes is in force, and since when (zero if it is not).
func (f *fakeAgent) inForce() (sent, applied string, since time.Time) {
	f.r.hub.Mu.Lock()
	defer f.r.hub.Mu.Unlock()
	v := f.r.hub.Views[f.id]
	return v.CfgHash, v.CfgApplied, v.CfgInForceAt
}

// changeTier is an administrator's change that makes the next Config differ, and the push that follows it.
func (f *fakeAgent) changeTier() *continuumv1.Config {
	f.t.Helper()
	cur, err := f.r.st.GetAgent(f.ctx, f.id)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.r.st.SetAccessTier(f.ctx, f.id, (cur.AccessTier+1)%3); err != nil {
		f.t.Fatal(err)
	}
	f.r.hub.PushOne(f.id)
	c := f.nextConfig(5 * time.Second)
	if c == nil {
		f.t.Fatal("no Config was pushed after the change")
	}
	return c
}

// ---------- what the harness pins ----------
//
// These are rules the config channel already keeps, written as scenarios an agent can break one way each. Stage 2 adds a protocol
// version and named capabilities to the hello and conditions to the heartbeat; the scenarios below must keep passing through it.

func fakeAgentCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestAConfigIsInForceWhenItsOwnDigestIsEchoedAndNotBefore(t *testing.T) {
	r := newHubRig(t)
	f := r.newFakeAgent(t, fakeAgentCtx(t))
	first := f.configs[0]
	sent, applied, since := f.inForce()
	if sent != first.Digest || applied != "" || !since.IsZero() {
		t.Fatalf("before any echo: sent %q applied %q in force since %v (the Config's digest is %q)", sent, applied, since, first.Digest)
	}
	f.beat(first.Digest)
	f.waitUntil("the echo", func() bool { _, a, _ := f.inForce(); return a == first.Digest })
	if _, _, since := f.inForce(); since.IsZero() {
		t.Fatal("the Config was echoed and is not in force")
	}
}

func TestAnEchoOfAnOlderConfigDoesNotPutTheNewOneInForce(t *testing.T) {
	r := newHubRig(t)
	f := r.newFakeAgent(t, fakeAgentCtx(t))
	old := f.configs[0]
	next := f.changeTier()
	if next.Digest == old.Digest {
		t.Fatal("the change did not change the Config")
	}
	// The echo of the first Config arrives after the second was sent: late, and about something no longer current.
	f.beat(old.Digest)
	f.waitUntil("the late echo", func() bool { _, a, _ := f.inForce(); return a == old.Digest })
	if sent, _, since := f.inForce(); sent != next.Digest || !since.IsZero() {
		t.Fatalf("a late echo of the older Config left sent %q (want %q) and in force since %v", sent, next.Digest, since)
	}
	f.beat(next.Digest)
	f.waitUntil("the echo of the current Config", func() bool { _, _, since := f.inForce(); return !since.IsZero() })
}

func TestAnEchoWithinTheGraceIsNotAskedForAgain(t *testing.T) {
	r := newHubRig(t)
	f := r.newFakeAgent(t, fakeAgentCtx(t))
	f.changeTier()
	f.beat(f.configs[0].Digest) // the agent is still on the first one, and the second was only just sent: it may be on its way
	f.waitUntil("the heartbeat", func() bool { _, a, _ := f.inForce(); return a == f.configs[0].Digest })
	if c := f.nextConfig(300 * time.Millisecond); c != nil {
		t.Fatalf("the server sent a Config again inside the grace: %v", c.Digest)
	}
}

func TestWhatAnAgentHadInForceBeforeARestartSaysNothingAboutTheNewStream(t *testing.T) {
	r := newHubRig(t)
	f := r.newFakeAgent(t, fakeAgentCtx(t))
	f.beat(f.configs[0].Digest)
	f.waitUntil("the echo", func() bool { _, _, since := f.inForce(); return !since.IsZero() })

	again := f.restart()
	if again.Digest != f.configs[0].Digest {
		t.Fatalf("a restart with nothing changed was sent a different Config: %q then %q", f.configs[0].Digest, again.Digest)
	}
	sent, applied, since := f.inForce()
	if sent != again.Digest || applied != "" || !since.IsZero() {
		t.Fatalf("after the restart: sent %q, applied %q, in force since %v: the old stream's echo carried over", sent, applied, since)
	}
}

func TestARestartAfterAChangeIsSentTheChangedConfig(t *testing.T) {
	r := newHubRig(t)
	f := r.newFakeAgent(t, fakeAgentCtx(t))
	before := f.configs[0]
	f.drop()
	cur, err := r.st.GetAgent(f.ctx, f.id) // changed while the agent was away: nothing to push to
	if err != nil {
		t.Fatal(err)
	}
	if err := r.st.SetAccessTier(f.ctx, f.id, (cur.AccessTier+1)%3); err != nil {
		t.Fatal(err)
	}
	after := f.connect()
	if after.Digest == before.Digest {
		t.Fatal("a change made while the agent was away was not in the Config it got on coming back")
	}
}
