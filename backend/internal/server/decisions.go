package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"continuum/internal/store"
)

// Decision log: a record of what each decider recommended, kept so a decider's quality can later be
// judged against what the topology actually looked like afterward. The recommendation is computed
// entirely in the browser (src/lib/placement/deciders.ts's runDecider/toDecisionLogEntry); this file is
// the backend half, which only ever files a copy away. Nothing here executes a move, and nothing here
// may: see Deciders.tsx's own "a decider recommends, a person decides."

// Bounds on what one recordDecisions call may add, so a confused or hostile client cannot grow the log by
// an unreasonable amount in a single request. The engine caps a single baseline run at 40 moves
// (MAX_MOVES in engine.ts); maxDecisionMovesTotal leaves generous headroom for comparing several
// deciders (built-in and external) in the same run without trusting the client's own count of them.
const (
	maxDecisionEntries    = 20
	maxDecisionMovesTotal = 500
)

// DecisionMove is one service a decider proposed moving, exactly as the browser scored it.
type DecisionMove struct {
	ServiceID, ServiceName, From, To, Reason, Confidence, Verdict string
	Benefit, BeforeCost, AfterCost, MigrationCost                 float64
}

// DecisionEntry is one decider's result from one comparison run.
type DecisionEntry struct {
	DeciderID, DeciderName, DeciderKind string
	Schema, ClusterCount, ServiceCount  int
	// Policy is kept as raw JSON, not decoded: its shape belongs to the frontend's Policy type
	// (src/lib/placement/types.ts), which this server has no reason to duplicate just to store it.
	Policy json.RawMessage
	Moves  []DecisionMove
}

// RecordDecisions appends every move in entries to the organisation's decision log, stamped with now and
// who was signed in when the recommendation was computed.
//
// Deliberately simple: no audit row (this is not a privileged action - see store.DecisionLog), and no
// "audited" fail-closed guard like ApproveAgent or CreateToken use. Those exist so an action with
// authority behind it never happens unseen; this has no action and no authority, only a recommendation a
// person may or may not act on later. So the rule here runs the other way from audited(): a failure to
// write this log must never stop the caller returning - or the dashboard having already shown - the
// recommendation it describes. The frontend calls this fire-and-forget for exactly that reason (see
// Deciders.tsx's run()).
func (c *Core) RecordDecisions(ctx context.Context, actor string, entries []DecisionEntry) (int, error) {
	if len(entries) > maxDecisionEntries {
		return 0, errf(KindInvalid, "too many decider results in one call")
	}
	now := c.Now()
	by := printable(actor, maxAuditActor)
	var rows []store.DecisionLog
	for _, e := range entries {
		if e.DeciderID == "" || (e.DeciderKind != "builtin" && e.DeciderKind != "external") {
			return 0, errf(KindInvalid, "each entry needs a decider id and a decider kind of \"builtin\" or \"external\"")
		}
		if len(rows)+len(e.Moves) > maxDecisionMovesTotal {
			return 0, errf(KindInvalid, "too many proposed moves in one call")
		}
		for _, m := range e.Moves {
			if m.ServiceID == "" || m.To == "" {
				continue // nothing proposed for this one - not a move worth recording
			}
			rows = append(rows, store.DecisionLog{
				OrgID: c.OrgID, At: now, RecordedBy: by,
				DeciderID: printable(e.DeciderID, 128), DeciderName: printable(e.DeciderName, 128), DeciderKind: e.DeciderKind,
				Schema: e.Schema, ClusterCount: e.ClusterCount, ServiceCount: e.ServiceCount, PolicyJSON: []byte(e.Policy),
				ServiceID: printable(m.ServiceID, maxAuditTarget), ServiceName: printable(m.ServiceName, maxAuditTarget),
				FromCluster: printable(m.From, maxAuditTarget), ToCluster: printable(m.To, maxAuditTarget),
				Reason: printable(m.Reason, maxAuditDetail), Benefit: m.Benefit,
				Confidence: printable(m.Confidence, 32), Verdict: printable(m.Verdict, 32),
				BeforeCost: m.BeforeCost, AfterCost: m.AfterCost, MigrationCost: m.MigrationCost,
			})
		}
	}
	if len(rows) == 0 {
		return 0, nil
	}
	if err := c.Store.AddDecisions(ctx, rows); err != nil {
		return 0, err
	}
	return len(rows), nil
}

// ---- HTTP ----

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
	n, err := a.core(r).RecordDecisions(r.Context(), actor(r), entries)
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
