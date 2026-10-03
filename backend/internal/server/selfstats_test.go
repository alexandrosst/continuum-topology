package server

import (
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
)

// TestSelfRingEvictsOnlySamplesOlderThanTheWindow covers the ring's own core contract: a sample stays
// as long as it is within window of the most recent add's own now, and is trimmed once an add's now
// pushes it past that - a plain trim from the front, since samples always arrive in time order.
func TestSelfRingEvictsOnlySamplesOlderThanTheWindow(t *testing.T) {
	r := newSelfRing(10*time.Second, func(s SelfStatsSample) time.Time { return s.At })
	base := time.Unix(1000, 0)
	r.add(SelfStatsSample{At: base}, base)
	r.add(SelfStatsSample{At: base.Add(3 * time.Second)}, base.Add(3*time.Second))
	r.add(SelfStatsSample{At: base.Add(6 * time.Second)}, base.Add(6*time.Second))
	if got := r.list(); len(got) != 3 {
		t.Fatalf("before any eviction: %d samples, want 3", len(got))
	}

	// Adding a fourth sample 9s after base (still within 10s of the very first) must not evict it yet.
	r.add(SelfStatsSample{At: base.Add(9 * time.Second)}, base.Add(9*time.Second))
	if got := r.list(); len(got) != 4 {
		t.Fatalf("at 9s, nothing is older than the 10s window yet: %d samples, want 4", len(got))
	}

	// Past the window relative to the oldest three (ages 17s/14s/11s, all > the 10s window at this
	// point): they must go, only base+9s (8s old) and the new one survive, in order.
	now := base.Add(17 * time.Second)
	r.add(SelfStatsSample{At: now}, now)
	got := r.list()
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
	r := newSelfRing(time.Hour, func(s SelfStatsSample) time.Time { return s.At })
	at := time.Unix(1, 0)
	r.add(SelfStatsSample{At: at, RSSBytes: 111}, at)
	got := r.list()
	got[0].RSSBytes = 999
	if r.samples[0].RSSBytes != 111 {
		t.Fatalf("list() leaked its backing array: ring now reads %d", r.samples[0].RSSBytes)
	}
}

// TestSelfRingEmptyListIsNilNotAnEmptySlice matches this codebase's usual "nil, not an empty-but-present
// slice" convention for "nothing here yet" (compare Diagnostics.installed_telemetry_signals).
func TestSelfRingEmptyListIsNilNotAnEmptySlice(t *testing.T) {
	r := newSelfRing(time.Hour, func(s SelfStatsSample) time.Time { return s.At })
	if got := r.list(); got != nil {
		t.Fatalf("list() on an empty ring = %#v, want nil", got)
	}
}

// TestNoteSelfStatsIgnoresANilSelfStats covers an agent older than the field, or a heartbeat that for
// whatever reason carried none: nothing is added, never a fabricated zero-valued sample.
func TestNoteSelfStatsIgnoresANilSelfStats(t *testing.T) {
	v := newView()
	noteSelfStats(v, nil, time.Unix(1, 0))
	if got := v.self.list(); len(got) != 0 {
		t.Fatalf("a nil SelfStats must add nothing, got %d samples", len(got))
	}
}

// TestNoteSelfStatsCopiesEveryField covers the plain field-by-field copy from the wire message into
// SelfStatsSample, with the server's own receipt time attached (not read from the message itself, which
// carries no timestamp of its own - the agent's clock may have drifted, see Heartbeat.clock_skew_ms).
func TestNoteSelfStatsCopiesEveryField(t *testing.T) {
	v := newView()
	at := time.Unix(5000, 0)
	noteSelfStats(v, &continuumv1.SelfStats{
		RssBytes: 123 << 20, Goroutines: 42, CpuSeconds: 3.5,
		FlowIntervalSeconds: 30, ProbeIntervalSeconds: 180,
		LastFlowBatchFlows: 17, LastFlowBatchBytes: 4096,
	}, at)
	got := v.self.list()
	if len(got) != 1 {
		t.Fatalf("%d samples, want 1", len(got))
	}
	s := got[0]
	if !s.At.Equal(at) || s.RSSBytes != 123<<20 || s.Goroutines != 42 || s.CPUSeconds != 3.5 ||
		s.FlowIntervalSeconds != 30 || s.ProbeIntervalSeconds != 180 ||
		s.LastFlowBatchFlows != 17 || s.LastFlowBatchBytes != 4096 {
		t.Fatalf("sample = %+v", s)
	}
}

