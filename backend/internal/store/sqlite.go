package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"continuum/internal/sealing"

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
  totp_secret TEXT NOT NULL DEFAULT '',
  totp_enabled_at INTEGER,
  totp_recovery TEXT NOT NULL DEFAULT '[]',
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
CREATE TABLE IF NOT EXISTS api_tokens (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  hash BLOB NOT NULL UNIQUE,
  name TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  last_used INTEGER
);
CREATE INDEX IF NOT EXISTS api_tokens_user ON api_tokens(user_id);
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
-- One row only (id is always 1): the server's mail configuration is not scoped to any organisation,
-- unlike settings above which is keyed per org_id.
CREATE TABLE IF NOT EXISTS mail_config (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  data BLOB NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS snapshots (
  agent_id TEXT PRIMARY KEY,
  data BLOB NOT NULL,
  at INTEGER NOT NULL
);
-- A regional operator never enrolls (see store.Operator's own comment), so unlike agents there is no
-- separate pending/approved dance here - status is only active or revoked.
CREATE TABLE IF NOT EXISTS operators (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  name TEXT NOT NULL,
  site_id TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  source_cluster_ids TEXT NOT NULL DEFAULT '[]',
  destination TEXT NOT NULL DEFAULT '{}',
  receiver_auth_token_hash BLOB NOT NULL,
  created_by TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  revoked_at INTEGER,
  reason TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS operators_org ON operators(org_id);
-- A telemetry intent is a local operator's grant: which signals (and from which namespaces) one agent's
-- own bundled OTel-collector telemetry extractors are told to collect, and where to export them - see
-- store.TelemetryIntent's own comment. Unlike operators above it belongs to exactly one agent; Core
-- enforces at most one active intent per agent (see Core.CreateTelemetryIntent), not this table.
CREATE TABLE IF NOT EXISTS telemetry_intents (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  name TEXT NOT NULL,
  status TEXT NOT NULL,
  namespaces TEXT NOT NULL DEFAULT '[]',
  exclude TEXT NOT NULL DEFAULT '[]',
  signals TEXT NOT NULL DEFAULT '[]',
  destination TEXT NOT NULL DEFAULT '{}',
  created_by TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  revoked_at INTEGER,
  reason TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS telemetry_intents_org ON telemetry_intents(org_id);
CREATE INDEX IF NOT EXISTS telemetry_intents_agent ON telemetry_intents(agent_id);
-- One row per service a decider proposed moving, in one run - see store.DecisionLog's own doc comment
-- for why this is an append-only log beside audit/events rather than a hash chain or a graph entity.
CREATE TABLE IF NOT EXISTS decisions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  org_id TEXT NOT NULL,
  at INTEGER NOT NULL,
  recorded_by TEXT NOT NULL DEFAULT '',
  decider_id TEXT NOT NULL,
  decider_name TEXT NOT NULL,
  decider_kind TEXT NOT NULL,
  schema_version INTEGER NOT NULL DEFAULT 0,
  cluster_count INTEGER NOT NULL DEFAULT 0,
  service_count INTEGER NOT NULL DEFAULT 0,
  policy TEXT NOT NULL DEFAULT '{}',
  service_id TEXT NOT NULL,
  service_name TEXT NOT NULL,
  from_cluster TEXT NOT NULL,
  to_cluster TEXT NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  benefit REAL NOT NULL DEFAULT 0,
  confidence TEXT NOT NULL DEFAULT '',
  verdict TEXT NOT NULL DEFAULT '',
  before_cost REAL NOT NULL DEFAULT 0,
  after_cost REAL NOT NULL DEFAULT 0,
  migration_cost REAL NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS decisions_org ON decisions(org_id, at DESC);
-- A change an advisor proposed to one agent's access, and what became of it - see store.Proposal. A new table, not a
-- migration (like telemetry_intents), so an older binary opens a database that has it and a rollback needs no repair.
CREATE TABLE IF NOT EXISTS proposals (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  advisor TEXT NOT NULL,
  model_version INTEGER NOT NULL DEFAULT 0,
  change TEXT NOT NULL,
  evidence TEXT NOT NULL DEFAULT '[]',
  confidence TEXT NOT NULL DEFAULT 'unknown',
  blast TEXT NOT NULL DEFAULT '{}',
  predicted TEXT NOT NULL DEFAULT '{}',
  widens INTEGER NOT NULL DEFAULT 0,
  mode TEXT NOT NULL DEFAULT 'advise',
  status TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  decided_by TEXT NOT NULL DEFAULT '',
  decided_at INTEGER,
  note TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS proposals_org ON proposals(org_id, created_at DESC);
CREATE INDEX IF NOT EXISTS proposals_agent ON proposals(org_id, agent_id, status);
-- What an advisor predicted for a proposal against what was observed once the change was in force: at most one row each.
CREATE TABLE IF NOT EXISTS proposal_outcomes (
  proposal_id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  result TEXT NOT NULL,
  predicted TEXT NOT NULL DEFAULT '{}',
  observed TEXT NOT NULL DEFAULT '{}',
  in_force_at INTEGER,
  recorded_at INTEGER NOT NULL
);
`

type SQLite struct {
	db *sql.DB
	// sealer seals the secrets this database must be able to read back (see UseSealer). Nil until it is given one: a value is then stored
	// as it comes.
	sealer *sealing.Sealer
}

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
	if _, err := db.Exec(fusionTokenSchema); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(linkCredentialSchema); err != nil {
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
	if err := migrateTOTP(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("upgrading to two-factor accounts: %w", err)
	}
	if err := migrateEmail(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("upgrading to email accounts: %w", err)
	}
	if err := migrateWebAuthn(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("upgrading to passkey accounts: %w", err)
	}
	if err := migrateTelemetry(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("upgrading to operator accepted modalities: %w", err)
	}
	if err := migrateHeartbeat(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("upgrading to operator heartbeats: %w", err)
	}
	if err := migrateReceiverAuth(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("upgrading to operator receiver auth modes: %w", err)
	}
	if err := migrateOperatorCA(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("upgrading to per-operator CAs: %w", err)
	}
	if err := migrateOperatorLabels(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("upgrading to operator labels: %w", err)
	}
	if err := migrateTelemetryRoutes(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("upgrading to per-signal telemetry destinations: %w", err)
	}
	if err := migrateOperatorAddress(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("upgrading to operator addresses: %w", err)
	}
	if err := migrateOperatorExposure(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("upgrading to operator exposure: %w", err)
	}
	if err := migrateOperatorCerts(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("upgrading to operator certificate expiry: %w", err)
	}
	if err := migrateOperatorLedger(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("upgrading to the operator certificate ledger: %w", err)
	}
	if err := migrateAuditIndex(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("upgrading to the audit index: %w", err)
	}
	if err := migrateGatewayTokensGone(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("upgrading to the removal of the gateway tokens: %w", err)
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

func (s *SQLite) CreateToken(ctx context.Context, t Token, hash []byte, opts ...WriteOption) error {
	return s.write(ctx, opts, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO tokens(id, org_id, hash, label, access_tier, created_by, created_at, expires_at, expected_fp) VALUES(?,?,?,?,?,?,?,?,?)`,
			t.ID, t.OrgID, hash, t.Label, t.AccessTier, t.CreatedBy, ms(t.CreatedAt), ms(t.ExpiresAt), t.ExpectedFingerprint)
		return err
	})
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

func (s *SQLite) DeleteToken(ctx context.Context, org, id string, opts ...WriteOption) error {
	return s.write(ctx, opts, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM tokens WHERE id=? AND org_id=? AND used_at IS NULL`, id, org)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
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

func (s *SQLite) ResumeEnrollment(ctx context.Context, org, id string, r Resume, reopen bool, now time.Time) error {
	var res sql.Result
	var err error
	if reopen {
		res, err = s.db.ExecContext(ctx,
			`UPDATE agents SET status='pending', poll_secret_hash=?, approval_hash=?, approval_attempts=0, csr=?, created_at=?, reason='', revoked_at=NULL,
			   version=?, k8s_version=?, connecting_ip=? WHERE id=? AND org_id=? AND status='expired'`,
			r.PollSecretHash, nullBytes(r.ApprovalHash), r.CSR, ms(now), r.Version, r.K8sVersion, r.IP, id, org)
	} else {
		res, err = s.db.ExecContext(ctx,
			`UPDATE agents SET poll_secret_hash=?, version=?, k8s_version=?, connecting_ip=? WHERE id=? AND org_id=?
			   AND (status='pending' OR (status='approved' AND poll_secret_hash IS NOT NULL))`,
			r.PollSecretHash, r.Version, r.K8sVersion, r.IP, id, org)
	}
	if err != nil {
		return err
	}
	return needOneAgent(ctx, s.db, res, org, id)
}

func (s *SQLite) CountApprovalAttempt(ctx context.Context, org, id string) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE agents SET approval_attempts=approval_attempts+1 WHERE id=? AND org_id=? AND status='pending'`, id, org)
	if err != nil {
		return 0, err
	}
	if err := needOneIn(ctx, tx, res, "agents", org, id); err != nil {
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

func (s *SQLite) SetClockSkew(ctx context.Context, org, id string, skewMs int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET clock_skew_ms=? WHERE id=? AND org_id=?`, skewMs, id, org)
	return err
}

func (s *SQLite) SetAccessTier(ctx context.Context, org, id string, tier int, opts ...WriteOption) error {
	return s.write(ctx, opts, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE agents SET access_tier=? WHERE id=? AND org_id=? AND status='approved'`, tier, id, org)
		if err != nil {
			return err
		}
		return needOneIn(ctx, tx, res, "agents", org, id)
	})
}

func (s *SQLite) SetInstalledTier(ctx context.Context, org, id string, tier int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET installed_tier=? WHERE id=? AND org_id=?`, tier, id, org)
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

func (s *SQLite) GetAgent(ctx context.Context, org, id string) (Agent, error) {
	return scanAgent(s.db.QueryRowContext(ctx, `SELECT `+agentCols+` FROM agents WHERE id=? AND org_id=?`, id, org))
}

func (s *SQLite) FindAgent(ctx context.Context, id string) (Agent, error) {
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

func (s *SQLite) ApproveAgent(ctx context.Context, org, id string, tier int, approver, clusterID string, leaf []byte, notAfter, now time.Time, opts ...WriteOption) error {
	return s.write(ctx, opts, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE agents SET status='approved', access_tier=?, approved_by=?, approved_at=?, cluster_id=?, leaf=?, leaf_not_after=?, reason=''
		 WHERE id=? AND org_id=? AND status='pending'`,
			tier, approver, ms(now), clusterID, leaf, ms(notAfter), id, org)
		if err != nil {
			// The partial unique index turns "second live agent for this cluster" into a constraint error.
			if isUnique(err) {
				return ErrClusterEnrolled
			}
			return err
		}
		return needOneIn(ctx, tx, res, "agents", org, id)
	})
}

