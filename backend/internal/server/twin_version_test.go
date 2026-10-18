package server

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/store"
)

// modelStateStore fails PutModelState while told to, and counts the calls that reached it.
type modelStateStore struct {
	store.Store
	fail atomic.Bool
	puts atomic.Int64
}

func (s *modelStateStore) PutModelState(ctx context.Context, org string, m store.ModelState) error {
	s.puts.Add(1)
	if s.fail.Load() {
		return errors.New("disk full")
	}
	return s.Store.PutModelState(ctx, org, m)
}

// A version number the model has handed out must end up in the store. The version is stored when the model is read
// and has changed; if that write fails the number is still in use (a caller holds it), so a restart that read an older
// number from the store would hand it out again for different content. The write is therefore tried again until it
// succeeds, rather than forgotten because the fingerprint already looks "seen".
func TestAModelVersionThatCouldNotBeSavedIsSavedLater(t *testing.T) {
	a := newAdminRig(t)
	h := a.hub()
	ms := &modelStateStore{Store: h.C.Store}
	h.C.Store = ms
	ag := twinAgent(t, a.env, h, fp)
	h.ApplySync(ag, twinFull(fp, []*continuumv1.NodeFacts{twinNode("n1", "")}, twinWorkload("shop", "cart")), false)

	m1, _, err := h.Model(a.ctx)
	if err != nil {
		t.Fatal(err)
	}
	saved := func() store.ModelState {
		t.Helper()
		st, err := h.C.Store.GetModelState(a.ctx, h.C.OrgID)
		if err != nil {
			t.Fatalf("no model state stored: %v", err)
		}
		return st
	}
	if got := saved(); got.Version != m1.ModelVersion {
		t.Fatalf("the first version was not stored: stored %d, handed out %d", got.Version, m1.ModelVersion)
	}

	// The store stops accepting writes; the model changes and is read.
	ms.fail.Store(true)
	h.ApplySync(ag, twinFull(fp, []*continuumv1.NodeFacts{twinNode("n1", "")}, twinWorkload("shop", "cart"), twinWorkload("shop", "pay")), true)
	*a.now = a.now.Add(3 * time.Second) // past the short cache
	m2, _, err := h.Model(a.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m2.ModelVersion <= m1.ModelVersion {
		t.Fatalf("a real change did not move the version: %d -> %d", m1.ModelVersion, m2.ModelVersion)
	}
	if got := saved(); got.Version != m1.ModelVersion {
		t.Fatalf("the store accepted a write it was told to refuse: %+v", got)
	}

	// The store recovers. Nothing about the model changes, but the next read stores the number it handed out.
	ms.fail.Store(false)
	*a.now = a.now.Add(3 * time.Second)
	m3, _, err := h.Model(a.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m3.ModelVersion != m2.ModelVersion {
		t.Fatalf("an unchanged model got a new version: %d -> %d", m2.ModelVersion, m3.ModelVersion)
	}
	if got := saved(); got.Version != m2.ModelVersion {
		t.Fatalf("the version handed out (%d) was never stored; the store holds %d, so a restart would hand out %d again for other content",
			m2.ModelVersion, got.Version, got.Version+1)
	}

	// Once stored, reading does not keep writing.
	before := ms.puts.Load()
	for range 3 {
		*a.now = a.now.Add(3 * time.Second)
		if _, _, err := h.Model(a.ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := ms.puts.Load() - before; n != 0 {
		t.Fatalf("reading an unchanged, stored model wrote %d times", n)
	}
}
