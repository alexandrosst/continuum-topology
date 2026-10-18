package server

import "time"

// agtView is what the server keeps per agent about the agent itself: what it has sent, its own load, why it was last refused, its
// clock, and its account of itself and the overrides pushed to it.
type agtView struct {
	// What this agent has sent since the server started (lost on restart; not history, only a gauge of the link).
	link linkStats

	// self is this agent's own self-telemetry history (SelfStats off every heartbeat that carried one),
	// a short fixed-window ring - see selfRing's own doc comment. Lost on restart, same as link above.
	self selfRing[SelfStatsSample]

	// The last message refused for being over a limit or malformed, until a later one is accepted. It is
	// shown with the agent so its operator can see why nothing new arrives; refusalLogged limits how
	// often the same problem is written to the audit trail and the event list.
	refusal       string
	refusalLogged time.Time
	// implausibleLogged limits how often a report that was accepted but had a physically impossible
	// value quietly omitted (see boundSyncFacts/boundFlowFacts) is itself warned about - the same
	// per-agent rate limit refusalLogged above gives a full refusal.
	implausibleLogged time.Time

	// skewMs is the clock skew the agent last reported (its clock minus the server's); skewKnown says it was reported.
	skewMs    int64
	skewKnown bool

	// ext is the agent's account of itself, the overrides pushed to it and the pieces of a picture still arriving.
	ext viewExt
}
