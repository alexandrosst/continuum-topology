package telemetry

import "ikhnos/internal/destination"

// The bodies this package's routes read. They were anonymous structs at the handlers; naming them puts them in the TypeScript written
// from the Go types (internal/wire), so the page sends what the server reads.

// createTelemetryIntentRequest is the body of POST .../telemetry-intents.
type createTelemetryIntentRequest struct {
	AgentID     string           `json:"agentId"`
	Name        string           `json:"name"`
	Namespaces  []string         `json:"namespaces"`
	Exclude     []string         `json:"exclude"`
	Signals     []signalGrantDoc `json:"signals"`
	Destination destination.Doc  `json:"destination"`
	// Routes: a destination per signal type that has one of its own - see store.TelemetryIntent.Routes.
	Routes map[string]destination.Doc `json:"routes"`
}

// telemetryIntentScopeRequest is the body of the call that changes what an intent collects.
type telemetryIntentScopeRequest struct {
	Namespaces []string         `json:"namespaces"`
	Exclude    []string         `json:"exclude"`
	Signals    []signalGrantDoc `json:"signals"`
}

// telemetryIntentDestinationRequest is the body of the call that changes where an intent sends what it collects.
type telemetryIntentDestinationRequest struct {
	Destination destination.Doc `json:"destination"`
	// Routes, when present (even empty), replaces the per-signal-type destinations together with the
	// default; absent leaves them as they are.
	Routes map[string]destination.Doc `json:"routes"`
}
