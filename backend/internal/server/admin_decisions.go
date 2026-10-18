package server

import (
	"encoding/json"
	"net/http"
	"strconv"

	"continuum/internal/decisionloop"
)

// The HTTP side of the decision log. What may be recorded, and how, is decisions.go's.

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

// recordDecisions is POST .../decisions. Editors only - the same role that can already run an external
// decider (route "POST .../decide") and the one RoleDescriptions already names for "manual records,
// overrides, decisions".
func (a *Admin) recordDecisions(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Entries []decisionEntryDoc `json:"entries"`
	}
	if err := decode(r, &body); err != nil {
		a.fail(w, err)
		return
	}
	entries := make([]decisionloop.DecisionEntry, len(body.Entries))
	for i, e := range body.Entries {
		moves := make([]decisionloop.DecisionMove, len(e.Moves))
		for j, m := range e.Moves {
			moves[j] = decisionloop.DecisionMove{
				ServiceID: m.ServiceID, ServiceName: m.ServiceName, From: m.From, To: m.To, Reason: m.Reason,
				Confidence: m.Confidence, Verdict: m.Verdict, Benefit: m.Benefit, BeforeCost: m.BeforeCost, AfterCost: m.AfterCost, MigrationCost: m.MigrationCost,
			}
		}
		entries[i] = decisionloop.DecisionEntry{
			DeciderID: e.DeciderID, DeciderName: e.DeciderName, DeciderKind: e.DeciderKind,
			Schema: e.Input.Schema, ClusterCount: e.Input.ClusterCount, ServiceCount: e.Input.ServiceCount, Policy: e.Input.Policy, Moves: moves,
		}
	}
	n, err := a.core(r).Loop.RecordDecisions(r.Context(), actor(r), entries)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"recorded": n})
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

// listDecisions is GET .../decisions: any member may read it back, the same reach as /events and /history.
func (a *Admin) listDecisions(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	ds, err := a.C.Store.ListDecisions(r.Context(), a.core(r).OrgID, limit)
	if err != nil {
		a.fail(w, err)
		return
	}
	out := make([]decisionLogDoc, len(ds))
	for i, d := range ds {
		out[i] = decisionLogDoc{
			ID: "dec-" + itoa(d.ID), At: rfc(d.At), RecordedBy: d.RecordedBy,
			DeciderID: d.DeciderID, DeciderName: d.DeciderName, DeciderKind: d.DeciderKind,
			ServiceID: d.ServiceID, ServiceName: d.ServiceName, From: d.FromCluster, To: d.ToCluster,
			Reason: d.Reason, Benefit: d.Benefit, Confidence: d.Confidence, Verdict: d.Verdict,
			BeforeCost: d.BeforeCost, AfterCost: d.AfterCost, MigrationCost: d.MigrationCost,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"decisions": out})
}
