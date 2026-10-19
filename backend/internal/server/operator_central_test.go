package server

import (
	"os"
	"strings"
	"testing"
)

// The operators' handlers know the central operator only as operators.Central. The package they live in is held to it by its imports; this
// holds the one still in the shell (the telemetry intents' command, which moves next) to it, so that step finds
// nothing of FUSION to take along.
func TestTheOperatorsHandlersNameNothingOfFusion(t *testing.T) {
	for _, f := range []string{"admin_telemetry_intents.go"} {
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
