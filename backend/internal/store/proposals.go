package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ProposalStatus is where a Proposal is in its life: pending until someone decides, then accepted or rejected; an accepted one is
// applied (the change was made through the same path a manual change takes) or failed (it could not be); a pending one that is
// not decided in time is expired.
type ProposalStatus string

const (
	ProposalPending  ProposalStatus = "pending"
	ProposalAccepted ProposalStatus = "accepted"
	ProposalRejected ProposalStatus = "rejected"
	ProposalExpired  ProposalStatus = "expired"
	ProposalApplied  ProposalStatus = "applied"
	ProposalFailed   ProposalStatus = "failed"
)

// Proposal is a change to one agent's access that an advisor suggests. The store keeps the typed parts (Change, Evidence, Blast)
// as the JSON the server validated and does not read them: their shape belongs to the server package. An advisor never changes
// anything; a Proposal is only a request for a person, or a narrow policy, to decide.
type Proposal struct {
	ID, OrgID, AgentID, Advisor string
	// ModelVersion is the version of the effective model the advisor reasoned over, so its evidence can be reproduced.
	ModelVersion int64
	Change       []byte
	Evidence     []byte
	// Confidence is the weakest class among the evidence: measured, derived, declared or unknown.
	Confidence string
	Blast      []byte
	// Predicted is what the advisor expects the change to do, as named metrics; the Outcome sets it beside what was observed.
	Predicted []byte
	// Widens is computed by the server from the change and the agent's state, never taken from the advisor.
	Widens bool
	// Mode is "advise"; "auto" is reserved for a later slice.
	Mode      string
	Status    ProposalStatus
	CreatedAt time.Time
	ExpiresAt time.Time
	DecidedBy string
	DecidedAt *time.Time
	Note      string
}

// ProposalQuery filters ListProposals. A zero Limit means 200, and at most 1000 are returned.
type ProposalQuery struct {
	Status  ProposalStatus
	AgentID string
	Limit   int
}

// ProposalOutcome is what a proposal led to: the advisor's prediction beside what was observed once the change was in force.
// Result is "measured" when the agent confirmed the change and the metrics were read, "unconfirmed" when it never did.
type ProposalOutcome struct {
	ProposalID, OrgID, Result string
	Predicted, Observed       []byte
	InForceAt                 *time.Time
	RecordedAt                time.Time
}

const proposalCols = `id, org_id, agent_id, advisor, model_version, change, evidence, confidence, blast, predicted, widens, mode, status, created_at, expires_at, decided_by, decided_at, note`

func scanProposal(row interface{ Scan(...any) error }) (Proposal, error) {
	var p Proposal
	var status string
	var widens int
	var created, expires int64
	var decidedAt sql.NullInt64
	err := row.Scan(&p.ID, &p.OrgID, &p.AgentID, &p.Advisor, &p.ModelVersion, &p.Change, &p.Evidence, &p.Confidence, &p.Blast, &p.Predicted, &widens, &p.Mode,
		&status, &created, &expires, &p.DecidedBy, &decidedAt, &p.Note)
	if errors.Is(err, sql.ErrNoRows) {
		return Proposal{}, ErrNotFound
	}
	if err != nil {
		return Proposal{}, err
	}
	p.Status, p.Widens = ProposalStatus(status), widens != 0
	p.CreatedAt, p.ExpiresAt, p.DecidedAt = fromMS(created), fromMS(expires), fromNullMS(decidedAt)
	return p, nil
}

func orEmptyJSON(b []byte, empty string) []byte {
	if len(b) == 0 {
		return []byte(empty)
	}
	return b
}

