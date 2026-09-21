package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver: no cgo, so the server cross-compiles to a scratch image
)

const schema = `
CREATE TABLE IF NOT EXISTS tokens (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  hash BLOB NOT NULL UNIQUE,
  label TEXT NOT NULL,
  access_tier INTEGER NOT NULL,
  created_by TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  used_at INTEGER,
  used_by TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS agents (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  name TEXT NOT NULL,
  status TEXT NOT NULL,
  installed_tier INTEGER NOT NULL,
  tier_cap INTEGER NOT NULL DEFAULT 0,
  access_tier INTEGER NOT NULL DEFAULT 0,
  fingerprint TEXT NOT NULL,
  cluster_id TEXT NOT NULL DEFAULT '',
  csr BLOB NOT NULL,
  poll_secret_hash BLOB,
  version TEXT NOT NULL DEFAULT '',
  k8s_version TEXT NOT NULL DEFAULT '',
  token_id TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  approved_at INTEGER,
  approved_by TEXT NOT NULL DEFAULT '',
  revoked_at INTEGER,
  reason TEXT NOT NULL DEFAULT '',
  last_seen INTEGER,
  connecting_ip TEXT NOT NULL DEFAULT '',
  leaf BLOB,
  leaf_not_after INTEGER
);
CREATE INDEX IF NOT EXISTS agents_org ON agents(org_id);
-- At most one approved agent may represent a cluster.
CREATE UNIQUE INDEX IF NOT EXISTS agents_one_live_per_cluster ON agents(org_id, fingerprint) WHERE status = 'approved';
CREATE TABLE IF NOT EXISTS audit (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  at INTEGER NOT NULL,
  org_id TEXT NOT NULL,
  actor TEXT NOT NULL,
  action TEXT NOT NULL,
  target_kind TEXT NOT NULL,
  target_id TEXT NOT NULL,
  detail TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS users (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  username TEXT NOT NULL,
  role TEXT NOT NULL,
  password_hash TEXT NOT NULL,
  must_change INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  disabled_at INTEGER,
  last_login INTEGER
);
CREATE UNIQUE INDEX IF NOT EXISTS users_name ON users(org_id, lower(username));
CREATE UNIQUE INDEX IF NOT EXISTS users_name_global ON users(lower(username));
CREATE TABLE IF NOT EXISTS orgs (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  created_by TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS memberships (
  org_id TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  added_by TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (org_id, user_id)
);
CREATE INDEX IF NOT EXISTS memberships_user ON memberships(user_id);
CREATE TABLE IF NOT EXISTS invites (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  hash BLOB NOT NULL UNIQUE,
  role TEXT NOT NULL,
  label TEXT NOT NULL DEFAULT '',
  created_by TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  used_at INTEGER,
  used_by TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS sessions (
  hash BLOB PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  last_used INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  ip TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS sessions_user ON sessions(user_id);
CREATE TABLE IF NOT EXISTS workspace (
  org_id TEXT PRIMARY KEY,
  rev INTEGER NOT NULL,
  data BLOB NOT NULL,
  updated_at INTEGER NOT NULL,
  updated_by TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS history (
  org_id TEXT NOT NULL,
  at INTEGER NOT NULL,
  data BLOB NOT NULL,
  PRIMARY KEY (org_id, at)
);
CREATE TABLE IF NOT EXISTS events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  org_id TEXT NOT NULL,
  at INTEGER NOT NULL,
  kind TEXT NOT NULL,
  target_kind TEXT NOT NULL,
  target_id TEXT NOT NULL,
  name TEXT NOT NULL DEFAULT '',
  cluster_id TEXT NOT NULL DEFAULT '',
  cluster_name TEXT NOT NULL DEFAULT '',
  detail TEXT NOT NULL DEFAULT '',
  cause TEXT NOT NULL DEFAULT '',
  severity TEXT NOT NULL DEFAULT 'info'
);
CREATE INDEX IF NOT EXISTS events_at ON events(org_id, at);
CREATE TABLE IF NOT EXISTS settings (
  org_id TEXT PRIMARY KEY,
  data BLOB NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS snapshots (
  agent_id TEXT PRIMARY KEY,
  data BLOB NOT NULL,
  at INTEGER NOT NULL
);
`

