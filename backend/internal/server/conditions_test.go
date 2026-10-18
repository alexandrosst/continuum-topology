package server

import (
	"encoding/json"
	"strings"
	"testing"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/agentcaps"
)

// ---------- through the hub: a real stream, an echo, a change, a drop ----------

// cond is the named condition on the document the API would serve for the agent.
func (f *fakeAgent) cond(typ string) Condition {
	f.t.Helper()
	for _, c := range f.r.agentDoc(f.t, f.id).Conditions {
		if c.Type == typ {
			return c
		}
	}
	f.t.Fatalf("no %s condition on the document", typ)
	return Condition{}
}

func (f *fakeAgent) expect(typ, status, reason string) {
	f.t.Helper()
	f.waitUntil(typ+" to be "+status+"/"+reason, func() bool { c := f.cond(typ); return c.Status == status && c.Reason == reason })
}

func TestTheConditionsFollowAnAgentThatEchoesThroughAChangeAndADrop(t *testing.T) {
	r := newHubRig(t)
	f := r.connectedWith(t, &continuumv1.Hello{AgentVersion: "fake", ProtocolVersion: 1, Capabilities: agentcaps.Names()})
	f.expect(CondConnected, CondTrue, "Open")
	f.expect(CondCompatible, CondTrue, "Current")
	f.expect(CondConfigInForce, CondFalse, "NotEchoed") // it says it echoes, and has not yet

	f.beat(f.configs[0].Digest)
	f.expect(CondConfigInForce, CondTrue, "Echoed")
	if c := f.cond(CondConfigInForce); c.Since == "" {
		t.Fatal("an echoed Config has no moment it came into force")
	}

	next := f.changeTier()
	f.expect(CondConfigInForce, CondFalse, "NotEchoed") // the newer one is sent and only the older is named
	f.beat(next.Digest)
	f.expect(CondConfigInForce, CondTrue, "Echoed")

	f.drop()
	f.expect(CondConnected, CondFalse, "NoStream")
	f.expect(CondConfigInForce, CondUnknown, "NotConnected")
	f.expect(CondCompatible, CondTrue, "Current") // what it speaks is still what it said

	f.connect()
	f.expect(CondConnected, CondTrue, "Open")
	f.expect(CondConfigInForce, CondFalse, "NotEchoed") // a new stream: the old stream's echo says nothing about it
}

func TestAnAgentWithNoProtocolVersionIsLegacyAndIsJudgedByAnyEchoItGives(t *testing.T) {
	r := newHubRig(t)
	f := r.connectedWith(t, &continuumv1.Hello{AgentVersion: "old"})
	f.expect(CondCompatible, CondTrue, "LegacyAgent")
	f.expect(CondConfigInForce, CondUnknown, "AgentDoesNotEcho")
	f.beat("") // an agent from before the echo says nothing
	f.beat(f.configs[0].Digest)
	f.expect(CondConfigInForce, CondTrue, "Echoed")
}

func TestAnAgentFromTheFutureIsNotCompatibleAndIsServedAllTheSame(t *testing.T) {
	r := newHubRig(t)
	f := r.connectedWith(t, &continuumv1.Hello{ProtocolVersion: 99})
	f.expect(CondCompatible, CondFalse, "ProtocolTooNew")
	if len(f.configs) != 1 {
		t.Fatalf("%d Configs", len(f.configs))
	}
	f.beat(f.configs[0].Digest)
	f.expect(CondConfigInForce, CondTrue, "Echoed") // the condition being False blocks nothing
}

func TestAnAgentTheServerHasNotHeardFromSinceItStartedIsNotSeen(t *testing.T) {
	r := newHubRig(t)
	id, _, _ := r.approvedAgent(t, fp)
	doc := r.agentDoc(t, id)
	want := map[string][2]string{CondConnected: {CondFalse, "NoStream"}, CondConfigInForce: {CondUnknown, "NotConnected"}, CondCompatible: {CondUnknown, "NotSeenSinceStart"}}
	if len(doc.Conditions) != 3 {
		t.Fatalf("%d conditions", len(doc.Conditions))
	}
	for _, c := range doc.Conditions {
		if w := want[c.Type]; c.Status != w[0] || c.Reason != w[1] {
			t.Errorf("%s is %s/%s, want %v", c.Type, c.Status, c.Reason, w)
		}
	}
	raw, _ := json.Marshal(doc)
	if !strings.Contains(string(raw), `"conditions":[{"type":"Connected"`) {
		t.Fatalf("the document does not carry the conditions under that name: %s", raw)
	}
}
