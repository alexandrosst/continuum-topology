package server

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/interpret"
	"continuum/internal/model"
)

// A cluster or machine that nobody onboarded cannot be found by asking, but it leaves marks in the
// traffic of the clusters that are onboarded: workloads calling a Kubernetes API server, a kubelet
// or an etcd at an address no onboarded cluster owns, or an overlay network port. These are
// suspicions with their evidence, for a person to act on or dismiss; they are never conclusions.

// k8sSignal is a port that says "a Kubernetes component lives here", and how strongly.
func k8sSignal(port uint32, protocol string) (what string, weight int) {
	tcp := protocol == "" || protocol == "tcp"
	switch {
	case tcp && port == 6443:
		return "Kubernetes API server (6443)", 3
	case tcp && port == 10250:
		return "kubelet (10250)", 3
	case tcp && (port == 2379 || port == 2380):
		return fmt.Sprintf("etcd (%d)", port), 3
	case tcp && port == 9345:
		return "RKE2 supervisor (9345)", 3
	case !tcp && port == 8472:
		return "overlay network, VXLAN (8472/udp)", 2
	case !tcp && port == 4789:
		return "overlay network, VXLAN (4789/udp)", 2
	case !tcp && port == 51820:
		return "overlay network, WireGuard (51820/udp)", 2
	case tcp && port >= 30000 && port <= 32767:
		return "a NodePort (30000-32767)", 1
	}
	return "", 0
}

type suspect struct {
	ip      netip.Addr
	weight  int
	signals map[string]bool
	ports   map[uint32]bool
	callers map[string]bool // "cluster/workload-name"
	conns   uint64
}

const maxSuspicions = 10