func (s *SQLite) CreateProposal(ctx context.Context, p Proposal, opts ...WriteOption) error {
	status := p.Status
	if status == "" {
		status = ProposalPending
	}
	mode := p.Mode
	if mode == "" {
		mode = "advise"
	}
	conf := p.Confidence
	if conf == "" {
		conf = "unknown"
	}
	widens := 0
	if p.Widens {
		widens = 1
	}
	return s.write(ctx, opts, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO proposals(`+proposalCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			p.ID, p.OrgID, p.AgentID, p.Advisor, p.ModelVersion, orEmptyJSON(p.Change, "{}"), orEmptyJSON(p.Evidence, "[]"), conf, orEmptyJSON(p.Blast, "{}"), orEmptyJSON(p.Predicted, "{}"),
			widens, mode, string(status), ms(p.CreatedAt), ms(p.ExpiresAt), p.DecidedBy, nullMS(p.DecidedAt), p.Note)
		if isUnique(err) {
			return ErrConflict
		}
		return err
	})
}

func (s *SQLite) GetProposal(ctx context.Context, org, id string) (Proposal, error) {
	return scanProposal(s.db.QueryRowContext(ctx, `SELECT `+proposalCols+` FROM proposals WHERE org_id=? AND id=?`, org, id))
}

func (s *SQLite) ListProposals(ctx context.Context, org string, q ProposalQuery) ([]Proposal, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+proposalCols+` FROM proposals
		WHERE org_id=? AND (?='' OR status=?) AND (?='' OR agent_id=?) ORDER BY created_at DESC, id DESC LIMIT ?`,
		org, string(q.Status), string(q.Status), q.AgentID, q.AgentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Proposal
	for rows.Next() {
		p, err := scanProposal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *SQLite) TransitionProposal(ctx context.Context, org, id string, from []ProposalStatus, to ProposalStatus, by, note string, at time.Time, opts ...WriteOption) error {
	return s.write(ctx, opts, func(tx *sql.Tx) error {
		var cur string
		err := tx.QueryRowContext(ctx, `SELECT status FROM proposals WHERE org_id=? AND id=?`, org, id).Scan(&cur)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		ok := false
		for _, f := range from {
			ok = ok || string(f) == cur
		}
		if !ok {
			return ErrBadState
		}
		// Who decided and when is set by the first transition, the decision; later ones (applied, failed) do not overwrite it, and a
		// note is replaced only by a non-empty one.
		_, err = tx.ExecContext(ctx, `UPDATE proposals SET status=?,
			decided_by=CASE WHEN decided_at IS NULL THEN ? ELSE decided_by END,
			decided_at=COALESCE(decided_at, ?),
			note=CASE WHEN ?='' THEN note ELSE ? END
			WHERE org_id=? AND id=? AND status=?`,
			string(to), by, ms(at), note, note, org, id, cur)
		return err
	})
}

func (s *SQLite) RecordProposalOutcome(ctx context.Context, o ProposalOutcome, opts ...WriteOption) error {
	return s.write(ctx, opts, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO proposal_outcomes(proposal_id, org_id, result, predicted, observed, in_force_at, recorded_at)
			SELECT ?,?,?,?,?,?,? WHERE EXISTS (SELECT 1 FROM proposals WHERE org_id=? AND id=?)
			ON CONFLICT(proposal_id) DO NOTHING`,
			o.ProposalID, o.OrgID, o.Result, orEmptyJSON(o.Predicted, "{}"), orEmptyJSON(o.Observed, "{}"), nullMS(o.InForceAt), ms(o.RecordedAt), o.OrgID, o.ProposalID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			var exists int
			if e := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM proposals WHERE org_id=? AND id=?`, o.OrgID, o.ProposalID).Scan(&exists); e != nil {
				return e
			}
			if exists == 0 {
				return ErrNotFound
			}
			return errNoChange // it already has one: nothing changes, and no audit row is written
		}
		return nil
	})
}

func (s *SQLite) GetProposalOutcome(ctx context.Context, org, id string) (ProposalOutcome, error) {
	var o ProposalOutcome
	var inForce sql.NullInt64
	var recorded int64
	err := s.db.QueryRowContext(ctx, `SELECT proposal_id, org_id, result, predicted, observed, in_force_at, recorded_at FROM proposal_outcomes WHERE org_id=? AND proposal_id=?`, org, id).
		Scan(&o.ProposalID, &o.OrgID, &o.Result, &o.Predicted, &o.Observed, &inForce, &recorded)
	if errors.Is(err, sql.ErrNoRows) {
		return ProposalOutcome{}, ErrNotFound
	}
	if err != nil {
		return ProposalOutcome{}, err
	}
	o.InForceAt, o.RecordedAt = fromNullMS(inForce), fromMS(recorded)
	return o, nil
}
