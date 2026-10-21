package fusion

import (
	"encoding/json"
	"testing"

	"continuum/internal/wire"
)

func TestEveryDocumentIsListedForTheWire(t *testing.T) {
	wire.RequireDocsListed(t, ".", WireTypes())
}

func TestCreatedTokenIsWrittenAsTheMapWas(t *testing.T) {
	got, err := json.Marshal(CreatedTokenDoc{Token: "s", Details: TokenDoc{ID: "i"}})
	if err != nil {
		t.Fatal(err)
	}
	was, err := json.Marshal(map[string]any{"token": "s", "details": TokenDoc{ID: "i"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(was) {
		t.Errorf("got %s\nwant %s", got, was)
	}
}
