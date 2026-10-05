package store

import "database/sql"

// migrateOperatorCA adds a regional operator's private issuing CA (PRAGMA user_version 11): client_ca_cert
// (the CA certificate, public) and client_ca_key (its private key, sealed exactly as the org CA key is - see
// pki.CA.NewOperatorCA). Both are nullable, and NULL is what every existing row keeps: an mTLS operator
// created before this version has no CA of its own and goes on issuing from the org CA (a weaker scope, see
// Operator.ClientCAScope); a bearer operator never has one. Nothing is backfilled - a CA cannot be added
// to a receiver already installed, whose Secret holds the org CA.
func migrateOperatorCA(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v >= 11 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, col := range []string{"client_ca_cert", "client_ca_key"} {
		has, err := hasColumn(tx, "operators", col)
		if err != nil {
			return err
		}
		if !has {
			if _, err := tx.Exec(`ALTER TABLE operators ADD COLUMN ` + col + ` BLOB`); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(`PRAGMA user_version = 11`); err != nil {
		return err
	}
	return tx.Commit()
}
