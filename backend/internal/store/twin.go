package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"continuum/internal/workspace"
)

// SchemaVersion is the newest database format this build reads and writes (PRAGMA user_version). The rule:
//
//   - Migrations only go forward and run at startup, each once, in order (see OpenSQLite).
//   - A database whose version is HIGHER than this is refused with ErrSchemaNewer and left untouched: an older
//     server must never open, and so never half-migrate or corrupt, data a newer one wrote.
//   - Bump this together with adding the next migration.
//
// History: 1 organisations, 2 audit hash chain, 3 approval codes and token binding, 4 the twin (observation
// tombstones, node identities, the model version, declared-only workspaces), 5 optional TOTP two-factor
// authentication on accounts, 6 an optional email address on accounts (verified-at, and whether it is turned
// on as a second sign-in factor), 7 optional passkeys/security keys (WebAuthn) on accounts.
const SchemaVersion = 7

// ErrSchemaNewer is returned when the database was written by a newer version of the software.
var ErrSchemaNewer = errors.New("the database was written by a newer version of Continuum")

// SchemaNewerError carries the versions for the message.
type SchemaNewerError struct{ Have, Know int }

func (e SchemaNewerError) Error() string {
	return fmt.Sprintf("the database uses schema version %d, but this server only understands up to version %d. It was written by a newer Continuum, and opening it with an older one could damage it, so nothing was touched. Start the newer server, or restore a backup taken with this version (server restore)", e.Have, e.Know)
}
func (e SchemaNewerError) Unwrap() error { return ErrSchemaNewer }

// checkSchemaNotNewer runs before anything else touches the database.
func checkSchemaNotNewer(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v > SchemaVersion {
		return SchemaNewerError{Have: v, Know: SchemaVersion}
	}
	return nil
}

// Tombstone is a record that disappeared from what its agent reports. It is kept for a retention window (the
// hub decides how long) so it can be shown as "gone", used for history, and so the workspace's references to
// it stay explainable, instead of the record silently vanishing.
type Tombstone struct {
	Kind      string // cluster | node | namespace | service
	ID        string
	Name      string
	ClusterID string
	AgentID   string
	GoneAt    time.Time
	LastSeen  time.Time // the last time its agent vouched for it
	Reason    string
	Record    []byte // the record as it was last known (JSON), for display
}

// TombstoneKey identifies one tombstone.
type TombstoneKey struct{ Kind, ID string }

// Identity remembers which record id a machine (or other stable identity) was given inside one cluster, so a
// rename keeps the id and a different machine that reuses a name does not inherit it.
type Identity struct {
	ClusterID string
	// Ident is "<basis>:<digest>", for example "provider-id:9f2c…". The raw identifier is never stored.
	Ident     string
	RecordID  string
	Name      string   // the name it was last seen under
	Aliases   []string // names it had before
	FirstSeen time.Time
	LastSeen  time.Time
}

// ModelState is the last published version of an organisation's effective model.
type ModelState struct {
	Version     int64
	Fingerprint string
	At          time.Time
}

const twinSchema = `
CREATE TABLE IF NOT EXISTS tombstones (
  org_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  id TEXT NOT NULL,
  name TEXT NOT NULL DEFAULT '',
  cluster_id TEXT NOT NULL DEFAULT '',
  agent_id TEXT NOT NULL DEFAULT '',
  gone_at INTEGER NOT NULL,
  last_seen INTEGER NOT NULL DEFAULT 0,
  reason TEXT NOT NULL DEFAULT '',
  record BLOB NOT NULL,
  PRIMARY KEY (org_id, kind, id)
);
CREATE INDEX IF NOT EXISTS tombstones_gone ON tombstones(org_id, gone_at);
CREATE TABLE IF NOT EXISTS identities (
  org_id TEXT NOT NULL,
  cluster_id TEXT NOT NULL,
  ident TEXT NOT NULL,
  record_id TEXT NOT NULL,
  name TEXT NOT NULL,
  aliases TEXT NOT NULL DEFAULT '',
  first_seen INTEGER NOT NULL,
  last_seen INTEGER NOT NULL,
  PRIMARY KEY (org_id, cluster_id, ident)
);
CREATE TABLE IF NOT EXISTS model_state (
  org_id TEXT PRIMARY KEY,
  version INTEGER NOT NULL,
  fingerprint TEXT NOT NULL,
  at INTEGER NOT NULL
);
`

