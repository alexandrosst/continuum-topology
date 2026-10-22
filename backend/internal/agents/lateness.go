package agents

import "time"

// LateAfter is how long an agent may be silent before it is called late, given how long it may be silent before its records are
// stale (the server's setting: a number of heartbeats). Late comes first and is a warning: five eighths of the way to stale, which
// is two and a half heartbeats at the default of four. The server owns the number and sends it with each agent, so a screen that says
// "late" is using the same setting as the server's "stale" and does not keep a figure of its own.
func LateAfter(stale time.Duration) time.Duration {
	return stale * 5 / 8
}
