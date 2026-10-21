package decisionloop

import (
	"encoding/json"
	"net/http"
	"strconv"

	"continuum/internal/httpkit"
	"continuum/internal/kernel"
	"continuum/internal/twin"
)

// The HTTP side of the decision log. What may be recorded, and how, is decisions.go's.

// API is the decision loop's routes: the decision log and the proposals. For finds the decision loop of the organisation a request is for: the shell does that from what its
// guard put in the request, and the handlers know nothing of how.
type API struct {
	For func(r *http.Request) *Service
	// Model is the effective model of the organisation a request is for (the estate builds it), which a request to the external
	// decider is checked against; Version is the server's, which the decider is told it is talking to.
	Model   func(r *http.Request) (twin.Model, error)
	Version string
}

// Mount puts the routes on r.
func (h API) Mount(r httpkit.Router) {
	r.Handle("POST "+httpkit.OrgPath+"/decide", httpkit.EditorRole, h.decide)
	r.Handle("POST "+httpkit.OrgPath+"/decisions", httpkit.EditorRole, h.record)
	r.Handle("GET "+httpkit.OrgPath+"/decisions", httpkit.MemberRole, h.list)
	h.mountProposals(r)
}

type decisionMoveDoc struct {
	ServiceID     string  `json:"serviceId"`
	ServiceName   string  `json:"serviceName"`
	From          string  `json:"from"`
	To            string  `json:"to"`
	Reason        string  `json:"reason"`
	Benefit       float64 `json:"benefit"`
	Confidence    string  `json:"confidence"`
	Verdict       string  `json:"verdict"`
	BeforeCost    float64 `json:"beforeCost"`
	AfterCost     float64 `json:"afterCost"`
	MigrationCost float64 `json:"migrationCost"`
}

// decisionInputDoc is the "meaningful summary" of DecisionInput (deciders.ts) that is worth keeping per
// run - see store.DecisionLog's own doc comment for why the full estate is not repeated here.
type decisionInputDoc struct {
	Schema       int             `json:"schema"`
	ClusterCount int             `json:"clusterCount"`
	ServiceCount int             `json:"serviceCount"`
	Policy       json.RawMessage `json:"policy"`
}

type decisionEntryDoc struct {
	DeciderID   string            `json:"deciderId"`
	DeciderName string            `json:"deciderName"`
	DeciderKind string            `json:"deciderKind"`
	Input       decisionInputDoc  `json:"input"`
	Moves       []decisionMoveDoc `json:"moves"`
}

// record is POST .../decisions. Editors only - the same role that can already run an external
// decider (route "POST .../decide") and the one RoleDescriptions already names for "manual records,
// overrides, decisions".
func (h API) record(w http.ResponseWriter, r *http.Request) {
	var body recordDecisionsRequest
	s := h.For(r)
	if err := httpkit.Decode(r, &body); err != nil {
		httpkit.Fail(w, s.Log, err)
		return
	}
	entries := make([]DecisionEntry, len(body.Entries))
	for i, e := range body.Entries {
		moves := make([]DecisionMove, len(e.Moves))
		for j, m := range e.Moves {
			moves[j] = DecisionMove{
				ServiceID: m.ServiceID, ServiceName: m.ServiceName, From: m.From, To: m.To, Reason: m.Reason,
				Confidence: m.Confidence, Verdict: m.Verdict, Benefit: m.Benefit, BeforeCost: m.BeforeCost, AfterCost: m.AfterCost, MigrationCost: m.MigrationCost,
			}
		}
		entries[i] = DecisionEntry{
			DeciderID: e.DeciderID, DeciderName: e.DeciderName, DeciderKind: e.DeciderKind,
			Schema: e.Input.Schema, ClusterCount: e.Input.ClusterCount, ServiceCount: e.Input.ServiceCount, Policy: e.Input.Policy, Moves: moves,
		}
	}
	n, err := s.RecordDecisions(r.Context(), httpkit.Actor(r), entries)
	if err != nil {
		httpkit.Fail(w, s.Log, err)
		return
	}
	httpkit.WriteJSON(w, http.StatusCreated, recordedDoc{Recorded: n})
}

type decisionLogDoc struct {
	ID            string  `json:"id"`
	At            string  `json:"at"`
	RecordedBy    string  `json:"recordedBy"`
	DeciderID     string  `json:"deciderId"`
	DeciderName   string  `json:"deciderName"`
	DeciderKind   string  `json:"deciderKind"`
	ServiceID     string  `json:"serviceId"`
	ServiceName   string  `json:"serviceName"`
	From          string  `json:"from"`
	To            string  `json:"to"`
	Reason        string  `json:"reason,omitempty"`
	Benefit       float64 `json:"benefit"`
	Confidence    string  `json:"confidence"`
	Verdict       string  `json:"verdict"`
	BeforeCost    float64 `json:"beforeCost"`
	AfterCost     float64 `json:"afterCost"`
	MigrationCost float64 `json:"migrationCost"`
}

// list is GET .../decisions: any member may read it back, the same reach as /events and /history.
func (h API) list(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	s := h.For(r)
	ds, err := s.Store.ListDecisions(r.Context(), s.OrgID, limit)
	if err != nil {
		httpkit.Fail(w, s.Log, err)
		return
	}
	out := make([]decisionLogDoc, len(ds))
	for i, d := range ds {
		out[i] = decisionLogDoc{
			ID: "dec-" + strconv.FormatInt(d.ID, 10), At: kernel.RFC(d.At), RecordedBy: d.RecordedBy,
			DeciderID: d.DeciderID, DeciderName: d.DeciderName, DeciderKind: d.DeciderKind,
			ServiceID: d.ServiceID, ServiceName: d.ServiceName, From: d.FromCluster, To: d.ToCluster,
			Reason: d.Reason, Benefit: d.Benefit, Confidence: d.Confidence, Verdict: d.Verdict,
			BeforeCost: d.BeforeCost, AfterCost: d.AfterCost, MigrationCost: d.MigrationCost,
		}
	}
	httpkit.WriteJSON(w, http.StatusOK, decisionsDoc{Decisions: out})
}
