package server

import (
	"context"
	"time"

	"continuum/internal/connectivity"
	"continuum/internal/kernel"
	"continuum/internal/store"
)

// LinkReport is the notes and the checklists of the listed pairs for the organisation's current agents, in one read of the agents.
// haveFusion says whether this server has a FUSION at all (for this organisation). It fails when the agents cannot be read: an empty
// list would read as "nothing needs doing", which is not known then.
func (h *Hub) LinkReport(ctx context.Context, links []kernel.LinkSetting, haveFusion bool) ([]connectivity.LinkNote, []connectivity.LinkSetup, error) {
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
			paused[o.ID] = h.ConsentOf(o.ID).Has("flow")
		}
	}
	now := h.C.Now()
	h.Mu.Lock()
	defer h.Mu.Unlock()
	return h.LinkNotes(links, agentList, paused), h.LinkSetups(links, agentList, paused, haveFusion, now), nil
}

// LinkSetups is the checklist of each listed pair. Called with h.mu held; agents and paused as for LinkNotes.
func (h *Hub) LinkSetups(links []kernel.LinkSetting, agentList []store.Agent, flowPausedOf map[string]bool, haveFusion bool, now time.Time) []connectivity.LinkSetup {
	byCluster := connectivity.ApprovedByCluster(agentList)
	plan := h.planner(links, agentList)
	end := func(l kernel.LinkSetting, c, peer string) connectivity.SetupEnd {
		e := connectivity.SetupEnd{ID: c}
		a, ok := byCluster[c]
		if !ok {
			return e
		}
		e.HasAgent, e.Name, e.Tier = true, a.Name, a.AccessTier
		if v := h.Views[a.ID]; v != nil {
			e.Caps = v.Ext.LinkCaps
		}
		if o, ok := byCluster[peer]; ok {
			e.Note = h.endNote(l, a, o, plan(a), flowPausedOf[a.ID])
		}
		return e
	}
	out := make([]connectivity.LinkSetup, 0, len(links))
	for _, l := range links {
		out = append(out, connectivity.LinkSetup{A: l.A, B: l.B, Steps: connectivity.LinkSetupSteps(l, end(l, l.A, l.B), end(l, l.B, l.A), haveFusion, now)})
	}
	return out
}
