package estate

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ikhnosv1 "ikhnos/gen/ikhnosv1"

	"ikhnos/internal/testkit"
)

// The Run goroutine's ticker calls rec.tick (-> scan) while POST /history/record runs Hub.ScanNow (-> scan)
// on an HTTP goroutine. Both write the recorder's bookkeeping, so they must not overlap; run with -race.
func TestRecorderTickAndScanNowDoNotRace(t *testing.T) {
	r := newHubRig(t)
	id, _, _ := r.approvedAgent(t, testkit.FP)
	r.hub.Mu.Lock()
	r.hub.Views[id] = liveView(*r.now, "n1", "n2")
	r.hub.Mu.Unlock()

	var clock atomic.Int64 // race-free test clock so only the code under test races
	base := r.now.UnixNano()
	r.hub.C.Now = func() time.Time { return time.Unix(0, base+clock.Add(int64(11*time.Second))) }

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // the HTTP handler goroutine(s)
		defer wg.Done()
		for i := 0; i < 60; i++ {
			r.hub.Mu.Lock()
			st := r.hub.Views[id].State
			if i%2 == 0 {
				delete(st.Nodes, "n2")
			} else {
				st.Nodes["n2"] = &ikhnosv1.NodeFacts{Key: "n2", Name: "n2", Ready: true}
			}
			r.hub.Mu.Unlock()
			r.hub.ScanNow(r.ctx)
		}
	}()
	go func() { // the Run ticker goroutine
		defer wg.Done()
		for i := 0; i < 60; i++ {
			r.hub.rec.changed.Store(true)
			r.hub.rec.tick(r.ctx)
		}
	}()
	wg.Wait()
}
