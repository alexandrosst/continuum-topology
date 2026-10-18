package server

import (
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/facts"
	"continuum/internal/observed"
)

// The same small fixtures observed's own tests use, for the tests here that need a cluster with flows fed into it.

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

func feed(c *observed.Cluster, at time.Time, window int32, fs ...*continuumv1.Flow) {
	c.Flows.Apply(&continuumv1.FlowBatch{WindowSeconds: window, Flows: fs}, at)
}
