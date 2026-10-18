package agents

import (
	"testing"
	"time"
)

// TestSelfRingEvictsOnlySamplesOlderThanTheWindow covers the ring's own core contract: a sample stays
// as long as it is within window of the most recent add's own now, and is trimmed once an add's now
// pushes it past that - a plain trim from the front, since samples always arrive in time order.
func TestSelfRingEvictsOnlySamplesOlderThanTheWindow(t *testing.T) {
	r := NewSelfRing(10*time.Second, func(s SelfStatsSample) time.Time { return s.At })
	base := time.Unix(1000, 0)
	r.Add(SelfStatsSample{At: base}, base)
	r.Add(SelfStatsSample{At: base.Add(3 * time.Second)}, base.Add(3*time.Second))
	r.Add(SelfStatsSample{At: base.Add(6 * time.Second)}, base.Add(6*time.Second))
	if got := r.List(); len(got) != 3 {
		t.Fatalf("before any eviction: %d samples, want 3", len(got))
	}

	// Adding a fourth sample 9s after base (still within 10s of the very first) must not evict it yet.
	r.Add(SelfStatsSample{At: base.Add(9 * time.Second)}, base.Add(9*time.Second))
	if got := r.List(); len(got) != 4 {
		t.Fatalf("at 9s, nothing is older than the 10s window yet: %d samples, want 4", len(got))
	}

	// Past the window relative to the oldest three (ages 17s/14s/11s, all > the 10s window at this
	// point): they must go, only base+9s (8s old) and the new one survive, in order.
	now := base.Add(17 * time.Second)
	r.Add(SelfStatsSample{At: now}, now)
	got := r.List()
	if len(got) != 2 {
		t.Fatalf("after eviction: %d samples, want 2 (base+9s and the new one)", len(got))
	}
	if !got[0].At.Equal(base.Add(9 * time.Second)) {
		t.Fatalf("oldest surviving sample = %v, want base+9s", got[0].At)
	}
	if !got[len(got)-1].At.Equal(now) {
		t.Fatalf("newest sample = %v, want %v", got[len(got)-1].At, now)
	}
}

// TestSelfRingListReturnsACopy covers the documented "safe to range over after the lock is released"
// contract: mutating the slice list() returned must never reach back into the ring's own backing array.
func TestSelfRingListReturnsACopy(t *testing.T) {
	r := NewSelfRing(time.Hour, func(s SelfStatsSample) time.Time { return s.At })
	at := time.Unix(1, 0)
	r.Add(SelfStatsSample{At: at, RSSBytes: 111}, at)
	got := r.List()
	got[0].RSSBytes = 999
	if r.samples[0].RSSBytes != 111 {
		t.Fatalf("list() leaked its backing array: ring now reads %d", r.samples[0].RSSBytes)
	}
}

// TestSelfRingEmptyListIsNilNotAnEmptySlice matches this codebase's usual "nil, not an empty-but-present
// slice" convention for "nothing here yet" (compare Diagnostics.installed_telemetry_signals).
func TestSelfRingEmptyListIsNilNotAnEmptySlice(t *testing.T) {
	r := NewSelfRing(time.Hour, func(s SelfStatsSample) time.Time { return s.At })
	if got := r.List(); got != nil {
		t.Fatalf("list() on an empty ring = %#v, want nil", got)
	}
}
