// Package flow turns the raw connection counts that node collectors report into observed
// dependencies: inside the cluster it attributes addresses to workloads (the agent's job), and it
// carries the result to the server.
//
// Only counts travel: source, destination, port, connections and bytes. Never payloads, never
// individual connections.
package flow

import (
	"net/netip"
	"sync"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/agent/collect"
)

// remember keeps the workload a pod address belonged to for a while after the pod is gone, so short-lived
// pods that finish inside a reporting window are still attributed.
const remember = 15 * time.Minute

type recent struct {
	key string
	at  time.Time
}

// Resolver attributes addresses using the collector's index, which it rebuilds at most every few seconds.
type Resolver struct {
	source func() *collect.Index
	now    func() time.Time

	mu      sync.Mutex
	ix      *collect.Index
	builtAt time.Time
	recent  map[string]recent
}

func NewResolver(source func() *collect.Index) *Resolver {
	return &Resolver{source: source, now: time.Now, recent: map[string]recent{}}
}

// SetSource replaces where the address index comes from (the agent supplies its collector once it exists).
func (r *Resolver) SetSource(source func() *collect.Index) {
	r.mu.Lock()
	r.source = source
	r.ix = nil
	r.mu.Unlock()
}

func (r *Resolver) index() (*collect.Index, map[string]recent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ix == nil || r.now().Sub(r.builtAt) > 5*time.Second {
		if ix := r.source(); ix != nil {
			r.ix, r.builtAt = ix, r.now()
			for ip, key := range ix.Pods {
				r.recent[ip] = recent{key, r.builtAt}
			}
			for ip, e := range r.recent {
				if r.builtAt.Sub(e.at) > remember {
					delete(r.recent, ip)
				}
			}
		}
	}
	if r.ix == nil {
		return &collect.Index{}, nil
	}
	return r.ix, r.recent
}

var systemNamespaces = map[string]bool{"kube-system": true, "kube-public": true, "kube-node-lease": true}

func namespaceOf(key string) string {
	for i := 0; i < len(key); i++ {
		if key[i] == '/' {
			return key[:i]
		}
	}
	return ""
}

func normalize(s string) (string, bool) {
	a, err := netip.ParseAddr(s)
	if err != nil {
		return "", false
	}
	a = a.Unmap()
	if a.IsLoopback() || a.IsUnspecified() || a.IsMulticast() {
		return "", false
	}
	return a.String(), true
}

func workload(key string) *continuumv1.FlowEndpoint {
	return &continuumv1.FlowEndpoint{Kind: continuumv1.FlowEndpoint_WORKLOAD, Ref: key}
}

// Resolve turns one raw observation into a flow between workloads, or reports ok=false when it is not
// application traffic: loopback, node-to-anything (image pulls, kubelet), traffic to the API server's
// Service, or an address that cannot be placed.
//
// A caller's side is always known (its pod). A connection that arrives from outside the cluster is
// recorded from the receiving side, and only then: connections between pods of this cluster are
// counted once, from the caller's side, so nothing is counted twice.
func (r *Resolver) Resolve(raw *continuumv1.RawFlow, method string, bytesKnown bool) (*continuumv1.Flow, bool) {
	local, ok1 := normalize(raw.LocalIp)
	peer, ok2 := normalize(raw.PeerIp)
	if !ok1 || !ok2 || (raw.Protocol != "tcp" && raw.Protocol != "udp") || raw.Port == 0 || raw.Port > 65535 {
		return nil, false
	}
	ix, hist := r.index()
	if ix.Hidden[local] || ix.Hidden[peer] {
		return nil, false // a namespace the agent was told to leave out
	}
	pod := func(ip string) (string, bool) {
		if k, ok := ix.Pods[ip]; ok {
			return k, true
		}
		if e, ok := hist[ip]; ok {
			return e.key, true
		}
		return "", false
	}
	f := &continuumv1.Flow{Port: raw.Port, Protocol: raw.Protocol, Connections: raw.Connections, BytesOut: raw.BytesOut, BytesIn: raw.BytesIn, Method: method, BytesKnown: bytesKnown}

	if raw.Client {
		src, ok := pod(local)
		if !ok {
			return nil, false // node processes and pods we cannot place
		}
		f.Src = workload(src)
		switch {
		case ix.Opaque[peer]:
			return nil, false
		case len(ix.Services[peer]) > 0:
			f.Dst = workload(ix.Services[peer][0])
		default:
			if k, ok := pod(peer); ok {
				f.Dst = workload(k)
			} else if _, isNode := ix.Nodes[peer]; isNode {
				keys := ix.NodePorts[int32(raw.Port)]
				if len(keys) == 0 {
					return nil, false // a node service (kubelet, ssh), not an application
				}
				f.Dst = workload(keys[0])
			} else {
				f.Dst = &continuumv1.FlowEndpoint{Kind: continuumv1.FlowEndpoint_EXTERNAL, Ip: peer}
			}
		}
	} else {
		dst, ok := pod(local)
		if !ok {
			return nil, false
		}
		if _, ok := pod(peer); ok {
			return nil, false // the caller's node reports this one
		}
		if _, ok := ix.Nodes[peer]; ok {
			return nil, false // masqueraded traffic from a node of this cluster: counted on the caller's side
		}
		f.Src = &continuumv1.FlowEndpoint{Kind: continuumv1.FlowEndpoint_EXTERNAL, Ip: peer}
		f.Dst = workload(dst)
	}
	switch {
	case f.Port == 53:
		f.Noise = "dns"
	case raw.Protocol == "udp" && (f.Port == 123 || f.Port == 5353 || f.Port == 1900):
		f.Noise = "system" // time sync and local service discovery
	case systemNamespaces[namespaceOf(f.GetSrc().GetRef())] || systemNamespaces[namespaceOf(f.GetDst().GetRef())]:
		f.Noise = "system"
	}
	return f, true
}