type SQLite struct{ db *sql.DB }

// OpenSQLite opens (creating if needed) a database file.
func OpenSQLite(path string) (*SQLite, error) {
	// Create the file private before SQLite does: SQLite copies the main file's mode to its -wal and -shm
	// files, so all three are 0600 whatever the umask. An existing file is left as the operator has it (the
	// server checks and reports its mode at startup).
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err == nil {
		_ = f.Close()
	}
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite allows one writer; a single connection avoids "database is locked" surprises
	// and keeps the atomic token-consume path trivially serialisable.
	db.SetMaxOpenConns(1)
	// A database written by a newer server is refused before anything is created or migrated in it.
	if err := checkSchemaNotNewer(db); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrateTenancy(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("upgrading to organisations: %w", err)
	}
	if err := migrateAuditChain(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("upgrading the audit trail: %w", err)
	}
	if err := migrateEnrollment(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("upgrading enrollment: %w", err)
	}
	if err := migrateTwin(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("upgrading to the twin model: %w", err)
	}
	return &SQLite{db: db}, nil
}

func (s *SQLite) Close() error { return s.db.Close() }

func ms(t time.Time) int64     { return t.UnixMilli() }
func fromMS(v int64) time.Time { return time.UnixMilli(v).UTC() }
func fromNullMS(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := fromMS(v.Int64)
	return &t
}

// ---- tokens ----

func (s *SQLite) CreateToken(ctx context.Context, t Token, hash []byte) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO tokens(id, org_id, hash, label, access_tier, created_by, created_at, expires_at, expected_fp) VALUES(?,?,?,?,?,?,?,?,?)`,
		t.ID, t.OrgID, hash, t.Label, t.AccessTier, t.CreatedBy, ms(t.CreatedAt), ms(t.ExpiresAt), t.ExpectedFingerprint)
	return err
}

func (s *SQLite) ListTokens(ctx context.Context, org string) ([]Token, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, org_id, label, access_tier, created_by, created_at, expires_at, used_at, used_by, expected_fp FROM tokens WHERE org_id=? ORDER BY created_at DESC`, org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Token
	for rows.Next() {
		var t Token
		var c, e int64
		var u sql.NullInt64
		if err := rows.Scan(&t.ID, &t.OrgID, &t.Label, &t.AccessTier, &t.CreatedBy, &c, &e, &u, &t.UsedBy, &t.ExpectedFingerprint); err != nil {
			return nil, err
		}
		t.CreatedAt, t.ExpiresAt, t.UsedAt = fromMS(c), fromMS(e), fromNullMS(u)
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *SQLite) DeleteToken(ctx context.Context, org, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM tokens WHERE id=? AND org_id=? AND used_at IS NULL`, id, org)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- agents ----

// EnrollAgent consumes the token with a single conditional UPDATE, so two agents racing
// with the same token cannot both win, then inserts the pending agent in the same transaction.
// A token that is bound to another cluster is looked at first and left unspent.
func (s *SQLite) EnrollAgent(ctx context.Context, tokenHash []byte, a Agent, now time.Time) (Token, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Token{}, err
	}
	defer tx.Rollback()
	var t Token
	var c, e int64
	err = tx.QueryRowContext(ctx,
		`SELECT id, org_id, label, access_tier, created_by, created_at, expires_at, expected_fp FROM tokens WHERE hash=? AND used_at IS NULL AND expires_at>?`, tokenHash, ms(now)).
		Scan(&t.ID, &t.OrgID, &t.Label, &t.AccessTier, &t.CreatedBy, &c, &e, &t.ExpectedFingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return Token{}, ErrTokenInvalid
	}
	if err != nil {
		return Token{}, err
	}
	t.CreatedAt, t.ExpiresAt = fromMS(c), fromMS(e)
	if t.ExpectedFingerprint != "" && t.ExpectedFingerprint != a.Fingerprint {
		return t, ErrWrongCluster
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE tokens SET used_at=?, used_by=? WHERE hash=? AND used_at IS NULL AND expires_at>?`,
		ms(now), a.ID, tokenHash, ms(now))
	if err != nil {
		return Token{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return Token{}, ErrTokenInvalid
	}
	t.UsedAt, t.UsedBy = &now, a.ID
	a.OrgID, a.Name, a.TokenID, a.Status, a.CreatedAt, a.TierCap = t.OrgID, t.Label, t.ID, StatusPending, now, t.AccessTier
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO agents(id, org_id, name, status, installed_tier, tier_cap, fingerprint, csr, poll_secret_hash, version, k8s_version, token_id, created_at, connecting_ip, approval_hash)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.OrgID, a.Name, string(a.Status), a.InstalledTier, a.TierCap, a.Fingerprint, a.CSR, a.PollSecretHash, a.Version, a.K8sVersion, a.TokenID, ms(now), a.ConnectingIP, nullBytes(a.ApprovalHash)); err != nil {
		return Token{}, err
	}
	return t, tx.Commit()
}

func nullBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

func (s *SQLite) AgentByToken(ctx context.Context, tokenHash []byte) (Agent, error) {
	return scanAgent(s.db.QueryRowContext(ctx,
		`SELECT `+agentCols+` FROM agents WHERE id=(SELECT used_by FROM tokens WHERE hash=? AND used_at IS NOT NULL)`, tokenHash))
}

func (s *SQLite) ResumeEnrollment(ctx context.Context, id string, r Resume, reopen bool, now time.Time) error {
	var res sql.Result
	var err error
	if reopen {
		res, err = s.db.ExecContext(ctx,
			`UPDATE agents SET status='pending', poll_secret_hash=?, approval_hash=?, approval_attempts=0, csr=?, created_at=?, reason='', revoked_at=NULL,
			   version=?, k8s_version=?, connecting_ip=? WHERE id=? AND status='expired'`,
			r.PollSecretHash, nullBytes(r.ApprovalHash), r.CSR, ms(now), r.Version, r.K8sVersion, r.IP, id)
	} else {
		res, err = s.db.ExecContext(ctx,
			`UPDATE agents SET poll_secret_hash=?, version=?, k8s_version=?, connecting_ip=? WHERE id=?
			   AND (status='pending' OR (status='approved' AND poll_secret_hash IS NOT NULL))`,
			r.PollSecretHash, r.Version, r.K8sVersion, r.IP, id)
	}
	if err != nil {
		return err
	}
	return needOne(res)
}

func (s *SQLite) CountApprovalAttempt(ctx context.Context, id string) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE agents SET approval_attempts=approval_attempts+1 WHERE id=? AND status='pending'`, id)
	if err != nil {
		return 0, err
	}
	if err := needOne(res); err != nil {
		return 0, err
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT approval_attempts FROM agents WHERE id=?`, id).Scan(&n); err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

func (s *SQLite) ExpirePending(ctx context.Context, org string, cutoff time.Time) ([]Agent, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT `+agentCols+` FROM agents WHERE org_id=? AND status='pending' AND created_at<?`, org, ms(cutoff))
	if err != nil {
		return nil, err
	}
	var out []Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if _, err := tx.ExecContext(ctx, `UPDATE agents SET status='expired', reason='nobody approved it in time' WHERE id=? AND status='pending'`, out[i].ID); err != nil {
			return nil, err
		}
		out[i].Status = StatusExpired
	}
	return out, tx.Commit()
}

func (s *SQLite) PurgeExpired(ctx context.Context, org string, cutoff time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM agents WHERE org_id=? AND status='expired' AND created_at<?`, org, ms(cutoff))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (s *SQLite) SetClockSkew(ctx context.Context, id string, skewMs int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET clock_skew_ms=? WHERE id=?`, skewMs, id)
	return err
}

func (s *SQLite) SetAccessTier(ctx context.Context, id string, tier int) error {
	res, err := s.db.ExecContext(ctx, `UPDATE agents SET access_tier=? WHERE id=? AND status='approved'`, tier, id)
	if err != nil {
		return err
	}
	return needOne(res)
}

func (s *SQLite) SetInstalledTier(ctx context.Context, id string, tier int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET installed_tier=? WHERE id=?`, tier, id)
	return err
}

