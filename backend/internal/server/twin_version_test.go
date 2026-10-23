package server

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	ikhnosv1 "ikhnos/gen/ikhnosv1"
	"ikhnos/internal/store"
	"ikhnos/internal/testkit"
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
	ag := twinAgent(t, a.env, h, testkit.FP)
	h.ApplySync(ag, testkit.TwinFull(testkit.FP, []*ikhnosv1.NodeFacts{testkit.TwinNode("n1", "")}, testkit.TwinWorkload("shop", "cart")), false)

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
	h.ApplySync(ag, testkit.TwinFull(testkit.FP, []*ikhnosv1.NodeFacts{testkit.TwinNode("n1", "")}, testkit.TwinWorkload("shop", "cart"), testkit.TwinWorkload("shop", "pay")), true)
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

// blockingModelStore holds PutModelState until released, and says when a write has arrived.
type blockingModelStore struct {
	store.Store
	arrived chan struct{}
	release chan struct{}
}

func (s *blockingModelStore) PutModelState(ctx context.Context, org string, m store.ModelState) error {
	select {
	case s.arrived <- struct{}{}:
	default:
	}
	<-s.release
	return s.Store.PutModelState(ctx, org, m)
}

// A read that has to store the new version waits for the store, and nothing else may wait with it. The twin's lock is
// taken by every sync and every saved workspace; if the write were made under it, one slow disk write would stop all
// of them. The reader still gets its answer only after the version is stored.
func TestAModelVersionBeingSavedDoesNotHoldUpTheTwin(t *testing.T) {
	a := newAdminRig(t)
	h := a.hub()
	bs := &blockingModelStore{Store: h.C.Store, arrived: make(chan struct{}, 1), release: make(chan struct{})}
	h.C.Store = bs
	ag := twinAgent(t, a.env, h, testkit.FP)
	h.ApplySync(ag, testkit.TwinFull(testkit.FP, []*ikhnosv1.NodeFacts{testkit.TwinNode("n1", "")}, testkit.TwinWorkload("shop", "cart")), false)

	type result struct {
		version int64
		err     error
	}
	read := make(chan result, 1)
	go func() {
		m, _, err := h.Model(a.ctx)
		read <- result{int64(m.ModelVersion), err}
	}()
	select {
	case <-bs.arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("the read never reached the store")
	}

	// The write is in progress. Something that only needs the twin's lock must not wait for it.
	other := make(chan struct{})
	go func() {
		h.Flush(a.ctx)
		close(other)
	}()
	select {
	case <-other:
	case <-time.After(2 * time.Second):
		t.Fatal("a write of the model version holds the twin's lock")
	}
	select {
	case r := <-read:
		t.Fatalf("the read returned (%+v) before its version was stored", r)
	default:
	}

	close(bs.release)
	select {
	case r := <-read:
		if r.err != nil {
			t.Fatal(r.err)
		}
		st, err := h.C.Store.GetModelState(a.ctx, h.C.OrgID)
		if err != nil || st.Version != r.version {
			t.Fatalf("the read returned version %d but the store holds %+v (%v)", r.version, st, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the read never finished")
	}
}
