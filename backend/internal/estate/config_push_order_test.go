package estate

import (
	"context"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"

	"continuum/internal/testkit"
)

// On a real stream: the agent names an older Config than the one the server sent (as if the newer one never arrived), and the
// server, once the grace has passed, sends the current one again.
func TestALostConfigIsSentAgainWhenTheAgentNamesAnOlderOne(t *testing.T) {
	r := newHubRig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id, key, leaf := r.approvedAgent(t, testkit.FP)
	s := r.agentStream(t, ctx, id, key, leaf)

	cur, err := r.st.GetAgent(ctx, "org-1", id)
	if err != nil {
		t.Fatal(err)
	}
	newTier := (cur.AccessTier + 1) % 3 // any tier the agent does not have: the Config then differs from the one sent at hello
	if err := r.st.SetAccessTier(ctx, "org-1", id, newTier); err != nil {
		t.Fatal(err)
	}
	r.hub.PushOne(id)
	pushed, err := s.Recv()
	if err != nil || int(pushed.GetConfig().GetApprovedAccessTier()) != newTier || pushed.GetConfig().GetDigest() == "" {
		t.Fatalf("the pushed Config: %v %v", pushed, err)
	}
	// The agent behaves as if that Config was lost: it still names the old digest. A heartbeat inside the grace changes nothing.
	r.hub.Mu.Lock()
	r.hub.Views[id].CfgSentAt = time.Now().Add(-time.Minute)
	r.hub.Mu.Unlock()
	beat := &continuumv1.AgentMessage{Msg: &continuumv1.AgentMessage_Heartbeat{Heartbeat: &continuumv1.Heartbeat{AppliedConfigDigest: "an-older-digest"}}}
	if err := s.Send(beat); err != nil {
		t.Fatal(err)
	}
	again, err := s.Recv()
	if err != nil || again.GetConfig().GetDigest() != pushed.GetConfig().GetDigest() {
		t.Fatalf("want the current Config sent again; got %v %v", again, err)
	}
}
