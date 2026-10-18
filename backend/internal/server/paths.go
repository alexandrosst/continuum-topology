package server

import (
	"sort"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/interpret"
	"continuum/internal/measure"

	"google.golang.org/protobuf/proto"
)

const (
	maxObservedTargets = 16
	roundsKept         = 5
)

// issuedTarget is an address the server asked an agent to time. Results are accepted only for
// targets the server issued, so an agent cannot make up paths for places it was never asked about.
type issuedTarget struct {
	ID     string
	Host   string
	Port   int
	Label  string
	Source string // observed | manual | address-test
}

// pathTrack keeps the last few rounds of samples for one target.
type pathTrack struct {
	t      issuedTarget
	rounds []measure.Result
	at     time.Time
}

func (p *pathTrack) add(r measure.Result, at time.Time) {
	p.rounds = append(p.rounds, r)
	if len(p.rounds) > roundsKept {
		p.rounds = p.rounds[len(p.rounds)-roundsKept:]
	}
	p.at = at
}

// summary merges the kept rounds: the median of the medians, the worst 95th percentile, the best
// minimum, and the share of failed attempts. Rounds where every attempt failed do not drag the
// round-trip times toward zero.
func (p *pathTrack) summary() (min, p50, p95, lossPct float64, samples int) {
	var meds []float64
	var failed int
	for _, r := range p.rounds {
		samples += r.Samples
		failed += r.Failed
		if r.Samples-r.Failed > 0 {
			meds = append(meds, r.P50)
			if r.P95 > p95 {
				p95 = r.P95
			}
			if min == 0 || r.Min < min {
				min = r.Min
			}
		}
	}
	if len(meds) > 0 {
		sort.Float64s(meds)
		p50 = meds[len(meds)/2]
	}
	if samples > 0 {
		lossPct = float64(failed) / float64(samples) * 100
	}
	return
}

func observedTargetID(ip string) string { return "ob-" + interpret.Hash("path", ip) }

// deriveTargets picks the addresses outside a cluster that its workloads call most: what to time to
// learn how far away the places it really talks to are. Only TCP, only real (non-noise) traffic,
// one target per address.
func deriveTargets(ft *flowTable) []issuedTarget {
	if ft == nil {
		return nil
	}
	type cand struct {
		ip    string
		port  uint32
		score float64
		best  float64
	}
	byIP := map[string]*cand{}
	for _, e := range ft.edges {
		k := e.Key
		if k.Src.Kind != continuumv1.FlowEndpoint_WORKLOAD || k.Dst.Kind != continuumv1.FlowEndpoint_EXTERNAL || k.Noise != "" || k.Port == 0 {
			continue
		}
		if k.Protocol != "" && k.Protocol != "tcp" {
			continue
		}
		if measure.CheckTarget(k.Dst.Ip, int(k.Port)) != nil {
			continue
		}
		w := float64(e.Connections) + float64(e.BytesOut+e.BytesIn)/1024
		c := byIP[k.Dst.Ip]
		if c == nil {
			c = &cand{ip: k.Dst.Ip}
			byIP[k.Dst.Ip] = c
		}
		c.score += w
		if w >= c.best {
			c.best, c.port = w, k.Port
		}
	}
	all := make([]*cand, 0, len(byIP))
	for _, c := range byIP {
		all = append(all, c)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].score != all[j].score {
			return all[i].score > all[j].score
		}
		return all[i].ip < all[j].ip
	})
	if len(all) > maxObservedTargets {
		all = all[:maxObservedTargets]
	}
	out := make([]issuedTarget, 0, len(all))
	for _, c := range all {
		out = append(out, issuedTarget{ID: observedTargetID(c.ip), Host: c.ip, Port: int(c.port), Source: "observed"})
	}
	return out
}

// configFacts is everything the Config of one agent depends on, gathered: what the agent is allowed (its tier and consent), what the
// organisation set (link peers, credentials, probe targets) and the moment. Gathering reads the store and the hub's views;
// compileConfig does not.
type configFacts struct {
	Tier      int
	ClusterID string
	Consent   Consent

	Heartbeat, Resync int32
	ServerTime        int64
	MeasureSeconds    int32

	// Peers are the link peers as the organisation's pairs make them for this cluster, before credentials are handed out.
	Peers []*continuumv1.LinkPeer
	// IperfClients are the client credentials by the id of the peer they test; IperfServer is the server side for this cluster.
	IperfClients map[string]*continuumv1.LinkIperfClient
	IperfServer  *continuumv1.LinkIperfServer

	// The candidate probe targets, in the order they are issued: chosen by an administrator, derived to test an address, and derived
	// from observed flows (only at tier 2 and above).
	ManualTargets, AddressTargets, DerivedTargets []issuedTarget
}

// configBuild is a Config being compiled and the targets issued with it.
type configBuild struct {
	cfg    *continuumv1.Config
	issued []issuedTarget
}

