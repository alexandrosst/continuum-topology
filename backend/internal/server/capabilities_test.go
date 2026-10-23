package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	ikhnosv1 "ikhnos/gen/ikhnosv1"
	"ikhnos/internal/agentcaps"
	"ikhnos/internal/estate"
	"ikhnos/internal/testkit"
)

// What an agent says about the protocol it speaks and what it does is recorded when it says hello. These scenarios are the ways
// a hello can say it: as it should, with too much, with something from the future, and not at all. In every one the stream stays
// open and the Config is the same: nothing the server sends depends on what the agent declared.

// declared is what the server holds of the agent's last hello.
func (f *fakeAgent) declared() (protocol uint32, caps []string) {
	f.r.hub.Mu.Lock()
	defer f.r.hub.Mu.Unlock()
	v := f.r.hub.Views[f.ID]
	return v.Ext.Protocol, append([]string(nil), v.Ext.Caps...)
}

// connectedWith is a fake agent whose hello is h.
func (r *hubRig) connectedWith(t *testing.T, h *ikhnosv1.Hello) *fakeAgent {
	t.Helper()
	f := r.newFakeAgentWith(t, fakeAgentCtx(t), h)
	return f
}

func TestTheVersionAndNamesAnAgentStatesAreRecorded(t *testing.T) {
	r := newHubRig(t)
	f := r.connectedWith(t, &ikhnosv1.Hello{AgentVersion: "fake", ProtocolVersion: agentcaps.ProtocolVersion, Capabilities: agentcaps.Names()})
	p, caps := f.declared()
	if p != 1 || strings.Join(caps, ",") != strings.Join(agentcaps.Names(), ",") || len(caps) != 6 {
		t.Fatalf("recorded protocol %d, names %v", p, caps)
	}
}

func TestMoreNamesThanTheLimitAreCutAndTheStreamStaysOpen(t *testing.T) {
	r := newHubRig(t)
	var names []string
	for i := 0; i < 100; i++ {
		names = append(names, "cap-"+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	f := r.connectedWith(t, &ikhnosv1.Hello{ProtocolVersion: 1, Capabilities: names})
	_, caps := f.declared()
	if len(caps) != agentcaps.MaxNames {
		t.Fatalf("%d names kept of 100, want %d", len(caps), agentcaps.MaxNames)
	}
	f.Beat("") // still a live stream
}

func TestANameOfTenThousandCharactersIsCut(t *testing.T) {
	r := newHubRig(t)
	f := r.connectedWith(t, &ikhnosv1.Hello{ProtocolVersion: 1, Capabilities: []string{strings.Repeat("a", 10000), "self-stats"}})
	_, caps := f.declared()
	if len(caps) != 2 {
		t.Fatalf("names %v", caps)
	}
	for _, n := range caps {
		if len(n) > agentcaps.MaxNameLen {
			t.Fatalf("a name of %d characters was kept", len(n))
		}
	}
}

func TestANameSaidTwiceIsKeptOnceAndGarbageIsNotKept(t *testing.T) {
	r := newHubRig(t)
	f := r.connectedWith(t, &ikhnosv1.Hello{ProtocolVersion: 1, Capabilities: []string{"self-stats", "self-stats", "Not A Name", "a\x00b", "diagnostics"}})
	_, caps := f.declared()
	if strings.Join(caps, ",") != "diagnostics,self-stats" {
		t.Fatalf("names %v", caps)
	}
}

func TestAProtocolVersionFromTheFutureIsRecordedAndTheStreamIsServed(t *testing.T) {
	r := newHubRig(t)
	f := r.connectedWith(t, &ikhnosv1.Hello{ProtocolVersion: 99, Capabilities: []string{"warp-drive", "self-stats"}})
	p, caps := f.declared()
	if p != 99 || strings.Join(caps, ",") != "self-stats,warp-drive" {
		t.Fatalf("protocol %d names %v: a version and a name the server does not know are still recorded", p, caps)
	}
	if len(f.Configs) != 1 {
		t.Fatalf("%d Configs on a stream from the future", len(f.Configs))
	}
	f.Beat("")
}

func TestAHelloWithNeitherFieldIsProtocolZeroAndTheConfigIsTheSameAsForOneWithBoth(t *testing.T) {
	r := newHubRig(t)
	legacy := r.connectedWith(t, &ikhnosv1.Hello{AgentVersion: "old", InstalledAccessTier: 2})
	if p, caps := legacy.declared(); p != 0 || len(caps) != 0 {
		t.Fatalf("an agent that says nothing was recorded as protocol %d with %v", p, caps)
	}
	// The same agent, now saying everything: the Config is the one it had.
	// (An agent that declares diagnostics states its ceiling, and says the same 2 as before; one that stated 0 would be believed.)
	legacy.Hello = &ikhnosv1.Hello{AgentVersion: "old", InstalledAccessTier: 2, ProtocolVersion: 1, Capabilities: agentcaps.Names()}
	again := legacy.restart()
	if again.Digest != legacy.Configs[0].Digest {
		t.Fatalf("declaring the protocol changed the Config: %q then %q", legacy.Configs[0].Digest, again.Digest)
	}
	if p, _ := legacy.declared(); p != 1 {
		t.Fatalf("protocol %d after the second hello", p)
	}
	// And back: a hello that says nothing replaces what the last one said.
	legacy.Hello = &ikhnosv1.Hello{AgentVersion: "old", InstalledAccessTier: 2}
	legacy.restart()
	if p, caps := legacy.declared(); p != 0 || len(caps) != 0 {
		t.Fatalf("an earlier stream's declaration outlived a hello that said nothing: protocol %d, %v", p, caps)
	}
}

// ---------- the agent document ----------

func TestTheDocumentOfAnAgentThatSaidNothingHasAnEmptyListAndNotNull(t *testing.T) {
	r := newHubRig(t)
	f := r.connectedWith(t, &ikhnosv1.Hello{AgentVersion: "old"})
	raw, _ := json.Marshal(r.agentDoc(t, f.ID))
	if !strings.Contains(string(raw), `"protocolVersion":0`) || !strings.Contains(string(raw), `"capabilities":[]`) {
		t.Fatalf("an agent from before the version: %s", raw)
	}
}

func TestTheDocumentOfAnAgentNeverSeenSinceTheServerStartedHasAnEmptyList(t *testing.T) {
	r := newHubRig(t)
	id, _, _ := r.approvedAgent(t, testkit.FP)
	raw, _ := json.Marshal(r.agentDoc(t, id))
	if !strings.Contains(string(raw), `"capabilities":[]`) {
		t.Fatalf("an agent with no view: %s", raw)
	}
}

// agentDoc is the document the API serves for one agent, as the hub builds it.
func (r *hubRig) agentDoc(t *testing.T, id string) estate.AgentDoc {
	t.Helper()
	s, err := r.hub.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range s.Agents {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("agent %s is not in the state", id)
	return estate.AgentDoc{}
}
