package server

import (
	"time"

	ikhnosv1 "ikhnos/gen/ikhnosv1"
	"ikhnos/internal/facts"
	"ikhnos/internal/observed"
)

// The same small fixtures observed's own tests use, for the tests here that need a cluster with flows fed into it.

func cluster(id string, egress string, nodes []*ikhnosv1.NodeFacts, ws ...*ikhnosv1.WorkloadFacts) observed.Cluster {
	st := facts.New()
	for _, w := range ws {
		st.Workloads[w.Key] = w
	}
	for _, n := range nodes {
		st.Nodes[n.Key] = n
	}
	return observed.Cluster{ID: id, Name: id, AgentID: "ag-" + id, State: st, Flows: observed.NewFlowTable(), EgressIP: egress}
}

func feed(c *observed.Cluster, at time.Time, window int32, fs ...*ikhnosv1.Flow) {
	c.Flows.Apply(&ikhnosv1.FlowBatch{WindowSeconds: window, Flows: fs}, at)
}
