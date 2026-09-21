package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

// The audit trail is a hash chain: every new row stores hash = SHA-256(previous hash || the row's
// content), so changing, removing or re-ordering a row breaks every link after it and VerifyAudit
// reports the first one that no longer holds.
//
// Rows written before the chain existed have no hash and are simply not covered: the chain starts at
// the first row written by a version that knows about it (its "previous hash" is 32 zero bytes).
//
// This is tamper-evidence, not tamper-proofing: someone who can rewrite the database file can also
// recompute the chain. Record the head hash that `server verify-audit` prints somewhere the server's
// host cannot write to (a ticket, a log service) and later output can be compared with it.

// genesis is the "previous hash" of the first chained row.
var genesis = make([]byte, sha256.Size)

// migrateAuditChain adds the hash column (PRAGMA user_version 2). Existing rows keep a NULL hash.
func migrateAuditChain(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v >= 2 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	has, err := hasColumn(tx, "audit", "hash")
	if err != nil {
		return err
	}
	if !has {
		if _, err := tx.Exec(`ALTER TABLE audit ADD COLUMN hash BLOB`); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`PRAGMA user_version = 2`); err != nil {
		return err
	}
	return tx.Commit()
}

func hasColumn(tx *sql.Tx, table, col string) (bool, error) {
	rows, err := tx.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return false, err
		}
		if n == col {
			return true, nil
		}
	}
	return false, rows.Err()
}

// auditHash is the chain function. Every field is length-prefixed so no two different rows can
// produce the same byte string.
func auditHash(prev []byte, e AuditEvent) []byte {
	h := sha256.New()
	h.Write(prev)
	var n [8]byte
	put := func(v int64) { binary.BigEndian.PutUint64(n[:], uint64(v)); h.Write(n[:]) }
	str := func(s string) { put(int64(len(s))); h.Write([]byte(s)) }
	put(e.ID)
	put(e.At.UnixMilli())
	str(e.OrgID)
	str(e.Actor)
	str(e.Action)
	str(e.TargetKind)
	str(e.TargetID)
	str(e.Detail)
	return h.Sum(nil)
}

// AddAudit appends a row and links it into the chain, atomically.
func (s *SQLite) AddAudit(ctx context.Context, e AuditEvent) error {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	prev := genesis
	var last []byte
	switch err := tx.QueryRowContext(ctx, `SELECT hash FROM audit WHERE hash IS NOT NULL ORDER BY id DESC LIMIT 1`).Scan(&last); {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return err
	default:
		prev = last
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO audit(at, org_id, actor, action, target_kind, target_id, detail) VALUES(?,?,?,?,?,?,?)`,
		ms(e.At), e.OrgID, e.Actor, e.Action, e.TargetKind, e.TargetID, e.Detail)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	e.ID = id
	// The millisecond the database stores is what the hash must cover, or verification would disagree.
	e.At = fromMS(ms(e.At))
	if _, err := tx.ExecContext(ctx, `UPDATE audit SET hash=? WHERE id=?`, auditHash(prev, e), id); err != nil {
		return err
	}
	return tx.Commit()
}

// AuditReport is the result of VerifyAudit.
type AuditReport struct {
	Rows      int    // rows in the trail
	Unchained int    // older rows written before the chain existed (not covered)
	Chained   int    // rows whose link was checked and holds (all of them when OK)
	Head      string // hex hash of the newest chained row; record it to detect a later rollback
	OK        bool
	// BrokenAt is the id of the first row whose link does not hold, and Problem says how (0 and "" when OK).
	BrokenAt int64
	Problem  string
}

// VerifyAudit walks the whole trail in order and checks every link.
func (s *SQLite) VerifyAudit(ctx context.Context) (AuditReport, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, at, org_id, actor, action, target_kind, target_id, detail, hash FROM audit ORDER BY id`)
	if err != nil {
		return AuditReport{}, err
	}
	defer rows.Close()
	r := AuditReport{OK: true}
	prev := genesis
	started := false
	var lastID int64
	fail := func(id int64, format string, a ...any) {
		if r.OK {
			r.OK, r.BrokenAt, r.Problem = false, id, fmt.Sprintf(format, a...)
		}
	}
	for rows.Next() {
		var e AuditEvent
		var at int64
		var hash []byte
		if err := rows.Scan(&e.ID, &at, &e.OrgID, &e.Actor, &e.Action, &e.TargetKind, &e.TargetID, &e.Detail, &hash); err != nil {
			return r, err
		}
		e.At = fromMS(at)
		r.Rows++
		lastID = e.ID
		if len(hash) == 0 {
			if started {
				fail(e.ID, "row %d has no hash although the chain had already started (it was inserted or altered outside the server)", e.ID)
			} else {
				r.Unchained++
			}
			continue
		}
		started = true
		want := auditHash(prev, e)
		if !bytes.Equal(want, hash) {
			fail(e.ID, "row %d does not match the chain: its content, or a row before it, was changed, removed or re-ordered", e.ID)
		} else if r.OK {
			r.Chained++
		}
		prev = hash // continue from what is stored so one edit is reported once, at its own row
	}
	if err := rows.Err(); err != nil {
		return r, err
	}
	if started {
		r.Head = fmt.Sprintf("%x", prev)
		// AUTOINCREMENT remembers the highest id ever used; a higher one than the last row means the tail was deleted.
		var seq sql.NullInt64
		if err := s.db.QueryRowContext(ctx, `SELECT seq FROM sqlite_sequence WHERE name='audit'`).Scan(&seq); err == nil && seq.Valid && seq.Int64 > lastID {
			fail(lastID+1, "rows after id %d were removed (the database remembers ids up to %d)", lastID, seq.Int64)
		}
	}
	return r, nil
}
