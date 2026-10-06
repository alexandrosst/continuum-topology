package store

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestFusionTokenRoundTripLookupTouchAndDelete(t *testing.T) {
	st := openFusionTokenStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	tok := FusionToken{ID: "fk-1", OrgID: "org-1", Name: "decider", Signals: []string{"traces", "logs"}, Namespaces: []string{"shop", "pay"},
		CreatedBy: "alex", CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour)}
	if err := st.CreateFusionToken(ctx, tok, []byte("hash-1")); err != nil {
		t.Fatal(err)
	}
	got, err := st.LookupFusionToken(ctx, []byte("hash-1"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Signals, tok.Signals) || !reflect.DeepEqual(got.Namespaces, tok.Namespaces) || len(got.Clusters) != 0 || got.LastUsed != nil || !got.ExpiresAt.Equal(tok.ExpiresAt) {
		t.Fatalf("round trip = %+v", got)
	}
	if _, err := st.LookupFusionToken(ctx, []byte("nope")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown hash = %v", err)
	}
	if err := st.TouchFusionToken(ctx, []byte("hash-1"), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, _ = st.LookupFusionToken(ctx, []byte("hash-1"))
	if got.LastUsed == nil || !got.LastUsed.Equal(now.Add(time.Minute)) {
		t.Fatalf("last used = %v", got.LastUsed)
	}
	list, err := st.ListFusionTokens(ctx, "org-1")
	if err != nil || len(list) != 1 || list[0].ID != "fk-1" {
		t.Fatalf("list = %+v %v", list, err)
	}
	if other, _ := st.ListFusionTokens(ctx, "org-2"); len(other) != 0 {
		t.Fatalf("another organisation's list = %+v", other)
	}
	// Another organisation cannot delete it.
	if err := st.DeleteFusionToken(ctx, "org-2", "fk-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-org delete = %v", err)
	}
	if err := st.DeleteFusionToken(ctx, "org-1", "fk-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.LookupFusionToken(ctx, []byte("hash-1")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete = %v", err)
	}
}

func TestPurgeFusionTokensOnlyDropsLapsedOnes(t *testing.T) {
	st := openFusionTokenStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	st.CreateFusionToken(ctx, FusionToken{ID: "old", OrgID: "o", Name: "old", CreatedAt: now.Add(-48 * time.Hour), ExpiresAt: now.Add(-24 * time.Hour)}, []byte("a"))
	st.CreateFusionToken(ctx, FusionToken{ID: "new", OrgID: "o", Name: "new", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, []byte("b"))
	if err := st.PurgeFusionTokens(ctx, now); err != nil {
		t.Fatal(err)
	}
	list, _ := st.ListFusionTokens(ctx, "o")
	if len(list) != 1 || list[0].ID != "new" {
		t.Fatalf("after purge = %+v", list)
	}
}

func openFusionTokenStore(t *testing.T) *SQLite {
	t.Helper()
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestAnUnreadableScopeIsAnErrorNotAnUnrestrictedToken(t *testing.T) {
	st := openFusionTokenStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	if err := st.CreateFusionToken(ctx, FusionToken{ID: "fk-1", OrgID: "o", Name: "n", Namespaces: []string{"shop"}, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, []byte("h")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE fusion_tokens SET namespaces='not json' WHERE id='fk-1'`); err != nil {
		t.Fatal(err)
	}
	if tok, err := st.LookupFusionToken(ctx, []byte("h")); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("an unreadable scope was returned as %+v (%v)", tok, err)
	}
	// It is still listed, with no rights, so an administrator can revoke it.
	list, err := st.ListFusionTokens(ctx, "o")
	if err != nil || len(list) != 1 || list[0].ID != "fk-1" || len(list[0].Signals) != 0 || len(list[0].Namespaces) != 0 {
		t.Fatalf("list = %+v %v", list, err)
	}
	if err := st.DeleteFusionToken(ctx, "o", "fk-1"); err != nil {
		t.Fatal(err)
	}
}
