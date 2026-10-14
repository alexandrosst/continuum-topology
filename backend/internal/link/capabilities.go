package link

import "time"

// What this install can do for the cluster-link telemetry, as far as the link meter can tell. The agent reports it to the server
// in every heartbeat (LinkCapabilities), so the Sites screen can say which step of setting a pair up is still missing: a pair
// that needs two clusters with Cluster links on, the cluster that runs its probes sending telemetry onward, and so on.

// Install is what the chart switched on around the agent. Cluster links itself being on is the meter existing at all.
type Install struct {
	// Probe: the prober and the responder are part of the install (telemetry.clusterLinks.probe.enabled).
	Probe bool
	// Capacity: the iperf3 server and client are part of the install (telemetry.clusterLinks.probe.capacity.enabled).
	Capacity bool
	// TelemetryExport: a cluster collector exports the link metrics to a destination, so what this cluster measures leaves it.
	TelemetryExport bool
}

// SetInstall records what the chart switched on.
func (m *Meter) SetInstall(i Install) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.install = i
	m.mu.Unlock()
}

// Install is what SetInstall recorded; the zero value for a nil meter (Cluster links is off).
func (m *Meter) Install() Install {
	if m == nil {
		return Install{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.install
}

// noteProberSeen records that the prober just made a valid request for the peers.
func (m *Meter) noteProberSeen() {
	now := m.now()
	m.mu.Lock()
	m.proberSeen = now
	m.mu.Unlock()
}

// ProberSeen is when the prober last asked for the peers to measure with a valid signed request; the zero time if it never has
// (since the agent started) or the meter is nil.
func (m *Meter) ProberSeen() time.Time {
	if m == nil {
		return time.Time{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.proberSeen
}
