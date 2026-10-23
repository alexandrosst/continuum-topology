package agents

import (
	"time"

	ikhnosv1 "ikhnos/gen/ikhnosv1"

	"google.golang.org/protobuf/proto"
)

// View is what the server keeps per agent about the agent itself: what it has sent, its own load, why it was last refused, its
// clock, and its account of itself and the overrides pushed to it.
type View struct {
	// What this agent has sent since the server started (lost on restart; not history, only a gauge of the link).
	Link linkStats

	// self is this agent's own self-telemetry history (SelfStats off every heartbeat that carried one),
	// a short fixed-window ring - see SelfRing's own doc comment. Lost on restart, same as link above.
	Self SelfRing[SelfStatsSample]

	// The last message refused for being over a limit or malformed, until a later one is accepted. It is
	// shown with the agent so its operator can see why nothing new arrives; refusalLogged limits how
	// often the same problem is written to the audit trail and the event list.
	Refusal       string
	RefusalLogged time.Time
	// implausibleLogged limits how often a report that was accepted but had a physically impossible
	// value quietly omitted (see boundSyncFacts/observed.BoundFlowFacts) is itself warned about - the same
	// per-agent rate limit refusalLogged above gives a full refusal.
	ImplausibleLogged time.Time

	// skewMs is the clock skew the agent last reported (its clock minus the server's); skewKnown says it was reported.
	SkewMs    int64
	SkewKnown bool

	// ext is the agent's account of itself, the overrides pushed to it and the pieces of a picture still arriving.
	Ext ViewExt
}

// linkStats counts what one agent has sent over its stream.
type linkStats struct {
	Since                        time.Time // when the current connection began
	Connects                     int       // connections accepted since the server started
	Bytes                        int64     // size of the messages received, as encoded on the wire (before gRPC framing and TLS)
	Syncs, Flows, Meas, Beats    int
	LastSync, LastFlows, lastAny time.Time
	// flowBytes is bytes, bytes is to everything: the same "encoded on the wire, before gRPC framing and
	// TLS" measure as bytes above, but counting only AgentMessage_Flows messages - what sampleSelfStats
	// sums across every agent to derive the server's own self-telemetry flow-ingestion rate.
	FlowBytes int64
}

func (l *linkStats) Note(m *ikhnosv1.AgentMessage, at time.Time) {
	n := int64(proto.Size(m))
	l.Bytes += n
	l.lastAny = at
	switch m.Msg.(type) {
	case *ikhnosv1.AgentMessage_Sync:
		l.Syncs++
		l.LastSync = at
	case *ikhnosv1.AgentMessage_Flows:
		l.Flows++
		l.FlowBytes += n
		l.LastFlows = at
	case *ikhnosv1.AgentMessage_Measurements:
		l.Meas++
	case *ikhnosv1.AgentMessage_Heartbeat:
		l.Beats++
	}
}
