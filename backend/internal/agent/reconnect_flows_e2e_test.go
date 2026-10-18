package agent_test

import (
	"net"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/agent"
	"continuum/internal/agent/collect"
	"continuum/internal/estate"
	"continuum/internal/flow"
	"continuum/internal/pki"
	"continuum/internal/server"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// Traffic the agent saw while its connection to the server was down is sent when it reconnects.
//
// The flow collector keeps running while the stream is down, so the aggregator holds what it saw. A new session
// used to begin by flushing the aggregator and throwing the batch away ("start the first window now"), so every
// connection, byte and edge seen during the outage was lost, and an edge that only existed during the outage
// never reached the server at all. The window is made so long that the regular flush never fires in this test,
// which leaves the session-start flush as the only way the traffic can arrive.
func TestTrafficSeenWhileDisconnectedIsSentOnReconnect(t *testing.T) {
	var pipe *flow.Pipeline
	d := newDx(t, dxOpts{
		objects: []runtime.Object{
			&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "cart", Namespace: "shop"}},
			&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "cart-1", Namespace: "shop", OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "cart"}}}},
			&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "cart-1-a", Namespace: "shop", OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "cart-1"}}},
				Spec: corev1.PodSpec{NodeName: "n1"}, Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.42.0.5"}},
		},
		tune: func(c *agent.Config, _ *server.Core, _ *estate.Hub) {
			pipe = flow.NewPipeline([]byte("flow-secret-0123456789abcdef0123"), func() *collect.Index { return nil }, nil)
			c.Flows, c.FlowWindow = pipe, time.Hour
			// The resolver learns the cluster's pods from the agent only once the receiver is listening.
			pl, _ := net.Listen("tcp", "127.0.0.1:0")
			c.ProbeListen = pl.Addr().String()
			pl.Close()
		},
	})
	waitFor(t, "the first picture, with the pod", 20*time.Second, func() bool { return services(d, "shop") >= 1 && d.agentDoc().Connected })
	probe := &continuumv1.RawFlow{Client: true, LocalIp: "10.42.0.5", PeerIp: "93.184.216.34", Port: 443, Protocol: "tcp", Connections: 1}
	waitFor(t, "the resolver to know the pod", 20*time.Second, func() bool {
		_, ok := pipe.Resolver.Resolve(probe, "ebpf", true)
		return ok
	})

	// The server goes away. The agent keeps running and starts retrying.
	addr := d.l.Addr().String()
	d.srv.Stop()
	waitFor(t, "the agent to be seen as disconnected", 20*time.Second, func() bool { return !d.agentDoc().Connected })

	// While it is away the node's collector still reports.
	if pipe.Ingest(&continuumv1.FlowReport{Method: "ebpf", Node: "n1", BytesKnown: true, Flows: []*continuumv1.RawFlow{
		{Client: true, LocalIp: "10.42.0.5", PeerIp: "93.184.216.34", Port: 443, Protocol: "tcp", Connections: 4, BytesOut: 400, BytesIn: 1200},
	}}) {
		t.Fatal("the pipeline was paused, so it threw the report away")
	}

	// The server comes back on the same address.
	l, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	srv := d.core.Agents.NewGRPC(pki.NewServerCerts(d.core.CA, []string{"127.0.0.1"}), d.hub)
	go srv.Serve(l)
	t.Cleanup(srv.Stop)

	waitFor(t, "the traffic seen during the outage", 40*time.Second, func() bool {
		for _, x := range d.state().Topology.Dependencies {
			if x.Port == 443 && x.ToKind == "external" && x.Connections == 4 {
				return true
			}
		}
		return false
	})
}