// migrateTwin is schema version 4. It adds the tables above, a note column on the workspace, and rewrites every
// stored workspace into the declared-only format (workspace.Declare): the observed records it carried are
// removed, the overrides and assignments people made on them are kept as references, and the note tells the next
// person to open it what happened. A workspace that cannot be read is left exactly as it is.
func migrateTwin(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v >= 4 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(twinSchema); err != nil {
		return err
	}
	has, err := hasColumn(tx, "workspace", "note")
	if err != nil {
		return err
	}
	if !has {
		if _, err := tx.Exec(`ALTER TABLE workspace ADD COLUMN note TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	rows, err := tx.Query(`SELECT org_id, data FROM workspace`)
	if err != nil {
		return err
	}
	type upd struct {
		org  string
		data []byte
		note string
	}
	var todo []upd
	for rows.Next() {
		var org string
		var data []byte
		if err := rows.Scan(&org, &data); err != nil {
			rows.Close()
			return err
		}
		out, rep, err := workspace.Declare(data)
		if err != nil || !rep.Changed() && workspace.Peek(data) == workspace.CurrentVersion {
			continue // unreadable or newer: never rewritten here
		}
		todo = append(todo, upd{org, out, rep.Note()})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, u := range todo {
		if _, err := tx.Exec(`UPDATE workspace SET data=?, note=? WHERE org_id=?`, u.data, u.note, u.org); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`PRAGMA user_version = 4`); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- tombstones ----

func (s *SQLite) ListTombstones(ctx context.Context, org string) ([]Tombstone, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind, id, name, cluster_id, agent_id, gone_at, last_seen, reason, record FROM tombstones WHERE org_id=? ORDER BY gone_at DESC, kind, id`, org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Tombstone
	for rows.Next() {
		var t Tombstone
		var gone, seen int64
		if err := rows.Scan(&t.Kind, &t.ID, &t.Name, &t.ClusterID, &t.AgentID, &gone, &seen, &t.Reason, &t.Record); err != nil {
			return nil, err
		}
		t.GoneAt = fromMS(gone)
		if seen > 0 {
			t.LastSeen = fromMS(seen)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// PutTombstones inserts or replaces tombstones in one transaction.
func (s *SQLite) PutTombstones(ctx context.Context, org string, ts []Tombstone) error {
	if len(ts) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, t := range ts {
		var seen int64
		if !t.LastSeen.IsZero() {
			seen = ms(t.LastSeen)
		}
		rec := t.Record
		if rec == nil {
			rec = []byte("{}")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO tombstones(org_id, kind, id, name, cluster_id, agent_id, gone_at, last_seen, reason, record) VALUES(?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(org_id, kind, id) DO UPDATE SET name=excluded.name, cluster_id=excluded.cluster_id, agent_id=excluded.agent_id, gone_at=excluded.gone_at, last_seen=excluded.last_seen, reason=excluded.reason, record=excluded.record`,
			org, t.Kind, t.ID, t.Name, t.ClusterID, t.AgentID, ms(t.GoneAt), seen, t.Reason, rec); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteTombstones removes tombstones (a record that came back, or one past its retention).
func (s *SQLite) DeleteTombstones(ctx context.Context, org string, keys []TombstoneKey) error {
	if len(keys) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, k := range keys {
		if _, err := tx.ExecContext(ctx, `DELETE FROM tombstones WHERE org_id=? AND kind=? AND id=?`, org, k.Kind, k.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ---- identities ----

func (s *SQLite) ListIdentities(ctx context.Context, org string) ([]Identity, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT cluster_id, ident, record_id, name, aliases, first_seen, last_seen FROM identities WHERE org_id=? ORDER BY cluster_id, ident`, org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Identity
	for rows.Next() {
		var i Identity
		var al string
		var first, last int64
		if err := rows.Scan(&i.ClusterID, &i.Ident, &i.RecordID, &i.Name, &al, &first, &last); err != nil {
			return nil, err
		}
		i.Aliases = splitAliases(al)
		i.FirstSeen, i.LastSeen = fromMS(first), fromMS(last)
		out = append(out, i)
	}
	return out, rows.Err()
}

func (s *SQLite) PutIdentities(ctx context.Context, org string, ids []Identity) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, i := range ids {
		if _, err := tx.ExecContext(ctx, `INSERT INTO identities(org_id, cluster_id, ident, record_id, name, aliases, first_seen, last_seen) VALUES(?,?,?,?,?,?,?,?)
			ON CONFLICT(org_id, cluster_id, ident) DO UPDATE SET record_id=excluded.record_id, name=excluded.name, aliases=excluded.aliases, last_seen=excluded.last_seen`,
			org, i.ClusterID, i.Ident, i.RecordID, i.Name, joinAliases(i.Aliases), ms(i.FirstSeen), ms(i.LastSeen)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// aliases are stored newline separated: a Kubernetes node name never contains one.
func joinAliases(a []string) string {
	out := ""
	for i, x := range a {
		if i > 0 {
			out += "\n"
		}
		out += x
	}
	return out
}

func splitAliases(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '\n' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	return out
}

// ---- model version ----

func (s *SQLite) GetModelState(ctx context.Context, org string) (ModelState, error) {
	var m ModelState
	var at int64
	err := s.db.QueryRowContext(ctx, `SELECT version, fingerprint, at FROM model_state WHERE org_id=?`, org).Scan(&m.Version, &m.Fingerprint, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return ModelState{}, ErrNotFound
	}
	if err != nil {
		return ModelState{}, err
	}
	m.At = fromMS(at)
	return m, nil
}

func (s *SQLite) PutModelState(ctx context.Context, org string, m ModelState) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO model_state(org_id, version, fingerprint, at) VALUES(?,?,?,?)
		ON CONFLICT(org_id) DO UPDATE SET version=excluded.version, fingerprint=excluded.fingerprint, at=excluded.at WHERE excluded.version >= model_state.version`,
		org, m.Version, m.Fingerprint, ms(m.At))
	return err
}
