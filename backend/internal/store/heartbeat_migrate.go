package store

import "database/sql"

// migrateHeartbeat adds a regional operator's optional heartbeat (PRAGMA user_version 9): the hash of the
// secret it reports with, when that secret was minted, and when a heartbeat last arrived. All three are
// nullable with no default, and NULL is exactly what every operator that predates this column means: it
// never opted in, nothing has ever been seen from it, and its health reads back as unknown. The hash is
// indexed because the heartbeat endpoint finds the operator by the secret alone (no organisation or
// operator id travels with it).
func migrateHeartbeat(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v >= 9 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, col := range []struct{ name, typ string }{
		{"heartbeat_hash", "BLOB"},
		{"heartbeat_enabled_at", "INTEGER"},
		{"last_seen_at", "INTEGER"},
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
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS operators_heartbeat_hash ON operators(heartbeat_hash)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`PRAGMA user_version = 9`); err != nil {
		return err
	}
	return tx.Commit()
}
