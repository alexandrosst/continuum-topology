package agents

import (
	"time"

	continuumv1 "continuum/gen/continuumv1"
)

// What an agent says its install can do for the cluster-link telemetry (Heartbeat.link_capabilities). The server keeps the latest
// one per agent in memory, like the diagnostics (not persisted: the next heartbeat, at most two minutes after a connect, brings it
// back), and uses it to tell an administrator which step of setting a pair up is missing (see connectivity.LinkSetupSteps).
//
// An agent that does not send it (one older than the field) leaves this zero, and the zero value means "not reported", which
// connectivity.LinkSetupSteps reads as unknown and never as "all off": an old agent behaves exactly as it did.

// LinkCaps is one agent's last report. Reported is when the server received it; the zero value is "never reported".
type LinkCaps struct {
	Reported time.Time
	// LinkMetrics, ProbeEnabled, CapacityEnabled and TelemetryExport are the switches the agent reported.
	LinkMetrics, ProbeEnabled, CapacityEnabled, TelemetryExport bool
	// ProberSeen is when the link prober last asked the agent for its peers, in the server's clock (zero: never).
	ProberSeen time.Time
}

// earliestProberSeen is the oldest time taken as a real one: an agent's clock that was never set, or a corrupt value, must not
// read as a prober that was last seen in 1970 rather than as one never seen.
var earliestProberSeen = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// noteLinkCaps keeps an agent's latest report. A nil one (an older agent, or a heartbeat that carried none) changes nothing: the
// previous report stands until linkCapsStaleAfter says it is too old to rely on. skewMs is the agent's clock minus the server's,
// as the heartbeat measured it, so a time the agent names is read in the server's clock. Nothing the agent says can put the
// prober's last visit in the future. Caller holds h.mu.
func (v *View) NoteLinkCaps(c *continuumv1.LinkCapabilities, now time.Time, skewMs int64) {
	if c == nil {
		return
	}
	out := LinkCaps{Reported: now, LinkMetrics: c.LinkMetrics, ProbeEnabled: c.ProbeEnabled, CapacityEnabled: c.CapacityEnabled, TelemetryExport: c.TelemetryExport}
	if c.ProberSeenUnix > 0 && c.ProberSeenUnix < 1<<36 { // 1<<36 seconds is the year 4147: anything larger is not a clock
		if t := time.Unix(c.ProberSeenUnix, 0).Add(-time.Duration(skewMs) * time.Millisecond); t.After(earliestProberSeen) {
			if t.After(now) {
				t = now
			}
			out.ProberSeen = t
		}
	}
	v.Ext.LinkCaps = out
}
