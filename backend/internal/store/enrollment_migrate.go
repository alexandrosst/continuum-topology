package store

import "database/sql"

// migrateEnrollment adds what approval codes, token binding and clock-skew reporting need (PRAGMA
// user_version 3): the hash of an agent's approval code and the wrong codes tried for it, the skew
// the agent reported, and the cluster a token may be bound to. Existing rows get the neutral defaults:
// an agent without a hash is a "legacy" enrollment, a token without a cluster is unbound.
func migrateEnrollment(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v >= 3 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, c := range []struct{ table, col, ddl string }{
		{"agents", "approval_hash", `ALTER TABLE agents ADD COLUMN approval_hash BLOB`},
		{"agents", "approval_attempts", `ALTER TABLE agents ADD COLUMN approval_attempts INTEGER NOT NULL DEFAULT 0`},
		{"agents", "clock_skew_ms", `ALTER TABLE agents ADD COLUMN clock_skew_ms INTEGER NOT NULL DEFAULT 0`},
		{"tokens", "expected_fp", `ALTER TABLE tokens ADD COLUMN expected_fp TEXT NOT NULL DEFAULT ''`},
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
	if _, err := tx.Exec(`PRAGMA user_version = 3`); err != nil {
		return err
	}
	return tx.Commit()
}