const agentCols = `id, org_id, name, status, installed_tier, tier_cap, access_tier, fingerprint, cluster_id, csr, poll_secret_hash, version, k8s_version,
 token_id, created_at, approved_at, approved_by, revoked_at, reason, last_seen, connecting_ip, leaf, leaf_not_after, approval_hash, approval_attempts, clock_skew_ms`

type scanner interface{ Scan(...any) error }

func scanAgent(r scanner) (Agent, error) {
	var a Agent
	var st string
	var created int64
	var approved, revoked, seen, leafExp sql.NullInt64
	err := r.Scan(&a.ID, &a.OrgID, &a.Name, &st, &a.InstalledTier, &a.TierCap, &a.AccessTier, &a.Fingerprint, &a.ClusterID, &a.CSR, &a.PollSecretHash,
		&a.Version, &a.K8sVersion, &a.TokenID, &created, &approved, &a.ApprovedBy, &revoked, &a.Reason, &seen, &a.ConnectingIP, &a.LeafDER, &leafExp, &a.ApprovalHash, &a.ApprovalAttempts, &a.ClockSkewMs)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Agent{}, ErrNotFound
		}
		return Agent{}, err
	}
	a.Status = AgentStatus(st)
	a.CreatedAt = fromMS(created)
	a.ApprovedAt, a.RevokedAt, a.LastSeen, a.LeafNotAfter = fromNullMS(approved), fromNullMS(revoked), fromNullMS(seen), fromNullMS(leafExp)
	return a, nil
}

func (s *SQLite) GetAgent(ctx context.Context, id string) (Agent, error) {
	return scanAgent(s.db.QueryRowContext(ctx, `SELECT `+agentCols+` FROM agents WHERE id=?`, id))
}

func (s *SQLite) ListAgents(ctx context.Context, org string) ([]Agent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+agentCols+` FROM agents WHERE org_id=? ORDER BY created_at`, org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *SQLite) ApproveAgent(ctx context.Context, id string, tier int, approver, clusterID string, leaf []byte, notAfter, now time.Time) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE agents SET status='approved', access_tier=?, approved_by=?, approved_at=?, cluster_id=?, leaf=?, leaf_not_after=?, reason=''
		 WHERE id=? AND status='pending'`,
		tier, approver, ms(now), clusterID, leaf, ms(notAfter), id)
	if err != nil {
		// The partial unique index turns "second live agent for this cluster" into a constraint error.
		if isUnique(err) {
			return ErrClusterEnrolled
		}
		return err
	}
	return needOne(res)
}

// RejectAgent and RevokeAgent keep the poll secret's hash on purpose: an agent that is still waiting asks
// with it and is told REJECTED with the reason, instead of only "unknown agent". The secret opens nothing else.
func (s *SQLite) RejectAgent(ctx context.Context, id, reason string, now time.Time) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE agents SET status='rejected', reason=?, revoked_at=? WHERE id=? AND status='pending'`, reason, ms(now), id)
	if err != nil {
		return err
	}
	return needOne(res)
}

func (s *SQLite) RevokeAgent(ctx context.Context, id, reason string, now time.Time) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE agents SET status='revoked', reason=?, revoked_at=?, leaf=NULL WHERE id=? AND status IN ('approved','pending')`,
		reason, ms(now), id)
	if err != nil {
		return err
	}
	return needOne(res)
}

func (s *SQLite) SetLeaf(ctx context.Context, id string, leaf []byte, notAfter time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE agents SET leaf=?, leaf_not_after=? WHERE id=? AND status='approved'`, leaf, ms(notAfter), id)
	if err != nil {
		return err
	}
	return needOne(res)
}

func (s *SQLite) ClearPollSecret(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET poll_secret_hash=NULL WHERE id=?`, id)
	return err
}

func (s *SQLite) Touch(ctx context.Context, id, ip, version, k8sVersion string, now time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE agents SET last_seen=?, connecting_ip=COALESCE(NULLIF(?,''), connecting_ip), version=COALESCE(NULLIF(?,''), version), k8s_version=COALESCE(NULLIF(?,''), k8s_version) WHERE id=?`,
		ms(now), ip, version, k8sVersion, id)
	return err
}

