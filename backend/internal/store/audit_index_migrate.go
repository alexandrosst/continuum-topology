package store

import "database/sql"

// migrateAuditIndex indexes the audit trail by organisation (PRAGMA user_version 18). ListAudit asks for one
// organisation's newest rows out of a table that every organisation shares and that is never pruned, so without
// it each poll walked backwards past everyone else's rows. The hash chain is untouched: it reads by id.
func migrateAuditIndex(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v >= 18 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS audit_org ON audit(org_id, id)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`PRAGMA user_version = 18`); err != nil {
		return err
	}
	return tx.Commit()
}
