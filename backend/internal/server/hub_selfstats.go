package server

import (
	"runtime"
	"time"
)

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
	return v.Self.List()
}

// ServerSelfStats returns a copy of this server process's own self-telemetry history (see
// Hub.serverSelf/ServerSelfStatsSample), oldest first.
func (h *Hub) ServerSelfStats() []ServerSelfStatsSample {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.serverSelf.List()
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
		totalFlowBytes += v.Link.FlowBytes
	}
	h.mu.Unlock()

	h.flow.Mu.Lock()
	var rate float64
	if !h.flow.At.IsZero() {
		if elapsed := now.Sub(h.flow.At).Seconds(); elapsed > 0 {
			if d := totalFlowBytes - h.flow.TotalBytes; d >= 0 {
				rate = float64(d) / elapsed
			}
		}
	}
	h.flow.TotalBytes, h.flow.At = totalFlowBytes, now
	h.flow.Mu.Unlock()

	s := ServerSelfStatsSample{
		At: now, RSSBytes: mem.Sys, Goroutines: uint32(runtime.NumGoroutine()), CPUSeconds: ProcessCPUSeconds(),
		ConnectedAgents: connected, FlowIngestBytesPerSec: rate,
		ModelCacheHits: h.tw.cacheHits.Load(), ModelCacheMisses: h.tw.cacheMisses.Load(),
		GCPauseTotalNs: mem.PauseTotalNs, NumGC: mem.NumGC,
	}
	h.mu.Lock()
	h.serverSelf.Add(s, now)
	h.mu.Unlock()
}