func needOne(res sql.Result) error {
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrBadState
	}
	return nil
}

func isUnique(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "UNIQUE constraint failed") || strings.Contains(err.Error(), "constraint failed: UNIQUE"))
}

// ---- audit ----

func (s *SQLite) AuditSince(ctx context.Context, afterID int64, limit int) ([]AuditEvent, error) {
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, at, org_id, actor, action, target_kind, target_id, detail FROM audit WHERE id>? ORDER BY id LIMIT ?`, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEvent
	for rows.Next() {
		var e AuditEvent
		var at int64
		if err := rows.Scan(&e.ID, &at, &e.OrgID, &e.Actor, &e.Action, &e.TargetKind, &e.TargetID, &e.Detail); err != nil {
			return nil, err
		}
		e.At = fromMS(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *SQLite) ListAudit(ctx context.Context, org string, limit int) ([]AuditEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, at, org_id, actor, action, target_kind, target_id, detail FROM audit WHERE org_id=? ORDER BY id DESC LIMIT ?`, org, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEvent
	for rows.Next() {
		var e AuditEvent
		var at int64
		if err := rows.Scan(&e.ID, &at, &e.OrgID, &e.Actor, &e.Action, &e.TargetKind, &e.TargetID, &e.Detail); err != nil {
			return nil, err
		}
		e.At = fromMS(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- snapshots ----

func (s *SQLite) SaveSnapshot(ctx context.Context, agentID string, data []byte, now time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO snapshots(agent_id, data, at) VALUES(?,?,?) ON CONFLICT(agent_id) DO UPDATE SET data=excluded.data, at=excluded.at`,
		agentID, data, ms(now))
	return err
}

func (s *SQLite) LoadSnapshot(ctx context.Context, agentID string) ([]byte, time.Time, error) {
	var d []byte
	var at int64
	err := s.db.QueryRowContext(ctx, `SELECT data, at FROM snapshots WHERE agent_id=?`, agentID).Scan(&d, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, time.Time{}, ErrNotFound
	}
	return d, fromMS(at), err
}

// migrateTenancy turns a single-organisation database into a multi-tenant one. Before it, every user
// belonged to exactly one organisation and carried a role of "admin" or "viewer"; now people are
// global and hold a membership per organisation. The earliest administrator of each organisation
// becomes its owner (someone must own it), the other administrators stay administrators and viewers
// stay viewers. It runs once (PRAGMA user_version) and changes nothing on a fresh database.
func migrateTenancy(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v >= 1 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Every organisation id that already has data.
	if _, err := tx.Exec(`INSERT OR IGNORE INTO orgs(id, name, created_at, created_by)
		SELECT org_id, org_id, MIN(t), '' FROM (
		  SELECT org_id, created_at AS t FROM users UNION ALL SELECT org_id, created_at FROM agents
		  UNION ALL SELECT org_id, created_at FROM tokens UNION ALL SELECT org_id, updated_at FROM workspace
		) WHERE org_id <> '' GROUP BY org_id`); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO memberships(org_id, user_id, role, created_at, added_by)
		SELECT org_id, id, CASE WHEN role='admin' THEN 'admin' ELSE 'viewer' END, created_at, '' FROM users WHERE org_id <> ''`); err != nil {
		return err
	}
	// The earliest administrator of an organisation without an owner becomes the owner.
	if _, err := tx.Exec(`UPDATE memberships SET role='owner' WHERE rowid IN (
		SELECT m.rowid FROM memberships m JOIN users u ON u.id=m.user_id
		WHERE m.role='admin' AND NOT EXISTS (SELECT 1 FROM memberships o WHERE o.org_id=m.org_id AND o.role='owner')
		AND u.created_at = (SELECT MIN(u2.created_at) FROM memberships m2 JOIN users u2 ON u2.id=m2.user_id WHERE m2.org_id=m.org_id AND m2.role='admin')
		GROUP BY m.org_id)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`PRAGMA user_version = 1`); err != nil {
		return err
	}
	return tx.Commit()
}
