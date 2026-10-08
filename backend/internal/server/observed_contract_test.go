package server

import (
	"strings"
	"testing"
	"unicode/utf8"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/facts"
)

// A module reason is raw error text from the agent. Over facts.MaxReason it is cut, not a reason to refuse the
// message (which ended the stream and so hid the very error the reason was reporting).
func TestValidateModulesCutsALongReason(t *testing.T) {
	ms := []*continuumv1.ModuleStatus{{Name: "services", State: continuumv1.ModuleStatus_ERROR, Reason: strings.Repeat("é", 1000) + "\n"}}
	if err := validateModules(ms); err != nil {
		t.Fatalf("a long reason was refused: %v", err)
	}
	if len(ms[0].Reason) > facts.MaxReason || !utf8.ValidString(ms[0].Reason) {
		t.Errorf("reason is %d bytes, valid utf-8 %v", len(ms[0].Reason), utf8.ValidString(ms[0].Reason))
	}
	if err := validateModules(make([]*continuumv1.ModuleStatus, 65)); err == nil {
		t.Error("more than 64 modules must still be refused")
	}
}
