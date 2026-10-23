package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"ikhnos/internal/sealing"
)

// What each sealed value is bound to. The same names are used wherever a sealed value is made or opened.
const (
	sealTOTP  = "totp"
	sealCheck = "check"
)

// sealingCheckKey is a settings row that belongs to no organisation. It holds one sealed word, and its presence is what says this database
// holds sealed secrets: a server started without the key, or with another one, finds it and stops, instead of running with logins that
// cannot be checked.
const sealingCheckKey = "_sealing.check"

// sealingScrubKey is a settings row that says the database was sealed for the first time and has not yet been rewritten (see ScrubPlaintext).
const sealingScrubKey = "_sealing.scrub-pending"

// ErrSealingKeyNeeded is a database that holds sealed secrets, opened by a server that was given no key.
var ErrSealingKeyNeeded = errors.New("this database holds sealed secrets and needs its sealing key")

// UseSealer turns sealing on for this database and seals what is in it. It is called once, at startup, before anything reads a secret.
//
// With a sealer: the first time, the check row is written and every secret already stored is sealed; later, the key must open the check row (so a
// wrong key stops the server here and not at the first login), and anything sealed under an older key of the file is sealed again under the
// first. Without one (nil, or off): a database that was never sealed goes on as it was; one that was is refused.
//
// Only the second-factor secrets are sealed here; the documents that carry a secret of their own (the mail configuration, an organisation's
// settings) seal that field themselves, through the same sealer, and are brought up to date by the code that owns them.
func (s *SQLite) UseSealer(ctx context.Context, sl *sealing.Sealer) error {
	check, err := s.GetSettings(ctx, sealingCheckKey)
	if err != nil {
		return err
	}
	if !sl.Enabled() {
		if check != nil {
			return fmt.Errorf("%w: start the server with --sealing-key-file (the key that sealed it is %s)", ErrSealingKeyNeeded, sealing.SealedUnder(string(check)))
		}
		s.sealer = nil
		return nil
	}
	if check != nil {
		word, err := sl.Open(sealCheck, "", string(check))
		var unknown *sealing.UnknownKeyError
		switch {
		case errors.As(err, &unknown):
			return fmt.Errorf("the sealing key file holds %s, and this database was sealed under %s: put that key in the file (a replaced key stays below the new one until nothing is sealed under it)", strings.Join(sl.KeyIDs(), ", "), unknown.ID)
		case err != nil:
			return fmt.Errorf("the sealing check of this database does not open: %w", err)
		case word != "ok":
			return errors.New("the sealing check of this database holds an unexpected word")
		}
	}
	if check == nil || !sl.Current(string(check)) {
		sealed, err := sl.Seal(sealCheck, "", "ok")
		if err != nil {
			return err
		}
		// Written before any value is sealed: a start that stops half way leaves a database that asks for its key, never one that holds
		// sealed values and does not know it.
		if err := s.PutSettings(ctx, sealingCheckKey, []byte(sealed), time.Now()); err != nil {
			return err
		}
		if check == nil {
			// The first time: whatever was stored in the clear before is about to be replaced, and SQLite leaves the old text in the file.
			if err := s.PutSettings(ctx, sealingScrubKey, []byte("pending"), time.Now()); err != nil {
				return err
			}
		}
	}
	s.sealer = sl
	return s.resealTOTP(ctx)
}

// resealTOTP seals the second-factor secrets that are plain, and the ones sealed under a key that is no longer the first.
func (s *SQLite) resealTOTP(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id, totp_secret FROM users WHERE totp_secret<>''`)
	if err != nil {
		return err
	}
	type change struct{ id, stored string }
	var changes []change
	for rows.Next() {
		var id, stored string
		if err := rows.Scan(&id, &stored); err != nil {
			rows.Close()
			return err
		}
		next, changed, err := s.sealer.Reseal(sealTOTP, id, stored)
		if err != nil {
			rows.Close()
			return fmt.Errorf("the second-factor secret of user %s: %w", id, err)
		}
		if changed {
			changes = append(changes, change{id, next})
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, c := range changes {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET totp_secret=? WHERE id=?`, c.stored, c.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Sealer is the sealer in use (nil when sealing is off), for the documents that seal a secret of their own.
func (s *SQLite) Sealer() *sealing.Sealer { return s.sealer }

// DiscardSealedTOTP turns off the second factor of every user whose secret is sealed and returns their usernames. It is for a key that is
// lost: the secrets cannot be opened again, and the people must set the factor up anew. A user whose secret is not sealed is untouched.
func (s *SQLite) DiscardSealedTOTP(ctx context.Context) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id, username FROM users WHERE totp_secret LIKE 'sealed:v1:%' ORDER BY username`)
	if err != nil {
		return nil, err
	}
	var ids, names []string
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return nil, err
		}
		ids, names = append(ids, id), append(names, name)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET totp_secret='', totp_enabled_at=NULL, totp_recovery=? WHERE id=?`, totpRecoveryJSON(nil), id); err != nil {
			return nil, err
		}
	}
	return names, tx.Commit()
}

// DropSealingCheck removes the row that says this database holds sealed secrets. Called last by the recovery from a lost key, after the sealed
// values are gone, so that an interrupted run leaves a database that still asks for its key.
func (s *SQLite) DropSealingCheck(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM settings WHERE org_id=?`, sealingCheckKey)
	return err
}

// ScrubPlaintext rewrites the database file after sealing was turned on for the first time. Replacing a stored value does not remove it from
// the file: SQLite leaves the old text in a free page and in the write-ahead log until something else is written over it, so a secret that
// was stored in the clear would stay readable to anyone with a copy of the file. This rewrites the file (VACUUM) and empties the log. It is
// called at startup, after every document that holds a secret was loaded and sealed, and does nothing when nothing is pending; the pending
// mark is a row of the database, so a start that stopped before this ran is finished by the next one.
func (s *SQLite) ScrubPlaintext(ctx context.Context) error {
	pending, err := s.GetSettings(ctx, sealingScrubKey)
	if err != nil || pending == nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM settings WHERE org_id=?`, sealingScrubKey); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `VACUUM`); err != nil {
		// Put the mark back: it was not done.
		_ = s.PutSettings(ctx, sealingScrubKey, pending, time.Now())
		return fmt.Errorf("rewriting the database to remove secrets that were stored in the clear: %w", err)
	}
	var busy, logFrames, checkpointed int
	if err := s.db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointed); err != nil {
		return err
	}
	return nil
}
