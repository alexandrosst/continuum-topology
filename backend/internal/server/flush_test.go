package server

import (
	continuumv1 "continuum/gen/continuumv1"
	"testing"
	"time"
)

// Tombstones and node identities are written behind a timer. A graceful shutdown must save what the timer had
// not yet reached, or every restart forgets the last interval's worth.
func TestFlushAllSavesWriteBehindStateBeforeShutdown(t *testing.T) {
	r := newHubRig(t)
	h := r.hub
	a := twinAgent(t, r.env, h, fp)
	nodes := []*continuumv1.NodeFacts{twinNode("worker-1", "4f1c0a9b2d7e4c3a8b6d5e2f1a0b9c8d")}
	cart, pay := twinWorkload("shop", "cart"), twinWorkload("shop", "pay")
	h.ApplySync(a, twinFull(fp, nodes, cart, pay), false)
	*r.now = r.now.Add(time.Minute)
	h.ApplySync(a, twinFull(fp, nodes, cart), true) // pay is gone: a tombstone, so far only in memory
	h.State(r.ctx)                                  // loads the model and builds the identities

	before, _ := r.st.ListTombstones(r.ctx, "org-1")
	if len(before) != 0 {
		t.Fatalf("setup: the timer has not run, so nothing should be stored yet, got %d tombstones", len(before))
	}

	r.plat.FlushAll(r.ctx)

	tombs, _ := r.st.ListTombstones(r.ctx, "org-1")
	ids, _ := r.st.ListIdentities(r.ctx, "org-1")
	if len(tombs) != 1 {
		t.Fatalf("FlushAll should have stored the tombstone, got %d", len(tombs))
	}
	if len(ids) == 0 {
		t.Fatal("FlushAll should have stored the node and workload identities")
	}
}