// RejectAgent and RevokeAgent keep the poll secret's hash on purpose: an agent that is still waiting asks
// with it and is told REJECTED with the reason, instead of only "unknown agent". The secret opens nothing else.
func (s *SQLite) RejectAgent(ctx context.Context, org, id, reason string, now time.Time, opts ...WriteOption) error {
	return s.write(ctx, opts, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE agents SET status='rejected', reason=?, revoked_at=? WHERE id=? AND org_id=? AND status='pending'`, reason, ms(now), id, org)
		if err != nil {
			return err
		}
		return needOneIn(ctx, tx, res, "agents", org, id)
	})
}

func (s *SQLite) RevokeAgent(ctx context.Context, org, id, reason string, now time.Time, opts ...WriteOption) error {
	return s.write(ctx, opts, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE agents SET status='revoked', reason=?, revoked_at=?, leaf=NULL WHERE id=? AND org_id=? AND status IN ('approved','pending')`,
			reason, ms(now), id, org)
		if err != nil {
			return err
		}
		return needOneIn(ctx, tx, res, "agents", org, id)
	})
}

func (s *SQLite) SetLeaf(ctx context.Context, org, id string, leaf []byte, notAfter time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE agents SET leaf=?, leaf_not_after=? WHERE id=? AND org_id=? AND status='approved'`, leaf, ms(notAfter), id, org)
	if err != nil {
		return err
	}
	return needOneAgent(ctx, s.db, res, org, id)
}

func (s *SQLite) ClearPollSecret(ctx context.Context, org, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET poll_secret_hash=NULL WHERE id=? AND org_id=?`, id, org)
	return err
}

