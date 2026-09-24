package store

import "database/sql"

// migrateWebAuthn adds passkeys/security keys to accounts (PRAGMA user_version 7): one JSON array column
// holding every credential registered to the account, the same shape totp_recovery already uses for a list
// that is read whole, modified, and written back whole (see webauthnCredentialsJSON). A separate table was
// the other option; a JSON column was chosen because nothing here is ever queried across accounts - only
// "this account's credentials", which GetUser already loads in one row. Existing rows get an empty list:
// no passkey means this method of signing in simply is not offered, with no separate enabled flag to keep
// in sync (unlike TOTP and email, a credential either exists, fully registered, or was never stored at all).
func migrateWebAuthn(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v >= 7 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	has, err := hasColumn(tx, "users", "webauthn_credentials")
	if err != nil {
		return err
	}
	if !has {
		if _, err := tx.Exec(`ALTER TABLE users ADD COLUMN webauthn_credentials TEXT NOT NULL DEFAULT '[]'`); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`PRAGMA user_version = 7`); err != nil {
		return err
	}
	return tx.Commit()
}
