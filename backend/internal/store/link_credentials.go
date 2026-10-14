package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// LinkCredential is one sealed row of the iperf3 credentials the server issues for the throughput tests of cluster pairs
// (see server/linkcreds.go). The store only keeps and returns the bytes: what is inside is encrypted by the server under a key
// derived from its CA key, so a copy of the database alone does not hold a usable credential, and the store never sees, logs or
// interprets it.
//
// Kind is "cluster" (ID is a cluster id: that cluster's iperf3 server identity) or "pair" (ID names the two clusters: the
// user name and password of the pair).
type LinkCredential struct {
	OrgID, Kind, ID string
	Sealed          []byte
	CreatedAt       time.Time
}

const linkCredentialSchema = `
CREATE TABLE IF NOT EXISTS link_iperf_credentials (
  org_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  id TEXT NOT NULL,
  sealed BLOB NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (org_id, kind, id)
);
`

// GetLinkCredential returns one row, ErrNotFound if there is none.
func (s *SQLite) GetLinkCredential(ctx context.Context, org, kind, id string) (LinkCredential, error) {
	var c LinkCredential
	var at int64
	err := s.db.QueryRowContext(ctx, `SELECT org_id, kind, id, sealed, created_at FROM link_iperf_credentials WHERE org_id=? AND kind=? AND id=?`, org, kind, id).
		Scan(&c.OrgID, &c.Kind, &c.ID, &c.Sealed, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return LinkCredential{}, ErrNotFound
	}
	if err != nil {
		return LinkCredential{}, err
	}
	c.CreatedAt = fromMS(at)
	return c, nil
}

// ListLinkCredentials returns every row of an organisation.
func (s *SQLite) ListLinkCredentials(ctx context.Context, org string) ([]LinkCredential, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT org_id, kind, id, sealed, created_at FROM link_iperf_credentials WHERE org_id=? ORDER BY kind, id`, org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LinkCredential
	for rows.Next() {
		var c LinkCredential
		var at int64
		if err := rows.Scan(&c.OrgID, &c.Kind, &c.ID, &c.Sealed, &at); err != nil {
			return nil, err
		}
		c.CreatedAt = fromMS(at)
		out = append(out, c)
	}
	return out, rows.Err()
}

// PutLinkCredential stores a row, replacing one with the same organisation, kind and id.
func (s *SQLite) PutLinkCredential(ctx context.Context, c LinkCredential) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO link_iperf_credentials(org_id, kind, id, sealed, created_at) VALUES(?,?,?,?,?)
		ON CONFLICT(org_id, kind, id) DO UPDATE SET sealed=excluded.sealed, created_at=excluded.created_at`, c.OrgID, c.Kind, c.ID, c.Sealed, ms(c.CreatedAt))
	return err
}

// DeleteLinkCredential removes a row; a row that is not there is not an error.
func (s *SQLite) DeleteLinkCredential(ctx context.Context, org, kind, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM link_iperf_credentials WHERE org_id=? AND kind=? AND id=?`, org, kind, id)
	return err
}