func (s *SQLite) Touch(ctx context.Context, org, id, ip, version, k8sVersion string, now time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE agents SET last_seen=?, connecting_ip=COALESCE(NULLIF(?,''), connecting_ip), version=COALESCE(NULLIF(?,''), version), k8s_version=COALESCE(NULLIF(?,''), k8s_version) WHERE id=? AND org_id=?`,
		ms(now), ip, version, k8sVersion, id, org)
	return err
}

func needOne(res sql.Result) error {
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrBadState
	}
	return nil
}

// needOneIn is needOne for a change addressed by an id inside an organisation. When nothing changed it tells "there is no such
// row in this organisation" (ErrNotFound, which is also what another organisation's row looks like) from "the row is there and
// not in a state that allows this" (ErrBadState). table is one of the store's own table names, never input.
func needOneIn(ctx context.Context, tx *sql.Tx, res sql.Result, table, org, id string) error {
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	var one int
	switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM `+table+` WHERE id=? AND org_id=?`, id, org).Scan(&one); {
	case errors.Is(err, sql.ErrNoRows):
		return ErrNotFound
	case err != nil:
		return err
	}
	return ErrBadState
}

// needOneAgent is needOneIn for a change made outside a transaction (a statement of its own).
func needOneAgent(ctx context.Context, db *sql.DB, res sql.Result, org, id string) error {
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	var one int
	switch err := db.QueryRowContext(ctx, `SELECT 1 FROM agents WHERE id=? AND org_id=?`, id, org).Scan(&one); {
	case errors.Is(err, sql.ErrNoRows):
		return ErrNotFound
	case err != nil:
		return err
	}
	return ErrBadState
}

func isUnique(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "UNIQUE constraint failed") || strings.Contains(err.Error(), "constraint failed: UNIQUE"))
}

// ---- regional operators ----

// sourceClusterIDsJSON encodes/decodes Operator.SourceClusterIDs as a JSON array, the same convention
// totpRecoveryJSON uses for User.TOTPRecovery: a list that is always read and written whole.
func sourceClusterIDsJSON(ids []string) string {
	if len(ids) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(ids)
	return string(b)
}
func parseSourceClusterIDs(s string) []string {
	var ids []string
	_ = json.Unmarshal([]byte(s), &ids)
	return ids
}

