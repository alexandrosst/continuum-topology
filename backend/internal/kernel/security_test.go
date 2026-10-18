package kernel

import (
	"fmt"
	"testing"
	"time"
)

func TestLimiterForgetsIdleKeysAndEvictsTheOldestAtTheCap(t *testing.T) {
	l := NewLimiter(60, 2)
	clock := time.Unix(1_000_000, 0)
	l.now = func() time.Time { return clock }
	for i := 0; i < 10; i++ {
		l.Allow(fmt.Sprintf("k%d", i))
	}
	if len(l.buckets) != 10 {
		t.Fatalf("buckets = %d", len(l.buckets))
	}
	clock = clock.Add(time.Minute) // every bucket has refilled: forgetting them is lossless
	l.Allow("fresh")
	if len(l.buckets) != 1 || l.order.Len() != 1 {
		t.Fatalf("idle buckets were not swept: %d left", len(l.buckets))
	}

	// At the cap a newcomer is admitted (it is not locked out by a spray of other addresses), the key used
	// longest ago is the one forgotten, and a key that is being throttled right now is kept.
	l = NewLimiter(1, 1)
	l.max = 100
	l.now = func() time.Time { return clock }
	if !l.Allow("victim") || l.Allow("victim") {
		t.Fatal("setup: the victim should be out of tokens")
	}
	for i := 0; i < 500; i++ {
		clock = clock.Add(time.Millisecond)
		if !l.Allow(fmt.Sprintf("spray%d", i)) {
			t.Fatalf("spray key %d: a new key was refused at the cap", i)
		}
		if i%10 == 0 && l.Allow("victim") {
			t.Fatal("the throttled key was forgotten and got a fresh bucket")
		}
	}
	if len(l.buckets) != 100 || l.order.Len() != 100 {
		t.Fatalf("memory is not bounded: %d keys, %d in the order", len(l.buckets), l.order.Len())
	}
	if _, ok := l.buckets["spray0"]; ok {
		t.Fatal("the oldest key was not the one evicted")
	}
	if _, ok := l.buckets["spray499"]; !ok {
		t.Fatal("the newest key was evicted")
	}
}
