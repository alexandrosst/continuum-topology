package server

import (
	"context"
	"encoding/json"
	"math"
	"sort"

	"continuum/internal/store"
)

// What each advisor has done so far: how many proposals, what people made of them, and whether what it predicted came true. It is
// read from what is already recorded (the proposals and their Outcomes), computed when asked, and changes nothing.
//
// Two things are kept apart on purpose. A person rejecting a proposal says they disagreed, not that the advisor was wrong, so
// decisions and predictions are counted separately. And an Outcome compares the agent's own load before and after a change, which
// moves for other reasons too, so "held" means the observed change went the way the advisor said and by at least half as much,
// not that the advisor caused it.

// AdvisorRecord is one advisor's record.
type AdvisorRecord struct {
	Advisor string
	// Proposals made, and where they stand now.
	Proposals, Pending, Accepted, Rejected, Expired int
	// Accepted counts the ones a person said yes to, whether or not the change then applied; Failed is how many of those could not be made.
	Failed int
	// Outcomes: how many of its applied changes were compared (Measured), could not be confirmed, or had too little data; of the
	// measured ones that predicted something that could be compared, how many Held and how many Missed.
	Measured, Unconfirmed, NoData, Held, Missed int
}

// heldPrediction reports whether every predicted metric that was observed moved the way the advisor said and by at least half as
// much. compared is false when nothing it predicted could be observed, so the outcome says nothing about the advisor.
func heldPrediction(predicted, observed []byte) (held, compared bool) {
	var p map[string]float64
	var o struct {
		Change map[string]float64 `json:"change"`
	}
	if json.Unmarshal(predicted, &p) != nil || json.Unmarshal(observed, &o) != nil {
		return false, false
	}
	held = true
	for m, want := range p {
		got, ok := o.Change[m]
		if !ok || want == 0 {
			continue
		}
		compared = true
		if math.Signbit(got) != math.Signbit(want) || math.Abs(got) < math.Abs(want)/2 {
			held = false
		}
	}
	return held && compared, compared
}

// AdvisorRecords returns every advisor that has made a proposal, by name.
func (h *Hub) AdvisorRecords(ctx context.Context) ([]AdvisorRecord, error) {
	h.expireDueProposals(ctx)
	counts, err := h.C.Store.ProposalCountsByAdvisor(ctx, h.C.OrgID)
	if err != nil {
		return nil, err
	}
	outcomes, err := h.C.Store.ListAdvisorOutcomes(ctx, h.C.OrgID)
	if err != nil {
		return nil, err
	}
	by := map[string]*AdvisorRecord{}
	rec := func(name string) *AdvisorRecord {
		if by[name] == nil {
			by[name] = &AdvisorRecord{Advisor: name}
		}
		return by[name]
	}
	for _, c := range counts {
		r := rec(c.Advisor)
		r.Proposals += c.N
		switch c.Status {
		case store.ProposalPending:
			r.Pending += c.N
		case store.ProposalRejected:
			r.Rejected += c.N
		case store.ProposalExpired:
			r.Expired += c.N
		case store.ProposalAccepted, store.ProposalApplied:
			r.Accepted += c.N
		case store.ProposalFailed:
			r.Accepted += c.N
			r.Failed += c.N
		}
	}
	for _, o := range outcomes {
		r := rec(o.Advisor)
		switch o.Result {
		case "measured":
			r.Measured++
			if held, compared := heldPrediction(o.Predicted, o.Observed); compared {
				if held {
					r.Held++
				} else {
					r.Missed++
				}
			}
		case "unconfirmed":
			r.Unconfirmed++
		case "no_data":
			r.NoData++
		}
	}
	out := make([]AdvisorRecord, 0, len(by))
	for _, r := range by {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Advisor < out[j].Advisor })
	return out, nil
}