// destinationJSON encodes/decodes Operator.Destination as a JSON object - a small, nested struct with
// nothing in it ever queried across rows, so a single JSON column is simpler than flattening it into
// seven more table columns.
func destinationJSON(d Destination) string {
	b, _ := json.Marshal(d)
	return string(b)
}
func parseDestination(s string) Destination {
	var d Destination
	_ = json.Unmarshal([]byte(s), &d)
	return d
}

// acceptedModalitiesJSON/parseAcceptedModalities encode/decode Operator.AcceptedModalities the same way
// sourceClusterIDsJSON does for SourceClusterIDs; a blank or unparsable column (a row from before this
// column existed) reads back as no restriction, per AcceptedModalities's own doc comment. A column that cannot
// be parsed is not that case: it is damage, and it reads back as ModalityUnreadable, which accepts nothing.
func acceptedModalitiesJSON(m []Modality) string {
	if len(m) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(m)
	return string(b)
}
func parseAcceptedModalities(s string) []Modality {
	var m []Modality
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return []Modality{ModalityUnreadable}
	}
	return m
}

// operatorLabelsJSON/parseOperatorLabels: the same whole-list JSON column convention as the two above.
func operatorLabelsJSON(l []OperatorLabel) string {
	if len(l) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(l)
	return string(b)
}
func parseOperatorLabels(s string) []OperatorLabel {
	var l []OperatorLabel
	_ = json.Unmarshal([]byte(s), &l)
	return l
}

const operatorCols = `id, org_id, name, site_id, status, source_cluster_ids, destination, accepted_modalities, receiver_auth_token_hash, created_by, created_at, revoked_at, reason, heartbeat_hash, heartbeat_enabled_at, last_seen_at, receiver_auth, client_ca_cert, labels, address, exposure, receiver_not_after, client_not_after, cert_alert_level`

func scanOperator(r scanner) (Operator, error) {
	var op Operator
	var st, sourceIDs, dest, modalities, recvAuth, labels string
	var created int64
	var revoked, hbEnabled, lastSeen, recvNotAfter, clientNotAfter sql.NullInt64
	err := r.Scan(&op.ID, &op.OrgID, &op.Name, &op.SiteID, &st, &sourceIDs, &dest, &modalities, &op.ReceiverAuthTokenHash, &op.CreatedBy, &created, &revoked, &op.Reason, &op.HeartbeatHash, &hbEnabled, &lastSeen, &recvAuth, &op.ClientCACertPEM, &labels, &op.Address, &op.Exposure, &recvNotAfter, &clientNotAfter, &op.CertAlertLevel)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Operator{}, ErrNotFound
		}
		return Operator{}, err
	}
	op.Status = OperatorStatus(st)
	op.SourceClusterIDs = parseSourceClusterIDs(sourceIDs)
	op.Destination = parseDestination(dest)
	op.AcceptedModalities = parseAcceptedModalities(modalities)
	op.Labels = parseOperatorLabels(labels)
	op.CreatedAt = fromMS(created)
	op.RevokedAt = fromNullMS(revoked)
	op.ReceiverAuth = ReceiverAuth(recvAuth)
	if op.ReceiverAuth == "" {
		op.ReceiverAuth = ReceiverAuthBearer
	}
	if len(op.ClientCACertPEM) == 0 {
		op.ClientCACertPEM = nil
	}
	op.HeartbeatEnabledAt = fromNullMS(hbEnabled)
	op.LastSeenAt = fromNullMS(lastSeen)
	op.ReceiverNotAfter, op.ClientNotAfter = fromNullMS(recvNotAfter), fromNullMS(clientNotAfter)
	if len(op.HeartbeatHash) == 0 {
		op.HeartbeatHash = nil
	}
	return op, nil
}

// nullMS is a nullable time column's value: NULL for a nil time.
func nullMS(t *time.Time) any {
	if t == nil {
		return nil
	}
	return ms(*t)
}

