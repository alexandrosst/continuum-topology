package link

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/probe"
)

// The prober runs in a pod of its own and learns who to measure from the agent, which is the only part of the cluster
// the server talks to. It asks over the cluster network with the signed-request scheme the node collectors use to report
// to the agent, but with a secret of its own (the chart generates it and mounts it into the agent and the prober only, never
// the flow secret, which the prober's measurement tools must not be able to read) and a MAC context of its own, so a
// flow report's signature is not a valid peers request nor the other way round, even if the two secrets were ever equal.
const (
	// PathPeers is where the agent answers.
	PathPeers = "/v1/link/peers"
	// ProberNode is the name the prober signs as; the agent accepts no other for this path.
	ProberNode   = "link-prober"
	maxPeersBody = 1 << 20
	// peersContext is what the MAC of a peers request is bound to (see probe.SignContext).
	peersContext = "continuum-link-peers-v1"
)

// PeersDoc is what the agent returns: this cluster's id and the peers it is paired with.
type PeersDoc struct {
	ClusterID string    `json:"cluster_id"`
	Peers     []PeerDoc `json:"peers"`
}

// PeerDoc is one peer in a PeersDoc.
type PeerDoc struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Addresses []string `json:"addresses"`
	// BytesSent and BytesReceived are the running totals of the traffic observed with the peer (as the passive series
	// count them), so the prober can tell how busy the link is from two answers.
	BytesSent     uint64 `json:"bytes_sent,omitempty"`
	BytesReceived uint64 `json:"bytes_received,omitempty"`
	// BytesUnknown says the totals above are not measurements: the collectors seeing the peer's traffic cannot count bytes, so
	// zero means nothing. The prober then has no estimate of the link's load (a zero here is "unknown", not "idle").
	BytesUnknown bool `json:"bytes_unknown,omitempty"`
	// Preferred are the addresses the real traffic with the peer is seen using, the busiest first (see Peer.Preferred). Only
	// addresses from Addresses; the prober's active measurements try them first unless the pair is pinned with Via.
	Preferred []string `json:"preferred,omitempty"`
	// What to measure towards the peer (see Peer).
	NoProbe                   bool   `json:"no_probe,omitempty"`
	SkipProbe                 bool   `json:"skip_probe,omitempty"`
	Via                       string `json:"via,omitempty"`
	Throughput                bool   `json:"throughput,omitempty"`
	ThroughputIntervalSeconds uint32 `json:"throughput_interval_seconds,omitempty"`
	ThroughputBudgetBytes     uint64 `json:"throughput_budget_bytes,omitempty"`
}

// Doc is the peers this meter currently measures, for the prober.
func (p *Peers) Doc() PeersDoc {
	d := PeersDoc{ClusterID: p.Own(), Peers: []PeerDoc{}}
	for _, pe := range p.List() {
		pd := PeerDoc{ID: pe.ID, Name: pe.Name, Addresses: make([]string, len(pe.Addresses)), NoProbe: pe.NoProbe, SkipProbe: pe.SkipProbe, Via: pe.Via, Throughput: pe.Throughput,
			ThroughputIntervalSeconds: uint32(pe.ThroughputEvery / time.Second), ThroughputBudgetBytes: pe.ThroughputBudget}
		for i, a := range pe.Addresses {
			pd.Addresses[i] = a.String()
		}
		d.Peers = append(d.Peers, pd)
	}
	return d
}

// peersDoc is the peers with the traffic seen with each, and the addresses that traffic is seen using.
func (m *Meter) peersDoc() PeersDoc {
	d := m.Peers.Doc()
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range d.Peers {
		if s := m.per[d.Peers[i].ID]; s != nil {
			d.Peers[i].Preferred = inList(s.used.ordered(now), d.Peers[i].Addresses)
			if s.bytesUnknown() {
				d.Peers[i].BytesUnknown = true
				continue
			}
			d.Peers[i].BytesSent, d.Peers[i].BytesReceived = s.bytesSent, s.bytesReceived
		}
	}
	return d
}

// inList are the addresses of used, in their order, that are among list.
func inList(used []netip.Addr, list []string) []string {
	var out []string
	for _, a := range used {
		if s := a.String(); slices.Contains(list, s) {
			out = append(out, s)
		}
	}
	return out
}

// SetFromDoc replaces the peers with those of a PeersDoc fetched from the agent, with the same checks as a Config from the
// server (the prober does not trust the agent's answer more than the agent trusts the server's).
func (p *Peers) SetFromDoc(d PeersDoc) []string {
	lps := make([]*continuumv1.LinkPeer, 0, len(d.Peers))
	preferred := map[string][]string{}
	for _, pe := range d.Peers {
		lps = append(lps, &continuumv1.LinkPeer{ClusterId: pe.ID, Name: pe.Name, Addresses: pe.Addresses, NoProbe: pe.NoProbe, SkipProbe: pe.SkipProbe, Via: pe.Via, Throughput: pe.Throughput,
			ThroughputIntervalSeconds: pe.ThroughputIntervalSeconds, ThroughputBudgetBytes: pe.ThroughputBudgetBytes})
		preferred[pe.ID] = pe.Preferred
	}
	return p.set(d.ClusterID, lps, preferred)
}

// ServePeers lets the agent answer the prober's questions on the same listener as the metrics. Without it the path does
// not exist. secret is the one shared with the prober, and with nothing else: not the flow secret.
func (m *Meter) ServePeers(secret []byte) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.peersSecret = append([]byte(nil), secret...)
	m.peersReplay = probe.NewReplayCache(0)
	m.mu.Unlock()
}

func (m *Meter) peersHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		m.mu.Lock()
		secret, replay := m.peersSecret, m.peersReplay
		m.mu.Unlock()
		if len(secret) == 0 {
			http.NotFound(w, req)
			return
		}
		_, _, status, err := probe.ReadSigned(w, req, probe.Signed{Secret: secret, MaxBody: 1 << 10, Replay: replay, NodeOK: func(n string) bool { return n == ProberNode }, Now: m.now(), Context: peersContext})
		if err != nil {
			http.Error(w, http.StatusText(status), status)
			return
		}
		m.noteProberSeen()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(m.peersDoc())
	}
}

// FetchPeers asks the agent at url (scheme, host and port) for the peers to measure.
func FetchPeers(ctx context.Context, hc *http.Client, url string, secret []byte, now time.Time) (PeersDoc, error) {
	ts := strconv.FormatInt(now.Unix(), 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url+PathPeers, http.NoBody)
	if err != nil {
		return PeersDoc{}, err
	}
	req.Header.Set(probe.HeaderNode, ProberNode)
	req.Header.Set(probe.HeaderTime, ts)
	req.Header.Set(probe.HeaderSig, probe.SignContext(peersContext, secret, ts, ProberNode, nil))
	resp, err := hc.Do(req)
	if err != nil {
		return PeersDoc{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return PeersDoc{}, fmt.Errorf("agent answered %s", resp.Status)
	}
	var d PeersDoc
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxPeersBody)).Decode(&d); err != nil {
		return PeersDoc{}, fmt.Errorf("agent's answer is not a peer list: %w", err)
	}
	if len(d.Peers) > MaxPeers {
		return PeersDoc{}, errors.New("agent named more peers than allowed")
	}
	for _, p := range d.Peers {
		for _, a := range p.Addresses {
			if _, err := netip.ParseAddr(a); err != nil {
				return PeersDoc{}, fmt.Errorf("agent's answer holds a bad address %q", a)
			}
		}
	}
	return d, nil
}
