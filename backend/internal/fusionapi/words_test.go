package fusionapi

import (
	"slices"
	"testing"
)

// The signals a document names are the three FUSION keeps, in the order the stores are listed everywhere.
func TestTheWordsOfASignalAreTheSignalsFusionKeeps(t *testing.T) {
	got := Signal("").WireWords()
	if !slices.Equal(got, Signals) {
		t.Errorf("words %q, signals %q", got, Signals)
	}
}
