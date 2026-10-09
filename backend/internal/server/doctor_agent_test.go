package server

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/agent/collect"
	"continuum/internal/flow"
	"continuum/internal/flow/wire"
	"continuum/internal/interpret"
	"continuum/internal/netid"

	"google.golang.org/protobuf/proto"
)

func doctorAgentToServer(t *testing.T) {
	t.Run("every kind, enum and limit the agent can produce is stored or counted as dropped", func(t *testing.T) {
		// The matrix of endpoint kinds, methods, protocols, ports and enum values at their limits; it is its own test so
		// that it also runs alone.
		TestWhatTheAgentCanSendIsAcceptedOrCountedAsDropped(t)
		TestFlowReferencesAtTheirLimits(t)
	})

	t.Run("what the agent's resolver makes of real traffic becomes a dependency on the server, or a counted drop", func(t *testing.T) {
		defer netid.SetLookupAddrForTest(func(context.Context, string) ([]string, error) { return nil, errors.New("offline") })()
		defer netid.SetLookupTXTForTest(func(context.Context, string) ([]string, error) { return nil, errors.New("offline") })()

		long := "shop/StatefulSet/" + strings.Repeat("x", 253)
		ix := &collect.Index{
			Pods:        map[string]string{"10.42.0.5": "shop/Deployment/cart", "10.42.0.7": "shop/Deployment/db", "10.42.0.8": long},
			PodNames:    map[string]string{"10.42.0.5": "cart-7d9f8b-abc12", "10.42.0.8": strings.Repeat("p", wire.MaxName)},
			Services:    map[string][]string{"10.43.0.21": {"shop/Deployment/db"}},
			Nodes:       map[string]string{"192.168.1.10": "n1"},
			PodCIDRs:    []netip.Prefix{netip.MustParsePrefix("10.42.0.0/16")},
			ServiceCIDR: netip.MustParsePrefix("10.43.0.0/16"),
		}
		const cart, db = "shop/Deployment/cart", "shop/Deployment/db"
		c := cluster("c", "", []*continuumv1.NodeFacts{{Key: "n1"}}, wk("shop", "Deployment", "cart"), wk("shop", "Deployment", "db"),
			wk("shop", "StatefulSet", strings.TrimPrefix(long, "shop/StatefulSet/")))

		// What each node's collector sees, and what the server is to make of it: a dependency between the two ends (a
		// workload by its key, an outside address by its IP), or nothing, because the traffic is a node's own.
		type seen struct {
			name     string
			raw      *continuumv1.RawFlow
			from, to string
			port     int
			dropped  bool
		}
		rawFlow := func(client bool, local, peer string, port uint32, proto string) *continuumv1.RawFlow {
			return &continuumv1.RawFlow{Client: client, LocalIp: local, PeerIp: peer, Port: port, Protocol: proto, Connections: 3, BytesOut: 300, BytesIn: 900}
		}
		withText := rawFlow(true, "10.42.0.5", "93.184.216.34", 443, "tcp")
		withText.SniHost, withText.Iface, withText.DnsQueryName = strings.Repeat("s", 5000), strings.Repeat("i", 5000), strings.Repeat("d", 5000)
		cases := []seen{
			{"a pod calls a Service by its cluster IP", rawFlow(true, "10.42.0.5", "10.43.0.21", 5432, "tcp"), cart, db, 5432, false},
			{"a pod calls a pod directly, over UDP", rawFlow(true, "10.42.0.5", "10.42.0.7", 5433, "udp"), cart, db, 5433, false},
			{"a pod calls the internet, with text far past the limits", withText, cart, "93.184.216.34", 443, false},
			{"a workload with the longest key and pod name there is", rawFlow(true, "10.42.0.8", "10.43.0.21", 5432, "tcp"), long, db, 5432, false},
			{"the internet calls a pod", rawFlow(false, "10.42.0.5", "203.0.113.50", 8080, "tcp"), "203.0.113.50", cart, 8080, false},
			{"a node's own process calls the internet", rawFlow(true, "192.168.1.10", "93.184.216.34", 443, "tcp"), "n1", "93.184.216.34", 443, true},
		}

		resolver, agg := flow.NewResolver(func() *collect.Index { return ix }), flow.NewAggregator()
		wantDropped := 0
		for _, k := range cases {
			f, ok := resolver.Resolve(k.raw, "ebpf", true)
			if !ok {
				t.Fatalf("%s: the agent's resolver does not make a flow of it, so there is nothing to follow", k.name)
			}
			agg.Add(f)
			if k.dropped {
				wantDropped++
			}
		}
		batch := agg.Flush()
		wireBytes, err := proto.Marshal(batch)
		if err != nil {
			t.Fatal(err)
		}
		var arrived continuumv1.FlowBatch
		if err := proto.Unmarshal(wireBytes, &arrived); err != nil {
			t.Fatal(err)
		}
		dropped, err := sanitizeFlowBatch(&arrived)
		if err != nil {
			t.Fatalf("the batch an agent sent is refused, which ends its stream: %v", err)
		}
		if dropped.n != wantDropped {
			t.Errorf("%d items dropped (%v), want %d: exactly the node-level flows, and they must be counted", dropped.n, dropped.first, wantDropped)
		}
		if len(arrived.Flows) != len(cases)-wantDropped {
			t.Fatalf("%d flows stored, want %d", len(arrived.Flows), len(cases)-wantDropped)
		}

		now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
		feed(&c, now.Add(-time.Minute), arrived.WindowSeconds, arrived.Flows...)
		deps, exts := observedTopology("org", []observedCluster{c}, now, 24*time.Hour)
		id := func(end string) string {
			if strings.Contains(end, "/") {
				return interpret.ServiceID("c", end)
			}
			for _, e := range exts {
				if e.Host == end {
					return e.ID
				}
			}
			return "(no external endpoint " + end + ")"
		}
		for _, k := range cases {
			if k.dropped {
				continue
			}
			found := false
			for _, d := range deps {
				found = found || (d.From == id(k.from) && d.To == id(k.to) && d.Port == k.port)
			}
			if !found {
				t.Errorf("%s: the server kept the flow but no dependency %s -> %s on port %d comes of it", k.name, k.from, k.to, k.port)
			}
		}
	})
}
