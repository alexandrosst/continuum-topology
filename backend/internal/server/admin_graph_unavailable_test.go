package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	"ikhnos/internal/graph"
	"ikhnos/internal/store"
)

// unreachableGraph is a store whose graph database cannot be reached: every question only the graph can answer fails the way the
// real one does while the database restarts.
type unreachableGraph struct {
	store.Store
	err error
}

func (u unreachableGraph) Status(context.Context, string) graph.Status { return graph.Status{} }
func (u unreachableGraph) Timeline(context.Context, string, string, string, int) (graph.Timeline, error) {
	return graph.Timeline{}, u.err
}
func (u unreachableGraph) Audit(context.Context, string, graph.AuditQuery) ([]graph.AuditRow, error) {
	return nil, u.err
}
func (u unreachableGraph) WorkspaceRevs(context.Context, string, int) ([]graph.WorkspaceRev, error) {
	return nil, u.err
}
func (u unreachableGraph) WorkspaceAt(context.Context, string, time.Time) (graph.WorkspaceRev, error) {
	return graph.WorkspaceRev{}, u.err
}
func (u unreachableGraph) AsOfEntities(context.Context, string, time.Time) (time.Time, []graph.EntitySnapshot, error) {
	return time.Time{}, nil, u.err
}
func (u unreachableGraph) Dependents(context.Context, string, time.Time, string, string, int) (time.Time, []graph.Reached, error) {
	return time.Time{}, nil, u.err
}
func (u unreachableGraph) Dependencies(context.Context, string, time.Time, string, string, int) (time.Time, []graph.Reached, error) {
	return time.Time{}, nil, u.err
}
func (u unreachableGraph) DiffEntities(context.Context, string, time.Time, time.Time) (graph.StructuralDiff, error) {
	return graph.StructuralDiff{}, u.err
}

// While the graph database is down, or a walk runs out of its time, the history routes say "unavailable, try again" (503 with a
// Retry-After), not "internal error": the records exist, and a monitor that counts 500s as server bugs must not count this.
func TestTheGraphBeingDownOrSlowIsUnavailableNotAnInternalError(t *testing.T) {
	for name, err := range map[string]error{
		"unreachable": fmt.Errorf("%w: connection refused", graph.ErrUnavailable),
		"timed out":   fmt.Errorf("walk: %w", context.DeadlineExceeded),
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnvBare(t)
			e.base.Store = unreachableGraph{Store: e.st, err: err}
			e.core = e.base.ForOrg("org-1")
			a := adminRigOn(t, e)
			cookie, orgID := a.register(t, "alice", "Alice Lab")
			at := time.Now().UTC().Format(time.RFC3339)
			for _, path := range []string{
				"graph/snapshot?at=" + at,
				"graph/dependents?kind=service&id=svc-web&at=" + at,
				"graph/dependencies?kind=service&id=svc-web&at=" + at,
				"graph/diff?from=" + at + "&to=" + at,
				"workspace/revisions",
			} {
				r := a.do("GET", org(orgID, path), nil, withCookie(cookie))
				if r.Code != 503 {
					t.Errorf("%s: %d %s, want 503", path, r.Code, r.Body.String())
				}
				if r.Header().Get("Retry-After") == "" {
					t.Errorf("%s: no Retry-After", path)
				}
			}
		})
	}
}
