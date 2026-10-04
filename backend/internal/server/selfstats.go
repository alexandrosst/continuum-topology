package server

import (
	"runtime"
	"sync"
	"syscall"
	"time"

	continuumv1 "continuum/gen/continuumv1"
)

// selfStatsWindow is how far back a self-telemetry ring (per agent, or the server's own) keeps samples -
// see selfRing's own doc comment. A var, not a const, so a test can shrink it; production code never
// changes it. One hour at whatever cadence samples actually arrive: a heartbeat every HeartbeatSeconds
// for an agent's own ring, selfStatsSampleEvery for the server's own.
var selfStatsWindow = time.Hour

// selfStatsSampleEvery is how often Hub.Run samples the server's own process self-stats (sampleSelfStats) -
// see Hub.Run's own ticker. A var, not a const, so a test can shrink it without waiting out a real hour.
var selfStatsSampleEvery = 30 * time.Second

// selfRing is a small, fixed time-window ring of self-telemetry samples - one per agent (view.self) or
// one for the server's own process (Hub.serverSelf). Samples always arrive in time order (one per
// heartbeat received, or one per tick of the server's own ticker), so eviction is a simple trim from the
// front; this is deliberately not the heap-based eviction this codebase uses elsewhere (see twin's
// Tombstones) for caches keyed by an arbitrary, not-necessarily-time-ordered key.
type selfRing[T any] struct {
	window  time.Duration
	at      func(T) time.Time
	samples []T
}

// newSelfRing builds a ring that keeps samples within window of the most recently added one's own
// timestamp, as reported by at. window<=0 falls back to selfStatsWindow, the same "0 means the real
// default" treatment Hub.FlowStaleAfter/ConsistencyEvery already use.
func newSelfRing[T any](window time.Duration, at func(T) time.Time) selfRing[T] {
	if window <= 0 {
		window = selfStatsWindow
	}
	return selfRing[T]{window: window, at: at}
}

// add appends s and evicts every sample older than window as measured from now, a plain trim from the
// front since samples arrive in time order - see selfRing's own doc comment. now is passed in rather
// than read from the clock here so a test can drive eviction without any real waiting.
func (r *selfRing[T]) add(s T, now time.Time) {
	r.samples = append(r.samples, s)
	cut := 0
	for cut < len(r.samples) && now.Sub(r.at(r.samples[cut])) > r.window {
		cut++
	}
	if cut > 0 {
		// A fresh backing array, not samples[cut:]: without this, the trimmed-off prefix's memory is
		// never released for as long as this ring (an agent's whole connection lifetime) keeps running.
		kept := make([]T, len(r.samples)-cut)
		copy(kept, r.samples[cut:])
		r.samples = kept
	}
}

// list returns a copy of the ring's current samples, oldest first, safe for the caller to range over
// after the lock that guards the ring (h.mu, for both view.self and Hub.serverSelf) has been released.
func (r *selfRing[T]) list() []T {
	if len(r.samples) == 0 {
		return nil
	}
	out := make([]T, len(r.samples))
	copy(out, r.samples)
	return out
}

// SelfStatsSample is one agent's SelfStats, as received on one heartbeat, with the server's own receipt
// time (not the agent's clock, which may have drifted - see Heartbeat.clock_skew_ms) attached. Held in
// view.self, a short fixed-window ring (selfStatsWindow) - see newSelfRing's own doc comment.
type SelfStatsSample struct {
	At                                        time.Time
	RSSBytes                                  uint64
	Goroutines                                uint32
	CPUSeconds                                float64
	FlowIntervalSeconds, ProbeIntervalSeconds uint32
	LastFlowBatchFlows, LastFlowBatchBytes    uint32
	// LinkBytesCumulative is a snapshot, taken at the same moment as this sample, of linkStats.bytes -
	// this agent's own total AgentMessage traffic received since the server started, as encoded on the
	// wire. Not part of the wire SelfStats message (the server already knows it independently, the same
	// way it already knows connects/syncs/flows/beats without the agent repeating them); carried here so
	// a caller deriving a bytes/sec rate (see bandwidthSharePct/rateBetween in admin_telemetry.go) has,
	// for every ring sample, both halves of the rate - the cumulative total and its own timestamp -
	// without a second, separately-timed read of linkStats that could race against this one.
	LinkBytesCumulative uint64
}

// noteSelfStats adds one agent's SelfStats to v.self, exactly the same "operate on v, caller already
// holds h.mu" convention noteDiagnostics (consent.go) uses - called from Hub.Connect's own Heartbeat
// case. A nil ss (an agent older than the field, or a heartbeat that for whatever reason carried none)
// is simply ignored: there is nothing to add, never a fabricated zero-valued sample.
func noteSelfStats(v *view, ss *continuumv1.SelfStats, now time.Time) {
	if ss == nil {
		return
	}
	v.self.add(SelfStatsSample{
		At: now, RSSBytes: ss.RssBytes, Goroutines: ss.Goroutines, CPUSeconds: ss.CpuSeconds,
		FlowIntervalSeconds: ss.FlowIntervalSeconds, ProbeIntervalSeconds: ss.ProbeIntervalSeconds,
		LastFlowBatchFlows: ss.LastFlowBatchFlows, LastFlowBatchBytes: ss.LastFlowBatchBytes,
		LinkBytesCumulative: uint64(v.link.bytes),
	}, now)
}

