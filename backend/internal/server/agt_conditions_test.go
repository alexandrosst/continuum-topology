package server

import (
	"strings"
	"testing"
	"time"

	"continuum/internal/agentcaps"
	"continuum/internal/kernel"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// Every outcome of the three conditions, with the inputs that give it. The function is pure, so each row is the whole story.
func TestEveryOutcomeOfTheConditions(t *testing.T) {
	digest := []string{agentcaps.ConfigDigest}
	cases := []struct {
		name        string
		in          CondInputs
		typ         string
		status, why string
		since       string
		messageHas  string
	}{
		{"open stream", CondInputs{Seen: true, Connected: true, ConnectedSince: t0}, CondConnected, CondTrue, "Open", kernel.RFC(t0), "open"},
		{"dropped stream", CondInputs{Seen: true}, CondConnected, CondFalse, "NoStream", "", "no stream"},
		{"never connected", CondInputs{}, CondConnected, CondFalse, "NoStream", "", "not connected since this server started"},

		{"config: not connected", CondInputs{Seen: true, CfgSent: "a", CfgApplied: "a"}, CondConfigInForce, CondUnknown, "NotConnected", "", "not connected"},
		{"config: echoed", CondInputs{Seen: true, Connected: true, CfgSent: "a", CfgApplied: "a", CfgInForceAt: t0}, CondConfigInForce, CondTrue, "Echoed", kernel.RFC(t0), "in force"},
		{"config: echoed by an agent with no protocol version", CondInputs{Seen: true, Connected: true, Protocol: 0, CfgSent: "a", CfgApplied: "a", CfgInForceAt: t0}, CondConfigInForce, CondTrue, "Echoed", kernel.RFC(t0), "in force"},
		{"config: an earlier one named", CondInputs{Seen: true, Connected: true, Protocol: 1, Caps: digest, CfgSent: "b", CfgApplied: "a", CfgSentAt: t0}, CondConfigInForce, CondFalse, "NotEchoed", kernel.RFC(t0), "earlier"},
		{"config: earlier one named by an agent with no version", CondInputs{Seen: true, Connected: true, CfgSent: "b", CfgApplied: "a", CfgSentAt: t0}, CondConfigInForce, CondFalse, "NotEchoed", kernel.RFC(t0), "earlier"},
		{"config: says it echoes and has not", CondInputs{Seen: true, Connected: true, Protocol: 1, Caps: digest, CfgSent: "a", CfgSentAt: t0}, CondConfigInForce, CondFalse, "NotEchoed", kernel.RFC(t0), "has not named"},
		{"config: has not echoed and does not say it does", CondInputs{Seen: true, Connected: true, Protocol: 1, CfgSent: "a", CfgSentAt: t0}, CondConfigInForce, CondUnknown, "AgentDoesNotEcho", "", "does not say"},
		{"config: legacy agent that never echoes", CondInputs{Seen: true, Connected: true, CfgSent: "a", CfgSentAt: t0}, CondConfigInForce, CondUnknown, "AgentDoesNotEcho", "", "does not say"},
		{"config: the send was cleared to be sent again", CondInputs{Seen: true, Connected: true, Protocol: 1, Caps: digest, CfgApplied: "a"}, CondConfigInForce, CondFalse, "NotEchoed", "", "earlier"},

		{"compatible: never seen", CondInputs{}, CondCompatible, CondUnknown, "NotSeenSinceStart", "", "not connected since"},
		{"compatible: legacy", CondInputs{Seen: true, Protocol: 0}, CondCompatible, CondTrue, "LegacyAgent", "", "before protocol"},
		{"compatible: current", CondInputs{Seen: true, Protocol: 1}, CondCompatible, CondTrue, "Current", "", "protocol 1"},
		{"compatible: from the future", CondInputs{Seen: true, Protocol: 99}, CondCompatible, CondFalse, "ProtocolTooNew", "", "99"},
		{"compatible: legacy agent is still served after it dropped", CondInputs{Seen: true, Connected: false, Protocol: 0}, CondCompatible, CondTrue, "LegacyAgent", "", "before protocol"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := AgentConditions(c.in, t0.Add(90*time.Second))
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
	got := AgentConditions(CondInputs{}, t0)
	if got[0].Type != CondConnected || got[1].Type != CondConfigInForce || got[2].Type != CondCompatible {
		t.Fatalf("order: %+v", got)
	}
	if agentcaps.ProtocolMin != 0 || agentcaps.ProtocolMax != agentcaps.ProtocolVersion {
		t.Fatalf("window [%d,%d]", agentcaps.ProtocolMin, agentcaps.ProtocolMax)
	}
}

func TestTheMessageSaysHowLongAgoTheConfigWasSent(t *testing.T) {
	in := CondInputs{Seen: true, Connected: true, Protocol: 1, Caps: []string{agentcaps.ConfigDigest}, CfgSent: "a", CfgSentAt: t0}
	for now, want := range map[time.Time]string{t0.Add(5 * time.Second): "5s ago", t0.Add(3 * time.Minute): "3m ago", t0.Add(26 * time.Hour): "26h ago"} {
		if m := configInForceCondition(in, now).Message; !strings.Contains(m, want) {
			t.Errorf("at %v: %q does not say %q", now, m, want)
		}
	}
}
