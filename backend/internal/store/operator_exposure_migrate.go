package store

import "database/sql"

// migrateOperatorExposure adds how a regional operator's Service was exposed when it was created (PRAGMA
// user_version 15). Existing rows get an empty one, which means "not recorded": nothing was ever asked of them.
func migrateOperatorExposure(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v >= 15 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	has, err := hasColumn(tx, "operators", "exposure")
	if err != nil {
		return err
	}
	if !has {
		if _, err := tx.Exec(`ALTER TABLE operators ADD COLUMN exposure TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`PRAGMA user_version = 15`); err != nil {
		return err
	}
	return tx.Commit()
}
