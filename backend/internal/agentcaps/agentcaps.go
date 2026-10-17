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

const (
	// MaxNames is how many names the server keeps from one hello, and MaxNameLen how long one may be. They are the limits of the
	// protocol and not of this list: an agent from the future may know more names than this one, and the server keeps the
	// first MaxNames of them in order rather than refusing the stream.
	MaxNames   = 32
	MaxNameLen = 40
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
