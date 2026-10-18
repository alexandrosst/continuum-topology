package agent_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"continuum/internal/agent"
	"continuum/internal/agent/collect"
	"continuum/internal/flow"
	"continuum/internal/kernel"
	"continuum/internal/link"
	"continuum/internal/server"
)

// peerUIDSorting finds a 12-digit uid suffix whose cluster (as addPeer enrolls it) sorts after (or before) mine.
func peerUIDSorting(t *testing.T, mine string, after bool) string {
	t.Helper()
	for i := 0; i < 200; i++ {
		uid := fmt.Sprintf("%012x", 0xabc000+i)
		id, _ := kernel.ClusterIDForUID("org", "d1a90002-aaaa-4bbb-8ccc-"+uid)
		if (id > mine) == after {
			return uid
		}
	}
	t.Fatal("no suitable uid")
	return ""
}

// The server issues the iperf3 credentials of a pair with throughput tests, delivers each end only its own, and the agent hands them to
// the pods that ask with the right signed request: the client side to the prober with its peer list, the server side to the responder.
func TestTheServerIssuesIperfCredentialsAndTheAgentHandsThemToThePodsOfItsRole(t *testing.T) {
	for name, runner := range map[string]bool{"this cluster runs the tests": true, "this cluster serves the tests": false} {
		t.Run(name, func(t *testing.T) {
			peersSecret := []byte("peers-secret-secret-secret-12345")
			iperfSecret := []byte("iperf-secret-secret-secret-12345")
			meter := link.NewMeter(link.NewPeers())
			meter.ServePeers(peersSecret)
			meter.ServeIperf(iperfSecret)
			var pipe *flow.Pipeline
			d := newDx(t, dxOpts{tune: func(c *agent.Config, _ *server.Core, _ *server.Hub) {
				pipe = flow.NewPipeline([]byte("secret-secret-secret-secret-1234"), func() *collect.Index { return nil }, nil)
				pipe.Links = meter
				c.Flows, c.Links = pipe, meter
			}})
			waitFor(t, "first picture", 20*time.Second, func() bool { return services(d, "shop") == 1 })
			mine := d.agentDoc().ClusterID
			peer := d.addPeer(peerUIDSorting(t, mine, runner), "10.9.9.9")

			srv := httptest.NewServer(meter.Handler())
			defer srv.Close()
			ctx := context.Background()
			fetchServer := func() (link.IperfServer, error) {
				return link.FetchIperfServer(ctx, srv.Client(), srv.URL, iperfSecret, time.Now())
			}

			if _, err := d.core.SaveSettings(ctx, "test", kernel.Settings{Links: []kernel.LinkSetting{{A: min(mine, peer), B: max(mine, peer), Throughput: &kernel.LinkThroughput{}}}}); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "the peer list", 30*time.Second, func() bool { return !meter.Peers.Empty() })

			if runner {
				waitFor(t, "the client credentials", 30*time.Second, func() bool {
					d, err := link.FetchPeersWith(ctx, srv.Client(), srv.URL, peersSecret, time.Now(), true)
					return err == nil && len(d.Peers) == 1 && d.Peers[0].Iperf != nil
				})
				doc, _ := link.FetchPeersWith(ctx, srv.Client(), srv.URL, peersSecret, time.Now(), true)
				if doc.Peers[0].Iperf.Username == "" || !strings.Contains(doc.Peers[0].Iperf.PublicKeyPEM, "PUBLIC KEY") {
					t.Errorf("client credentials = %v", doc.Peers[0].Iperf)
				}
				if s, err := fetchServer(); err != nil || !s.Empty() {
					t.Errorf("the runner holds server credentials: %v %v", s, err)
				}
				return
			}
			waitFor(t, "the server credentials", 30*time.Second, func() bool {
				s, err := fetchServer()
				return err == nil && !s.Empty()
			})
			s, _ := fetchServer()
			if err := s.Validate(); err != nil || strings.Count(s.UsersCSV.Reveal(), "\n") != 1 {
				t.Errorf("server credentials: %v", err)
			}
			doc, _ := link.FetchPeersWith(ctx, srv.Client(), srv.URL, peersSecret, time.Now(), true)
			for _, p := range doc.Peers {
				if p.Iperf != nil {
					t.Error("the serving cluster holds client credentials")
				}
			}

			// Pausing the flow collector withdraws them with the peers; so does removing the pair.
			if _, err := d.hub.SetConsent(ctx, "test", d.id, server.Consent{Paused: []string{"flow"}}); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "the credentials to be withdrawn while flow is paused", 30*time.Second, func() bool {
				s, err := fetchServer()
				return err == nil && s.Empty()
			})
			if _, err := d.hub.SetConsent(server.ActingAs(ctx, server.RoleAdmin), "test", d.id, server.Consent{}); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "the credentials to return", 30*time.Second, func() bool {
				s, err := fetchServer()
				return err == nil && !s.Empty()
			})
			if _, err := d.core.SaveSettings(ctx, "test", kernel.Settings{}); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "the credentials to go", 30*time.Second, func() bool {
				s, err := fetchServer()
				return err == nil && s.Empty()
			})
		})
	}
}
