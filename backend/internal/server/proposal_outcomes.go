package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"continuum/internal/store"
)

// The Outcome of an applied proposal: what the advisor predicted beside what was observed. An agent's own load is read from the
// self-telemetry its heartbeats carry (the ring each view keeps), over a window before the change was decided and a window after
// the agent first reported the new Config in force. That is the whole of what is measured in this slice: link bytes per second,
// CPU and memory of the agent itself. The ring is in memory, so a server restart in between leaves too little to compare, and the
// Outcome says so ("no_data") instead of guessing.

const (
	defaultOutcomeWindow = 5 * time.Minute
	defaultOutcomeGiveUp = 30 * time.Minute
	// outcomeLookback bounds how far back applied proposals are still waiting for an Outcome.
	outcomeLookback = 24 * time.Hour
)

// OutcomeWindow is how long a window is on each side of the change (zero: five minutes); OutcomeGiveUp is how long after a
// proposal was decided the server keeps waiting for the agent to confirm it, or for enough samples (zero: thirty minutes).
// Tests shrink them.
type outcomeTimes struct{ window, giveUp time.Duration }

func (h *Hub) outcomeTimes() outcomeTimes {
	t := outcomeTimes{h.OutcomeWindow, h.OutcomeGiveUp}
	if t.window <= 0 {
		t.window = defaultOutcomeWindow
	}
	if t.giveUp <= 0 {
		t.giveUp = defaultOutcomeGiveUp
	}
	return t
}

// windowMetrics reads the metrics from the samples inside [from, to].
func windowMetrics(samples []SelfStatsSample, from, to time.Time) map[string]float64 {
	var in []SelfStatsSample
	for _, s := range samples {
		if !s.At.Before(from) && !s.At.After(to) {
			in = append(in, s)
		}
	}
	out := map[string]float64{}
	if len(in) < 2 {
		return out
	}
	first, last := in[0], in[len(in)-1]
	el := last.At.Sub(first.At)
	if r, ok := rateBetween(float64(first.LinkBytesCumulative), float64(last.LinkBytesCumulative), el); ok {
		out["link_bytes_per_sec"] = r
	}
	if r, ok := rateBetween(first.CPUSeconds, last.CPUSeconds, el); ok {
		out["agent_cpu_cores"] = r
	}
	var rss float64
	for _, s := range in {
		rss += float64(s.RSSBytes)
	}
	out["agent_rss_bytes"] = rss / float64(len(in))
	return out
}

// recordOutcomes writes the Outcome of every applied proposal that is ready for one. It runs once a minute from Hub.Run.
func (h *Hub) recordOutcomes(ctx context.Context) {
	now := h.C.Now()
	applied, err := h.C.Store.ListProposals(ctx, h.C.OrgID, store.ProposalQuery{Status: store.ProposalApplied, Limit: 1000})
	if err != nil {
		return
	}
	for _, p := range applied {
		if p.DecidedAt == nil || now.Sub(*p.DecidedAt) > outcomeLookback {
			continue
		}
		if _, err := h.C.Store.GetProposalOutcome(ctx, h.C.OrgID, p.ID); err == nil {
			continue
		}
		o, ready := h.outcomeFor(p, now)
		if !ready {
			continue
		}
		detail := fmt.Sprintf("proposal %s from advisor %s: %s", p.ID, p.Advisor, o.Result)
		err := h.C.Store.RecordProposalOutcome(ctx, o, store.WithAudit(h.C.auditRow(h.C.OrgID, SystemActor.String(), "proposal-outcome", "proposal", p.ID, detail)))
		if err != nil {
			h.Log.Warn("a proposal's outcome could not be recorded", "proposal", p.ID, "err", err)
		}
	}
}

// outcomeFor works out the Outcome of one applied proposal as of now, or says it is not ready yet.
func (h *Hub) outcomeFor(p store.Proposal, now time.Time) (store.ProposalOutcome, bool) {
	tm := h.outcomeTimes()
	decided := *p.DecidedAt
	out := store.ProposalOutcome{ProposalID: p.ID, OrgID: p.OrgID, Predicted: p.Predicted, RecordedAt: now}
	h.mu.Lock()
	v := h.views[p.AgentID]
	var samples []SelfStatsSample
	var inForce time.Time
	confirmed := false
	if v != nil {
		samples = v.self.list()
		confirmed = v.cfgHash != "" && v.cfgHash == v.cfgApplied && !v.cfgInForceAt.Before(decided)
		inForce = v.cfgInForceAt
	}
	h.mu.Unlock()

	if !confirmed {
		if now.Sub(decided) < tm.giveUp {
			return out, false
		}
		out.Result, out.Observed = "unconfirmed", []byte(`{}`)
		return out, true
	}
	out.InForceAt = &inForce
	if now.Before(inForce.Add(tm.window)) {
		return out, false // the window after the change is still filling
	}
	before := windowMetrics(samples, decided.Add(-tm.window), decided)
	after := windowMetrics(samples, inForce, inForce.Add(tm.window))
	change := map[string]float64{}
	for m, a := range after {
		if b, ok := before[m]; ok && b > 0 {
			change[m] = (a - b) / b
		}
	}
	var predicted map[string]float64
	_ = json.Unmarshal(p.Predicted, &predicted)
	want := make([]string, 0, len(predicted))
	for m := range predicted {
		want = append(want, m)
	}
	sort.Strings(want)
	usable := len(change) > 0
	if len(want) > 0 {
		usable = false
		for _, m := range want {
			if _, ok := change[m]; ok {
				usable = true
			}
		}
	}
	if !usable {
		if now.Sub(decided) < tm.giveUp {
			return out, false // maybe the samples are still arriving
		}
		out.Result, out.Observed = "no_data", []byte(`{}`)
		return out, true
	}
	out.Result = "measured"
	out.Observed, _ = json.Marshal(map[string]any{"before": before, "after": after, "change": change})
	return out, true
}
