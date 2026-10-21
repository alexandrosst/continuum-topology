package settingsapi

import (
	"encoding/json"
	"testing"

	"continuum/internal/wire"
)

func TestEveryDocumentIsListedForTheWire(t *testing.T) {
	wire.RequireDocsListed(t, ".", WireTypes())
}

// The conflict answer is the settings document with an error beside it, as the anonymous struct it replaced was.
func TestConflictIsTheSettingsWithAnError(t *testing.T) {
	d := SettingsDoc{}
	got, err := json.Marshal(settingsConflictDoc{d, "taken"})
	if err != nil {
		t.Fatal(err)
	}
	was, err := json.Marshal(struct {
		SettingsDoc
		Error string `json:"error"`
	}{d, "taken"})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(was) {
		t.Errorf("got %s\nwant %s", got, was)
	}
}