// CreateOperator inserts the operator with its receiver token hash; op.HeartbeatHash/HeartbeatEnabledAt, when
// set, are stored with it (an operator created with its heartbeat already on), and are NULL otherwise.
func (s *SQLite) CreateOperator(ctx context.Context, op Operator, tokenHash []byte, opts ...WriteOption) error {
	var hb any
	if len(op.HeartbeatHash) > 0 {
		hb = op.HeartbeatHash
	}
	recv := op.ReceiverAuth
	if recv == "" {
		recv = ReceiverAuthBearer
	}
	if tokenHash == nil {
		tokenHash = []byte{} // the column is NOT NULL; an mTLS operator has no bearer token, so its hash is empty
	}
	var caCert, caKey any // NULL for an operator without a CA of its own
	if len(op.ClientCACertPEM) > 0 {
		caCert = op.ClientCACertPEM
	}
	if len(op.ClientCAKeyPEM) > 0 {
		caKey = op.ClientCAKeyPEM
	}
	return s.write(ctx, opts, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO operators(id, org_id, name, site_id, status, source_cluster_ids, destination, accepted_modalities, receiver_auth_token_hash, created_by, created_at, reason, heartbeat_hash, heartbeat_enabled_at, receiver_auth, client_ca_cert, client_ca_key, labels, address, exposure, receiver_not_after, client_not_after)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			op.ID, op.OrgID, op.Name, op.SiteID, string(op.Status), sourceClusterIDsJSON(op.SourceClusterIDs), destinationJSON(op.Destination), acceptedModalitiesJSON(op.AcceptedModalities), tokenHash, op.CreatedBy, ms(op.CreatedAt), op.Reason, hb, nullMS(op.HeartbeatEnabledAt), string(recv), caCert, caKey, operatorLabelsJSON(op.Labels), op.Address, op.Exposure, nullMS(op.ReceiverNotAfter), nullMS(op.ClientNotAfter))
		if isUnique(err) {
			return fmt.Errorf("%w: an operator has the id %q", ErrExists, op.ID)
		}
		return err
	})
}

// GetOperatorClientCAKey returns the sealed private key of an operator's own CA (the PEM CreateOperator was
// given in Operator.ClientCAKeyPEM). It is deliberately not part of Operator as read back: the key is
// fetched only by the one caller that must sign with it, never listed. ErrNotFound when the operator does not
// exist or has no key - a legacy or bearer operator, or one that has been revoked (RevokeOperator erases it).
func (s *SQLite) GetOperatorClientCAKey(ctx context.Context, org, id string) ([]byte, error) {
	var key []byte
	err := s.db.QueryRowContext(ctx, `SELECT client_ca_key FROM operators WHERE id=? AND org_id=?`, id, org).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && len(key) == 0) {
		return nil, ErrNotFound
	}
	return key, err
}

func (s *SQLite) SetOperatorHeartbeat(ctx context.Context, org, id string, hash []byte, now time.Time, opts ...WriteOption) error {
	return s.write(ctx, opts, func(tx *sql.Tx) error { return setOperatorHeartbeatTx(ctx, tx, org, id, hash, now) })
}

func setOperatorHeartbeatTx(ctx context.Context, tx *sql.Tx, org, id string, hash []byte, now time.Time) error {
	res, err := tx.ExecContext(ctx,
		`UPDATE operators SET heartbeat_hash=?, heartbeat_enabled_at=? WHERE id=? AND org_id=? AND status='active'`, hash, ms(now), id, org)
	if err != nil {
		return err
	}
	return needOneIn(ctx, tx, res, "operators", org, id)
}

func (s *SQLite) GetOperatorByHeartbeatHash(ctx context.Context, hash []byte) (Operator, error) {
	if len(hash) == 0 {
		return Operator{}, ErrNotFound
	}
	return scanOperator(s.db.QueryRowContext(ctx, `SELECT `+operatorCols+` FROM operators WHERE heartbeat_hash=?`, hash))
}

func (s *SQLite) TouchOperatorSeen(ctx context.Context, org, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE operators SET last_seen_at=? WHERE id=? AND org_id=?`, ms(at), id, org)
	return err
}

func (s *SQLite) GetOperator(ctx context.Context, org, id string) (Operator, error) {
	return scanOperator(s.db.QueryRowContext(ctx, `SELECT `+operatorCols+` FROM operators WHERE id=? AND org_id=?`, id, org))
}

func (s *SQLite) ListOperators(ctx context.Context, org string) ([]Operator, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+operatorCols+` FROM operators WHERE org_id=? ORDER BY created_at`, org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Operator
	for rows.Next() {
		op, err := scanOperator(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

func (s *SQLite) UpdateOperatorScope(ctx context.Context, org, id string, sourceClusterIDs []string, dest Destination, acceptedModalities []Modality, opts ...WriteOption) error {
	return s.write(ctx, opts, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE operators SET source_cluster_ids=?, destination=?, accepted_modalities=? WHERE id=? AND org_id=? AND status='active'`,
			sourceClusterIDsJSON(sourceClusterIDs), destinationJSON(dest), acceptedModalitiesJSON(acceptedModalities), id, org)
		if err != nil {
			return err
		}
		return needOneIn(ctx, tx, res, "operators", org, id)
	})
}

