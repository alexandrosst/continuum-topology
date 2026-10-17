package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/agentcaps"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// Every outcome of the three conditions, with the inputs that give it. The function is pure, so each row is the whole story.
func TestEveryOutcomeOfTheConditions(t *testing.T) {
	digest := []string{agentcaps.ConfigDigest}
	cases := []struct {
		name        string
		in          condInputs
		typ         string
		status, why string
		since       string
		messageHas  string
	}{
		{"open stream", condInputs{Seen: true, Connected: true, ConnectedSince: t0}, CondConnected, CondTrue, "Open", rfc(t0), "open"},
		{"dropped stream", condInputs{Seen: true}, CondConnected, CondFalse, "NoStream", "", "no stream"},
		{"never connected", condInputs{}, CondConnected, CondFalse, "NoStream", "", "not connected since this server started"},

		{"config: not connected", condInputs{Seen: true, CfgSent: "a", CfgApplied: "a"}, CondConfigInForce, CondUnknown, "NotConnected", "", "not connected"},
		{"config: echoed", condInputs{Seen: true, Connected: true, CfgSent: "a", CfgApplied: "a", CfgInForceAt: t0}, CondConfigInForce, CondTrue, "Echoed", rfc(t0), "in force"},
		{"config: echoed by an agent with no protocol version", condInputs{Seen: true, Connected: true, Protocol: 0, CfgSent: "a", CfgApplied: "a", CfgInForceAt: t0}, CondConfigInForce, CondTrue, "Echoed", rfc(t0), "in force"},
		{"config: an earlier one named", condInputs{Seen: true, Connected: true, Protocol: 1, Caps: digest, CfgSent: "b", CfgApplied: "a", CfgSentAt: t0}, CondConfigInForce, CondFalse, "NotEchoed", rfc(t0), "earlier"},
		{"config: earlier one named by an agent with no version", condInputs{Seen: true, Connected: true, CfgSent: "b", CfgApplied: "a", CfgSentAt: t0}, CondConfigInForce, CondFalse, "NotEchoed", rfc(t0), "earlier"},
		{"config: says it echoes and has not", condInputs{Seen: true, Connected: true, Protocol: 1, Caps: digest, CfgSent: "a", CfgSentAt: t0}, CondConfigInForce, CondFalse, "NotEchoed", rfc(t0), "has not named"},
		{"config: has not echoed and does not say it does", condInputs{Seen: true, Connected: true, Protocol: 1, CfgSent: "a", CfgSentAt: t0}, CondConfigInForce, CondUnknown, "AgentDoesNotEcho", "", "does not say"},
		{"config: legacy agent that never echoes", condInputs{Seen: true, Connected: true, CfgSent: "a", CfgSentAt: t0}, CondConfigInForce, CondUnknown, "AgentDoesNotEcho", "", "does not say"},
		{"config: the send was cleared to be sent again", condInputs{Seen: true, Connected: true, Protocol: 1, Caps: digest, CfgApplied: "a"}, CondConfigInForce, CondFalse, "NotEchoed", "", "earlier"},

		{"compatible: never seen", condInputs{}, CondCompatible, CondUnknown, "NotSeenSinceStart", "", "not connected since"},
		{"compatible: legacy", condInputs{Seen: true, Protocol: 0}, CondCompatible, CondTrue, "LegacyAgent", "", "before protocol"},
		{"compatible: current", condInputs{Seen: true, Protocol: 1}, CondCompatible, CondTrue, "Current", "", "protocol 1"},
		{"compatible: from the future", condInputs{Seen: true, Protocol: 99}, CondCompatible, CondFalse, "ProtocolTooNew", "", "99"},
		{"compatible: legacy agent is still served after it dropped", condInputs{Seen: true, Connected: false, Protocol: 0}, CondCompatible, CondTrue, "LegacyAgent", "", "before protocol"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := agentConditions(c.in, t0.Add(90*time.Second))
			if len(got) != 3 {
				t.Fatalf("%d conditions, want 3", len(got))
			}
			var cond *Condition
			for i := range got {
				if got[i].Type == c.typ {
					cond = &got[i]
				}
			}
			if cond == nil {
				t.Fatalf("no %s condition in %+v", c.typ, got)
			}
			if cond.Status != c.status || cond.Reason != c.why || cond.Since != c.since || !strings.Contains(cond.Message, c.messageHas) {
				t.Fatalf("got %+v, want %s/%s since %q with %q in the message", *cond, c.status, c.why, c.since, c.messageHas)
			}
		})
	}
}

func TestTheConditionsComeInAFixedOrderAndTheWindowStartsAtZero(t *testing.T) {
	got := agentConditions(condInputs{}, t0)
	if got[0].Type != CondConnected || got[1].Type != CondConfigInForce || got[2].Type != CondCompatible {
		t.Fatalf("order: %+v", got)
	}
	if agentcaps.ProtocolMin != 0 || agentcaps.ProtocolMax != agentcaps.ProtocolVersion {
		t.Fatalf("window [%d,%d]", agentcaps.ProtocolMin, agentcaps.ProtocolMax)
	}
}

func TestTheMessageSaysHowLongAgoTheConfigWasSent(t *testing.T) {
	in := condInputs{Seen: true, Connected: true, Protocol: 1, Caps: []string{agentcaps.ConfigDigest}, CfgSent: "a", CfgSentAt: t0}
	for now, want := range map[time.Time]string{t0.Add(5 * time.Second): "5s ago", t0.Add(3 * time.Minute): "3m ago", t0.Add(26 * time.Hour): "26h ago"} {
		if m := configInForceCondition(in, now).Message; !strings.Contains(m, want) {
			t.Errorf("at %v: %q does not say %q", now, m, want)
		}
	}
}

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
