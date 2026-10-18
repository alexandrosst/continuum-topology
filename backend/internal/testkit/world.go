package testkit

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"continuum/internal/kernel"
	"continuum/internal/pki"
	"continuum/internal/store"
)

// World is what a test server stands on: a SQLite file, a CA, a clock the test moves, and the kernel Core over them. The server and the
// estate each wrap Base in their own Core, since each has its own idea of what a Core carries.
type World struct {
	Base *kernel.Core
	// Store is the database behind Base, and DBPath the file it is in, for tests that must look at what is really stored.
	Store  *store.SQLite
	DBPath string
	// Now is the clock of Base: a test moves the time by setting it.
	Now *time.Time
	Ctx context.Context
}

// NewBareWorld has no accounts and no organisation yet (BootstrapAdmin would create "org-1").
func NewBareWorld(t *testing.T) *World {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "t.db")
	st, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ca, err := pki.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base := kernel.NewCore(st, ca, "", nil)
	base.DefaultOrg = "org-1"
	now := time.Now()
	w := &World{Base: base, Store: st, DBPath: dbPath, Now: &now, Ctx: context.Background()}
	base.Now = func() time.Time { return *w.Now }
	base.EnrollRL = kernel.NewLimiter(6000, 1000) // out of the way unless a test wants it
	return w
}

// NewWorld is a server with one organisation, "org-1", owned by a user nobody signs in as.
func NewWorld(t *testing.T) *World {
	t.Helper()
	w := NewBareWorld(t)
	w.CreateOrg1(t)
	return w
}

// CreateOrg1 makes the organisation "org-1" and its owner.
func (w *World) CreateOrg1(t *testing.T) {
	t.Helper()
	u := store.User{ID: "u-owner", Username: "org1-owner", PasswordHash: "!", CreatedAt: time.Now()}
	if err := w.Store.CreateUser(w.Ctx, u); err != nil {
		t.Fatal(err)
	}
	if err := w.Store.CreateOrg(w.Ctx, store.Org{ID: "org-1", Name: "Org One", CreatedAt: time.Now(), CreatedBy: u.ID}, u.ID); err != nil {
		t.Fatal(err)
	}
}
