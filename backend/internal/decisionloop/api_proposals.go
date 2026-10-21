package decisionloop

import (
	"encoding/json"
	"net/http"
	"time"

	"continuum/internal/httpkit"
	"continuum/internal/kernel"
	"continuum/internal/store"
)

// mountProposals puts the proposals' routes on r. Every one needs an editor.
func (h API) mountProposals(r httpkit.Router) {
	r.Handle("POST "+httpkit.OrgPath+"/proposals", httpkit.EditorRole, h.submitProposal)
	r.Handle("GET "+httpkit.OrgPath+"/proposals", httpkit.EditorRole, h.listProposals)
	r.Handle("GET "+httpkit.OrgPath+"/proposals/advisors", httpkit.EditorRole, h.listAdvisors)
	r.Handle("GET "+httpkit.OrgPath+"/proposals/{id}", httpkit.EditorRole, h.getProposal)
	r.Handle("POST "+httpkit.OrgPath+"/proposals/{id}/accept", httpkit.EditorRole, h.decideProposal(true))
	r.Handle("POST "+httpkit.OrgPath+"/proposals/{id}/reject", httpkit.EditorRole, h.decideProposal(false))
}

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
		CreatedAt: kernel.RFC(p.CreatedAt), ExpiresAt: kernel.RFC(p.ExpiresAt), DecidedBy: p.DecidedBy, Note: p.Note}
	if p.DecidedAt != nil {
		d.DecidedAt = kernel.RFC(*p.DecidedAt)
	}
	if o != nil {
		od := &outcomeDoc{Result: o.Result, Predicted: rawOr(o.Predicted, "{}"), Observed: rawOr(o.Observed, "{}"), RecordedAt: kernel.RFC(o.RecordedAt)}
		if o.InForceAt != nil {
			od.InForceAt = kernel.RFC(*o.InForceAt)
		}
		d.Outcome = od
	}
	return d
}

// submitProposal is POST .../proposals.
func (h API) submitProposal(w http.ResponseWriter, r *http.Request) {
	s := h.For(r)
	var req submitProposalRequest
	if err := httpkit.Decode(r, &req); err != nil {
		httpkit.Fail(w, s.Log, err)
		return
	}
	if req.TTLSeconds < 0 || req.TTLSeconds > int(MaxProposalTTL/time.Second) {
		httpkit.Fail(w, s.Log, kernel.Errf(kernel.KindInvalid, "ttlSeconds must be between 0 and %d", int(MaxProposalTTL/time.Second)))
		return
	}
	p, err := s.SubmitProposal(r.Context(), httpkit.Actor(r), ProposalInput{Advisor: req.Advisor, ModelVersion: req.ModelVersion, Change: req.Change,
		Evidence: req.Evidence, Predicted: req.Predicted, TTL: time.Duration(req.TTLSeconds) * time.Second})
	if err != nil {
		httpkit.Fail(w, s.Log, err)
		return
	}
	httpkit.WriteJSON(w, http.StatusCreated, toProposalDoc(p, nil))
}

// listProposals is GET .../proposals?status=&agent=&limit=.
func (h API) listProposals(w http.ResponseWriter, r *http.Request) {
	s := h.For(r)
	q := store.ProposalQuery{Status: store.ProposalStatus(r.URL.Query().Get("status")), AgentID: r.URL.Query().Get("agent")}
	switch q.Status {
	case "", store.ProposalPending, store.ProposalAccepted, store.ProposalRejected, store.ProposalExpired, store.ProposalApplied, store.ProposalFailed:
	default:
		httpkit.Fail(w, s.Log, kernel.Errf(kernel.KindInvalid, "status must be pending, accepted, rejected, expired, applied or failed"))
		return
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		n, ok := atoiPositive(v)
		if !ok {
			httpkit.Fail(w, s.Log, kernel.Errf(kernel.KindInvalid, "limit must be a positive number"))
			return
		}
		q.Limit = n
	}
	ps, err := s.ListProposals(r.Context(), q)
	if err != nil {
		httpkit.Fail(w, s.Log, err)
		return
	}
	out := make([]proposalDoc, len(ps))
	for i, p := range ps {
		out[i] = toProposalDoc(p, nil)
	}
	httpkit.WriteJSON(w, http.StatusOK, proposalsDoc{Proposals: out})
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
func (h API) listAdvisors(w http.ResponseWriter, r *http.Request) {
	s := h.For(r)
	rs, err := s.AdvisorRecords(r.Context())
	if err != nil {
		httpkit.Fail(w, s.Log, err)
		return
	}
	out := make([]advisorRecordDoc, len(rs))
	for i, x := range rs {
		out[i] = advisorRecordDoc{Advisor: x.Advisor, Proposals: x.Proposals, Pending: x.Pending, Accepted: x.Accepted, Failed: x.Failed, Rejected: x.Rejected,
			Expired: x.Expired, Measured: x.Measured, Unconfirmed: x.Unconfirmed, NoData: x.NoData, Held: x.Held, Missed: x.Missed}
	}
	httpkit.WriteJSON(w, http.StatusOK, advisorsDoc{Advisors: out})
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
func (h API) getProposal(w http.ResponseWriter, r *http.Request) {
	s := h.For(r)
	p, o, err := s.GetProposal(r.Context(), r.PathValue("id"))
	if err != nil {
		httpkit.Fail(w, s.Log, err)
		return
	}
	httpkit.WriteJSON(w, http.StatusOK, toProposalDoc(p, o))
}

func (h API) decideProposal(accept bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s := h.For(r)
		var req decideProposalRequest
		if err := httpkit.Decode(r, &req); err != nil {
			httpkit.Fail(w, s.Log, err)
			return
		}
		var p store.Proposal
		var err error
		if accept {
			p, err = s.AcceptProposal(r.Context(), httpkit.Actor(r), r.PathValue("id"), req.Note)
		} else {
			p, err = s.RejectProposal(r.Context(), httpkit.Actor(r), r.PathValue("id"), req.Note)
		}
		if err != nil {
			httpkit.Fail(w, s.Log, err)
			return
		}
		httpkit.WriteJSON(w, http.StatusOK, toProposalDoc(p, nil))
	}
}
