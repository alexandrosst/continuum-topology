package server

import (
	"time"

	continuumv1 "continuum/gen/continuumv1"

	"google.golang.org/protobuf/proto"
)

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
	// value quietly omitted (see boundSyncFacts/observed.BoundFlowFacts) is itself warned about - the same
	// per-agent rate limit refusalLogged above gives a full refusal.
	implausibleLogged time.Time

	// skewMs is the clock skew the agent last reported (its clock minus the server's); skewKnown says it was reported.
	skewMs    int64
	skewKnown bool

	// ext is the agent's account of itself, the overrides pushed to it and the pieces of a picture still arriving.
	ext viewExt
}

// linkStats counts what one agent has sent over its stream.
type linkStats struct {
	since                        time.Time // when the current connection began
	connects                     int       // connections accepted since the server started
	bytes                        int64     // size of the messages received, as encoded on the wire (before gRPC framing and TLS)
	syncs, flows, meas, beats    int
	lastSync, lastFlows, lastAny time.Time
	// flowBytes is bytes, bytes is to everything: the same "encoded on the wire, before gRPC framing and
	// TLS" measure as bytes above, but counting only AgentMessage_Flows messages - what sampleSelfStats
	// sums across every agent to derive the server's own self-telemetry flow-ingestion rate.
	flowBytes int64
}

func (l *linkStats) note(m *continuumv1.AgentMessage, at time.Time) {
	n := int64(proto.Size(m))
	l.bytes += n
	l.lastAny = at
	switch m.Msg.(type) {
	case *continuumv1.AgentMessage_Sync:
		l.syncs++
		l.lastSync = at
	case *continuumv1.AgentMessage_Flows:
		l.flows++
		l.flowBytes += n
		l.lastFlows = at
	case *continuumv1.AgentMessage_Measurements:
		l.meas++
	case *continuumv1.AgentMessage_Heartbeat:
		l.beats++
	}
}
