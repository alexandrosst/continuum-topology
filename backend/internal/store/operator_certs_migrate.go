package store

import "database/sql"

// migrateOperatorCerts adds what the server knows about when an operator's certificates stop working (PRAGMA
// user_version 16): the notAfter of the receiver certificate and of the client certificate, as issued, and the
// level of the expiry warning already raised for them. Existing rows get NULL dates, which means "not recorded";
// the server works them out from what it does hold (see Core.operatorCertDates), and the level starts at 0 (no
// warning raised yet).
func migrateOperatorCerts(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v >= 16 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, col := range []struct{ name, typ string }{
		{"receiver_not_after", "INTEGER"},
		{"client_not_after", "INTEGER"},
		{"cert_alert_level", "INTEGER NOT NULL DEFAULT 0"},
	} {
		has, err := hasColumn(tx, "operators", col.name)
		if err != nil {
			return err
		}
		if !has {
			if _, err := tx.Exec(`ALTER TABLE operators ADD COLUMN ` + col.name + ` ` + col.typ); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(`PRAGMA user_version = 16`); err != nil {
		return err
	}
	return tx.Commit()
}
