package server

import (
	"context"
	"crypto/tls"
	"testing"
	"time"

	ikhnosv1 "ikhnos/gen/ikhnosv1"
	"ikhnos/internal/pki"
	"ikhnos/internal/testkit"

	"google.golang.org/grpc"
)

// fakeAgent is the scripted agent of testkit with the server's side of it: what the sync hub in front of the test's gRPC listener has
// seen of it.
type fakeAgent struct {
	*testkit.FakeAgent
	r *hubRig
}

// newFakeAgent makes an approved agent and connects it: the hello is sent and the first Config, which the server sends at once, is
// received before it returns.
func (r *hubRig) newFakeAgent(t *testing.T, ctx context.Context) *fakeAgent {
	t.Helper()
	return r.newFakeAgentWith(t, ctx, &ikhnosv1.Hello{AgentVersion: "fake"})
}

// newFakeAgentWith is newFakeAgent with a hello of the test's choosing.
func (r *hubRig) newFakeAgentWith(t *testing.T, ctx context.Context, hello *ikhnosv1.Hello) *fakeAgent {
	t.Helper()
	id, key, leaf := r.approvedAgent(t, testkit.FP)
	f := &fakeAgent{r: r, FakeAgent: &testkit.FakeAgent{T: t, Ctx: ctx, ID: id, Key: key, Leaf: leaf, Hello: hello,
		Dial: func(c *tls.Certificate) *grpc.ClientConn {
			return r.dial(t, pki.ClientTLS(r.core.CA.Pin(), "127.0.0.1", c))
		}}}
	f.Connect()
	return f
}

// drop ends the stream the way a crash or a cut network does: nothing is said, the connection just stops.
func (f *fakeAgent) drop() {
	f.T.Helper()
	f.Cancel()
	f.WaitUntil("the server to notice the stream ended", func() bool {
		f.r.hub.Mu.Lock()
		defer f.r.hub.Mu.Unlock()
		return f.r.hub.Sessions[f.ID] == nil
	})
}

// restart is a drop and a new stream with the same identity, which is what a restarted agent pod does.
func (f *fakeAgent) restart() *ikhnosv1.Config {
	f.T.Helper()
	f.drop()
	return f.Connect()
}

// inForce is the digest the server believes is in force, and since when (zero if it is not).
func (f *fakeAgent) inForce() (sent, applied string, since time.Time) {
	f.r.hub.Mu.Lock()
	defer f.r.hub.Mu.Unlock()
	v := f.r.hub.Views[f.ID]
	return v.CfgHash, v.CfgApplied, v.CfgInForceAt
}

// changeTier is an administrator's change that makes the next Config differ, and the push that follows it.
func (f *fakeAgent) changeTier() *ikhnosv1.Config {
	f.T.Helper()
	cur, err := f.r.st.GetAgent(f.Ctx, "org-1", f.ID)
	if err != nil {
		f.T.Fatal(err)
	}
	if err := f.r.st.SetAccessTier(f.Ctx, "org-1", f.ID, (cur.AccessTier+1)%3); err != nil {
		f.T.Fatal(err)
	}
	f.r.hub.PushOne(f.ID)
	c := f.NextConfig(5 * time.Second)
	if c == nil {
		f.T.Fatal("no Config was pushed after the change")
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
	first := f.Configs[0]
	sent, applied, since := f.inForce()
	if sent != first.Digest || applied != "" || !since.IsZero() {
		t.Fatalf("before any echo: sent %q applied %q in force since %v (the Config's digest is %q)", sent, applied, since, first.Digest)
	}
	f.Beat(first.Digest)
	f.WaitUntil("the echo", func() bool { _, a, _ := f.inForce(); return a == first.Digest })
	if _, _, since := f.inForce(); since.IsZero() {
		t.Fatal("the Config was echoed and is not in force")
	}
}

func TestAnEchoOfAnOlderConfigDoesNotPutTheNewOneInForce(t *testing.T) {
	r := newHubRig(t)
	f := r.newFakeAgent(t, fakeAgentCtx(t))
	old := f.Configs[0]
	next := f.changeTier()
	if next.Digest == old.Digest {
		t.Fatal("the change did not change the Config")
	}
	// The echo of the first Config arrives after the second was sent: late, and about something no longer current.
	f.Beat(old.Digest)
	f.WaitUntil("the late echo", func() bool { _, a, _ := f.inForce(); return a == old.Digest })
	if sent, _, since := f.inForce(); sent != next.Digest || !since.IsZero() {
		t.Fatalf("a late echo of the older Config left sent %q (want %q) and in force since %v", sent, next.Digest, since)
	}
	f.Beat(next.Digest)
	f.WaitUntil("the echo of the current Config", func() bool { _, _, since := f.inForce(); return !since.IsZero() })
}

func TestAnEchoWithinTheGraceIsNotAskedForAgain(t *testing.T) {
	r := newHubRig(t)
	f := r.newFakeAgent(t, fakeAgentCtx(t))
	f.changeTier()
	f.Beat(f.Configs[0].Digest) // the agent is still on the first one, and the second was only just sent: it may be on its way
	f.WaitUntil("the heartbeat", func() bool { _, a, _ := f.inForce(); return a == f.Configs[0].Digest })
	if c := f.NextConfig(300 * time.Millisecond); c != nil {
		t.Fatalf("the server sent a Config again inside the grace: %v", c.Digest)
	}
}

func TestWhatAnAgentHadInForceBeforeARestartSaysNothingAboutTheNewStream(t *testing.T) {
	r := newHubRig(t)
	f := r.newFakeAgent(t, fakeAgentCtx(t))
	f.Beat(f.Configs[0].Digest)
	f.WaitUntil("the echo", func() bool { _, _, since := f.inForce(); return !since.IsZero() })

	again := f.restart()
	if again.Digest != f.Configs[0].Digest {
		t.Fatalf("a restart with nothing changed was sent a different Config: %q then %q", f.Configs[0].Digest, again.Digest)
	}
	sent, applied, since := f.inForce()
	if sent != again.Digest || applied != "" || !since.IsZero() {
		t.Fatalf("after the restart: sent %q, applied %q, in force since %v: the old stream's echo carried over", sent, applied, since)
	}
}

func TestARestartAfterAChangeIsSentTheChangedConfig(t *testing.T) {
	r := newHubRig(t)
	f := r.newFakeAgent(t, fakeAgentCtx(t))
	before := f.Configs[0]
	f.drop()
	cur, err := r.st.GetAgent(f.Ctx, "org-1", f.ID) // changed while the agent was away: nothing to push to
	if err != nil {
		t.Fatal(err)
	}
	if err := r.st.SetAccessTier(f.Ctx, "org-1", f.ID, (cur.AccessTier+1)%3); err != nil {
		t.Fatal(err)
	}
	after := f.Connect()
	if after.Digest == before.Digest {
		t.Fatal("a change made while the agent was away was not in the Config it got on coming back")
	}
}
