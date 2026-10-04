package store

import "database/sql"

// migrateTelemetry adds Operator.AcceptedModalities (PRAGMA user_version 8): one JSON array column, the
// same convention source_cluster_ids already uses for a list that is read and written whole (see
// acceptedModalitiesJSON). Existing rows get an empty list, which Operator.AcceptedModalities's own
// comment says means "accepts everything" - the only behaviour an operator could have before this column
// existed, so nothing changes for it.
func migrateTelemetry(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v >= 8 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	has, err := hasColumn(tx, "operators", "accepted_modalities")
	if err != nil {
		return err
	}
	if !has {
		if _, err := tx.Exec(`ALTER TABLE operators ADD COLUMN accepted_modalities TEXT NOT NULL DEFAULT '[]'`); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`PRAGMA user_version = 8`); err != nil {
		return err
	}
	return tx.Commit()
}
