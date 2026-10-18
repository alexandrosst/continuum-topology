package estate

import (
	"testing"
	"time"
)

func TestAnAgentThatSaysNothingAboutItsConfigIsNeverSentOneAgain(t *testing.T) {
	r := newHubRig(t)
	f := r.newFakeAgent(t, fakeAgentCtx(t))
	f.changeTier()
	r.hub.Mu.Lock()
	r.hub.Views[f.id].CfgSentAt = time.Now().Add(-time.Hour) // long past the grace
	r.hub.Mu.Unlock()
	for i := 0; i < 5; i++ {
		f.beat("") // an agent from before the echo
	}
	f.beat("") // the last one is only a marker: when this one is seen the earlier ones were handled too
	if c := f.nextConfig(500 * time.Millisecond); c != nil {
		t.Fatalf("an agent that echoes nothing was sent a Config again: %v", c.Digest)
	}
	if _, applied, _ := f.inForce(); applied != "" {
		t.Fatalf("an empty echo was recorded as %q", applied)
	}
}

func TestANewConnectionEndsTheOldOne(t *testing.T) {
	r := newHubRig(t)
	f := r.newFakeAgent(t, fakeAgentCtx(t))
	oldCancel, oldIn := f.cancel, f.incoming
	defer oldCancel()
	f.connect() // no drop: the old stream is still open when the new one arrives
	deadline := time.After(5 * time.Second)
	for ended := false; !ended; {
		select {
		case in := <-oldIn:
			ended = in.err != nil
		case <-deadline:
			t.Fatal("the server kept the old stream open after a newer one connected")
		}
	}
	r.hub.Mu.Lock()
	n := len(r.hub.Sessions)
	r.hub.Mu.Unlock()
	if n != 1 {
		t.Fatalf("%d sessions for one agent", n)
	}
}

// Deleting an organisation ends its agents' streams, and tells each agent to stop for good, in the words given.
func TestDroppingAllEndsEveryStreamAndTellsTheAgentToStopForGood(t *testing.T) {
	r := newHubRig(t)
	f := r.newFakeAgent(t, fakeAgentCtx(t))
	defer f.cancel()
	r.hub.DropAll("the organisation was deleted")
	deadline := time.After(5 * time.Second)
	var told, ended bool
	for !ended {
		select {
		case in := <-f.incoming:
			if rv := in.m.GetRevoked(); rv != nil {
				told = rv.Reason == "the organisation was deleted"
			}
			ended = in.err != nil
		case <-deadline:
			t.Fatal("the stream was still open after every stream was dropped")
		}
	}
	if !told {
		t.Fatal("the agent was not told to stop for good, with the reason")
	}
}
