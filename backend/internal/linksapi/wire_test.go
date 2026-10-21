package linksapi

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

func TestRotatedIsWrittenAsTheMapWas(t *testing.T) {
	sameBytes(t, "rotated", rotatedDoc{Rotated: true}, map[string]any{"rotated": true})
}
