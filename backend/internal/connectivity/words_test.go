package connectivity

import (
	"testing"

	"continuum/internal/wire"
)

// The words the page switches on are the constants the code writes, no more and no fewer.
func TestTheWordsOfTheNotesAndTheChecklistAreTheirConstants(t *testing.T) {
	wire.RequireWordsComplete(t, ".", NoteCode(""), StepState(""), StepCode(""))
}
