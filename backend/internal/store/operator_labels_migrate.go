package store

import "database/sql"

// migrateOperatorLabels adds a regional operator's labels (PRAGMA user_version 12): one JSON array column of
// {Key, Value} pairs, read and written whole like source_cluster_ids. Existing rows get an empty list - an
// operator created before labels existed was installed without any, and nothing here can change what its
// running collector stamps.
func migrateOperatorLabels(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v >= 12 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	has, err := hasColumn(tx, "operators", "labels")
	if err != nil {
		return err
	}
	if !has {
		if _, err := tx.Exec(`ALTER TABLE operators ADD COLUMN labels TEXT NOT NULL DEFAULT '[]'`); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`PRAGMA user_version = 12`); err != nil {
		return err
	}
	return tx.Commit()
}
