package server

import "time"

// What the decision loop reads of an agent: its own load, and which Config it has confirmed. The loop does not see the hub's views;
// this is the one place that says what of them it may have.

// agentLoad is the server's record of one agent's own load and of the Config it was told.
type agentLoad struct {
	// Known is whether the server holds anything of the agent (a restart empties the in-memory views).
	Known bool
	// Samples are the agent's own self-telemetry, oldest first.
	Samples []LoadSample
	// ConfigHash is the Config the server last told the agent to run, ConfigApplied the one the agent last said it runs, and
	// InForceAt when it first said so.
	ConfigHash, ConfigApplied string
	InForceAt                 time.Time
}

func (h *Hub) agentLoad(agentID string) agentLoad {
	h.mu.Lock()
	defer h.mu.Unlock()
	v := h.views[agentID]
	if v == nil {
		return agentLoad{}
	}
	var samples []LoadSample
	for _, s := range v.self.list() {
		samples = append(samples, LoadSample{At: s.At, RSSBytes: s.RSSBytes, CPUSeconds: s.CPUSeconds, LinkBytesCumulative: s.LinkBytesCumulative})
	}
	return agentLoad{Known: true, Samples: samples, ConfigHash: v.cfgHash, ConfigApplied: v.cfgApplied, InForceAt: v.cfgInForceAt}
}
