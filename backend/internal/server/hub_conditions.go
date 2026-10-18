package server

import (
	"continuum/internal/agents"
)

// condInputsOf copies what agents.AgentConditions needs out of a view (nil: an agent the hub has no view of). The caller holds h.mu.
func condInputsOf(v *view) agents.CondInputs {
	if v == nil {
		return agents.CondInputs{}
	}
	return agents.CondInputs{
		Seen: v.Link.Connects > 0, Connected: v.connected, ConnectedSince: v.Link.Since,
		Protocol: v.Ext.Protocol, Caps: v.Ext.Caps,
		CfgSent: v.cfgHash, CfgApplied: v.cfgApplied, CfgSentAt: v.cfgSentAt, CfgInForceAt: v.cfgInForceAt,
	}
}
