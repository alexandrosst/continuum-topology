package store

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestLinkCredentialRowsRoundTripReplaceAndGo(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "c.db")
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Millisecond)
	put := func(org, kind, id, body string) {
		t.Helper()
		if err := s.PutLinkCredential(ctx, LinkCredential{OrgID: org, Kind: kind, ID: id, Sealed: []byte(body), CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	put("o1", "cluster", "cl-b", "one")
	put("o1", "pair", "cl-a\x1fcl-b", "two")
	put("o2", "cluster", "cl-b", "other organisation")

	got, err := s.GetLinkCredential(ctx, "o1", "cluster", "cl-b")
	if err != nil || !bytes.Equal(got.Sealed, []byte("one")) || !got.CreatedAt.Equal(now) {
		t.Fatalf("get = %+v %v", got, err)
	}
	if _, err := s.GetLinkCredential(ctx, "o1", "cluster", "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a missing row: %v", err)
	}
	if rows, err := s.ListLinkCredentials(ctx, "o1"); err != nil || len(rows) != 2 {
		t.Errorf("list for o1 = %d rows, %v (another organisation's rows must not show)", len(rows), err)
	}

	put("o1", "cluster", "cl-b", "replaced")
	if got, _ := s.GetLinkCredential(ctx, "o1", "cluster", "cl-b"); string(got.Sealed) != "replaced" {
		t.Errorf("a second put did not replace: %q", got.Sealed)
	}

	// The rows survive a restart, and the schema is created on a database that never had the table.
	s.Close()
	s, err = OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got, err := s.GetLinkCredential(ctx, "o1", "pair", "cl-a\x1fcl-b"); err != nil || string(got.Sealed) != "two" {
		t.Errorf("after reopening: %+v %v", got, err)
	}

	if err := s.DeleteLinkCredential(ctx, "o1", "cluster", "cl-b"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteLinkCredential(ctx, "o1", "cluster", "cl-b"); err != nil {
		t.Errorf("deleting a row that is gone is not an error: %v", err)
	}
	if _, err := s.GetLinkCredential(ctx, "o1", "cluster", "cl-b"); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted row still there: %v", err)
	}
	if _, err := s.GetLinkCredential(ctx, "o2", "cluster", "cl-b"); err != nil {
		t.Errorf("deleting in one organisation removed another's row: %v", err)
	}
}
