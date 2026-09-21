package main

import (
	"flag"
	"os"
	"time"

	"continuum/internal/server"
)

// The enrollment flags live here, registered before main parses the command line.
var (
	pendingTTL = flag.Duration("pending-enrollment-ttl", envDuration("CONTINUUM_PENDING_ENROLLMENT_TTL", server.DefaultPendingTTL),
		"how long an agent that enrolled may wait for an administrator to approve it before the request expires (the agent then enrolls again by itself and shows a new approval code); env CONTINUUM_PENDING_ENROLLMENT_TTL")
	refuseLegacy = flag.Bool("refuse-legacy-approval", os.Getenv("CONTINUUM_REFUSE_LEGACY_APPROVAL") == "true",
		"refuse to approve an agent that enrolled without an approval code (an older agent). By default it can still be approved by confirming its cluster fingerprint, and the UI and the audit trail mark it as a legacy enrollment; env CONTINUUM_REFUSE_LEGACY_APPROVAL=true")
)

// envDuration reads a duration such as "12h" from the environment, falling back to def when it is unset
// or does not parse.
func envDuration(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}
