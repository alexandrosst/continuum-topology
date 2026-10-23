package store

import "database/sql"

// migrateGatewayTokensGone drops the table of quick-start gateway tokens (PRAGMA user_version 19). The tokens were minted for an nginx
// gateway that the server no longer generates, so nothing reads or writes the table any more. A row held only the hash of a secret
// that had expired or been forgotten, never the secret, so dropping them gives up nothing that could be used.
func migrateGatewayTokensGone(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v >= 19 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// The index goes with its table.
	if _, err := tx.Exec(`DROP TABLE IF EXISTS gateway_tokens`); err != nil {
		return err
	}
	if _, err := tx.Exec(`PRAGMA user_version = 19`); err != nil {
		return err
	}
	return tx.Commit()
}
