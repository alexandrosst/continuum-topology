package server

import (
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/facts"
	"continuum/internal/observed"
)

// The same small fixtures observed's own tests use, for the tests here that need a cluster with flows fed into it.

func wk(ns, kind, name string, reach ...*continuumv1.Address) *continuumv1.WorkloadFacts {
	return &continuumv1.WorkloadFacts{Key: ns + "/" + kind + "/" + name, Namespace: ns, Kind: kind, Name: name, Reachable: reach}
}

func cluster(id string, egress string, nodes []*continuumv1.NodeFacts, ws ...*continuumv1.WorkloadFacts) observed.Cluster {
	st := facts.New()
	for _, w := range ws {
		st.Workloads[w.Key] = w
	}
	for _, n := range nodes {
		st.Nodes[n.Key] = n
	}
	return observed.Cluster{ID: id, Name: id, AgentID: "ag-" + id, State: st, Flows: observed.NewFlowTable(), EgressIP: egress}
}

func wep(key string) *continuumv1.FlowEndpoint {
	return &continuumv1.FlowEndpoint{Kind: continuumv1.FlowEndpoint_WORKLOAD, Ref: key}
}

func xep(ip string) *continuumv1.FlowEndpoint {
	return &continuumv1.FlowEndpoint{Kind: continuumv1.FlowEndpoint_EXTERNAL, Ip: ip}
}

func flowOf(src, dst *continuumv1.FlowEndpoint, port uint32, conns uint64) *continuumv1.Flow {
	return &continuumv1.Flow{Src: src, Dst: dst, Port: port, Protocol: "tcp", Connections: conns, BytesOut: 1000 * conns, BytesIn: 3000 * conns, Method: "ebpf", BytesKnown: true}
}

func feed(c *observed.Cluster, at time.Time, window int32, fs ...*continuumv1.Flow) {
	c.Flows.Apply(&continuumv1.FlowBatch{WindowSeconds: window, Flows: fs}, at)
}
