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

// WriteOption changes how one store write is carried out.
type WriteOption func(*writeOptions)

type writeOptions struct{ audit *AuditEvent }

// WithAudit makes the write and its audit row one transaction: either both happen or neither does. A
// write that is refused (not found, wrong state, a constraint) leaves no row, because nothing happened.
// The event's ID is assigned by the store.
func WithAudit(e AuditEvent) WriteOption { return func(o *writeOptions) { o.audit = &e } }

func collectWriteOptions(opts []WriteOption) writeOptions {
	var o writeOptions
	for _, f := range opts {
		f(&o)
	}
	return o
}

// write runs fn in a transaction and, when WithAudit was given, adds the audit row before committing.
// An error from fn is returned unchanged so callers can still tell ErrNotFound and ErrBadState apart.
func (s *SQLite) write(ctx context.Context, opts []WriteOption, fn func(tx *sql.Tx) error) error {
	o := collectWriteOptions(opts)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	if o.audit != nil {
		if err := appendAudit(ctx, tx, *o.audit); err != nil {
			return fmt.Errorf("%w: %v", ErrAuditWrite, err)
		}
	}
	return tx.Commit()
}
