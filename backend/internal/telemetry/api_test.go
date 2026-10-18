package telemetry

import (
	"net/http"
	"testing"

	"continuum/internal/httpkit"
)

type recordedRoutes map[string]httpkit.Need

func (r recordedRoutes) Handle(pattern string, n httpkit.Need, h http.HandlerFunc) {
	if h == nil {
		panic("a route with no handler: " + pattern)
	}
	r[pattern] = n
}

func TestTelemetryMountsItsRoutesWithTheRolesTheyAsk(t *testing.T) {
	got := recordedRoutes{}
	API{}.Mount(got)
	const o = "/api/v1/orgs/{org}"
	want := recordedRoutes{
		// Granting scope and signals is an editor's act, as consent is.
		"GET " + o + "/telemetry-intents":                   httpkit.EditorRole,
		"POST " + o + "/telemetry-intents":                  httpkit.EditorRole,
		"GET " + o + "/telemetry-intents/{id}":              httpkit.EditorRole,
		"POST " + o + "/telemetry-intents/{id}/scope":       httpkit.EditorRole,
		"POST " + o + "/telemetry-intents/{id}/destination": httpkit.EditorRole,
		"POST " + o + "/telemetry-intents/{id}/revoke":      httpkit.EditorRole,
		"DELETE " + o + "/telemetry-intents/{id}":           httpkit.EditorRole,
		// A gateway token is credential material.
		"POST " + o + "/quick-start/{id}/gateway-token": httpkit.AdminRole,
		"GET " + o + "/quick-start/{id}/gateway-token":  httpkit.AdminRole,
	}
	if len(got) != len(want) {
		t.Fatalf("mounted %d routes, want %d: %v", len(got), len(want), got)
	}
	for p, n := range want {
		if got[p] != n {
			t.Errorf("%s asks %v, want %v", p, got[p], n)
		}
	}
}
