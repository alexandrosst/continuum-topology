package store

import "database/sql"

// migrateTelemetryRoutes adds a telemetry intent's per-signal-type destinations (PRAGMA user_version 13): one
// JSON object column, modality -> Destination, read and written whole like the intent's own destination.
// Existing rows get an empty object - an intent from before routes existed sends every signal to its one
// destination, which is exactly what an empty map still means.
func migrateTelemetryRoutes(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v >= 13 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	has, err := hasColumn(tx, "telemetry_intents", "routes")
	if err != nil {
		return err
	}
	if !has {
		if _, err := tx.Exec(`ALTER TABLE telemetry_intents ADD COLUMN routes TEXT NOT NULL DEFAULT '{}'`); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`PRAGMA user_version = 13`); err != nil {
		return err
	}
	return tx.Commit()
}
