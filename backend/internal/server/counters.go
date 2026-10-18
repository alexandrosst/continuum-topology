package server

import (
	"sync/atomic"
	"time"
)

// Metrics are counters the process keeps about itself, exposed in the Prometheus text format by hand (no dependency). They are
// process-wide: the server may serve several organisations, and these describe the process, not one tenant. They are
// only ever counts and gauges; they carry no names, addresses or cluster data.
type metrics struct {
	started        time.Time
	syncsApplied   atomic.Int64
	syncsChunked   atomic.Int64 // of which arrived as several messages
	syncsRefused   atomic.Int64
	authFailures   atomic.Int64
	rateLimited    atomic.Int64
	streamPanics   atomic.Int64
	storeErrors    atomic.Int64
	tierChanges    atomic.Int64
	consentChanges atomic.Int64
	// implausibleFacts is how many individual field values, across every agent, have been omitted
	// (never clamped) for being physically impossible - a negative count, a percentage outside 0-100 -
	// rather than the whole report being refused the way syncsRefused above is. See
	// boundSyncFacts/boundFlowFacts for exactly what this counts.
	implausibleFacts atomic.Int64
	// flowItemsDropped is how many individual flows, pod flows or collector reports, across every agent, were
	// dropped from an otherwise accepted flow batch for being unusable (see sanitizeFlowBatch).
	flowItemsDropped atomic.Int64
}

// Metrics is the process's counters. Tests read differences, not absolute values, since one process runs many servers.
var Metrics = &metrics{started: time.Now()}

// SyncsChunked is how many applied pictures arrived as several messages (for tests and tooling).
func (m *metrics) SyncsChunked() int64 { return m.syncsChunked.Load() }
