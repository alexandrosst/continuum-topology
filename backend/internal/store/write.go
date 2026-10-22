package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrAuditWrite means the audit row that travels with a write could not be added, so the write was not
// made either. Callers turn it into "nothing was changed".
var ErrAuditWrite = errors.New("the audit row could not be written")

// errNoChange is returned from inside a write to say "I looked, and there was nothing to change". The
// transaction is dropped and no audit row is added: a compare-and-set that lost the race did not happen.
var errNoChange = errors.New("nothing changed")

// WriteOption changes how one store write is carried out.
type WriteOption func(*writeOptions)

type writeOptions struct {
	audit *AuditEvent
	certs []OperatorCert
}

// WithAudit makes the write and its audit row one transaction: either both happen or neither does. A
// write that is refused (not found, wrong state, a constraint) leaves no row, because nothing happened.
// The event's ID is assigned by the store.
func WithAudit(e AuditEvent) WriteOption { return func(o *writeOptions) { o.audit = &e } }

// WithOperatorCert makes a certificate's ledger row part of the same transaction as the write: an operator is created with
// the receiver certificate it is about to be handed, and either both are stored or neither is. A row that cannot be added
// (a serial already recorded, the database) fails the write with ErrLedgerWrite and leaves nothing behind.
func WithOperatorCert(c OperatorCert) WriteOption {
	return func(o *writeOptions) { o.certs = append(o.certs, c) }
}

// ErrLedgerWrite means a certificate's ledger row could not be added, so the write that carried it was not made either.
var ErrLedgerWrite = errors.New("the certificate could not be recorded in the ledger")

func collectWriteOptions(opts []WriteOption) writeOptions {
	var o writeOptions
	for _, f := range opts {
		f(&o)
	}
	return o
}

// write runs fn in a transaction and, when WithAudit or WithOperatorCert was given, adds the audit row and the ledger rows before
// committing.
// An error from fn is returned unchanged so callers can still tell ErrNotFound and ErrBadState apart.
func (s *SQLite) write(ctx context.Context, opts []WriteOption, fn func(tx *sql.Tx) error) error {
	o := collectWriteOptions(opts)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		if errors.Is(err, errNoChange) {
			return nil
		}
		return err
	}
	for _, c := range o.certs {
		if err := insertOperatorCert(ctx, tx, c); err != nil {
			return fmt.Errorf("%w: %v", ErrLedgerWrite, err)
		}
	}
	if o.audit != nil {
		if err := appendAudit(ctx, tx, *o.audit); err != nil {
			return fmt.Errorf("%w: %v", ErrAuditWrite, err)
		}
	}
	return tx.Commit()
}
