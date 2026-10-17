package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/agentcaps"
)

// What an agent says about the protocol it speaks and what it does is recorded when it says hello. These scenarios are the ways
// a hello can say it: as it should, with too much, with something from the future, and not at all. In every one the stream stays
// open and the Config is the same: nothing the server sends depends on what the agent declared.

// declared is what the server holds of the agent's last hello.
func (f *fakeAgent) declared() (protocol uint32, caps []string) {
	f.r.hub.mu.Lock()
	defer f.r.hub.mu.Unlock()
	v := f.r.hub.views[f.id]
	return v.ext.protocol, append([]string(nil), v.ext.caps...)
}

// connectedWith is a fake agent whose hello is h.
func (r *hubRig) connectedWith(t *testing.T, h *continuumv1.Hello) *fakeAgent {
	t.Helper()
	f := r.newFakeAgentWith(t, fakeAgentCtx(t), h)
	return f
}

func TestTheVersionAndNamesAnAgentStatesAreRecorded(t *testing.T) {
	r := newHubRig(t)
	f := r.connectedWith(t, &continuumv1.Hello{AgentVersion: "fake", ProtocolVersion: agentcaps.ProtocolVersion, Capabilities: agentcaps.Names()})
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
	f := r.connectedWith(t, &continuumv1.Hello{ProtocolVersion: 1, Capabilities: names})
	_, caps := f.declared()
	if len(caps) != agentcaps.MaxNames {
		t.Fatalf("%d names kept of 100, want %d", len(caps), agentcaps.MaxNames)
	}
	f.beat("") // still a live stream
}

func TestANameOfTenThousandCharactersIsCut(t *testing.T) {
	r := newHubRig(t)
	f := r.connectedWith(t, &continuumv1.Hello{ProtocolVersion: 1, Capabilities: []string{strings.Repeat("a", 10000), "self-stats"}})
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
	f := r.connectedWith(t, &continuumv1.Hello{ProtocolVersion: 1, Capabilities: []string{"self-stats", "self-stats", "Not A Name", "a\x00b", "diagnostics"}})
	_, caps := f.declared()
	if strings.Join(caps, ",") != "diagnostics,self-stats" {
		t.Fatalf("names %v", caps)
	}
}

func TestAProtocolVersionFromTheFutureIsRecordedAndTheStreamIsServed(t *testing.T) {
	r := newHubRig(t)
	f := r.connectedWith(t, &continuumv1.Hello{ProtocolVersion: 99, Capabilities: []string{"warp-drive", "self-stats"}})
	p, caps := f.declared()
	if p != 99 || strings.Join(caps, ",") != "self-stats,warp-drive" {
		t.Fatalf("protocol %d names %v: a version and a name the server does not know are still recorded", p, caps)
	}
	if len(f.configs) != 1 {
		t.Fatalf("%d Configs on a stream from the future", len(f.configs))
	}
	f.beat("")
}

func TestAHelloWithNeitherFieldIsProtocolZeroAndTheConfigIsTheSameAsForOneWithBoth(t *testing.T) {
	r := newHubRig(t)
	legacy := r.connectedWith(t, &continuumv1.Hello{AgentVersion: "old", InstalledAccessTier: 2})
	if p, caps := legacy.declared(); p != 0 || len(caps) != 0 {
		t.Fatalf("an agent that says nothing was recorded as protocol %d with %v", p, caps)
	}
	// The same agent, now saying everything: the Config is the one it had.
	// (An agent that declares diagnostics states its ceiling, and says the same 2 as before; one that stated 0 would be believed.)
	legacy.hello = &continuumv1.Hello{AgentVersion: "old", InstalledAccessTier: 2, ProtocolVersion: 1, Capabilities: agentcaps.Names()}
	again := legacy.restart()
	if again.Digest != legacy.configs[0].Digest {
		t.Fatalf("declaring the protocol changed the Config: %q then %q", legacy.configs[0].Digest, again.Digest)
	}
	if p, _ := legacy.declared(); p != 1 {
		t.Fatalf("protocol %d after the second hello", p)
	}
	// And back: a hello that says nothing replaces what the last one said.
	legacy.hello = &continuumv1.Hello{AgentVersion: "old", InstalledAccessTier: 2}
	legacy.restart()
	if p, caps := legacy.declared(); p != 0 || len(caps) != 0 {
		t.Fatalf("an earlier stream's declaration outlived a hello that said nothing: protocol %d, %v", p, caps)
	}
}

// ---------- the agent document ----------

func TestTheAgentDocumentCarriesWhatTheAgentDeclared(t *testing.T) {
	r := newHubRig(t)
	f := r.connectedWith(t, &continuumv1.Hello{ProtocolVersion: 1, Capabilities: []string{"self-stats", "config-digest"}})
	doc := r.agentDoc(t, f.id)
	if doc.ProtocolVersion != 1 || strings.Join(doc.Capabilities, ",") != "config-digest,self-stats" {
		t.Fatalf("document says protocol %d, names %v", doc.ProtocolVersion, doc.Capabilities)
	}
	raw, _ := json.Marshal(doc)
	if !strings.Contains(string(raw), `"protocolVersion":1`) || !strings.Contains(string(raw), `"capabilities":["config-digest","self-stats"]`) {
		t.Fatalf("the JSON names are not the contract: %s", raw)
	}
}

func TestTheDocumentOfAnAgentThatSaidNothingHasAnEmptyListAndNotNull(t *testing.T) {
	r := newHubRig(t)
	f := r.connectedWith(t, &continuumv1.Hello{AgentVersion: "old"})
	raw, _ := json.Marshal(r.agentDoc(t, f.id))
	if !strings.Contains(string(raw), `"protocolVersion":0`) || !strings.Contains(string(raw), `"capabilities":[]`) {
		t.Fatalf("an agent from before the version: %s", raw)
	}
}

func TestTheDocumentOfAnAgentNeverSeenSinceTheServerStartedHasAnEmptyList(t *testing.T) {
	r := newHubRig(t)
	id, _, _ := r.approvedAgent(t, fp)
	raw, _ := json.Marshal(r.agentDoc(t, id))
	if !strings.Contains(string(raw), `"capabilities":[]`) {
		t.Fatalf("an agent with no view: %s", raw)
	}
}

// agentDoc is the document the API serves for one agent, as the hub builds it.
func (r *hubRig) agentDoc(t *testing.T, id string) AgentDoc {
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
	return AgentDoc{}
}
