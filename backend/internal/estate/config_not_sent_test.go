package estate

import (
	"context"
	"testing"
	"time"

	ikhnosv1 "ikhnos/gen/ikhnosv1"
	"ikhnos/internal/connectivity"
	"ikhnos/internal/kernel"
	"ikhnos/internal/testkit"
)

// A Config that was built and not sent changes nothing the server keeps about the agent. The agent still holds the Config it was
// last sent, so it goes on measuring the targets in it, and what it measures must still be accepted and kept: forgetting a target
// and its paths for a Config that never left the server would drop the agent's results until a push finally got through.
func TestAConfigThatIsNotSentDoesNotChangeWhatTheServerKeepsOfTheAgent(t *testing.T) {
	r := newHubRig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id, key, leaf := r.approvedAgent(t, testkit.FP)
	r.agentStream(t, ctx, id, key, leaf)
	a, err := r.st.GetAgent(ctx, "org-1", id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.core.SaveSettings(ctx, "alex", kernel.Settings{MeasureSeconds: 90, ProbeTargets: []kernel.ProbeTarget{
		{ID: "pt-1", ClusterID: a.ClusterID, Label: "gateway", Host: "203.0.113.10", Port: 443}}}); err != nil {
		t.Fatal(err)
	}
	r.hub.Settle()
	r.hub.PushOne(id) // the agent now holds a Config with pt-1
	r.hub.Mu.Lock()
	v := r.hub.Views[id]
	if _, ok := v.Targets["pt-1"]; !ok {
		r.hub.Mu.Unlock()
		t.Fatalf("setup: the target was not issued: %v", v.Targets)
	}
	v.Paths["pt-1"] = &connectivity.PathTrack{T: v.Targets["pt-1"]}
	sentHash := v.CfgHash
	real := r.hub.Sessions[id]
	r.hub.Sessions[id] = &Session{Push: make(chan *ikhnosv1.ServerMessage)} // a stream that is busy: nothing can be queued on it
	r.hub.Mu.Unlock()

	if _, err := r.core.SaveSettings(ctx, "alex", kernel.Settings{MeasureSeconds: 90}); err != nil { // the target is taken away
		t.Fatal(err)
	}
	r.hub.Settle()
	r.hub.PushOne(id) // built, and not sent

	r.hub.Mu.Lock()
	_, issued := v.Targets["pt-1"]
	_, kept := v.Paths["pt-1"]
	hash := v.CfgHash
	r.hub.Sessions[id] = real
	r.hub.Mu.Unlock()
	if !issued || !kept {
		t.Errorf("a Config that was not sent made the server forget what the agent still measures: target issued %v, path kept %v", issued, kept)
	}
	if hash != sentHash {
		t.Errorf("the hash of the Config last sent changed to that of one never sent")
	}

	r.hub.PushOne(id) // now it goes
	r.hub.Mu.Lock()
	_, issued = v.Targets["pt-1"]
	_, kept = v.Paths["pt-1"]
	r.hub.Mu.Unlock()
	if issued || kept {
		t.Errorf("once the Config is sent the target and its path are dropped: issued %v, path kept %v", issued, kept)
	}
}
