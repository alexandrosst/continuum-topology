package server

import (
	"context"
	"errors"
	"testing"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/kernel"
	"continuum/internal/observed"
	"continuum/internal/store"
)

// agentsDown is a store whose list of the organisation's agents cannot be read (a busy database).
type agentsDown struct{ store.Store }

func (agentsDown) ListAgents(context.Context, string) ([]store.Agent, error) {
	return nil, errors.New("database is locked")
}

// While the organisation's agents cannot be read the peer list would come out empty, not because nobody is paired: pushing that
// Config would make the agent drop every peer until the next good minute. The agent keeps what it has and the push is retried.
func TestAPushIsSkippedWhileTheAgentsCannotBeRead(t *testing.T) {
	r := newHubRig(t)
	id1, _, _ := r.approvedAgent(t, fp)
	id2, _, _ := r.approvedAgent(t, fp2)
	a1, _ := r.st.GetAgent(r.ctx, id1)
	a2, _ := r.st.GetAgent(r.ctx, id2)
	first, second := a1, a2
	if second.ClusterID < first.ClusterID {
		first, second = second, first
	}
	r.core.OnSettings = nil // the push a save starts runs in the background and would race the pushes under test
	if _, err := r.core.SaveSettings(r.ctx, "alex", kernel.Settings{Links: []kernel.LinkSetting{{A: first.ClusterID, B: second.ClusterID}}}); err != nil {
		t.Fatal(err)
	}
	newV := func(ip string) *view {
		return &view{estView: estView{state: nodeState(ip), flows: observed.NewFlowTable()}, netView: netView{paths: map[string]*pathTrack{}, targets: map[string]issuedTarget{}}}
	}
	fv, sv := newV("10.0.0.1"), newV("10.9.0.1")
	push := make(chan *continuumv1.ServerMessage, 4)
	r.hub.mu.Lock()
	r.hub.views[first.ID], r.hub.views[second.ID] = fv, sv
	r.hub.sessions[second.ID] = &session{push: push, cancel: func(endReason) {}}
	r.hub.mu.Unlock()

	// The agent is on the Config with its peer.
	cfg := r.hub.configFor(second, sv)
	if len(cfg.LinkPeers) != 1 {
		t.Fatalf("peers = %v", cfg.LinkPeers)
	}
	sent := sv.cfgHash

	real := r.hub.C.Store
	r.hub.C.Store = agentsDown{real}
	if _, complete := r.hub.configForChecked(second, sv); complete {
		t.Error("a Config built without the agents is reported as complete")
	}
	sv.cfgHash = sent
	r.hub.pushOne(second.ID)
	select {
	case m := <-push:
		t.Fatalf("a Config was pushed while the agents could not be read: %v", m.GetConfig().GetLinkPeers())
	default:
	}
	if sv.cfgHash != sent {
		t.Error("the Config that was not sent is remembered as sent, so the retry would find nothing to do")
	}
	// A cluster in no pair is unaffected by the failure: it does not read the agents at all.
	if _, complete := r.hub.configForChecked(store.Agent{ID: "ag-x", Status: store.StatusApproved, ClusterID: "cl-x"}, newV("10.5.0.1")); !complete {
		t.Error("a cluster with no pair needs no agents list")
	}

	// The next good minute: the peer list changed (the peer reported a new address), so it is sent, with the peer.
	r.hub.C.Store = real
	r.hub.mu.Lock()
	fv.state = nodeState("10.0.0.77")
	r.hub.mu.Unlock()
	r.hub.pushOne(second.ID)
	select {
	case m := <-push:
		if got := m.GetConfig().GetLinkPeers(); len(got) != 1 || got[0].Addresses[0] != "10.0.0.77" {
			t.Fatalf("pushed %v", got)
		}
	default:
		t.Fatal("the retry did not push")
	}

	// At first connect the agent has nothing to lose: configFor still answers, with whatever it could build.
	r.hub.C.Store = agentsDown{real}
	if cfg := r.hub.configFor(second, sv); cfg == nil || len(cfg.LinkPeers) != 0 {
		t.Errorf("first connect: %+v", cfg)
	}
}
