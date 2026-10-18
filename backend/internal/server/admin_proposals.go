package server

import (
	"encoding/json"
	"net/http"
	"time"

	"continuum/internal/store"
)

// The HTTP face of the decision loop (see proposals.go). Every route needs an editor, the role that can already change an
// agent's access: the proposals carry the evidence an advisor used about the agent, which a viewer does not see either. An
// advisor is a client holding an editor's API token; it can only submit, and submitting changes nothing. Accepting a proposal
// that widens takes an administrator, checked in the hub.

type outcomeDoc struct {
	Result     string          `json:"result"`
	Predicted  json.RawMessage `json:"predicted"`
	Observed   json.RawMessage `json:"observed"`
	InForceAt  string          `json:"inForceAt,omitempty"`
	RecordedAt string          `json:"recordedAt"`
}

type proposalDoc struct {
	ID           string          `json:"id"`
	AgentID      string          `json:"agentId"`
	Advisor      string          `json:"advisor"`
	ModelVersion int64           `json:"modelVersion"`
	Change       json.RawMessage `json:"change"`
	Evidence     json.RawMessage `json:"evidence"`
	Confidence   string          `json:"confidence"`
	Blast        json.RawMessage `json:"blast"`
	Predicted    json.RawMessage `json:"predicted"`
	Widens       bool            `json:"widens"`
	Mode         string          `json:"mode"`
	Status       string          `json:"status"`
	CreatedAt    string          `json:"createdAt"`
	ExpiresAt    string          `json:"expiresAt"`
	DecidedBy    string          `json:"decidedBy,omitempty"`
	DecidedAt    string          `json:"decidedAt,omitempty"`
	Note         string          `json:"note,omitempty"`
	Outcome      *outcomeDoc     `json:"outcome,omitempty"`
}

func rawOr(b []byte, empty string) json.RawMessage {
	if len(b) == 0 || !json.Valid(b) {
		return json.RawMessage(empty)
	}
	return json.RawMessage(b)
}

func toProposalDoc(p store.Proposal, o *store.ProposalOutcome) proposalDoc {
	d := proposalDoc{ID: p.ID, AgentID: p.AgentID, Advisor: p.Advisor, ModelVersion: p.ModelVersion, Change: rawOr(p.Change, "{}"), Evidence: rawOr(p.Evidence, "[]"),
		Confidence: p.Confidence, Blast: rawOr(p.Blast, "{}"), Predicted: rawOr(p.Predicted, "{}"), Widens: p.Widens, Mode: p.Mode, Status: string(p.Status),
		CreatedAt: rfc(p.CreatedAt), ExpiresAt: rfc(p.ExpiresAt), DecidedBy: p.DecidedBy, Note: p.Note}
	if p.DecidedAt != nil {
		d.DecidedAt = rfc(*p.DecidedAt)
	}
	if o != nil {
		od := &outcomeDoc{Result: o.Result, Predicted: rawOr(o.Predicted, "{}"), Observed: rawOr(o.Observed, "{}"), RecordedAt: rfc(o.RecordedAt)}
		if o.InForceAt != nil {
			od.InForceAt = rfc(*o.InForceAt)
		}
		d.Outcome = od
	}
	return d
}

