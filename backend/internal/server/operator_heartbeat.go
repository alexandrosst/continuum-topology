package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"sync"
	"time"

	"continuum/internal/store"
)

// Operator heartbeat. A regional operator never dials this server by default (see store.Operator): the
// server learns an operator exists from its own record, and nothing else about it. An operator can OPT IN
// to a heartbeat - its collector's own health_check endpoint is probed locally and the bare result of that
// probe is POSTed here on a timer (see the continuum-regional-operator chart's `heartbeat` values). It
// carries no telemetry: this server drains and ignores the body entirely and records only that a
// correctly-authenticated request arrived, and when. That is what powers online/offline/last-seen.

const (
	// OperatorHeartbeatInterval is how often an opted-in operator reports. The chart's default
	// heartbeat.intervalSeconds is the same number and its schema forbids a longer one, since
	// operatorOnlineWithin below is derived from this and a slower operator would flap offline.
	OperatorHeartbeatInterval = 60 * time.Second
	// operatorOnlineWithin is how long after the last heartbeat an operator still counts as online: three
	// missed intervals, so one lost or retried request does not flip it.
	operatorOnlineWithin = 3 * OperatorHeartbeatInterval
	// heartbeatWriteEvery bounds database writes: a heartbeat arriving less than this after the last one
	// recorded for that operator is acknowledged without touching the database.
	heartbeatWriteEvery = 15 * time.Second
	// maxHeartbeatBody is the most of a heartbeat's body this server will read (and discard).
	maxHeartbeatBody = 1 << 20
)

// Operator health states.
const (
	HealthUnknown = "unknown"
	HealthOnline  = "online"
	HealthOffline = "offline"
)

// OperatorHealth is what the read model says about an operator's liveness, computed from its stored
// last-seen time and the clock, never stored itself.
type OperatorHealth struct {
	State      string
	LastSeenAt *time.Time
	// Reporting is true once the operator has an active heartbeat credential AND a heartbeat has arrived.
	Reporting bool
}

// operatorHealthAt computes an operator's health at time now. unknown: no heartbeat credential, or none
// has ever arrived. online: the last heartbeat is within operatorOnlineWithin. offline otherwise - and for a
// revoked operator that was ever seen, whose heartbeats are refused from the moment it is revoked.
func operatorHealthAt(op store.Operator, now time.Time) OperatorHealth {
	if len(op.HeartbeatHash) == 0 || op.LastSeenAt == nil {
		return OperatorHealth{State: HealthUnknown}
	}
	h := OperatorHealth{LastSeenAt: op.LastSeenAt, Reporting: true, State: HealthOffline}
	if op.Status == store.OperatorActive && now.Sub(*op.LastSeenAt) <= operatorOnlineWithin {
		h.State = HealthOnline
	}
	return h
}

// heartbeatSeen remembers when a heartbeat was last written to the database per operator.
type heartbeatSeen struct {
	mu   sync.Mutex
	last map[string]time.Time
	// rl bounds how many heartbeats one operator may send, authenticated or not: a healthy one sends one a
	// minute, so this only ever bites a misconfigured exporter or someone holding a leaked secret.
	rl *Limiter
}

func newHeartbeatSeen() *heartbeatSeen {
	return &heartbeatSeen{last: map[string]time.Time{}, rl: NewLimiter(60, 20)}
}

// reserve reports whether a write is due for this operator at now, and if so claims it (so a concurrent
// heartbeat does not also write). The previous value is returned for release.
func (h *heartbeatSeen) reserve(id string, now time.Time) (prev time.Time, had, due bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	prev, had = h.last[id]
	if had && !now.Before(prev) && now.Sub(prev) < heartbeatWriteEvery {
		return prev, had, false
	}
	h.last[id] = now
	return prev, had, true
}

func (h *heartbeatSeen) release(id string, prev time.Time, had bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if had {
		h.last[id] = prev
	} else {
		delete(h.last, id)
	}
}

// errHeartbeatRejected is every way a heartbeat can fail to authenticate - malformed, unknown, replaced by
// a rotation, or belonging to a revoked operator - deliberately one error, so nothing can tell them apart.
var errHeartbeatRejected = errors.New("heartbeat rejected")

// errHeartbeatRateLimited is an authenticated operator sending far more often than it should.
var errHeartbeatRateLimited = errors.New("heartbeat rate limited")

// EnableOperatorHeartbeat mints a heartbeat secret for an existing active operator and stores its hash,
// replacing (so invalidating) any earlier one. It is both "turn it on" and "rotate it": the audit action
// says which. The secret is returned once and never stored. Nothing is sent to the operator - the person
// applies it with the commands the admin API returns.
func (c *Core) EnableOperatorHeartbeat(ctx context.Context, actor, id string) (secret string, rotated bool, err error) {
	op, err := c.operatorInOrg(ctx, id)
	if err != nil {
		return "", false, err
	}
	if op.Status != store.OperatorActive {
		return "", false, errf(KindConflict, "only an active operator can report a heartbeat")
	}
	secret, err = NewOperatorHeartbeatSecret()
	if err != nil {
		return "", false, err
	}
	rotated = len(op.HeartbeatHash) > 0
	action := "operator-heartbeat-enabled"
	if rotated {
		action = "operator-heartbeat-rotated"
	}
	// No secret, hash or prefix in the detail: the trail records that it happened, by whom, to what.
	err = c.audited(ctx, actor, action, "operator", id, "", func() error {
		if err := c.Store.SetOperatorHeartbeat(ctx, id, HashSecret(secret), c.Now()); err != nil {
			if errors.Is(err, store.ErrBadState) {
				return errf(KindConflict, "only an active operator can report a heartbeat")
			}
			if errors.Is(err, store.ErrNotFound) {
				return errf(KindNotFound, "no such regional operator")
			}
			return err
		}
		return nil
	})
	if err != nil {
		return "", false, err
	}
	return secret, rotated, nil
}

// RecordOperatorHeartbeat authenticates a heartbeat by its secret alone and notes that the operator is
// alive. It is not organisation-scoped (the caller holds no session), so call it on the platform-wide core.
// Unknown, malformed, rotated-away and revoked all return errHeartbeatRejected. Database writes are
// coalesced to one per heartbeatWriteEvery per operator; there is no audit row per heartbeat.
func (c *Core) RecordOperatorHeartbeat(ctx context.Context, secret string) error {
	if !looksLikeHeartbeatSecret(secret) {
		return errHeartbeatRejected
	}
	h := HashSecret(secret)
	op, err := c.Store.GetOperatorByHeartbeatHash(ctx, h)
	if errors.Is(err, store.ErrNotFound) {
		return errHeartbeatRejected
	}
	if err != nil {
		return err
	}
	// The row was found by this hash already; comparing again in constant time keeps the decision itself
	// independent of how the database matched, and is the check a reader expects to see.
	if subtle.ConstantTimeCompare(op.HeartbeatHash, h) != 1 || op.Status != store.OperatorActive {
		return errHeartbeatRejected
	}
	if !c.heartbeats.rl.Allow(op.ID) {
		return errHeartbeatRateLimited
	}
	now := c.Now()
	prev, had, due := c.heartbeats.reserve(op.ID, now)
	if !due {
		return nil
	}
	if err := c.Store.TouchOperatorSeen(ctx, op.ID, now); err != nil {
		c.heartbeats.release(op.ID, prev, had)
		return err
	}
	return nil
}
