package store

import (
	"context"
	"time"
)

// The decision log is a plain append-only table, not a hash chain: see DecisionLog's own doc comment for
// why it does not need the tamper-evidence audit has. AddDecisions writes one run's rows together so a
// comparison of several deciders lands as one unit, but there is nothing atomic about the log itself -
// unlike EnrollAgent's token consume, two calls can never race over the same row because each row is
// brand new.

// AddDecisions appends ds in one transaction. Each row's At is taken as given (callers stamp a single
// "now" across a whole run, the way AddAudit defaults a lone row's At when it is zero - here, zero would
// make every row in the batch impossible to tell apart by time, so the caller always sets it).
func (s *SQLite) AddDecisions(ctx context.Context, ds []DecisionLog) error {
	if len(ds) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO decisions(
		org_id, at, recorded_by, decider_id, decider_name, decider_kind, schema_version, cluster_count, service_count, policy,
		service_id, service_name, from_cluster, to_cluster, reason, benefit, confidence, verdict, before_cost, after_cost, migration_cost
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, d := range ds {
		at := d.At
		if at.IsZero() {
			at = time.Now()
		}
		policy := d.PolicyJSON
		if len(policy) == 0 {
			policy = []byte("{}")
		}
		if _, err := stmt.ExecContext(ctx,
			d.OrgID, ms(at), d.RecordedBy, d.DeciderID, d.DeciderName, d.DeciderKind, d.Schema, d.ClusterCount, d.ServiceCount, policy,
			d.ServiceID, d.ServiceName, d.FromCluster, d.ToCluster, d.Reason, d.Benefit, d.Confidence, d.Verdict, d.BeforeCost, d.AfterCost, d.MigrationCost,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListDecisions returns one organisation's decision log, newest first.
func (s *SQLite) ListDecisions(ctx context.Context, org string, limit int) ([]DecisionLog, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT
		id, org_id, at, recorded_by, decider_id, decider_name, decider_kind, schema_version, cluster_count, service_count, policy,
		service_id, service_name, from_cluster, to_cluster, reason, benefit, confidence, verdict, before_cost, after_cost, migration_cost
		FROM decisions WHERE org_id=? ORDER BY id DESC LIMIT ?`, org, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DecisionLog
	for rows.Next() {
		var d DecisionLog
		var at int64
		var policy []byte
		if err := rows.Scan(
			&d.ID, &d.OrgID, &at, &d.RecordedBy, &d.DeciderID, &d.DeciderName, &d.DeciderKind, &d.Schema, &d.ClusterCount, &d.ServiceCount, &policy,
			&d.ServiceID, &d.ServiceName, &d.FromCluster, &d.ToCluster, &d.Reason, &d.Benefit, &d.Confidence, &d.Verdict, &d.BeforeCost, &d.AfterCost, &d.MigrationCost,
		); err != nil {
			return nil, err
		}
		d.At = fromMS(at)
		d.PolicyJSON = policy
		out = append(out, d)
	}
	return out, rows.Err()
}