// ServerSelfStatsSample is this server process's own self-telemetry, sampled once per
// selfStatsSampleEvery by Hub.Run's own ticker (sampleSelfStats) - see Hub.serverSelf.
type ServerSelfStatsSample struct {
	At              time.Time
	RSSBytes        uint64
	Goroutines      uint32
	CPUSeconds      float64
	ConnectedAgents int
	// FlowIngestBytesPerSec is how many bytes of AgentMessage_Flows traffic, as encoded on the wire
	// (the same measure linkStats.bytes already uses), this server received across every currently-held
	// agent view, per second, since the previous sample - a proxy for this tenant's flow-ingestion load,
	// derived from a plain diff-over-elapsed-time of the cumulative flowBytes counters linkStats already
	// keeps per agent (see noteFlows/linkStats.note), never a separate always-on counter of its own. 0
	// for the very first sample taken after a restart (no previous total to diff against) and whenever
	// the diff would otherwise go negative (a tenant whose agents all disconnected and a new one's
	// counters start lower than the old sum did) - never a fabricated, misleadingly large rate.
	FlowIngestBytesPerSec float64
	// ModelCacheHits/ModelCacheMisses are twinRT's own running totals (see twin.go's Model) - how many
	// times a caller's request for the effective model was served from the short-lived cache kept there
	// versus how many times it had to be rebuilt. Cumulative since the server started, like every other
	// counter in this file; a caller wanting a rate diffs two samples the same way FlowIngestBytesPerSec
	// is derived from linkStats' own cumulative counters.
	ModelCacheHits, ModelCacheMisses uint64
	// GCPauseTotalNs/NumGC are runtime.MemStats' own cumulative GC figures (mem.PauseTotalNs/mem.NumGC),
	// read in the same ReadMemStats call this file already makes for RSSBytes. Cumulative since the
	// process started, the same "caller diffs two samples" treatment as CPUSeconds - see
	// serverTelemetrySamples' own derivation of gcPauseMsPerSec. NumGC is kept alongside it for a future
	// caller that wants average pause-per-collection, even though this pass derives no field from it on
	// its own (see SelfTelemetrySample's own doc comment for why no tile is added for it yet).
	GCPauseTotalNs uint64
	NumGC          uint32
}

// processCPUSeconds is this process's own cumulative CPU time (user + system) in seconds, from the
// kernel's per-process accounting (getrusage(RUSAGE_SELF, ...)) - the same measure, and the same
// raw-cumulative-counter-diffed-by-the-caller treatment, as the agent's own identically-named helper
// (internal/agent/diag.go) uses for SelfStats.cpu_seconds; duplicated rather than shared because the two
// packages have no existing common dependency to hang a few lines of syscall code on, and this is small
// enough that doing so would cost more clarity than it saves. 0 on the (practically never seen on this
// server's own deployment target) platform where getrusage fails outright.
func processCPUSeconds() float64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	user := float64(ru.Utime.Sec) + float64(ru.Utime.Usec)/1e6
	sys := float64(ru.Stime.Sec) + float64(ru.Stime.Usec)/1e6
	return user + sys
}

// flowIngest tracks the running total sampleSelfStats diffs to derive FlowIngestBytesPerSec - see that
// field's own doc comment. Guarded by its own lock, never Hub.mu: sampleSelfStats already releases
// Hub.mu before touching this, so the two never need to be held together.
type flowIngest struct {
	mu         sync.Mutex
	totalBytes int64
	at         time.Time
}

// SelfStatsFor returns a copy of one agent's self-telemetry history (see view.self/SelfStatsSample),
// oldest first - nil when the agent has never sent a heartbeat carrying SelfStats, or this hub holds no
// view for it at all (never approved, or forgotten after a restart with nothing restored for it yet).
// Exported so the self-telemetry API handler (admin_telemetry.go) never reaches into Hub internals
// directly - the same "small accessor, the lock stays inside the package" shape StateFor already uses.
func (h *Hub) SelfStatsFor(agentID string) []SelfStatsSample {
	h.mu.Lock()
	defer h.mu.Unlock()
	v := h.views[agentID]
	if v == nil {
		return nil
	}
	return v.self.list()
}

// ServerSelfStats returns a copy of this server process's own self-telemetry history (see
// Hub.serverSelf/ServerSelfStatsSample), oldest first.
func (h *Hub) ServerSelfStats() []ServerSelfStatsSample {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.serverSelf.list()
}

// sampleSelfStats takes one sample of this server process's own self-telemetry and adds it to
// Hub.serverSelf - see Hub.Run's own ticker for when this runs in production, and ServerSelfStatsSample's
// own doc comment for what each field means. now is passed in (h.C.Now(), which tests can fix) rather
// than read from time.Now() here, the same injectable-clock discipline the rest of this package follows.
func (h *Hub) sampleSelfStats(now time.Time) {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	h.mu.Lock()
	connected := len(h.sessions)
	var totalFlowBytes int64
	for _, v := range h.views {
		totalFlowBytes += v.link.flowBytes
	}
	h.mu.Unlock()

	h.flow.mu.Lock()
	var rate float64
	if !h.flow.at.IsZero() {
		if elapsed := now.Sub(h.flow.at).Seconds(); elapsed > 0 {
			if d := totalFlowBytes - h.flow.totalBytes; d >= 0 {
				rate = float64(d) / elapsed
			}
		}
	}
	h.flow.totalBytes, h.flow.at = totalFlowBytes, now
	h.flow.mu.Unlock()

	s := ServerSelfStatsSample{
		At: now, RSSBytes: mem.Sys, Goroutines: uint32(runtime.NumGoroutine()), CPUSeconds: processCPUSeconds(),
		ConnectedAgents: connected, FlowIngestBytesPerSec: rate,
		ModelCacheHits: h.tw.cacheHits.Load(), ModelCacheMisses: h.tw.cacheMisses.Load(),
		GCPauseTotalNs: mem.PauseTotalNs, NumGC: mem.NumGC,
	}
	h.mu.Lock()
	h.serverSelf.add(s, now)
	h.mu.Unlock()
}
