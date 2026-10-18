package server

import (
	"sync"
	"time"
)

// netView is what the server keeps per agent about the network it is told to measure and the Config it has sent: the paths and the
// targets it issued, the addresses an administrator asked it to try, and the echo that says whether its Config arrived.
type netView struct {
	// Measured network paths, keyed by the target id the server issued for them.
	paths   map[string]*pathTrack
	targets map[string]issuedTarget
	// addrTests are the addresses an administrator asked this agent to try (see address_reach.go), keyed by test id.
	addrTests map[string]*addressTest
	// reach is the last result of testing each address from this cluster, kept after the test itself has expired so the
	// senders list can show it (see address_drift.go). Keyed by address, bounded.
	reach map[string]*addrReach
	// cfgHash identifies the last Config pushed on the stream, so a new one is sent only when it differs.
	cfgHash string
	// cfgSentAt is when a Config was last put on the stream, and cfgApplied the digest the agent last said is in force (empty: it
	// has not said, as an older agent never does). A heartbeat whose digest is not cfgHash, some time after cfgSentAt, means the
	// Config did not arrive or was not applied, and it is sent again (see noteConfigEcho).
	cfgSentAt  time.Time
	cfgApplied string
	// cfgInForceAt is when the agent first named the Config now held as sent (zero until it does).
	cfgInForceAt time.Time
	// pushMu serializes the building and queueing of this agent's Configs, so they are queued in the order they were read.
	pushMu sync.Mutex
}
