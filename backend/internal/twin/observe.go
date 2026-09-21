// Package twin is the server's model of the estate: what was declared by people, what was observed by agents, and
// the effective model that combines them with explicit precedence and per-attribute provenance.
//
// The principles it enforces:
//
//   - Observed data is held apart from declared intent. Nothing here writes to the workspace.
//   - Every fact says who reported it, how sure that source is, in which unit, and when it was last confirmed.
//     "Unknown" is a value of its own: an unknown capacity is not a capacity of zero.
//   - A record is never silently removed. It is live, or stale, or its agent is disconnected or revoked, or it is a
//     tombstone (gone), kept for a retention window with the time and the reason.
//   - Nothing acts on data that is not live: placement and deciders see only live targets, and everything else is
//     listed as excluded together with why.
package twin

import (
	"fmt"
	"time"
)

// State is how much a record can be trusted right now, computed from when its owner last vouched for it, whether
// that owner is connected, and the server's staleness setting. It is never stored: it is a function of the
// clock.
type State string

const (
	// Live: the agent is connected and has been heard from recently. The record describes the cluster as it is.
	Live State = "live"
	// Disconnected: the agent's stream is closed but it was heard from within the staleness window. The record is
	// probably still right; nothing new is arriving.
	Disconnected State = "disconnected"
	// Stale: the agent has not been heard from for longer than the staleness window (connected or not). What is
	// shown is the last known state, not the current one.
	Stale State = "stale"
	// Revoked: an administrator revoked the agent. Its records are kept for the retention window as the last
	// known state and are never a target of anything.
	Revoked State = "revoked"
	// Gone: the record disappeared from its agent's picture. A tombstone.
	Gone State = "gone"
	// Declared: a person authored the record; no agent observes it, so it has no observation state.
	Declared State = "declared"
)

// Actionable says whether anything may act on a record in this state (place workloads on it, recommend moves to it).
func (s State) Actionable() bool { return s == Live || s == Declared || s == "" }

// AssessInput is what decides a state.
type AssessInput struct {
	Now time.Time
	// LastObserved is the last time the owning agent vouched for the record: a sync or a heartbeat.
	LastObserved time.Time
	Connected    bool
	Revoked      bool
	RevokedAt    time.Time
	// StaleAfter is how long silence is tolerated (StaleAfterBeats times the heartbeat interval).
	StaleAfter time.Duration
}

// Observation is the answer.
type Observation struct {
	State State
	// Reason is a sentence for a person, empty when live. It contains ages, so it changes with the clock.
	Reason       string
	LastObserved time.Time
	// Age is now minus LastObserved.
	Age time.Duration
}

// Assess computes the state. Precedence: revoked, then stale (too old to trust however it is connected), then
// disconnected, then live.
func Assess(in AssessInput) Observation {
	o := Observation{LastObserved: in.LastObserved}
	if !in.LastObserved.IsZero() {
		o.Age = max(in.Now.Sub(in.LastObserved), 0)
	}
	switch {
	case in.Revoked:
		o.State = Revoked
		o.Reason = "agent revoked"
		if !in.RevokedAt.IsZero() {
			o.Reason += " " + HumanAge(in.Now.Sub(in.RevokedAt)) + " ago"
		}
	case in.LastObserved.IsZero():
		o.State = Stale
		o.Reason = "never heard from its agent"
	case o.Age > in.StaleAfter:
		o.State = Stale
		o.Reason = "stale for " + HumanAge(o.Age)
	case !in.Connected:
		o.State = Disconnected
		o.Reason = "agent disconnected; last heard " + HumanAge(o.Age) + " ago"
	default:
		o.State = Live
	}
	return o
}

// HumanAge writes a duration the way a person reads an age: "45 s", "12 min", "2 h", "3 d".
func HumanAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < 90*time.Second:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < 59*time.Minute+30*time.Second:
		return fmt.Sprintf("%d min", int((d + 30*time.Second).Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h", int((d + 30*time.Minute).Hours()))
	default:
		return fmt.Sprintf("%d d", int((d+12*time.Hour).Hours()/24))
	}
}
