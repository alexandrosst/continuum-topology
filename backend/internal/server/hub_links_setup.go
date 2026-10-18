package server

import (
	"context"
	"time"

	"continuum/internal/kernel"
	"continuum/internal/store"
)

// LinkReport is the notes and the checklists of the listed pairs for the organisation's current agents, in one read of the agents.
// haveFusion says whether this server has a FUSION at all (for this organisation). It fails when the agents cannot be read: an empty
// list would read as "nothing needs doing", which is not known then.
func (h *Hub) LinkReport(ctx context.Context, links []kernel.LinkSetting, haveFusion bool) ([]LinkNote, []LinkSetup, error) {
	if len(links) == 0 {
		return nil, nil, nil
	}
	agentList, err := h.C.Store.ListAgents(ctx, h.C.OrgID)
	if err != nil {
		return nil, nil, err
	}
	in := map[string]bool{}
	for _, l := range links {
		in[l.A], in[l.B] = true, true
	}
	paused := map[string]bool{}
	for _, o := range agentList { // before h.mu: the consent may be read from the store
		if o.Status == store.StatusApproved && in[o.ClusterID] {
			paused[o.ID] = h.consentOf(o.ID).Has("flow")
		}
	}
	now := h.C.Now()
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.linkNotes(links, agentList, paused), h.linkSetups(links, agentList, paused, haveFusion, now), nil
}

// linkSetups is the checklist of each listed pair. Called with h.mu held; agents and paused as for linkNotes.
func (h *Hub) linkSetups(links []kernel.LinkSetting, agentList []store.Agent, flowPausedOf map[string]bool, haveFusion bool, now time.Time) []LinkSetup {
	byCluster := ApprovedByCluster(agentList)
	plan := h.planner(links, agentList)
	end := func(l kernel.LinkSetting, c, peer string) SetupEnd {
		e := SetupEnd{ID: c}
		a, ok := byCluster[c]
		if !ok {
			return e
		}
		e.HasAgent, e.Name, e.Tier = true, a.Name, a.AccessTier
		if v := h.views[a.ID]; v != nil {
			e.Caps = v.Ext.LinkCaps
		}
		if o, ok := byCluster[peer]; ok {
			e.Note = h.endNote(l, a, o, plan(a), flowPausedOf[a.ID])
		}
		return e
	}
	out := make([]LinkSetup, 0, len(links))
	for _, l := range links {
		out = append(out, LinkSetup{A: l.A, B: l.B, Steps: LinkSetupSteps(l, end(l, l.A, l.B), end(l, l.B, l.A), haveFusion, now)})
	}
	return out
}
