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

type failDeleteTombstones struct {
	store.Store
	fail atomic.Bool
}

func (f *failDeleteTombstones) DeleteTombstones(ctx context.Context, org string, keys []store.TombstoneKey) error {
	if f.fail.Load() {
		return errors.New("disk I/O error")
	}
	return f.Store.DeleteTombstones(ctx, org, keys)
}

// A DeleteTombstones that fails is tried again on the next flush: the expired row does not stay in the database until a restart.
func TestAFailedTombstoneDeleteIsTriedAgain(t *testing.T) {
	r := newHubRig(t)
	h := r.hub
	a := twinAgent(t, r.env, h, testkit.FP)
	nodes := []*ikhnosv1.NodeFacts{testkit.TwinNode("worker-1", "4f1c0a9b2d7e4c3a8b6d5e2f1a0b9c8d")}
	cart, pay := testkit.TwinWorkload("shop", "cart"), testkit.TwinWorkload("shop", "pay")
	h.ApplySync(a, testkit.TwinFull(testkit.FP, nodes, cart, pay), false)
	*r.now = r.now.Add(time.Minute)
	h.ApplySync(a, testkit.TwinFull(testkit.FP, nodes, cart), true)
	h.State(r.ctx)
	h.Flush(r.ctx)
	if tombs, _ := r.st.ListTombstones(r.ctx, "org-1"); len(tombs) != 1 {
		t.Fatalf("setup: want 1 stored tombstone, got %d", len(tombs))
	}
	w := &failDeleteTombstones{Store: h.C.Store}
	h.C.Store = w
	defer func() { h.C.Store = w.Store }()
	*r.now = r.now.Add(8 * 24 * time.Hour) // past the seven days a tombstone is kept: the sweep wants it deleted
	w.fail.Store(true)
	h.Flush(r.ctx)
	w.fail.Store(false)
	h.Flush(r.ctx) // the store is healthy again: a retry would delete it now
	if tombs, _ := r.st.ListTombstones(r.ctx, "org-1"); len(tombs) != 0 {
		t.Errorf("%d expired tombstone row(s) stay in the database after a failed delete; Flush never retries them", len(tombs))
	}
}
