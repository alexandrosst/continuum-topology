package store

import "database/sql"

// migrateTOTP adds optional two-factor authentication to accounts (PRAGMA user_version 5): a TOTP secret,
// when it was confirmed (nil until then, so a setup nobody finished never blocks sign-in), and the unused
// recovery codes. Existing rows get the neutral defaults: no secret, never enabled, no recovery codes -
// exactly what an account that has never touched 2FA looks like once the feature exists.
func migrateTOTP(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v >= 5 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, c := range []struct{ table, col, ddl string }{
		{"users", "totp_secret", `ALTER TABLE users ADD COLUMN totp_secret TEXT NOT NULL DEFAULT ''`},
		{"users", "totp_enabled_at", `ALTER TABLE users ADD COLUMN totp_enabled_at INTEGER`},
		{"users", "totp_recovery", `ALTER TABLE users ADD COLUMN totp_recovery TEXT NOT NULL DEFAULT '[]'`},
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
	if _, err := tx.Exec(`PRAGMA user_version = 5`); err != nil {
		return err
	}
	return tx.Commit()
}
