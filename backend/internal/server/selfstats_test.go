package server

import (
	"runtime"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"

	"continuum/internal/kernel"
)

// TestNoteSelfStatsIgnoresANilSelfStats covers an agent older than the field, or a heartbeat that for
// whatever reason carried none: nothing is added, never a fabricated zero-valued sample.
func TestNoteSelfStatsIgnoresANilSelfStats(t *testing.T) {
	v := NewView()
	v.NoteSelfStats(nil, time.Unix(1, 0))
	if got := v.Self.List(); len(got) != 0 {
		t.Fatalf("a nil SelfStats must add nothing, got %d samples", len(got))
	}
}

// TestNoteSelfStatsCopiesEveryField covers the plain field-by-field copy from the wire message into
// SelfStatsSample, with the server's own receipt time attached (not read from the message itself, which
// carries no timestamp of its own - the agent's clock may have drifted, see Heartbeat.clock_skew_ms).
func TestNoteSelfStatsCopiesEveryField(t *testing.T) {
	v := NewView()
	at := time.Unix(5000, 0)
	v.NoteSelfStats(&continuumv1.SelfStats{
		RssBytes: 123 << 20, Goroutines: 42, CpuSeconds: 3.5,
		FlowIntervalSeconds: 30, ProbeIntervalSeconds: 180,
		LastFlowBatchFlows: 17, LastFlowBatchBytes: 4096,
	}, at)
	got := v.Self.List()
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

// TestSampleSelfStatsCountsConnectedAgentsAndDerivesTheFlowRate covers SampleSelfStats end to end: the
// connected-agent count comes from live sessions (not views, which also hold disconnected/revoked
// agents' last-known state), and FlowIngestBytesPerSec is 0 on the very first sample (nothing to diff
// against yet) and a real diff-over-elapsed-time rate on the next one - never negative, even when the
// total it is diffing against somehow goes down.
func TestSampleSelfStatsCountsConnectedAgentsAndDerivesTheFlowRate(t *testing.T) {
	h := NewHub(wrap(&kernel.Core{Now: time.Now}))
	h.Mu.Lock()
	h.Sessions["ag-1"] = &Session{}
	h.Sessions["ag-2"] = &Session{}
	v1, v2 := NewView(), NewView()
	v1.Link.FlowBytes, v2.Link.FlowBytes = 1000, 2000
	h.Views["ag-1"], h.Views["ag-2"] = v1, v2
	h.Mu.Unlock()

	t0 := time.Unix(10_000, 0)
	h.SampleSelfStats(t0)
	samples := h.serverSelf.List()
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
	h.Mu.Lock()
	v1.Link.FlowBytes = 4000 // +3000
	v2.Link.FlowBytes = 4000 // +2000
	h.Mu.Unlock()
	t1 := t0.Add(10 * time.Second)
	h.SampleSelfStats(t1)
	samples = h.serverSelf.List()
	if len(samples) != 2 {
		t.Fatalf("%d samples, want 2", len(samples))
	}
	if got := samples[1].FlowIngestBytesPerSec; got != 500 {
		t.Fatalf("rate = %v, want 500 ((4000+4000 - 1000-2000) / 10s)", got)
	}

	// A total that goes down (every agent reconnected since, restarting its own counters from a lower
	// base) must never be read as a fabricated, misleadingly large negative-turned-positive rate.
	h.Mu.Lock()
	v1.Link.FlowBytes, v2.Link.FlowBytes = 100, 100
	h.Mu.Unlock()
	t2 := t1.Add(10 * time.Second)
	h.SampleSelfStats(t2)
	samples = h.serverSelf.List()
	if got := samples[2].FlowIngestBytesPerSec; got != 0 {
		t.Fatalf("rate after the total went down = %v, want 0, not a negative-turned-positive fabrication", got)
	}
}

// TestSampleSelfStatsCapturesGCStats covers the plumbing from runtime.MemStats' own cumulative
// PauseTotalNs/NumGC (read by the same ReadMemStats call this function already makes for RSSBytes)
// through to ServerSelfStatsSample.GCPauseTotalNs/NumGC. Bounded rather than exact, since a real GC can
// run at any point around the call: both figures are monotonic counters, so a sample taken between two
// ReadMemStats calls made just before and just after it must fall within that bracket.
func TestSampleSelfStatsCapturesGCStats(t *testing.T) {
	h := NewHub(wrap(&kernel.Core{Now: time.Now}))
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	h.SampleSelfStats(time.Unix(1, 0))
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	samples := h.serverSelf.List()
	if len(samples) != 1 {
		t.Fatalf("%d samples, want 1", len(samples))
	}
	s := samples[0]
	if s.GCPauseTotalNs < before.PauseTotalNs || s.GCPauseTotalNs > after.PauseTotalNs {
		t.Fatalf("gcPauseTotalNs = %d, want between %d and %d (a monotonic counter, bracketed by reads just before/after)",
			s.GCPauseTotalNs, before.PauseTotalNs, after.PauseTotalNs)
	}
	if s.NumGC < before.NumGC || s.NumGC > after.NumGC {
		t.Fatalf("numGC = %d, want between %d and %d", s.NumGC, before.NumGC, after.NumGC)
	}
}

// TestSampleSelfStatsCarriesTheModelCacheCounters covers the plumbing from twinRT's own CacheHits/
// CacheMisses (see TestModelCacheHitsAndMissesAreCounted) through to ServerSelfStatsSample.
func TestSampleSelfStatsCarriesTheModelCacheCounters(t *testing.T) {
	h := NewHub(wrap(&kernel.Core{Now: time.Now}))
	h.Tw.CacheHits.Store(7)
	h.Tw.CacheMisses.Store(3)
	now := time.Unix(1, 0)
	h.SampleSelfStats(now)
	samples := h.serverSelf.List()
	if len(samples) != 1 || samples[0].ModelCacheHits != 7 || samples[0].ModelCacheMisses != 3 {
		t.Fatalf("samples = %+v", samples)
	}
}
