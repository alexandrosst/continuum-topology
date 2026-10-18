package server

import (
	"fmt"
	"time"

	"continuum/internal/agents"
	"continuum/internal/kernel"
)

// The checklist of one pair: what it needs before its figures reach FUSION, step by step, computed by the server from what it
// knows (the agents, the pair's choices, and what each install reported it can do) and shown under the pair on the Sites screen.
// Without it a user has to work out by himself that a pair needs two clusters that each run the agent with Cluster links on, that
// the cluster that runs the pair's probes must also send telemetry onward, and that throughput tests need iperf3 in both.
//
// Every step has a state:
//   - ok:       met.
//   - todo:     not met, and the pair will not give its figures until it is.
//   - unknown:  cannot be told: the agent is too old to say what its install can do, has not reported lately, or this server
//     has no FUSION to say where the figures would go. Never a failure.
//   - optional: not needed, but would add something.

const (
	// proberFreshFor is how recently the link prober must have asked an agent for its peers to count as running (it asks about
	// once a minute).
	proberFreshFor = 10 * time.Minute
	// linkCapsStaleAfter is how long a report of what an install can do is relied on without a newer one (heartbeats come at
	// least every two minutes, so this is many missed ones: the agent is gone, or restarted as an older version).
	linkCapsStaleAfter = 15 * time.Minute
)

// Step states.
const (
	stepOK       = "ok"
	stepTodo     = "todo"
	stepUnknown  = "unknown"
	stepOptional = "optional"
)

// Step codes. They are part of the interface: the Sites screen keys on them.
const (
	stepAgent        = "agent"        // the cluster has an approved agent at access tier 2 with the flow collector running
	stepAddresses    = "addresses"    // the cluster's agent can be told which addresses of the peer to measure (only shown when it cannot)
	stepCapabilities = "capabilities" // unknown: the agent has not said what its install can do
	stepLinksOn      = "links-on"     // Cluster links (and, when the pair asks for active measurement, probing) is switched on in the install
	stepExport       = "export"       // the cluster whose measurements the pair needs sends telemetry onward
	stepProber       = "prober"       // the runner's link prober has asked its agent for peers recently
	stepCapacity     = "capacity"     // iperf3 is switched on, for a pair that asks for throughput tests
	stepPeerExport   = "peer-export"  // optional: the other cluster sends telemetry onward too
)

// LinkSetupStep is one line of a pair's checklist.
type LinkSetupStep struct {
	Code string `json:"code"`
	// Cluster is the cluster the step is about; empty when it is about the pair.
	Cluster string `json:"cluster,omitempty"`
	// State is ok | todo | unknown | optional.
	State string `json:"state"`
	// Text says what is wrong and what to do (or, for ok, what is fine), naming the cluster.
	Text string `json:"text"`
}

// LinkSetup is the checklist of one pair, in the order the pair is listed.
type LinkSetup struct {
	A     string          `json:"a"`
	B     string          `json:"b"`
	Steps []LinkSetupStep `json:"steps"`
}

// setupEnd is what linkSetup is given about one cluster of a pair.
type setupEnd struct {
	ID string
	// Name is how the cluster reads in a sentence; empty falls back to ID.
	Name     string
	HasAgent bool
	// Tier is the agent's access tier. Note is why the agent is not told about the other cluster (see endNote), nil if it is.
	Tier int
	Note *LinkNote
	Caps agents.LinkCaps
}

func (e setupEnd) name() string {
	if e.Name != "" {
		return e.Name
	}
	return e.ID
}

// fresh says whether the report of what the install can do can be relied on.
func (e setupEnd) fresh(now time.Time) bool {
	return !e.Caps.Reported.IsZero() && now.Sub(e.Caps.Reported) <= linkCapsStaleAfter
}

// ready says whether the end can measure at all: an approved agent that is told about the other cluster.
func (e setupEnd) ready() bool { return e.HasAgent && e.Note == nil }

