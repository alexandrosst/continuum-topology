package store

import "database/sql"

// migrateOperatorLedger adds the record of the certificates the server issued for regional operators (PRAGMA
// user_version 17). Nothing is backfilled: certificates issued before it were never recorded, and the ledger says so by
// being empty for them rather than guessing.
func migrateOperatorLedger(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v >= 17 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
CREATE TABLE IF NOT EXISTS operator_certs (
  serial TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  operator_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  subject TEXT NOT NULL,
  sender TEXT NOT NULL DEFAULT '',
  issued_by TEXT NOT NULL DEFAULT '',
  issued_at INTEGER NOT NULL,
  not_before INTEGER NOT NULL,
  not_after INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS operator_certs_operator ON operator_certs(operator_id, issued_at DESC);`); err != nil {
		return err
	}
	if _, err := tx.Exec(`PRAGMA user_version = 17`); err != nil {
		return err
	}
	return tx.Commit()
}