// SetOperatorAddress records the host:port other clusters reach the operator's receiver at ("" clears it).
func (s *SQLite) SetOperatorAddress(ctx context.Context, org, id, address string, opts ...WriteOption) error {
	return s.write(ctx, opts, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE operators SET address=? WHERE id=? AND org_id=? AND status='active'`, address, id, org)
		if err != nil {
			return err
		}
		return needOneIn(ctx, tx, res, "operators", org, id)
	})
}

// SetOperatorAddressIfEmpty records address only while the operator has none, in one statement, so a value an
// administrator typed in the meantime is never overwritten (the server learning an address by itself must lose to a
// person). It reports whether it wrote; ErrNotFound / ErrBadState for an operator that is missing or not active.
// When it did not write, no audit row is added either: the attempt changed nothing.
func (s *SQLite) SetOperatorAddressIfEmpty(ctx context.Context, org, id, address string, opts ...WriteOption) (bool, error) {
	wrote := false
	err := s.write(ctx, opts, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE operators SET address=? WHERE id=? AND org_id=? AND status='active' AND address=''`, address, id, org)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			wrote = true
			return nil
		}
		var status string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM operators WHERE id=? AND org_id=?`, id, org).Scan(&status); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if OperatorStatus(status) != OperatorActive {
			return ErrBadState
		}
		return errNoChange
	})
	return wrote && err == nil, err
}

// SetOperatorCerts records when the receiver and client certificates just issued expire, and starts the expiry
// warnings over: what was raised for the certificates they replace says nothing about these.
func (s *SQLite) SetOperatorCerts(ctx context.Context, org, id string, receiverNotAfter, clientNotAfter time.Time) error {
	return s.write(ctx, nil, func(tx *sql.Tx) error { return setOperatorCertsTx(ctx, tx, org, id, receiverNotAfter, clientNotAfter) })
}

func setOperatorCertsTx(ctx context.Context, tx *sql.Tx, org, id string, receiverNotAfter, clientNotAfter time.Time) error {
	res, err := tx.ExecContext(ctx,
		`UPDATE operators SET receiver_not_after=?, client_not_after=?, cert_alert_level=0 WHERE id=? AND org_id=? AND status='active'`,
		ms(receiverNotAfter), ms(clientNotAfter), id, org)
	if err != nil {
		return err
	}
	return needOneIn(ctx, tx, res, "operators", org, id)
}

// SetOperatorCertAlertLevel records the expiry warning level already raised, so the daily check raises each only once.
func (s *SQLite) SetOperatorCertAlertLevel(ctx context.Context, org, id string, level int) error {
	return s.write(ctx, nil, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE operators SET cert_alert_level=? WHERE id=? AND org_id=? AND status='active'`, level, id, org)
		if err != nil {
			return err
		}
		return needOneIn(ctx, tx, res, "operators", org, id)
	})
}

// SetOperatorReceiverToken replaces the hash of a bearer operator's receiver token (the earlier token stops
// working at once). ErrBadState for an operator that is not active or has no bearer gate.
func (s *SQLite) SetOperatorReceiverToken(ctx context.Context, org, id string, hash []byte) error {
	return s.write(ctx, nil, func(tx *sql.Tx) error { return setOperatorReceiverTokenTx(ctx, tx, org, id, hash) })
}

func setOperatorReceiverTokenTx(ctx context.Context, tx *sql.Tx, org, id string, hash []byte) error {
	res, err := tx.ExecContext(ctx,
		`UPDATE operators SET receiver_auth_token_hash=? WHERE id=? AND org_id=? AND status='active' AND receiver_auth='bearer'`, hash, id, org)
	if err != nil {
		return err
	}
	return needOneIn(ctx, tx, res, "operators", org, id)
}

// OperatorReissue is what reissuing an operator's install changes on its row: a bearer operator's new receiver token
// hash, or an mTLS operator's new certificate end dates, and (when it has one) the new heartbeat secret's hash.
type OperatorReissue struct {
	ReceiverTokenHash []byte // set for a bearer operator; when nil the certificate end dates are recorded instead
	ReceiverNotAfter  time.Time
	ClientNotAfter    time.Time
	HeartbeatHash     []byte // set when the operator has a heartbeat secret that is rotated with it
	Now               time.Time
}

// ReissueOperatorInstall applies a reissue in one transaction, with its audit row when one is given.
func (s *SQLite) ReissueOperatorInstall(ctx context.Context, org, id string, in OperatorReissue, opts ...WriteOption) error {
	return s.write(ctx, opts, func(tx *sql.Tx) error {
		if in.ReceiverTokenHash != nil {
			if err := setOperatorReceiverTokenTx(ctx, tx, org, id, in.ReceiverTokenHash); err != nil {
				return err
			}
		} else if err := setOperatorCertsTx(ctx, tx, org, id, in.ReceiverNotAfter, in.ClientNotAfter); err != nil {
			return err
		}
		if in.HeartbeatHash != nil {
			return setOperatorHeartbeatTx(ctx, tx, org, id, in.HeartbeatHash, in.Now)
		}
		return nil
	})
}

