package agent

import (
	"testing"

	"ikhnos/internal/link"
)

func TestTheAgentReportsWhatItsInstallCanDoForLinks(t *testing.T) {
	// Cluster links off: a report all the same, so the server can tell "off here" from "an agent too old to say".
	off := (&runner{}).linkCapabilities()
	if off == nil || off.LinkMetrics || off.ProbeEnabled || off.CapacityEnabled || off.TelemetryExport || off.ProberSeenUnix != 0 {
		t.Fatalf("Cluster links off: %+v", off)
	}

	m := link.NewMeter(link.NewPeers())
	m.SetInstall(link.Install{Probe: true, Capacity: true, TelemetryExport: true})
	r := &runner{cfg: Config{Links: m}}
	on := r.linkCapabilities()
	if !on.LinkMetrics || !on.ProbeEnabled || !on.CapacityEnabled || !on.TelemetryExport {
		t.Fatalf("everything on: %+v", on)
	}
	if on.ProberSeenUnix != 0 {
		t.Fatalf("the prober has not asked yet: %d", on.ProberSeenUnix)
	}

	// metrics only: probing, iperf3 and export are reported off
	m2 := link.NewMeter(link.NewPeers())
	only := (&runner{cfg: Config{Links: m2}}).linkCapabilities()
	if !only.LinkMetrics || only.ProbeEnabled || only.CapacityEnabled || only.TelemetryExport {
		t.Fatalf("metrics only: %+v", only)
	}
}
