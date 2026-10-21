package telemetry

import (
	"encoding/json"
	"testing"

	"continuum/internal/wire"
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
	yes, no := true, false
	sameBytes(t, "minted", mintedGatewayTokenDoc{Token: "s", CreatedAt: "c", ExpiresAt: "e"}, map[string]any{"token": "s", "createdAt": "c", "expiresAt": "e"})
	sameBytes(t, "no token out", gatewayTokenDoc{}, map[string]any{"active": false})
	sameBytes(t, "token out", gatewayTokenDoc{Active: true, Expired: &no, CreatedAt: "c", ExpiresAt: "e"},
		map[string]any{"active": true, "expired": false, "createdAt": "c", "expiresAt": "e"})
	sameBytes(t, "token expired", gatewayTokenDoc{Active: true, Expired: &yes, CreatedAt: "c", ExpiresAt: "e"},
		map[string]any{"active": true, "expired": true, "createdAt": "c", "expiresAt": "e"})
}
