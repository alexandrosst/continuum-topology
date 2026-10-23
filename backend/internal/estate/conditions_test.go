package estate

import (
	"encoding/json"
	"strings"
	"testing"

	"ikhnos/internal/agents"
	"ikhnos/internal/testkit"
)

func TestAnAgentTheServerHasNotHeardFromSinceItStartedIsNotSeen(t *testing.T) {
	r := newHubRig(t)
	id, _, _ := r.approvedAgent(t, testkit.FP)
	doc := r.agentDoc(t, id)
	want := map[string][2]string{agents.CondConnected: {agents.CondFalse, "NoStream"}, agents.CondConfigInForce: {agents.CondUnknown, "NotConnected"}, agents.CondCompatible: {agents.CondUnknown, "NotSeenSinceStart"}}
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