func (s *SQLite) RevokeOperator(ctx context.Context, org, id, reason string, now time.Time, opts ...WriteOption) error {
	return s.write(ctx, opts, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE operators SET status='revoked', reason=?, revoked_at=?, client_ca_key=NULL WHERE id=? AND org_id=? AND status='active'`,
			reason, ms(now), id, org)
		if err != nil {
			return err
		}
		return needOneIn(ctx, tx, res, "operators", org, id)
	})
}

func (s *SQLite) DeleteOperator(ctx context.Context, org, id string, opts ...WriteOption) error {
	return s.write(ctx, opts, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM operators WHERE id=? AND org_id=?`, id, org)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		// The ledger describes certificates of an operator that no longer exists; nothing is left to ask it about.
		_, err = tx.ExecContext(ctx, `DELETE FROM operator_certs WHERE operator_id=? AND org_id=?`, id, org)
		return err
	})
}

// AddOperatorCert records one issued certificate. The serial is the primary key, so recording the same one twice is an error.
// The row carries its organisation, and the ledger is read and erased only inside it. A creation records its receiver certificate
// with the operator in one transaction (WithOperatorCert); this is for every later one, where the operator already exists.
func (s *SQLite) AddOperatorCert(ctx context.Context, c OperatorCert) error {
	return insertOperatorCert(ctx, s.db, c)
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func insertOperatorCert(ctx context.Context, db execer, c OperatorCert) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO operator_certs(serial, org_id, operator_id, kind, subject, sender, issued_by, issued_at, not_before, not_after) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		c.Serial, c.OrgID, c.OperatorID, string(c.Kind), c.Subject, c.Sender, c.IssuedBy, ms(c.IssuedAt), ms(c.NotBefore), ms(c.NotAfter))
	return err
}

// ListOperatorCerts returns what was issued for an operator, newest first.
func (s *SQLite) ListOperatorCerts(ctx context.Context, org, operatorID string) ([]OperatorCert, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT serial, org_id, operator_id, kind, subject, sender, issued_by, issued_at, not_before, not_after FROM operator_certs WHERE org_id=? AND operator_id=? ORDER BY issued_at DESC, serial`, org, operatorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OperatorCert
	for rows.Next() {
		var c OperatorCert
		var kind string
		var issued, nb, na int64
		if err := rows.Scan(&c.Serial, &c.OrgID, &c.OperatorID, &kind, &c.Subject, &c.Sender, &c.IssuedBy, &issued, &nb, &na); err != nil {
			return nil, err
		}
		c.Kind, c.IssuedAt, c.NotBefore, c.NotAfter = OperatorCertKind(kind), fromMS(issued), fromMS(nb), fromMS(na)
		out = append(out, c)
	}
	return out, rows.Err()
}

// ---- telemetry intents ----

// namespacesJSON/parseNamespaces encode/decode a plain list of namespace names - TelemetryIntent.Namespaces
// and .Exclude both need this, the same convention sourceClusterIDsJSON uses for Operator.SourceClusterIDs.
func namespacesJSON(ns []string) string {
	if len(ns) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(ns)
	return string(b)
}
func parseNamespaces(s string) []string {
	var ns []string
	_ = json.Unmarshal([]byte(s), &ns)
	return ns
}

// signalGrantsJSON/parseSignalGrants encode/decode TelemetryIntent.Signals - a small list of {ID, Source}
// pairs with nothing in it ever queried across rows, the same reasoning destinationJSON gives for keeping
// Destination a single JSON column instead of flattening it into more table columns.
func signalGrantsJSON(sg []SignalGrant) string {
	if len(sg) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(sg)
	return string(b)
}
func parseSignalGrants(s string) []SignalGrant {
	var sg []SignalGrant
	_ = json.Unmarshal([]byte(s), &sg)
	return sg
}

// routesJSON/parseRoutes encode/decode TelemetryIntent.Routes the way destinationJSON does Destination: one
// small nested object, read whole. A blank or unparsable column reads back as no routes.
func routesJSON(r map[Modality]Destination) string {
	if len(r) == 0 {
		return "{}"
	}
	b, _ := json.Marshal(r)
	return string(b)
}
func parseRoutes(s string) map[Modality]Destination {
	var r map[Modality]Destination
	if err := json.Unmarshal([]byte(s), &r); err != nil || len(r) == 0 {
		return nil
	}
	return r
}

const telemetryIntentCols = `id, org_id, agent_id, name, status, namespaces, exclude, signals, destination, routes, created_by, created_at, revoked_at, reason`

func scanTelemetryIntent(r scanner) (TelemetryIntent, error) {
	var ti TelemetryIntent
	var st, ns, exc, sig, dest, routes string
	var created int64
	var revoked sql.NullInt64
	err := r.Scan(&ti.ID, &ti.OrgID, &ti.AgentID, &ti.Name, &st, &ns, &exc, &sig, &dest, &routes, &ti.CreatedBy, &created, &revoked, &ti.Reason)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TelemetryIntent{}, ErrNotFound
		}
		return TelemetryIntent{}, err
	}
	ti.Status = TelemetryIntentStatus(st)
	ti.Namespaces = parseNamespaces(ns)
	ti.Exclude = parseNamespaces(exc)
	ti.Signals = parseSignalGrants(sig)
	ti.Destination = parseDestination(dest)
	ti.Routes = parseRoutes(routes)
	ti.CreatedAt = fromMS(created)
	ti.RevokedAt = fromNullMS(revoked)
	return ti, nil
}

func (s *SQLite) CreateTelemetryIntent(ctx context.Context, ti TelemetryIntent, opts ...WriteOption) error {
	return s.write(ctx, opts, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO telemetry_intents(id, org_id, agent_id, name, status, namespaces, exclude, signals, destination, routes, created_by, created_at, reason)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			ti.ID, ti.OrgID, ti.AgentID, ti.Name, string(ti.Status), namespacesJSON(ti.Namespaces), namespacesJSON(ti.Exclude), signalGrantsJSON(ti.Signals), destinationJSON(ti.Destination), routesJSON(ti.Routes), ti.CreatedBy, ms(ti.CreatedAt), ti.Reason)
		return err
	})
}

func (s *SQLite) GetTelemetryIntent(ctx context.Context, org, id string) (TelemetryIntent, error) {
	return scanTelemetryIntent(s.db.QueryRowContext(ctx, `SELECT `+telemetryIntentCols+` FROM telemetry_intents WHERE id=? AND org_id=?`, id, org))
}

func (s *SQLite) ListTelemetryIntentsByAgent(ctx context.Context, org, agentID string) ([]TelemetryIntent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+telemetryIntentCols+` FROM telemetry_intents WHERE org_id=? AND agent_id=? ORDER BY created_at`, org, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TelemetryIntent
	for rows.Next() {
		ti, err := scanTelemetryIntent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ti)
	}
	return out, rows.Err()
}

func (s *SQLite) ListTelemetryIntents(ctx context.Context, org string) ([]TelemetryIntent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+telemetryIntentCols+` FROM telemetry_intents WHERE org_id=? ORDER BY created_at`, org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TelemetryIntent
	for rows.Next() {
		ti, err := scanTelemetryIntent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ti)
	}
	return out, rows.Err()
}

func (s *SQLite) UpdateTelemetryIntentScope(ctx context.Context, org, id string, namespaces, exclude []string, signals []SignalGrant, opts ...WriteOption) error {
	return s.write(ctx, opts, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE telemetry_intents SET namespaces=?, exclude=?, signals=? WHERE id=? AND org_id=? AND status='active'`,
			namespacesJSON(namespaces), namespacesJSON(exclude), signalGrantsJSON(signals), id, org)
		if err != nil {
			return err
		}
		return needOneIn(ctx, tx, res, "telemetry_intents", org, id)
	})
}

