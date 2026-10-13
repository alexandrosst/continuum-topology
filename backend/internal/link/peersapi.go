package link

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/probe"
)

// The prober runs in a pod of its own and learns who to measure from the agent, which is the only part of the cluster
// the server talks to. It asks over the cluster network with the same signed-request scheme the node collectors use to
// report to the agent (a secret the chart generates and mounts into both), under a node name of its own so a signature
// made for one purpose is never valid for another.
const (
	// PathPeers is where the agent answers.
	PathPeers = "/v1/link/peers"
	// ProberNode is the name the prober signs as; the agent accepts no other for this path.
	ProberNode   = "link-prober"
	maxPeersBody = 1 << 20
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
}

// Doc is the peers this meter currently measures, for the prober.
func (p *Peers) Doc() PeersDoc {
	d := PeersDoc{ClusterID: p.Own(), Peers: []PeerDoc{}}
	for _, pe := range p.List() {
		pd := PeerDoc{ID: pe.ID, Name: pe.Name, Addresses: make([]string, len(pe.Addresses))}
		for i, a := range pe.Addresses {
			pd.Addresses[i] = a.String()
		}
		d.Peers = append(d.Peers, pd)
	}
	return d
}

// SetFromDoc replaces the peers with those of a PeersDoc fetched from the agent, with the same checks as a Config from the
// server (the prober does not trust the agent's answer more than the agent trusts the server's).
func (p *Peers) SetFromDoc(d PeersDoc) []string {
	lps := make([]*continuumv1.LinkPeer, 0, len(d.Peers))
	for _, pe := range d.Peers {
		lps = append(lps, &continuumv1.LinkPeer{ClusterId: pe.ID, Name: pe.Name, Addresses: pe.Addresses})
	}
	return p.Set(d.ClusterID, lps)
}

// ServePeers lets the agent answer the prober's questions on the same listener as the metrics. Without it the path does
// not exist. secret is the one shared with the prober.
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
		_, _, status, err := probe.ReadSigned(w, req, probe.Signed{Secret: secret, MaxBody: 1 << 10, Replay: replay, NodeOK: func(n string) bool { return n == ProberNode }, Now: m.now()})
		if err != nil {
			http.Error(w, http.StatusText(status), status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(m.Peers.Doc())
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
	req.Header.Set(probe.HeaderSig, probe.Sign(secret, ts, ProberNode, nil))
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
