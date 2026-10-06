package store

import "database/sql"

// migrateOperatorAddress adds a regional operator's advertised address (PRAGMA user_version 14): the host:port
// other clusters reach its receiver at. Existing rows get an empty one, which keeps meaning "only reachable by
// its in-cluster name", exactly what every command built for them assumed before the address existed.
func migrateOperatorAddress(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v >= 14 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	has, err := hasColumn(tx, "operators", "address")
	if err != nil {
		return err
	}
	if !has {
		if _, err := tx.Exec(`ALTER TABLE operators ADD COLUMN address TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`PRAGMA user_version = 14`); err != nil {
		return err
	}
	return tx.Commit()
}
