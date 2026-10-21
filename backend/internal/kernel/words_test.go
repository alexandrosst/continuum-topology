package kernel

import (
	"testing"

	"continuum/internal/link"
	"continuum/internal/wire"
)

// The words the page switches on are the constants the code writes, no more and no fewer.
func TestTheWordsOfTheSettingsAreTheirConstants(t *testing.T) {
	wire.RequireWordsComplete(t, ".", BackendKind(""), Modality(""))
}

// A pair's via is one of the classes of address, and every class is one the page can choose.
func TestEveryWordOfLinkViaIsAValidViaAndEveryKindOfBackendHasAModality(t *testing.T) {
	for _, w := range LinkVia("").WireWords() {
		if !link.ValidVia(w) {
			t.Errorf("%q is a word of LinkVia but not a valid via", w)
		}
	}
	for _, k := range knownBackendKinds {
		if _, fixed := quickStartBackendModality[k]; !fixed && k != BackendCustom {
			t.Errorf("the %q backend kind fixes no modality and is not custom", k)
		}
	}
	for _, w := range BackendKind("").WireWords() {
		found := false
		for _, k := range knownBackendKinds {
			found = found || string(k) == w
		}
		if !found {
			t.Errorf("%q is a word of BackendKind but not a known kind", w)
		}
	}
}
