package estate

import (
	"encoding/json"
	"strings"
	"testing"

	continuumv1 "continuum/gen/continuumv1"
)

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
