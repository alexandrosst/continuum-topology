package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// ---- history snapshots ----

func (s *SQLite) AddHistory(ctx context.Context, org string, at time.Time, data []byte) error {
	// The API names snapshots by whole-second RFC 3339 times; storing the same resolution keeps every
	// listed time usable to fetch its own snapshot.
	at = at.Truncate(time.Second)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO history(org_id, at, data) VALUES(?,?,?) ON CONFLICT(org_id, at) DO UPDATE SET data=excluded.data`,
		org, ms(at), data)
	return err
}

func (s *SQLite) ListHistory(ctx context.Context, org string, since, until time.Time) ([]HistoryPoint, error) {
	q := `SELECT at, length(data) FROM history WHERE org_id=?`
	args := []any{org}
	if !since.IsZero() {
		q += ` AND at>=?`
		args = append(args, ms(since))
	}
	if !until.IsZero() {
		q += ` AND at<=?`
		args = append(args, ms(until))
	}
	q += ` ORDER BY at`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HistoryPoint{}
	for rows.Next() {
		var at int64
		var n int
		if err := rows.Scan(&at, &n); err != nil {
			return nil, err
		}
		out = append(out, HistoryPoint{At: fromMS(at), Bytes: n})
	}
	return out, rows.Err()
}

func (s *SQLite) GetHistory(ctx context.Context, org string, at time.Time) (HistoryPoint, []byte, error) {
	var d []byte
	var got int64
	err := s.db.QueryRowContext(ctx, `SELECT at, data FROM history WHERE org_id=? AND at<=? ORDER BY at DESC LIMIT 1`, org, ms(at)).Scan(&got, &d)
	if errors.Is(err, sql.ErrNoRows) {
		return HistoryPoint{}, nil, ErrNotFound
	}
	if err != nil {
		return HistoryPoint{}, nil, err
	}
	return HistoryPoint{At: fromMS(got), Bytes: len(d)}, d, nil
}

func (s *SQLite) DeleteHistory(ctx context.Context, org string, ats []time.Time) error {
	if len(ats) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, at := range ats {
		if _, err := tx.ExecContext(ctx, `DELETE FROM history WHERE org_id=? AND at=?`, org, ms(at)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ---- events ----

func (s *SQLite) AddEvents(ctx context.Context, org string, evs []Event) error {
	if len(evs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, e := range evs {
		if e.Severity == "" {
			e.Severity = "info"
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO events(org_id, at, kind, target_kind, target_id, name, cluster_id, cluster_name, detail, cause, severity) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			org, ms(e.At), e.Kind, e.TargetKind, e.TargetID, e.Name, e.ClusterID, e.ClusterName, e.Detail, e.Cause, e.Severity); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLite) ListEvents(ctx context.Context, org string, q EventQuery) ([]Event, error) {
	if q.Limit <= 0 {
		q.Limit = 200
	}
	if q.Limit > 2000 {
		q.Limit = 2000
	}
	var where []string
	args := []any{org}
	where = append(where, "org_id=?")
	if !q.Since.IsZero() {
		where = append(where, "at>=?")
		args = append(args, ms(q.Since))
	}
	if !q.Until.IsZero() {
		where = append(where, "at<=?")
		args = append(args, ms(q.Until))
	}
	if q.Kind != "" {
		where = append(where, "kind=?")
		args = append(args, q.Kind)
	}
	if q.ClusterID != "" {
		where = append(where, "cluster_id=?")
		args = append(args, q.ClusterID)
	}
	if q.TargetID != "" {
		where = append(where, "target_id=?")
		args = append(args, q.TargetID)
	}
	args = append(args, q.Limit)
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, at, kind, target_kind, target_id, name, cluster_id, cluster_name, detail, cause, severity FROM events WHERE `+
			strings.Join(where, " AND ")+` ORDER BY at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		var at int64
		if err := rows.Scan(&e.ID, &at, &e.Kind, &e.TargetKind, &e.TargetID, &e.Name, &e.ClusterID, &e.ClusterName, &e.Detail, &e.Cause, &e.Severity); err != nil {
			return nil, err
		}
		e.At = fromMS(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *SQLite) PruneEvents(ctx context.Context, org string, before time.Time, keepNewest int) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM events WHERE org_id=? AND at<?`, org, ms(before)); err != nil {
		return err
	}
	if keepNewest > 0 {
		_, err := s.db.ExecContext(ctx,
			`DELETE FROM events WHERE org_id=? AND id NOT IN (SELECT id FROM events WHERE org_id=? ORDER BY at DESC, id DESC LIMIT ?)`, org, org, keepNewest)
		return err
	}
	return nil
}

func (s *SQLite) DeleteEvents(ctx context.Context, org string, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `DELETE FROM events WHERE org_id=? AND id=?`, org, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ---- settings ----

func (s *SQLite) GetSettings(ctx context.Context, org string) ([]byte, error) {
	var d []byte
	err := s.db.QueryRowContext(ctx, `SELECT data FROM settings WHERE org_id=?`, org).Scan(&d)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return d, err
}

func (s *SQLite) PutSettings(ctx context.Context, org string, data []byte, now time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings(org_id, data, updated_at) VALUES(?,?,?) ON CONFLICT(org_id) DO UPDATE SET data=excluded.data, updated_at=excluded.updated_at`,
		org, data, ms(now))
	return err
}
