package server

// condInputsOf copies what agentConditions needs out of a view (nil: an agent the hub has no view of). The caller holds h.mu.
func condInputsOf(v *view) condInputs {
	if v == nil {
		return condInputs{}
	}
	return condInputs{
		Seen: v.link.connects > 0, Connected: v.connected, ConnectedSince: v.link.since,
		Protocol: v.ext.protocol, Caps: v.ext.caps,
		CfgSent: v.cfgHash, CfgApplied: v.cfgApplied, CfgSentAt: v.cfgSentAt, CfgInForceAt: v.cfgInForceAt,
	}
}