// linkSetup works out the checklist of one pair. It is a pure function of its arguments, so that every step and every state can be
// tested without a hub: now is the time to compare the prober's last visit and the report's age with; haveFusion says whether this
// server has a FUSION at all (without one it cannot say where the figures would go, and says so rather than call it a failure).
func linkSetup(l kernel.LinkSetting, a, b setupEnd, haveFusion bool, now time.Time) []LinkSetupStep {
	var out []LinkSetupStep
	add := func(code string, e *setupEnd, state, format string, args ...any) {
		s := LinkSetupStep{Code: code, State: state, Text: fmt.Sprintf(format, args...)}
		if e != nil {
			s.Cluster = e.ID
		}
		out = append(out, s)
	}
	ends := []*setupEnd{&a, &b}
	other := func(e *setupEnd) *setupEnd {
		if e == &a {
			return &b
		}
		return &a
	}
	// Active measurement (the one-way probe, the connect timing, the throughput tests) is asked for unless the pair is passive only.
	active := !l.NoProbe || l.Throughput != nil
	// The cluster whose measurements the pair gives: the one whose id sorts first, or the other while the first cannot measure (the
	// server then lets the second probe, see peerList). Throughput tests are run by the first only.
	runner, second := &a, &b
	if linkRunner(a.ID, b.ID) == b.ID {
		runner, second = &b, &a
	}
	if !l.NoProbe && !runner.ready() && second.ready() {
		runner, second = second, runner
	}

	// 1. An agent that is told about the other cluster.
	for _, e := range ends {
		switch {
		case !e.HasAgent:
			add(stepAgent, e, stepTodo, "%s has no approved agent, so it can neither measure its pairs nor be measured. Install the Ikhnos agent there and approve it under Agents.", e.name())
		case e.Note != nil && e.Note.Code == "tier":
			add(stepAgent, e, stepTodo, "The agent of %s is at access tier %d. Link measuring needs tier 2, so the agent is not told which clusters to measure. Raise its access tier to 2 under Agents.", e.name(), e.Tier)
		case e.Note != nil && e.Note.Code == "paused":
			add(stepAgent, e, stepTodo, "The flow collector is paused for the agent of %s, which pauses link measuring there too. Resume it under Agents.", e.name())
		default:
			add(stepAgent, e, stepOK, "%s has an approved agent at access tier 2 or higher, with the flow collector not paused.", e.name())
		}
	}
	for _, e := range ends {
		if e.HasAgent && e.Note != nil && e.Note.Code != "tier" && e.Note.Code != "paused" {
			add(stepAddresses, e, stepTodo, "%s: %s", e.name(), e.Note.Text)
		}
	}

	// 2. What each install can do. An agent that has not said (too old, or silent for a while) is not called missing.
	known := func(e *setupEnd) bool { return e.HasAgent && e.fresh(now) }
	for _, e := range ends {
		if !e.HasAgent {
			continue
		}
		c := e.Caps
		switch {
		case c.Reported.IsZero():
			add(stepCapabilities, e, stepUnknown, "The agent of %s is too old to say what its install can do, so whether Cluster links, probing, telemetry and iperf3 are switched on there cannot be checked.", e.name())
		case !e.fresh(now):
			add(stepCapabilities, e, stepUnknown, "The agent of %s has not reported lately, so what its install can do is not known right now.", e.name())
		case !c.LinkMetrics:
			add(stepLinksOn, e, stepTodo, "Cluster links is not switched on in the install of %s, so it neither measures nor can be measured. Turn on Cluster links in the telemetry settings of that install.", e.name())
		case active && !c.ProbeEnabled:
			add(stepLinksOn, e, stepTodo, "Cluster links is on in %s but probing is off, so there is no prober or responder there for this pair. Turn on Probing under Cluster links in the telemetry settings of that install.", e.name())
		case active:
			add(stepLinksOn, e, stepOK, "Cluster links and probing are switched on in %s.", e.name())
		default:
			add(stepLinksOn, e, stepOK, "Cluster links is switched on in %s.", e.name())
		}
	}

	// 3. The measurements leave the cluster that makes them.
	switch {
	case active && !haveFusion:
		if runner.HasAgent {
			add(stepExport, runner, stepUnknown, "This server has no FUSION, so it cannot tell whether what %s measures for this pair arrives anywhere. It reaches FUSION only through the telemetry %s sends; check that on the server that has the FUSION.", runner.name(), runner.name())
		}
	case active:
		if known(runner) {
			if runner.Caps.TelemetryExport {
				add(stepExport, runner, stepOK, "%s runs the probes for this pair and sends its telemetry onward, so what it measures can reach FUSION.", runner.name())
			} else {
				add(stepExport, runner, stepTodo, "%s runs the probes for this pair but has no telemetry sent to FUSION, so what it measures stays in the cluster. Turn on telemetry with Cluster links there and point it at a regional operator or backend.", runner.name())
			}
		}
	case !haveFusion:
		// Passive only, and nothing to say it to: the same single sentence below.
		if known(&a) || known(&b) {
			add(stepExport, nil, stepUnknown, "This server has no FUSION, so it cannot tell whether the traffic series of %s and %s arrive anywhere. They reach FUSION only through the telemetry each cluster sends.", a.name(), b.name())
		}
	default:
		// Passive only: each cluster's own traffic series leave through its own telemetry, and one cluster sending is enough to see the pair.
		for _, e := range ends {
			if !known(e) {
				continue
			}
			o := other(e)
			switch {
			case e.Caps.TelemetryExport:
				add(stepExport, e, stepOK, "%s sends its telemetry onward, so the traffic it sees on this link can reach FUSION.", e.name())
			case known(o) && o.Caps.TelemetryExport:
				add(stepExport, e, stepOptional, "%s sends no telemetry to FUSION. Optional here: %s does, and sending from both would also give FUSION the traffic series of %s and its half of the path MTU.", e.name(), o.name(), e.name())
			case o.HasAgent && !known(o):
				add(stepExport, e, stepUnknown, "%s sends no telemetry to FUSION, and whether %s does cannot be checked, so it is not known whether the traffic on this link reaches FUSION.", e.name(), o.name())
			default:
				add(stepExport, e, stepTodo, "%s has no telemetry sent to FUSION, so the traffic it sees on this link stays in the cluster. Turn on telemetry with Cluster links there and point it at a regional operator or backend.", e.name())
			}
		}
	}

	// 4. The runner's prober is asking for the peers.
	if active && known(runner) && runner.Caps.LinkMetrics && runner.Caps.ProbeEnabled {
		seen := runner.Caps.ProberSeen
		switch {
		case seen.IsZero():
			add(stepProber, runner, stepTodo, "The link prober in %s has not asked its agent for the clusters to measure, so nothing is probed from there. Check that its link prober pod is running and can reach the agent.", runner.name())
		case now.Sub(seen) > proberFreshFor:
			add(stepProber, runner, stepTodo, "The link prober in %s last asked its agent for the clusters to measure %s ago, so probing from there has stopped. Check that its link prober pod is running and can reach the agent.", runner.name(), setupAgo(now.Sub(seen)))
		default:
			add(stepProber, runner, stepOK, "The link prober in %s is asking its agent for the clusters to measure.", runner.name())
		}
	}

	// 5. Throughput tests need iperf3 in both clusters: the first runs them, the other answers.
	if l.Throughput != nil {
		first := &a
		if linkRunner(a.ID, b.ID) == b.ID {
			first = &b
		}
		for _, e := range []*setupEnd{first, other(first)} {
			if !known(e) {
				continue
			}
			role := "answers the throughput tests of this pair"
			if e == first {
				role = "runs the throughput tests of this pair"
			}
			if e.Caps.CapacityEnabled {
				add(stepCapacity, e, stepOK, "iperf3 is switched on in %s, which %s.", e.name(), role)
			} else {
				add(stepCapacity, e, stepTodo, "iperf3 is not switched on in %s, which %s. Turn on Throughput tests under Cluster links in the telemetry settings of that install (in both clusters).", e.name(), role)
			}
		}
	}

	// 6. Optional: the other cluster sends telemetry too.
	if active && haveFusion && known(second) {
		if second.Caps.TelemetryExport {
			add(stepPeerExport, second, stepOK, "%s also sends its telemetry onward, which adds its traffic series and its half of the path MTU in FUSION.", second.name())
		} else {
			add(stepPeerExport, second, stepOptional, "%s sends no telemetry to FUSION. Optional: with it, FUSION would also hold its traffic series and its half of the path MTU.", second.name())
		}
	}
	return out
}

// ago says a duration the way the checklist wants it: "12 minutes", "3 hours", "2 days".
func setupAgo(d time.Duration) string {
	switch {
	case d < time.Hour:
		return setupPlural(int(d/time.Minute), "minute")
	case d < 48*time.Hour:
		return setupPlural(int(d/time.Hour), "hour")
	}
	return setupPlural(int(d/(24*time.Hour)), "day")
}

func setupPlural(n int, w string) string {
	if n == 1 {
		return "1 " + w
	}
	return fmt.Sprintf("%d %ss", n, w)
}
