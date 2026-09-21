package agent

import "time"

// The server sends the agent its timings (Config.ResyncSeconds, HeartbeatSeconds, MeasureSeconds). The
// server is not fully trusted by the agent: a compromised or buggy one must not be able to make every
// agent in every cluster resynchronise, beat or connect out every second (a denial of service on the
// cluster's API server, the network and the server itself), nor set them so far out that the agent
// looks dead or never reports. Every value taken from the server goes through one of these.
const (
	MinResync  = 60 * time.Second
	MaxResync  = 24 * time.Hour
	MinBeat    = 10 * time.Second
	MaxBeat    = 2 * time.Minute
	MinMeasure = 30 * time.Second
	MaxMeasure = time.Hour
	// MinPoll and MaxPoll bound how often a pending enrollment asks whether it was approved. The server
	// suggests the pace; it may not make every waiting agent hammer it (or go silent for an hour).
	MinPoll = 2 * time.Second
	MaxPoll = 60 * time.Second
	// MaxPollBackoff is the longest wait between polls while the server cannot be reached.
	MaxPollBackoff = 2 * time.Minute
)

func clamp(d, lo, hi time.Duration) time.Duration {
	switch {
	case d < lo: // includes zero and negative
		return lo
	case d > hi:
		return hi
	}
	return d
}

// ClampResync limits how often a full resynchronisation happens: at least every 24 hours, at most once a minute.
func ClampResync(d time.Duration) time.Duration { return clamp(d, MinResync, MaxResync) }

// ClampHeartbeat limits the heartbeat interval to 10 seconds through 2 minutes; beyond that the server's
// staleness check would flag a healthy agent.
func ClampHeartbeat(d time.Duration) time.Duration { return clamp(d, MinBeat, MaxBeat) }

// ClampMeasureInterval limits how often connection-time measurements run: not more than every 30
// seconds (each round opens TCP connections to up to 32 addresses), not less than hourly.
func ClampMeasureInterval(d time.Duration) time.Duration { return clamp(d, MinMeasure, MaxMeasure) }

// ClampPoll limits the pace the server suggests for polling a pending enrollment to 2 through 60 seconds.
func ClampPoll(d time.Duration) time.Duration { return clamp(d, MinPoll, MaxPoll) }

// Floors lowers the minimums above. It exists for tests, which need to run the same loops in
// milliseconds; a zero field keeps the real minimum. Production code never sets it.
type Floors struct{ Resync, Beat, Measure, Poll time.Duration }

func (f Floors) resync(d time.Duration) time.Duration {
	if f.Resync > 0 {
		return clamp(d, f.Resync, MaxResync)
	}
	return ClampResync(d)
}
func (f Floors) beat(d time.Duration) time.Duration {
	if f.Beat > 0 {
		return clamp(d, f.Beat, MaxBeat)
	}
	return ClampHeartbeat(d)
}
func (f Floors) measure(d time.Duration) time.Duration {
	if f.Measure > 0 {
		return clamp(d, f.Measure, MaxMeasure)
	}
	return ClampMeasureInterval(d)
}
func (f Floors) poll(d time.Duration) time.Duration {
	if f.Poll > 0 {
		return clamp(d, f.Poll, MaxPoll)
	}
	return ClampPoll(d)
}
