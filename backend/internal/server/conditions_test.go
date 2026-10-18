package server

import (
	"testing"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/agentcaps"
	"continuum/internal/agents"
)

// ---------- through the hub: a real stream, an echo, a change, a drop ----------

// cond is the named condition on the document the API would serve for the agent.
func (f *fakeAgent) cond(typ string) agents.Condition {
	f.T.Helper()
	for _, c := range f.r.agentDoc(f.T, f.ID).Conditions {
		if c.Type == typ {
			return c
		}
	}
	f.T.Fatalf("no %s condition on the document", typ)
	return agents.Condition{}
}

func (f *fakeAgent) expect(typ, status, reason string) {
	f.T.Helper()
	f.WaitUntil(typ+" to be "+status+"/"+reason, func() bool { c := f.cond(typ); return c.Status == status && c.Reason == reason })
}

func TestTheConditionsFollowAnAgentThatEchoesThroughAChangeAndADrop(t *testing.T) {
	r := newHubRig(t)
	f := r.connectedWith(t, &continuumv1.Hello{AgentVersion: "fake", ProtocolVersion: 1, Capabilities: agentcaps.Names()})
	f.expect(agents.CondConnected, agents.CondTrue, "Open")
	f.expect(agents.CondCompatible, agents.CondTrue, "Current")
	f.expect(agents.CondConfigInForce, agents.CondFalse, "NotEchoed") // it says it echoes, and has not yet

	f.Beat(f.Configs[0].Digest)
	f.expect(agents.CondConfigInForce, agents.CondTrue, "Echoed")
	if c := f.cond(agents.CondConfigInForce); c.Since == "" {
		t.Fatal("an echoed Config has no moment it came into force")
	}

	next := f.changeTier()
	f.expect(agents.CondConfigInForce, agents.CondFalse, "NotEchoed") // the newer one is sent and only the older is named
	f.Beat(next.Digest)
	f.expect(agents.CondConfigInForce, agents.CondTrue, "Echoed")

	f.drop()
	f.expect(agents.CondConnected, agents.CondFalse, "NoStream")
	f.expect(agents.CondConfigInForce, agents.CondUnknown, "NotConnected")
	f.expect(agents.CondCompatible, agents.CondTrue, "Current") // what it speaks is still what it said

	f.Connect()
	f.expect(agents.CondConnected, agents.CondTrue, "Open")
	f.expect(agents.CondConfigInForce, agents.CondFalse, "NotEchoed") // a new stream: the old stream's echo says nothing about it
}

func TestAnAgentWithNoProtocolVersionIsLegacyAndIsJudgedByAnyEchoItGives(t *testing.T) {
	r := newHubRig(t)
	f := r.connectedWith(t, &continuumv1.Hello{AgentVersion: "old"})
	f.expect(agents.CondCompatible, agents.CondTrue, "LegacyAgent")
	f.expect(agents.CondConfigInForce, agents.CondUnknown, "AgentDoesNotEcho")
	f.Beat("") // an agent from before the echo says nothing
	f.Beat(f.Configs[0].Digest)
	f.expect(agents.CondConfigInForce, agents.CondTrue, "Echoed")
}

func TestAnAgentFromTheFutureIsNotCompatibleAndIsServedAllTheSame(t *testing.T) {
	r := newHubRig(t)
	f := r.connectedWith(t, &continuumv1.Hello{ProtocolVersion: 99})
	f.expect(agents.CondCompatible, agents.CondFalse, "ProtocolTooNew")
	if len(f.Configs) != 1 {
		t.Fatalf("%d Configs", len(f.Configs))
	}
	f.Beat(f.Configs[0].Digest)
	f.expect(agents.CondConfigInForce, agents.CondTrue, "Echoed") // the condition being False blocks nothing
}
