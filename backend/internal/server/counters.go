package server

import (
	"sync/atomic"
	"time"
)

// Metrics are counters the process keeps about itself, exposed in the Prometheus text format by hand (no dependency). They are
// process-wide: the server may serve several organisations, and these describe the process, not one tenant. They are
// only ever counts and gauges; they carry no names, addresses or cluster data.
type metrics struct {
	Started        time.Time
	SyncsApplied   atomic.Int64
	SyncsChunked   atomic.Int64 // of which arrived as several messages
	SyncsRefused   atomic.Int64
	AuthFailures   atomic.Int64
	RateLimited    atomic.Int64
	StreamPanics   atomic.Int64
	StoreErrors    atomic.Int64
	TierChanges    atomic.Int64
	ConsentChanges atomic.Int64
	// ImplausibleFacts is how many individual field values, across every agent, have been omitted
	// (never clamped) for being physically impossible - a negative count, a percentage outside 0-100 -
	// rather than the whole report being refused the way SyncsRefused above is. See
	// boundSyncFacts/boundFlowFacts for exactly what this counts.
	ImplausibleFacts atomic.Int64
	// FlowItemsDropped is how many individual flows, pod flows or collector reports, across every agent, were
	// dropped from an otherwise accepted flow batch for being unusable (see sanitizeFlowBatch).
	FlowItemsDropped atomic.Int64
}

// Metrics is the process's counters. Tests read differences, not absolute values, since one process runs many servers.
var Metrics = &metrics{Started: time.Now()}
