package decisionloop

import (
	"context"
	"encoding/json"

	"continuum/internal/kernel"
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
//
// MaxDecisionPolicyBytes bounds the policy an entry carries. The policy is copied onto every move row of that
// entry (so each row stands alone), which multiplies its size by the move count: without a cap a request within
// the body limit could write tens of megabytes. The frontend's Policy is a handful of numeric weights, well under
// 1 KiB, so this leaves room for it to grow.
const (
	MaxDecisionEntries     = 20
	maxDecisionMovesTotal  = 500
	MaxDecisionPolicyBytes = 4 << 10
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
// person may or may not act on later. So the rule here runs the other way from Audited(): a failure to
// write this log must never stop the caller returning - or the dashboard having already shown - the
// recommendation it describes. The frontend calls this fire-and-forget for exactly that reason (see
// Deciders.tsx's run()).
func (s *Service) RecordDecisions(ctx context.Context, actor string, entries []DecisionEntry) (int, error) {
	if len(entries) > MaxDecisionEntries {
		return 0, kernel.Errf(kernel.KindInvalid, "too many decider results in one call")
	}
	now := s.Now()
	by := kernel.Printable(actor, kernel.MaxAuditActor)
	var rows []store.DecisionLog
	for _, e := range entries {
		if e.DeciderID == "" || (e.DeciderKind != "builtin" && e.DeciderKind != "external") {
			return 0, kernel.Errf(kernel.KindInvalid, "each entry needs a decider id and a decider kind of \"builtin\" or \"external\"")
		}
		if len(e.Policy) > MaxDecisionPolicyBytes {
			return 0, kernel.Errf(kernel.KindInvalid, "a decider's policy is too large to record")
		}
		if len(rows)+len(e.Moves) > maxDecisionMovesTotal {
			return 0, kernel.Errf(kernel.KindInvalid, "too many proposed moves in one call")
		}
		for _, m := range e.Moves {
			if m.ServiceID == "" || m.To == "" {
				continue // nothing proposed for this one - not a move worth recording
			}
			rows = append(rows, store.DecisionLog{
				OrgID: s.OrgID, At: now, RecordedBy: by,
				DeciderID: kernel.Printable(e.DeciderID, 128), DeciderName: kernel.Printable(e.DeciderName, 128), DeciderKind: e.DeciderKind,
				Schema: e.Schema, ClusterCount: e.ClusterCount, ServiceCount: e.ServiceCount, PolicyJSON: []byte(e.Policy),
				ServiceID: kernel.Printable(m.ServiceID, kernel.MaxAuditTarget), ServiceName: kernel.Printable(m.ServiceName, kernel.MaxAuditTarget),
				FromCluster: kernel.Printable(m.From, kernel.MaxAuditTarget), ToCluster: kernel.Printable(m.To, kernel.MaxAuditTarget),
				Reason: kernel.Printable(m.Reason, kernel.MaxAuditDetail), Benefit: m.Benefit,
				Confidence: kernel.Printable(m.Confidence, 32), Verdict: kernel.Printable(m.Verdict, 32),
				BeforeCost: m.BeforeCost, AfterCost: m.AfterCost, MigrationCost: m.MigrationCost,
			})
		}
	}
	if len(rows) == 0 {
		return 0, nil
	}
	if err := s.Store.AddDecisions(ctx, rows); err != nil {
		return 0, err
	}
	return len(rows), nil
}
