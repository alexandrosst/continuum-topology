package graph

import (
	"sync"
	"testing"
)

// These exercise only the lock-map bookkeeping (no Neo4j needed): an org's mutex is created lazily on
// first use and must be reclaimed once the org is gone, or it sits in the map for the life of the process.

func TestDBForgetsAnOrgsLockOnceItsGone(t *testing.T) {
	d := &DB{locks: map[string]*sync.Mutex{}}
	unlock := d.lock("org-1")
	unlock()
	if _, ok := d.locks["org-1"]; !ok {
		t.Fatal("lock should exist after first use")
	}
	d.forgetLock("org-1")
	if _, ok := d.locks["org-1"]; ok {
		t.Error("forgetLock should have removed the org's entry")
	}
	// forgetting an org that was never locked, or twice, must not panic
	d.forgetLock("never-locked")
	d.forgetLock("org-1")
}

func TestStoreForgetsBothAnOrgsLocksOnceItsGone(t *testing.T) {
	s := &Store{locks: map[string]*sync.Mutex{}}
	s.lock("org-1")()
	s.lock("ev:org-1")()
	s.lock("org-2")()
	if len(s.locks) != 3 {
		t.Fatalf("locks = %v, want 3 entries", s.locks)
	}
	s.forgetLocks("org-1")
	if _, ok := s.locks["org-1"]; ok {
		t.Error("org-1's own lock should be gone")
	}
	if _, ok := s.locks["ev:org-1"]; ok {
		t.Error("org-1's event lock should be gone too")
	}
	if _, ok := s.locks["org-2"]; !ok {
		t.Error("a different org's lock must survive")
	}
}
