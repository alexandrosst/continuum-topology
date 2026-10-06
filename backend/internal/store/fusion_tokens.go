package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// FusionToken is a read credential for FUSION's data API (see server/fusion_data.go): what another system - a
// decision engine, a dashboard, a script - presents to read the metrics, logs and traces saved in FUSION without
// being a person with a session. It is deliberately not a personal access token: that one acts as its owner with
// the owner's whole role, whereas this one can read nothing but FUSION's data, only the signal types it names, and
// only the namespaces and clusters it names. Only the SHA-256 of the secret is stored (the same rule as every other
// secret here); the secret is shown once, when the token is made.
type FusionToken struct {
	ID    string
	OrgID string
	Name  string
	// Signals limits the token to some of "metrics", "logs", "traces". Empty means all three.
	Signals []string
	// Namespaces and Clusters limit what it can see to telemetry whose k8s.namespace.name / continuum.cluster.id is
	// one of these. Empty means no limit on that dimension. A signal that carries no such attribute is invisible to
	// a token that limits it: the limit fails closed.
	Namespaces []string
	Clusters   []string
	CreatedBy  string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastUsed   *time.Time
}

// errUnreadableScope marks a stored token whose scope cannot be parsed.
var errUnreadableScope = errors.New("a FUSION token's scope is unreadable")

const fusionTokenSchema = `
CREATE TABLE IF NOT EXISTS fusion_tokens (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  name TEXT NOT NULL,
  hash BLOB NOT NULL UNIQUE,
  signals TEXT NOT NULL DEFAULT '[]',
  namespaces TEXT NOT NULL DEFAULT '[]',
  clusters TEXT NOT NULL DEFAULT '[]',
  created_by TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  last_used INTEGER
);
CREATE INDEX IF NOT EXISTS fusion_tokens_org ON fusion_tokens(org_id);
`

const fusionTokenCols = `id, org_id, name, signals, namespaces, clusters, created_by, created_at, expires_at, last_used`

func stringsJSON(v []string) string {
	if v == nil {
		v = []string{}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// jsonStrings reads a stored scope list. An unreadable one is an error, never "no limit": a token whose scope cannot be
// read must not be honoured, since an empty list means unrestricted.
func jsonStrings(s string) ([]string, error) {
	var v []string
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil, err
	}
	return v, nil
}

func scanFusionToken(r scanner) (FusionToken, error) {
	var t FusionToken
	var signals, namespaces, clusters string
	var created, expires int64
	var used sql.NullInt64
	err := r.Scan(&t.ID, &t.OrgID, &t.Name, &signals, &namespaces, &clusters, &t.CreatedBy, &created, &expires, &used)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return FusionToken{}, ErrNotFound
		}
		return FusionToken{}, err
	}
	var e1, e2, e3 error
	t.Signals, e1 = jsonStrings(signals)
	t.Namespaces, e2 = jsonStrings(namespaces)
	t.Clusters, e3 = jsonStrings(clusters)
	if err := errors.Join(e1, e2, e3); err != nil {
		// Identified, but allowed nothing: no signals. The caller decides whether to refuse it (lookup) or list it so it
		// can be revoked (list).
		t.Signals, t.Namespaces, t.Clusters = nil, nil, nil
		return t, fmt.Errorf("%w: token %s: %v", errUnreadableScope, t.ID, err)
	}
	t.CreatedAt, t.ExpiresAt = fromMS(created), fromMS(expires)
	if used.Valid {
		lu := fromMS(used.Int64)
		t.LastUsed = &lu
	}
	return t, nil
}

// CreateFusionToken records a freshly minted token; id and hash are the caller's to generate.
func (s *SQLite) CreateFusionToken(ctx context.Context, t FusionToken, hash []byte) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO fusion_tokens(id, org_id, name, hash, signals, namespaces, clusters, created_by, created_at, expires_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.OrgID, t.Name, hash, stringsJSON(t.Signals), stringsJSON(t.Namespaces), stringsJSON(t.Clusters), t.CreatedBy, ms(t.CreatedAt), ms(t.ExpiresAt))
	return err
}

// LookupFusionToken resolves a secret's hash. ErrNotFound for an unknown one. Expiry is the caller's to check.
func (s *SQLite) LookupFusionToken(ctx context.Context, hash []byte) (FusionToken, error) {
	return scanFusionToken(s.db.QueryRowContext(ctx, `SELECT `+fusionTokenCols+` FROM fusion_tokens WHERE hash=?`, hash))
}

// ListFusionTokens lists an organisation's tokens, newest first. Never a secret or hash.
func (s *SQLite) ListFusionTokens(ctx context.Context, org string) ([]FusionToken, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+fusionTokenCols+` FROM fusion_tokens WHERE org_id=? ORDER BY created_at DESC, id`, org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FusionToken
	for rows.Next() {
		t, err := scanFusionToken(rows)
		if err != nil && !errors.Is(err, errUnreadableScope) {
			return nil, err
		}
		out = append(out, t) // an unreadable one is listed with no rights, so it can still be revoked
	}
	return out, rows.Err()
}

// TouchFusionToken records that a token was just used.
func (s *SQLite) TouchFusionToken(ctx context.Context, hash []byte, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE fusion_tokens SET last_used=? WHERE hash=?`, ms(now), hash)
	return err
}

// DeleteFusionToken removes one of an organisation's tokens; it stops working at once. ErrNotFound if id is not
// in org.
func (s *SQLite) DeleteFusionToken(ctx context.Context, org, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM fusion_tokens WHERE org_id=? AND id=?`, org, id)
	if err != nil {
		return err
	}
	return needFound(res)
}

// PurgeFusionTokens deletes tokens that expired before olderThan, so a lapsed token does not sit in the list for ever.
func (s *SQLite) PurgeFusionTokens(ctx context.Context, olderThan time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM fusion_tokens WHERE expires_at<?`, ms(olderThan))
	return err
}