func (s *SQLite) UpdateTelemetryIntentDestination(ctx context.Context, org, id string, dest Destination, opts ...WriteOption) error {
	return s.write(ctx, opts, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE telemetry_intents SET destination=? WHERE id=? AND org_id=? AND status='active'`, destinationJSON(dest), id, org)
		if err != nil {
			return err
		}
		return needOneIn(ctx, tx, res, "telemetry_intents", org, id)
	})
}

func (s *SQLite) UpdateTelemetryIntentDestinations(ctx context.Context, org, id string, dest Destination, routes map[Modality]Destination, opts ...WriteOption) error {
	return s.write(ctx, opts, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE telemetry_intents SET destination=?, routes=? WHERE id=? AND org_id=? AND status='active'`, destinationJSON(dest), routesJSON(routes), id, org)
		if err != nil {
			return err
		}
		return needOneIn(ctx, tx, res, "telemetry_intents", org, id)
	})
}

func (s *SQLite) RevokeTelemetryIntent(ctx context.Context, org, id, reason string, now time.Time, opts ...WriteOption) error {
	return s.write(ctx, opts, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE telemetry_intents SET status='revoked', reason=?, revoked_at=? WHERE id=? AND org_id=? AND status='active'`,
			reason, ms(now), id, org)
		if err != nil {
			return err
		}
		return needOneIn(ctx, tx, res, "telemetry_intents", org, id)
	})
}

func (s *SQLite) DeleteTelemetryIntent(ctx context.Context, org, id string, opts ...WriteOption) error {
	return s.write(ctx, opts, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM telemetry_intents WHERE id=? AND org_id=?`, id, org)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
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

func (s *SQLite) SaveSnapshot(ctx context.Context, agentID string, data []byte, now time.Time, opts ...WriteOption) error {
	return s.write(ctx, opts, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO snapshots(agent_id, data, at) VALUES(?,?,?) ON CONFLICT(agent_id) DO UPDATE SET data=excluded.data, at=excluded.at`,
			agentID, data, ms(now))
		return err
	})
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

func (s *SQLite) DeleteSnapshots(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	args := make([]any, len(keys))
	for i, k := range keys {
		args[i] = k
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM snapshots WHERE agent_id IN (?`+strings.Repeat(",?", len(keys)-1)+`)`, args...)
	return err
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