// suspicions works out, from what every cluster's agent saw, where unmanaged infrastructure may be.
func suspicions(org string, observed, located []observedCluster, now time.Time) []model.Suggestion {
	if len(observed) == 0 {
		return nil
	}
	ix := buildAddrIndex(located)
	byIP := map[netip.Addr]*suspect{}
	nodeNets := map[string]map[netip.Prefix]bool{} // cluster name -> /24s its nodes are in
	for _, c := range observed {
		for _, n := range c.state.Nodes {
			for _, s := range n.InternalIps {
				if a, err := netip.ParseAddr(s); err == nil && a.Is4() {
					if nodeNets[c.name] == nil {
						nodeNets[c.name] = map[netip.Prefix]bool{}
					}
					nodeNets[c.name][netip.PrefixFrom(a, 24).Masked()] = true
				}
			}
		}
	}
	type machine struct {
		ip      netip.Addr
		cluster string
		conns   uint64
	}
	machines := map[netip.Addr]*machine{}

	workloadName := func(c observedCluster, key string) string {
		if w := c.state.Workloads[key]; w != nil {
			return c.name + "/" + w.Name
		}
		return c.name
	}
	for _, c := range observed {
		for _, e := range c.flows.edges {
			k := e.Key
			if k.Noise != "" {
				continue
			}
			var ipStr string
			var caller string
			outbound := k.Src.Kind == continuumv1.FlowEndpoint_WORKLOAD && k.Dst.Kind == continuumv1.FlowEndpoint_EXTERNAL
			inbound := k.Src.Kind == continuumv1.FlowEndpoint_EXTERNAL && k.Dst.Kind == continuumv1.FlowEndpoint_WORKLOAD
			switch {
			case outbound:
				ipStr, caller = k.Dst.Ip, workloadName(c, k.Src.Ref)
			case inbound:
				ipStr, caller = k.Src.Ip, workloadName(c, k.Dst.Ref)
			default:
				continue
			}
			ip, err := netip.ParseAddr(ipStr)
			if err != nil {
				continue
			}
			ip = ip.Unmap()
			if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() {
				continue
			}
			if ix.clusterOf(ipStr, int(k.Port)) != "" {
				continue // an onboarded cluster's own address
			}
			if outbound {
				if what, w := k8sSignal(k.Port, k.Protocol); w > 0 {
					s := byIP[ip]
					if s == nil {
						s = &suspect{ip: ip, signals: map[string]bool{}, ports: map[uint32]bool{}, callers: map[string]bool{}}
						byIP[ip] = s
					}
					if what != "a NodePort (30000-32767)" || !s.signals[what] {
						s.weight += w
					}
					s.signals[what] = true
					s.ports[k.Port] = true
					s.callers[caller] = true
					s.conns += e.Connections
				}
			}
			if ip.Is4() {
				p := netip.PrefixFrom(ip, 24).Masked()
				if nodeNets[c.name][p] {
					m := machines[ip]
					if m == nil {
						m = &machine{ip: ip, cluster: c.name}
						machines[ip] = m
					}
					m.conns += e.Connections
				}
			}
		}
	}

	stamp := now.UTC().Format(time.RFC3339)
	var out []model.Suggestion

	// Group suspect addresses by their /24 so a whole unknown cluster is one suspicion, not one per node.
	type group struct {
		key     string
		label   string
		ips     []string
		signals map[string]bool
		callers map[string]bool
		conns   uint64
		weight  int
	}
	groups := map[string]*group{}
	inGroup := map[netip.Addr]bool{}
	for ip, s := range byIP {
		if s.weight < 3 {
			continue
		}
		key, label := ip.String(), ip.String()
		if ip.Is4() {
			p := netip.PrefixFrom(ip, 24).Masked()
			key, label = p.String(), p.String()
		}
		g := groups[key]
		if g == nil {
			g = &group{key: key, label: label, signals: map[string]bool{}, callers: map[string]bool{}}
			groups[key] = g
		}
		g.ips = append(g.ips, ip.String())
		for x := range s.signals {
			g.signals[x] = true
		}
		for x := range s.callers {
			g.callers[x] = true
		}
		g.conns += s.conns
		g.weight += s.weight
		inGroup[ip] = true
	}
	gs := make([]*group, 0, len(groups))
	for _, g := range groups {
		gs = append(gs, g)
	}
	sort.Slice(gs, func(i, j int) bool {
		if gs[i].weight != gs[j].weight {
			return gs[i].weight > gs[j].weight
		}
		return gs[i].key < gs[j].key
	})
	for _, g := range gs {
		if len(out) >= maxSuspicions {
			break
		}
		sort.Strings(g.ips)
		title := "Unknown cluster at " + g.label + "?"
		if len(g.ips) == 1 {
			title = "Unknown cluster at " + g.ips[0] + "?"
		}
		addrs := g.ips
		shown := strings.Join(addrs, ", ")
		if len(addrs) > 4 {
			shown = strings.Join(addrs[:4], ", ") + fmt.Sprintf(" and %d more", len(addrs)-4)
		}
		detail := fmt.Sprintf("%s reach%s %s, which looks like %s, %d connections seen. No onboarded cluster owns %s. If it is a Kubernetes cluster, connect it so Continuum can see what runs there; otherwise dismiss this.",
			joinSet(g.callers, 3), plural1(len(g.callers)), shown, joinKeys(g.signals), g.conns, thisThese(len(addrs)))
		out = append(out, model.Suggestion{
			ID: "sg-unk-" + interpret.Hash("cluster", g.key), OrgID: org, Kind: "infrastructure", Title: title, Detail: detail, CreatedAt: stamp, Status: "open",
			Apply: map[string]any{"type": "connect-cluster", "addresses": addrs},
		})
	}

	// Machines that share a network with a cluster's nodes without being part of it.
	ms := make([]*machine, 0, len(machines))
	for _, m := range machines {
		if inGroup[m.ip] || m.conns < 10 {
			continue
		}
		ms = append(ms, m)
	}
	sort.Slice(ms, func(i, j int) bool {
		if ms[i].conns != ms[j].conns {
			return ms[i].conns > ms[j].conns
		}
		return ms[i].ip.Less(ms[j].ip)
	})
	for _, m := range ms {
		if len(out) >= maxSuspicions {
			break
		}
		out = append(out, model.Suggestion{
			ID: "sg-unk-" + interpret.Hash("machine", m.ip.String(), m.cluster), OrgID: org, Kind: "infrastructure",
			Title:     fmt.Sprintf("%s is on the same network as %s's nodes but is not one of them", m.ip, m.cluster),
			Detail:    fmt.Sprintf("%d connections were seen between %s's workloads and %s. It may be a server, a device or a machine outside the cluster that the model does not know about. Add it under Devices or as an external endpoint if it belongs in the picture.", m.conns, m.cluster, m.ip),
			CreatedAt: stamp, Status: "open",
		})
	}
	return out
}

func joinKeys(m map[string]bool) string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return strings.Join(ks, ", ")
}

func joinSet(m map[string]bool, most int) string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	if len(ks) > most {
		return strings.Join(ks[:most], ", ") + fmt.Sprintf(" and %d more", len(ks)-most)
	}
	return strings.Join(ks, ", ")
}

func plural1(n int) string {
	if n == 1 {
		return "es"
	}
	return ""
}

func thisThese(n int) string {
	if n == 1 {
		return "this address"
	}
	return "these addresses"
}
