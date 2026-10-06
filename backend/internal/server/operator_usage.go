package server

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"continuum/internal/store"
)

// OperatorRef names an operator that another one depends on or is depended on by.
type OperatorRef struct {
	ID   string
	Name string
}

// OperatorUsage is what is configured to send to one operator: other (non-revoked) operators that export into it,
// telemetry intents (active ones) that grant a cluster export to it, and the distinct clusters those intents belong to.
// It is read off the records at the time, never stored, so it cannot go stale.
type OperatorUsage struct {
	Operators []OperatorRef
	Intents   int
	Clusters  int
}

// Empty says nothing depends on the operator.
func (u OperatorUsage) Empty() bool { return len(u.Operators) == 0 && u.Intents == 0 }

// OperatorUsages computes the usage of every operator of the organisation in one pass over the operators, the
// intents and the agents, so listing N operators does not scan N times. An operator nothing sends to has no entry.
func (c *Core) OperatorUsages(ctx context.Context) (map[string]OperatorUsage, error) {
	ops, err := c.Store.ListOperators(ctx, c.OrgID)
	if err != nil {
		return nil, err
	}
	intents, err := c.Store.ListTelemetryIntents(ctx, c.OrgID)
	if err != nil {
		return nil, err
	}
	agents, err := c.Store.ListAgents(ctx, c.OrgID)
	if err != nil {
		return nil, err
	}
	clusterOf := make(map[string]string, len(agents))
	for _, a := range agents {
		clusterOf[a.ID] = a.ClusterID
	}
	out := map[string]OperatorUsage{}
	for _, op := range ops {
		if op.Status == store.OperatorActive && op.Destination.Kind == store.DestinationOperator {
			u := out[op.Destination.TargetOperatorID]
			u.Operators = append(u.Operators, OperatorRef{ID: op.ID, Name: op.Name})
			out[op.Destination.TargetOperatorID] = u
		}
	}
	clusters := map[string]map[string]bool{}
	for _, ti := range intents {
		if ti.Status != store.TelemetryIntentActive {
			continue
		}
		targets := map[string]bool{}
		if ti.Destination.Kind == store.DestinationOperator {
			targets[ti.Destination.TargetOperatorID] = true
		}
		for _, d := range ti.Routes {
			if d.Kind == store.DestinationOperator {
				targets[d.TargetOperatorID] = true
			}
		}
		for id := range targets {
			u := out[id]
			u.Intents++
			out[id] = u
			if cl := clusterOf[ti.AgentID]; cl != "" {
				if clusters[id] == nil {
					clusters[id] = map[string]bool{}
				}
				clusters[id][cl] = true
			}
		}
	}
	for id, set := range clusters {
		u := out[id]
		u.Clusters = len(set)
		out[id] = u
	}
	return out, nil
}

// dependentsDetail is the audit detail for revoking or deleting an operator, and the guard: while something sends
// to it and force is not set, a KindConflict that says what. note (the revoke reason) is kept in the detail.
func (c *Core) dependentsDetail(ctx context.Context, id string, force bool, note string) (string, error) {
	usages, err := c.OperatorUsages(ctx)
	if err != nil {
		return "", err
	}
	u := usages[id]
	if u.Empty() {
		return note, nil
	}
	if !force {
		return "", errf(KindConflict, "%s depends on this operator; revoking or deleting it leaves them with nowhere to send. Move them to another destination first, or confirm with force", u.describe())
	}
	detail := fmt.Sprintf("forced; operators=%d intents=%d clusters=%d", len(u.Operators), u.Intents, u.Clusters)
	if note != "" {
		detail = note + " (" + detail + ")"
	}
	return detail, nil
}

// describe words the dependents for a person: "2 operators (a, b) and 3 telemetry intents on 2 clusters".
func (u OperatorUsage) describe() string {
	var parts []string
	if n := len(u.Operators); n > 0 {
		names := make([]string, 0, n)
		for _, o := range u.Operators {
			names = append(names, o.Name)
		}
		sort.Strings(names)
		if len(names) > 5 {
			names = append(names[:5], "...")
		}
		parts = append(parts, fmt.Sprintf("%d %s (%s)", n, plural(n, "operator", "operators"), strings.Join(names, ", ")))
	}
	if u.Intents > 0 {
		s := fmt.Sprintf("%d %s", u.Intents, plural(u.Intents, "telemetry intent", "telemetry intents"))
		if u.Clusters > 0 {
			s += fmt.Sprintf(" on %d %s", u.Clusters, plural(u.Clusters, "cluster", "clusters"))
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " and ")
}
