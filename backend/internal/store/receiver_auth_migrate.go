package store

import "database/sql"

// migrateReceiverAuth adds Operator.ReceiverAuth (PRAGMA user_version 10): which gate a regional operator's
// OTLP receiver has - a bearer token, or only the org-CA mTLS client certificate. The default 'bearer' is
// exactly what every existing row is: they were installed with receiver.auth.enabled=true, and the server
// cannot recover their token (it keeps only the hash), so nothing about them may change.
func migrateReceiverAuth(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v >= 10 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	has, err := hasColumn(tx, "operators", "receiver_auth")
	if err != nil {
		return err
	}
	if !has {
		if _, err := tx.Exec(`ALTER TABLE operators ADD COLUMN receiver_auth TEXT NOT NULL DEFAULT 'bearer'`); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`PRAGMA user_version = 10`); err != nil {
		return err
	}
	return tx.Commit()
}
