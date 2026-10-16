package server

import (
	"context"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/store"
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

func pushRig(t *testing.T) (*adminRig, *Hub, string, chan *continuumv1.ServerMessage) {
	t.Helper()
	a := newAdminRig(t)
	agentID := a.approvedAgentID(t, fp)
	push := make(chan *continuumv1.ServerMessage, 64)
	h := a.hub()
	h.mu.Lock()
	h.views[agentID] = liveView(*a.now, "n1")
	h.sessions[agentID] = &session{push: push, cancel: func(endReason) {}}
	h.mu.Unlock()
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
	go func() { defer close(first); h.pushOne(agentID) }()
	select {
	case <-hold.reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the first push never read the agent")
	}
	if err := hold.Store.SetAccessTier(bg(), agentID, 2); err != nil {
		t.Fatal(err)
	}
	second := make(chan struct{})
	go func() { defer close(second); h.pushOne(agentID) }()
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