// TestSampleSelfStatsCountsConnectedAgentsAndDerivesTheFlowRate covers sampleSelfStats end to end: the
// connected-agent count comes from live sessions (not views, which also hold disconnected/revoked
// agents' last-known state), and FlowIngestBytesPerSec is 0 on the very first sample (nothing to diff
// against yet) and a real diff-over-elapsed-time rate on the next one - never negative, even when the
// total it is diffing against somehow goes down.
func TestSampleSelfStatsCountsConnectedAgentsAndDerivesTheFlowRate(t *testing.T) {
	h := NewHub(&Core{Now: time.Now})
	h.mu.Lock()
	h.sessions["ag-1"] = &session{}
	h.sessions["ag-2"] = &session{}
	v1, v2 := newView(), newView()
	v1.link.flowBytes, v2.link.flowBytes = 1000, 2000
	h.views["ag-1"], h.views["ag-2"] = v1, v2
	h.mu.Unlock()

	t0 := time.Unix(10_000, 0)
	h.sampleSelfStats(t0)
	samples := h.serverSelf.list()
	if len(samples) != 1 {
		t.Fatalf("%d samples, want 1", len(samples))
	}
	if samples[0].ConnectedAgents != 2 {
		t.Fatalf("connectedAgents = %d, want 2 (len(sessions), not len(views))", samples[0].ConnectedAgents)
	}
	if samples[0].FlowIngestBytesPerSec != 0 {
		t.Fatalf("first sample's rate = %v, want 0 (nothing to diff against yet)", samples[0].FlowIngestBytesPerSec)
	}
	if samples[0].RSSBytes == 0 {
		t.Fatal("rssBytes must be a real, nonzero figure for a running process")
	}

	// 10 seconds later, 5000 more flow-bytes total across both agents: 500 B/s.
	h.mu.Lock()
	v1.link.flowBytes = 4000 // +3000
	v2.link.flowBytes = 4000 // +2000
	h.mu.Unlock()
	t1 := t0.Add(10 * time.Second)
	h.sampleSelfStats(t1)
	samples = h.serverSelf.list()
	if len(samples) != 2 {
		t.Fatalf("%d samples, want 2", len(samples))
	}
	if got := samples[1].FlowIngestBytesPerSec; got != 500 {
		t.Fatalf("rate = %v, want 500 ((4000+4000 - 1000-2000) / 10s)", got)
	}

	// A total that goes down (every agent reconnected since, restarting its own counters from a lower
	// base) must never be read as a fabricated, misleadingly large negative-turned-positive rate.
	h.mu.Lock()
	v1.link.flowBytes, v2.link.flowBytes = 100, 100
	h.mu.Unlock()
	t2 := t1.Add(10 * time.Second)
	h.sampleSelfStats(t2)
	samples = h.serverSelf.list()
	if got := samples[2].FlowIngestBytesPerSec; got != 0 {
		t.Fatalf("rate after the total went down = %v, want 0, not a negative-turned-positive fabrication", got)
	}
}

// TestSampleSelfStatsCarriesTheModelCacheCounters covers the plumbing from twinRT's own cacheHits/
// cacheMisses (see TestModelCacheHitsAndMissesAreCounted) through to ServerSelfStatsSample.
func TestSampleSelfStatsCarriesTheModelCacheCounters(t *testing.T) {
	h := NewHub(&Core{Now: time.Now})
	h.tw.cacheHits.Store(7)
	h.tw.cacheMisses.Store(3)
	now := time.Unix(1, 0)
	h.sampleSelfStats(now)
	samples := h.serverSelf.list()
	if len(samples) != 1 || samples[0].ModelCacheHits != 7 || samples[0].ModelCacheMisses != 3 {
		t.Fatalf("samples = %+v", samples)
	}
}
