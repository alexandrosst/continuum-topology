package agent

import (
	ikhnosv1 "ikhnos/gen/ikhnosv1"
)

// linkCapabilities is what this install can do for the cluster-link telemetry, for the heartbeat. It is cheap (a few fields
// read under the meter's lock) and carries no cluster data. Cluster links being off is a report of its own, with every switch
// false, so the server can tell "off here" (this agent knows the field) from "an agent too old to say" (no message at all).
func (r *runner) linkCapabilities() *ikhnosv1.LinkCapabilities {
	m := r.cfg.Links
	if m == nil {
		return &ikhnosv1.LinkCapabilities{}
	}
	in := m.Install()
	c := &ikhnosv1.LinkCapabilities{LinkMetrics: true, ProbeEnabled: in.Probe, CapacityEnabled: in.Capacity, TelemetryExport: in.TelemetryExport}
	if t := m.ProberSeen(); !t.IsZero() {
		c.ProberSeenUnix = t.Unix()
	}
	return c
}
