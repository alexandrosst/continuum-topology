package server

import (
	"os"
	"strings"
	"testing"
)

// The operators' handlers know the central operator only as operators.Central. This holds them to it, so the step that moves them into a
// package of their own finds nothing of FUSION to take along.
func TestTheOperatorsHandlersNameNothingOfFusion(t *testing.T) {
	for _, f := range []string{"admin_operators.go", "admin_operator_destinations.go", "admin_operator_heartbeat.go", "operator_candidates.go", "operator_senders.go", "admin_telemetry_intents.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for n, line := range strings.Split(string(src), "\n") {
			code, _, _ := strings.Cut(line, "//")
			if strings.Contains(code, "a.Fusion") || strings.Contains(code, `"continuum/internal/fusion"`) || strings.Contains(code, "fusion.Fusion") {
				t.Errorf("%s:%d reaches FUSION itself: %s", f, n+1, strings.TrimSpace(line))
			}
		}
	}
}