// A configSection is one part of an agent's Config and the facts it is made from. Each owns named fields of the Config and sets
// nothing else, so what an agent is told is the sum of its sections, and a new field has to be given to one of them
// (config_sections_test.go fails until it is). The sections are independent: none reads what another wrote.
type configSection struct {
	name   string
	fields []string // the proto names of the Config fields this section sets
	fill   func(f configFacts, b *configBuild)
}

// configSections are the parts of a Config, in the order they are compiled.
var configSections = []configSection{
	{"policy", []string{"approved_access_tier", "paused_collectors", "excluded_namespaces"}, func(f configFacts, b *configBuild) {
		b.cfg.ApprovedAccessTier = uint32(f.Tier)
		b.cfg.PausedCollectors = append([]string(nil), f.Consent.Paused...)
		b.cfg.ExcludedNamespaces = append([]string(nil), f.Consent.Excluded...)
	}},
	{"timing", []string{"heartbeat_seconds", "resync_seconds"}, func(f configFacts, b *configBuild) {
		b.cfg.HeartbeatSeconds, b.cfg.ResyncSeconds = f.Heartbeat, f.Resync
	}},
	{"cluster", []string{"cluster_id"}, func(f configFacts, b *configBuild) {
		b.cfg.ClusterId = f.ClusterID
	}},
	{"links", []string{"link_peers", "link_iperf_server"}, func(f configFacts, b *configBuild) {
		// The iperf3 credentials follow the same gates as the peers (tier 2, flow collector not paused) and go only where the cluster
		// has the role: the client side to the runner's LinkPeer for the server, the server side to the server's own Config. The peers
		// are copied, so the facts are left as they were given.
		for _, p := range f.Peers {
			lp := proto.Clone(p).(*continuumv1.LinkPeer)
			if lp.Throughput {
				lp.IperfClient = f.IperfClients[lp.ClusterId]
			}
			b.cfg.LinkPeers = append(b.cfg.LinkPeers, lp)
		}
		if f.Tier >= 2 && !f.Consent.has("flow") {
			b.cfg.LinkIperfServer = f.IperfServer
		}
	}},
	{"probes", []string{"probe_targets", "measure_seconds"}, func(f configFacts, b *configBuild) {
		var ts []issuedTarget
		ts = append(ts, f.ManualTargets...)
		ts = append(ts, f.AddressTargets...)
		if f.Tier >= 2 {
			ts = append(ts, f.DerivedTargets...)
		}
		if f.Consent.has("measure") {
			ts = nil // the agent is asked to stop measuring; nothing is issued, so nothing measured is accepted either
		}
		if len(ts) > measure.MaxTargets {
			ts = ts[:measure.MaxTargets]
		}
		for _, t := range ts {
			b.cfg.ProbeTargets = append(b.cfg.ProbeTargets, &continuumv1.ProbeTarget{Id: t.ID, Host: t.Host, Port: uint32(t.Port)})
		}
		if len(ts) > 0 {
			b.cfg.MeasureSeconds = f.MeasureSeconds
		}
		b.issued = ts
	}},
}

// configEnvelope are the fields of a Config that belong to no section: the moment it was made, and the digest of everything else.
// Neither is something an administrator or the organisation set, so neither is part of what the digest stands for.
var configEnvelope = []string{"server_time_unix", "digest"}

// compileConfig is the whole of what an agent is told, decided from the facts alone, section by section: what it may send, how
// often to check itself, the link peers and which credentials go where, and which targets it measures. It returns the Config, with
// its digest, and the targets it issued. It reads nothing and records nothing.
func compileConfig(f configFacts) (*continuumv1.Config, []issuedTarget) {
	b := &configBuild{cfg: &continuumv1.Config{}}
	for _, s := range configSections {
		s.fill(f, b)
	}
	b.cfg.ServerTimeUnix = f.ServerTime
	b.cfg.Digest = configHash(b.cfg)
	return b.cfg, b.issued
}

// configEchoGrace is how long a Config is given to be applied and echoed before a heartbeat that still names an older one is taken
// to mean it was lost. A heartbeat follows every applied Config at once, so this only has to cover the queue and the network.
const configEchoGrace = 15 * time.Second

// noteConfigEcho records the digest an agent says is in force and reports whether the current Config must be sent again: the
// agent named one, it is not the one last sent, and the last send is old enough that it cannot still be on its way. It clears
// cfgHash so the next pushOne sends whatever is current. An empty digest (an older agent) never asks for anything. The caller
// holds h.mu.
func (v *netView) noteConfigEcho(digest string, now time.Time) bool {
	if digest == "" {
		return false
	}
	prev := v.cfgApplied
	v.cfgApplied = digest
	if digest == v.cfgHash && prev != digest {
		v.cfgInForceAt = now
	}
	if v.cfgHash == "" || digest == v.cfgHash || now.Sub(v.cfgSentAt) < configEchoGrace {
		return false
	}
	v.cfgHash = ""
	return true
}

// sane keeps a reported time within what a TCP connect can plausibly take.
func sane(ms float64) float64 {
	if ms != ms || ms < 0 {
		return 0
	}
	if ms > 60000 {
		return 60000
	}
	return ms
}
