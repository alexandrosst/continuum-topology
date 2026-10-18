package kernel

import (
	"fmt"
	"testing"
	"time"
)

func TestFailureTrackerBacksOffExponentiallyUpToACap(t *testing.T) {
	ft := NewFailureTracker()
	now := time.Unix(2_000_000, 0)
	var waits []time.Duration
	for i := 0; i < 20; i++ {
		waits = append(waits, ft.Fail("alex", now))
		now = now.Add(waits[i] + time.Millisecond) // the next attempt comes right after the wait
	}
	want := []time.Duration{0, 0, 0, 0, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second, 64 * time.Second, 128 * time.Second, 256 * time.Second, failCap, failCap}
	for i, w := range want {
		if waits[i] != w {
			t.Errorf("failure %d earned %v, want %v", i+1, waits[i], w)
		}
	}
	for _, w := range waits {
		if w > failCap {
			t.Fatalf("wait %v is over the cap", w)
		}
	}
	// blocked reports the remaining wait, and it ends
	ft = NewFailureTracker()
	for i := 0; i < 6; i++ {
		ft.Fail("bob", now)
	}
	if d := ft.Blocked("bob", now); d != 2*time.Second {
		t.Fatalf("blocked = %v", d)
	}
	if d := ft.Blocked("bob", now.Add(3*time.Second)); d != 0 {
		t.Fatalf("still blocked after the wait: %v", d)
	}
	if ft.Blocked("someone else", now) != 0 {
		t.Fatal("one name's failures affected another")
	}
	ft.Succeed("bob")
	if ft.Blocked("bob", now) != 0 || ft.Fail("bob", now) != 0 {
		t.Fatal("a success did not reset the count")
	}
	// idle names are forgotten, and memory is bounded
	ft = NewFailureTracker()
	ft.max = 50
	for i := 0; i < 500; i++ {
		ft.Fail(fmt.Sprint("name", i), now)
	}
	if len(ft.names) != 50 || ft.order.Len() != 50 {
		t.Fatalf("tracker holds %d names", len(ft.names))
	}
	ft2 := NewFailureTracker()
	for i := 0; i < 8; i++ {
		ft2.Fail("old", now)
	}
	if ft2.Fail("old", now.Add(failFor+time.Minute)) != 0 {
		t.Fatal("a failure from long ago still counted")
	}
}
