package decisionloop

import (
	"encoding/json"
	"testing"

	"ikhnos/internal/wire"
)

func TestEveryDocumentIsListedForTheWire(t *testing.T) {
	wire.RequireDocsListed(t, ".", WireTypes())
}

// sameBytes fails the test unless got is written exactly as was is: was being the map a handler wrote before the answer had a type.
func sameBytes(t *testing.T, name string, got, was any) {
	t.Helper()
	g, err := json.Marshal(got)
	if err != nil {
		t.Fatal(name, err)
	}
	w, err := json.Marshal(was)
	if err != nil {
		t.Fatal(name, err)
	}
	if string(g) != string(w) {
		t.Errorf("%s:\n got %s\nwant %s", name, g, w)
	}
}

// The answers that were maps at the handlers are structs now, with their fields in the order a map's keys were encoded in; each is held to
// the map it replaced so that the bytes on the wire are the ones they were.
func TestAnswersThatWereMapsAreWrittenAsTheyWere(t *testing.T) {
	sameBytes(t, "recorded", recordedDoc{Recorded: 3}, map[string]any{"recorded": 3})
	sameBytes(t, "decisions", decisionsDoc{Decisions: []decisionLogDoc{{ID: "dec-1"}}}, map[string]any{"decisions": []decisionLogDoc{{ID: "dec-1"}}})
	sameBytes(t, "external", externalDecisionDoc{Decider: "d", Result: json.RawMessage(`{"a":1}`)}, map[string]any{"decider": "d", "result": json.RawMessage(`{"a":1}`)})
	sameBytes(t, "proposals", proposalsDoc{Proposals: []proposalDoc{{ID: "p"}}}, map[string]any{"proposals": []proposalDoc{{ID: "p"}}})
	sameBytes(t, "advisors", advisorsDoc{Advisors: []advisorRecordDoc{{Advisor: "a"}}}, map[string]any{"advisors": []advisorRecordDoc{{Advisor: "a"}}})
}

// The words the page switches on are the constants the code writes, no more and no fewer.
func TestTheWordsOfADecisionAreItsConstants(t *testing.T) {
	wire.RequireWordsComplete(t, ".", DeciderKind(""))
}
