package estate

import (
	"encoding/json"
	"strings"
	"testing"

	ikhnosv1 "ikhnos/gen/ikhnosv1"
)

func TestTheAgentDocumentCarriesWhatTheAgentDeclared(t *testing.T) {
	r := newHubRig(t)
	f := r.connectedWith(t, &ikhnosv1.Hello{ProtocolVersion: 1, Capabilities: []string{"self-stats", "config-digest"}})
	doc := r.agentDoc(t, f.ID)
	if doc.ProtocolVersion != 1 || strings.Join(doc.Capabilities, ",") != "config-digest,self-stats" {
		t.Fatalf("document says protocol %d, names %v", doc.ProtocolVersion, doc.Capabilities)
	}
	raw, _ := json.Marshal(doc)
	if !strings.Contains(string(raw), `"protocolVersion":1`) || !strings.Contains(string(raw), `"capabilities":["config-digest","self-stats"]`) {
		t.Fatalf("the JSON names are not the contract: %s", raw)
	}
}
