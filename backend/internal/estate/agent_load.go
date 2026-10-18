package estate

import "continuum/internal/decisionloop"

// What the decision loop reads of an agent: its own load, and which Config it has confirmed. The loop does not see the hub's views;
// this is the one place that says what of them it may have.

// hubLoad is the estate's answer to the decision loop (decisionloop.Load).
type hubLoad struct{ h *Hub }

func (l hubLoad) AgentLoad(agentID string) decisionloop.AgentLoad {
	h := l.h
	h.Mu.Lock()
	defer h.Mu.Unlock()
	v := h.Views[agentID]
	if v == nil {
		return decisionloop.AgentLoad{}
	}
	var samples []decisionloop.LoadSample
	for _, s := range v.Self.List() {
		samples = append(samples, decisionloop.LoadSample{At: s.At, RSSBytes: s.RSSBytes, CPUSeconds: s.CPUSeconds, LinkBytesCumulative: s.LinkBytesCumulative})
	}
	return decisionloop.AgentLoad{Known: true, Samples: samples, ConfigHash: v.CfgHash, ConfigApplied: v.CfgApplied, InForceAt: v.CfgInForceAt}
}
