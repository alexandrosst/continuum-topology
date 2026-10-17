// Package agentcaps is the list of things an agent can say it does. An agent names them in its hello, and the server keeps the
// names it was told for as long as the stream is open. The point of a name is that "does this agent do X" is answered by the agent
// and not by a guess from its version number, or from which fields happened to be filled in.
//
// The list lives here, in a package that depends on nothing, so the agent (which declares names) and the server (which reads
// them) are written against the same source. A name is added by adding it below with a description; a name that is not below
// is not a capability, and the test beside this file refuses one that is badly formed, repeated or undescribed.
//
// A capability says what the agent does, not what it is allowed to do: permission is the tier and the consent, and they stay where
// they are. A name is never reused for something else. If a capability changes in a way an older agent does not meet, it gets a
// new name.
package agentcaps

import "sort"

// ProtocolVersion is the version of the agent protocol this build speaks. It is a counter, and it only goes up when the meaning of
// what is already on the wire changes; a new thing an agent can do is a new capability name, not a new version. 0 is an agent
// from before the version was stated, and 1 is the first that states its capabilities.
const ProtocolVersion uint32 = 1

// The protocols a server serves. An agent that states a version inside [ProtocolMin, ProtocolMax] is understood in full; outside it
// the server still serves the stream, and says so in the agent's Compatible condition. ProtocolMin is 0 because an agent from
// before the version was stated (protocol 0) is fully served; it is raised only when the server stops serving the oldest agents,
// which is a decision and not a side effect. ProtocolMax is the newest this build knows.
const (
	ProtocolMin uint32 = 0
	ProtocolMax uint32 = ProtocolVersion
)

const (
	// MaxNames is how many names the server keeps from one hello, and MaxNameLen how long one may be. They are the limits of the
	// protocol and not of this list: an agent from the future may know more names than this one, and the server keeps the
	// first MaxNames of them (in the order the agent listed them) rather than refusing the stream.
	MaxNames   = 32
	MaxNameLen = 40
	// maxScan is how many entries of a hello are looked at at all, so a hello full of rubbish costs a bounded amount.
	maxScan = 256
)

// The names. Each is lower-case words joined by hyphens.
const (
	// ConfigDigest: the heartbeat names the digest of the Config the agent has in force, so the server knows a Config arrived and
	// not only that it was sent.
	ConfigDigest = "config-digest"
	// Diagnostics: the agent gives its own account of itself (what it is, what its install allows, what it is doing about it) in
	// its hello and again in its heartbeat when it changes.
	Diagnostics = "diagnostics"
	// ReleaseInfo: the hello names the namespace and the Helm release the agent runs as, so a command printed for it can be exact.
	ReleaseInfo = "release-info"
	// SelfStats: the heartbeat carries the agent's own resource use.
	SelfStats = "self-stats"
	// LinkCapabilities: the heartbeat says which link measurements this node can make.
	LinkCapabilities = "link-capabilities"
	// ChunkedSync: the agent sends a large inventory in pieces and not in one message.
	ChunkedSync = "chunked-sync"
)

// registry holds the description of every name. It is the one place a name is defined.
var registry = map[string]string{
	ConfigDigest:     "echoes the digest of the Config it has in force in every heartbeat",
	Diagnostics:      "gives its own account of itself, and the ceiling its install sets, in its hello and heartbeat",
	ReleaseInfo:      "states the namespace and Helm release it runs as in its hello",
	SelfStats:        "reports its own resource use in its heartbeat",
	LinkCapabilities: "reports which link measurements its node can make in its heartbeat",
	ChunkedSync:      "sends a large inventory in pieces rather than in one message",
}

// Names returns every capability this build knows, sorted.
func Names() []string {
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Describe is what a name means, and false if it is not a name this build knows. A server that meets a name it does not know keeps
// it all the same: the agent may be newer.
func Describe(name string) (string, bool) {
	d, ok := registry[name]
	return d, ok
}

// Wellformed is the rule for what a name may look like: one to MaxNameLen characters, lower-case letters and digits in words
// joined by single hyphens, starting with a letter. Anything else cannot be a name the registry would ever hold, so the server
// drops it rather than storing what an agent made up or garbled.
func Wellformed(name string) bool {
	if name == "" || len(name) > MaxNameLen {
		return false
	}
	prevHyphen := true // starting as if a hyphen came before refuses a hyphen first
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z':
			prevHyphen = false
		case c >= '0' && c <= '9':
			if i == 0 {
				return false
			}
			prevHyphen = false
		case c == '-':
			if prevHyphen {
				return false
			}
			prevHyphen = true
		default:
			return false
		}
	}
	return !prevHyphen
}

// Clean is what the server keeps of the names a hello listed: each is cut to MaxNameLen, one that is still not well formed is
// dropped, repeats are dropped, at most MaxNames are kept (the first, in the order listed), and the result is sorted so two hellos
// that say the same thing are stored the same way. It never fails and never returns more than MaxNames, whatever it is given.
// A name that is well formed but not in the registry is kept: the agent may be newer than the server.
func Clean(names []string) []string {
	seen := make(map[string]bool, MaxNames)
	var out []string
	for i, n := range names {
		if i >= maxScan || len(out) >= MaxNames {
			break
		}
		if len(n) > MaxNameLen {
			n = n[:MaxNameLen]
		}
		if !Wellformed(n) || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