// submitProposal is POST .../proposals.
func (a *Admin) submitProposal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Advisor      string             `json:"advisor"`
		ModelVersion int64              `json:"modelVersion"`
		Change       AccessChange       `json:"change"`
		Evidence     []EvidenceFact     `json:"evidence"`
		Predicted    map[string]float64 `json:"predicted"`
		TTLSeconds   int                `json:"ttlSeconds"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	if req.TTLSeconds < 0 || req.TTLSeconds > int(maxProposalTTL/time.Second) {
		a.fail(w, Errf(KindInvalid, "ttlSeconds must be between 0 and %d", int(maxProposalTTL/time.Second)))
		return
	}
	p, err := a.tn(r).Hub.SubmitProposal(r.Context(), actor(r), ProposalInput{Advisor: req.Advisor, ModelVersion: req.ModelVersion, Change: req.Change,
		Evidence: req.Evidence, Predicted: req.Predicted, TTL: time.Duration(req.TTLSeconds) * time.Second})
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toProposalDoc(p, nil))
}

// listProposals is GET .../proposals?status=&agent=&limit=.
func (a *Admin) listProposals(w http.ResponseWriter, r *http.Request) {
	q := store.ProposalQuery{Status: store.ProposalStatus(r.URL.Query().Get("status")), AgentID: r.URL.Query().Get("agent")}
	switch q.Status {
	case "", store.ProposalPending, store.ProposalAccepted, store.ProposalRejected, store.ProposalExpired, store.ProposalApplied, store.ProposalFailed:
	default:
		a.fail(w, Errf(KindInvalid, "status must be pending, accepted, rejected, expired, applied or failed"))
		return
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		n, ok := atoiPositive(v)
		if !ok {
			a.fail(w, Errf(KindInvalid, "limit must be a positive number"))
			return
		}
		q.Limit = n
	}
	ps, err := a.tn(r).Hub.ListProposals(r.Context(), q)
	if err != nil {
		a.fail(w, err)
		return
	}
	out := make([]proposalDoc, len(ps))
	for i, p := range ps {
		out[i] = toProposalDoc(p, nil)
	}
	writeJSON(w, http.StatusOK, map[string]any{"proposals": out})
}

// advisorRecordDoc is one advisor's record, as GET .../proposals/advisors returns it.
type advisorRecordDoc struct {
	Advisor     string `json:"advisor"`
	Proposals   int    `json:"proposals"`
	Pending     int    `json:"pending"`
	Accepted    int    `json:"accepted"`
	Failed      int    `json:"failed"`
	Rejected    int    `json:"rejected"`
	Expired     int    `json:"expired"`
	Measured    int    `json:"measured"`
	Unconfirmed int    `json:"unconfirmed"`
	NoData      int    `json:"noData"`
	Held        int    `json:"held"`
	Missed      int    `json:"missed"`
}

// listAdvisors is GET .../proposals/advisors: what each advisor has proposed, what people did with it, and whether its predictions held.
func (a *Admin) listAdvisors(w http.ResponseWriter, r *http.Request) {
	rs, err := a.tn(r).Hub.AdvisorRecords(r.Context())
	if err != nil {
		a.fail(w, err)
		return
	}
	out := make([]advisorRecordDoc, len(rs))
	for i, x := range rs {
		out[i] = advisorRecordDoc{Advisor: x.Advisor, Proposals: x.Proposals, Pending: x.Pending, Accepted: x.Accepted, Failed: x.Failed, Rejected: x.Rejected,
			Expired: x.Expired, Measured: x.Measured, Unconfirmed: x.Unconfirmed, NoData: x.NoData, Held: x.Held, Missed: x.Missed}
	}
	writeJSON(w, http.StatusOK, map[string]any{"advisors": out})
}

func atoiPositive(s string) (int, bool) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' || n > 1<<20 {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, n > 0
}

// getProposal is GET .../proposals/{id}, with its Outcome once it has one.
func (a *Admin) getProposal(w http.ResponseWriter, r *http.Request) {
	p, o, err := a.tn(r).Hub.GetProposal(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toProposalDoc(p, o))
}

func (a *Admin) decideProposal(accept bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Note string `json:"note"`
		}
		if err := decode(r, &req); err != nil {
			a.fail(w, err)
			return
		}
		hub := a.tn(r).Hub
		var p store.Proposal
		var err error
		if accept {
			p, err = hub.AcceptProposal(r.Context(), actor(r), r.PathValue("id"), req.Note)
		} else {
			p, err = hub.RejectProposal(r.Context(), actor(r), r.PathValue("id"), req.Note)
		}
		if err != nil {
			a.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, toProposalDoc(p, nil))
	}
}
