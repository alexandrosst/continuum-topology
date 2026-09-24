package store

import "database/sql"

// migrateEmail adds an optional email address to accounts (PRAGMA user_version 6), together with when it
// was last confirmed and whether it is turned on as a second sign-in factor - the same shape as the TOTP
// columns migrateTOTP added, for the same reason: one confirmed-or-not timestamp says everything about
// whether the feature is live, without a separate boolean that could drift out of sync with it. Existing
// rows get the neutral defaults: no address, never verified, never enabled.
func migrateEmail(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v >= 6 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, c := range []struct{ table, col, ddl string }{
		{"users", "email", `ALTER TABLE users ADD COLUMN email TEXT NOT NULL DEFAULT ''`},
		{"users", "email_verified_at", `ALTER TABLE users ADD COLUMN email_verified_at INTEGER`},
		{"users", "email_otp_enabled_at", `ALTER TABLE users ADD COLUMN email_otp_enabled_at INTEGER`},
	} {
		has, err := hasColumn(tx, c.table, c.col)
		if err != nil {
			return err
		}
		if !has {
			if _, err := tx.Exec(c.ddl); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(`PRAGMA user_version = 6`); err != nil {
		return err
	}
	return tx.Commit()
}
