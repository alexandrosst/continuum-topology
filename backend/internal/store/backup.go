package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
)

// BackupDatabase writes a consistent copy of the SQLite database at src into a new file dst. It is safe while the
// server is running (the copy is one read transaction, so it is a single moment in time, and it includes what is
// still in the write-ahead log) and it never migrates or otherwise changes src: it does not go through OpenSQLite.
// dst must not exist. It returns the schema version of the copy.
func BackupDatabase(ctx context.Context, src, dst string) (int, error) {
	if _, err := os.Stat(src); err != nil {
		return 0, err
	}
	if _, err := os.Stat(dst); err == nil {
		return 0, fmt.Errorf("%s already exists", dst)
	}
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=busy_timeout(10000)", src))
	if err != nil {
		return 0, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `VACUUM INTO '`+strings.ReplaceAll(dst, `'`, `''`)+`'`); err != nil {
		os.Remove(dst)
		return 0, fmt.Errorf("copying the database: %w", err)
	}
	if err := os.Chmod(dst, 0o600); err != nil {
		return 0, err
	}
	return CheckDatabase(ctx, dst)
}

// CheckDatabase verifies a database file without changing it: SQLite's own integrity check passes, and its schema
// is one this server understands (a newer one gives a SchemaNewerError). It returns the schema version.
func CheckDatabase(ctx context.Context, path string) (int, error) {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=ro&immutable=1", path))
	if err != nil {
		return 0, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var res string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&res); err != nil {
		return 0, fmt.Errorf("%s is not a readable database: %w", path, err)
	}
	if res != "ok" {
		return 0, fmt.Errorf("%s is damaged: %s", path, res)
	}
	var v int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&v); err != nil {
		return 0, err
	}
	if v > SchemaVersion {
		return v, SchemaNewerError{Have: v, Know: SchemaVersion}
	}
	return v, nil
}

// IsSchemaNewer reports whether err says a database was written by a newer server.
func IsSchemaNewer(err error) bool {
	var e SchemaNewerError
	return errors.As(err, &e) || errors.Is(err, ErrSchemaNewer)
}
