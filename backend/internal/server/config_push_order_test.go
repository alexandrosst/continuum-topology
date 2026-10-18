package server

import (
	"context"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/estate"
	"continuum/internal/store"
	"continuum/internal/testkit"
)

// holdingStore lets a test stop one GetAgent call after it has read the record and before it returns it, which is exactly the
// moment two pushes to the same agent can go out of order.
type holdingStore struct {
	store.Store
	armed   chan struct{} // closed-over by the test: receives once, from the first call to hold
	reached chan struct{}
	release chan struct{}
}

func (s *holdingStore) GetAgent(ctx context.Context, id string) (store.Agent, error) {
	a, err := s.Store.GetAgent(ctx, id)
	select {
	case <-s.armed:
		close(s.reached)
		<-s.release
	default:
	}
	return a, err
}

func pushRig(t *testing.T) (*adminRig, *estate.Hub, string, chan *continuumv1.ServerMessage) {
	t.Helper()
	a := newAdminRig(t)
	agentID := a.approvedAgentID(t, testkit.FP)
	push := make(chan *continuumv1.ServerMessage, 64)
	h := a.hub()
	h.Mu.Lock()
	h.Views[agentID] = liveView(*a.now, "n1")
	h.Sessions[agentID] = &estate.Session{Push: push, Cancel: func(estate.EndReason) {}}
	h.Mu.Unlock()
	return a, h, agentID, push
}

func drain(push chan *continuumv1.ServerMessage) []*continuumv1.Config {
	var out []*continuumv1.Config
	for {
		select {
		case m := <-push:
			if c := m.GetConfig(); c != nil {
				out = append(out, c)
			}
		default:
			return out
		}
	}
}

// Two pushes to the same agent can read its record at different moments. If the one that read the older record were to queue
// after the one that read the newer, the agent would be left with the old Config while the server believed it had sent the new one.
// The first push is held right after it has read the record; an administrator changes the tier and pushes again; then the first is
// let go. Whatever order they finish in, the last Config queued must carry the new tier.
func TestTheLastConfigPushedToAnAgentIsTheCurrentOne(t *testing.T) {
	_, h, agentID, push := pushRig(t)
	hold := &holdingStore{Store: h.C.Store, armed: make(chan struct{}, 1), reached: make(chan struct{}), release: make(chan struct{})}
	h.C.Store = hold
	hold.armed <- struct{}{}

	first := make(chan struct{})
	go func() { defer close(first); h.PushOne(agentID) }()
	select {
	case <-hold.reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the first push never read the agent")
	}
	if err := hold.Store.SetAccessTier(context.Background(), agentID, 2); err != nil {
		t.Fatal(err)
	}
	second := make(chan struct{})
	go func() { defer close(second); h.PushOne(agentID) }()
	select {
	case <-second: // without the per-agent lock the second push finishes first, and the first then queues the older Config
	case <-time.After(300 * time.Millisecond):
	}
	close(hold.release)
	for _, c := range []chan struct{}{first, second} {
		select {
		case <-c:
		case <-time.After(5 * time.Second):
			t.Fatal("a push did not finish")
		}
	}
	got := drain(push)
	if len(got) == 0 {
		t.Fatal("nothing was queued")
	}
	if last := got[len(got)-1]; last.ApprovedAccessTier != 2 {
		t.Fatalf("the last Config queued carries tier %d; the agent now has tier 2", last.ApprovedAccessTier)
	}
}

// Every Config carries the digest the server keeps as "sent", so the agent can echo it.
func TestAConfigCarriesItsDigest(t *testing.T) {
	_, h, agentID, push := pushRig(t)
	h.PushOne(agentID)
	got := drain(push)
	if len(got) != 1 || got[0].Digest == "" {
		t.Fatalf("want one Config with a digest, got %+v", got)
	}
	h.Mu.Lock()
	want := h.Views[agentID].CfgHash
	h.Mu.Unlock()
	if got[0].Digest != want {
		t.Fatalf("digest %q is not the one the server holds as sent, %q", got[0].Digest, want)
	}
}

// A heartbeat that names an older Config than the last one sent, once the grace has passed, means the new one was lost: it is
// sent again, once. An empty digest (an older agent) and a matching one ask for nothing, and neither does a heartbeat inside the grace.
func TestAHeartbeatNamingAnOlderConfigAsksForItToBeSentAgain(t *testing.T) {
	_, h, agentID, push := pushRig(t)
	h.PushOne(agentID)
	sent := drain(push)
	if len(sent) != 1 {
		t.Fatalf("want one Config, got %d", len(sent))
	}
	now := time.Now()
	h.Mu.Lock()
	v := h.Views[agentID]
	v.CfgSentAt = now.Add(-time.Minute)
	cases := []struct {
		name, digest string
		at           time.Time
		want         bool
	}{
		{"an older agent echoes nothing", "", now, false},
		{"the digest in force is the one sent", sent[0].Digest, now, false},
		{"an older Config, inside the grace", "old", v.CfgSentAt.Add(time.Second), false},
		{"an older Config, after the grace", "old", now, true},
	}
	for _, c := range cases {
		if got := v.NoteConfigEcho(c.digest, c.at); got != c.want {
			t.Errorf("%s: asked again = %v, want %v", c.name, got, c.want)
		}
		if !c.want && v.CfgHash == "" {
			t.Errorf("%s: the sent hash was cleared", c.name)
		}
		if c.want {
			v.CfgHash = sent[0].Digest // the test puts it back, as the next push would
		}
	}
	h.Mu.Unlock()

	// End to end through PushOne: the lost Config is queued again, once.
	h.Mu.Lock()
	v.CfgSentAt = now.Add(-time.Minute)
	_ = v.NoteConfigEcho("old", now)
	h.Mu.Unlock()
	h.PushOne(agentID)
	again := drain(push)
	if len(again) != 1 || again[0].Digest != sent[0].Digest {
		t.Fatalf("want the same Config sent again, got %+v", again)
	}
	h.PushOne(agentID)
	if extra := drain(push); len(extra) != 0 {
		t.Fatalf("a Config nothing changed for was sent a third time: %+v", extra)
	}
}
