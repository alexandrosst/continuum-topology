package graph

import (
	"context"
	"fmt"
)

// SchemaVersion is bumped when the shape of the graph changes; Ensure applies what is missing.
// 2: added entity_org, the dedicated (org) index on :Entity.
const SchemaVersion = 2

// The graph, in one place:
//
//	(:Tenant {id, name})                              one organisation
//	(:Entity {org, kind, id, name, status})           the identity of a cluster, node, namespace,
//	   also labelled :Cluster :Node :Namespace :Service :ExternalEndpoint :Dependency :Path
//	   -[:HAS_VERSION]-> (:Version {org, kind, id, validFrom, validTo, hash, doc})
//	      one row per distinct state; validTo is null while it is current
//	(:Entity)-[:IN_CLUSTER|RUNS_ON|CALLS|PATH_FROM|PATH_TO|CONTAINS {org, key, validFrom, validTo}]->(:Entity)
//	   relationships are temporal too, so "what ran where on Tuesday" is a query. CONTAINS is the odd one
//	   out: the other four are diffed against a full topology poll every time Record runs, but CONTAINS
//	   (an application's member services) is written only through LinkEntities, the moment a workspace
//	   save says an application's membership changed - Record's own sweep leaves any edge whose source
//	   kind it does not poll (kinds.Polled == false) alone entirely, so the two never fight over it.
//	(:Snapshot {org, at, fp, bytes, traffic, paths})   the moments the estate was recorded, plus the
//	   volatile counters (traffic, path quality) that are not versioned
//	(:Event {org, id, at, kind, targetKind, targetId, ...})  what changed, in words
//	   -[:EXPLAINS]-> (:Version)   when the change was one that also produced a new version of its
//	      target in the same moment: the graph's own record of what caused what, not just that they
//	      happened close together
//	(:Audit {org, id, at, action, targetKind, targetId, detail})-[:BY]->(:Actor {org, name})
//	(:Actor)-[:MEMBER_OF {role, validFrom, validTo}]->(:Tenant)
//	(:WorkspaceRev {org, rev, at, by, data})            every saved revision of the human layer
//
// Two more relationships exist for bookkeeping rather than for querying: (:Tenant)-[:HAS_SNAPSHOT]->
// (:Snapshot) and (:Tenant)-[:HAS_WORKSPACE_REV]->(:WorkspaceRev), both write-only today (every read of
// either goes by the org property, not by traversing from Tenant). A (:Counter {org, name, n}) holds the
// one monotonic counter event ids are drawn from.
var ddl = []string{
	`CREATE CONSTRAINT tenant_id IF NOT EXISTS FOR (t:Tenant) REQUIRE t.id IS UNIQUE`,
	`CREATE CONSTRAINT entity_key IF NOT EXISTS FOR (e:Entity) REQUIRE (e.org, e.kind, e.id) IS UNIQUE`,
	`CREATE CONSTRAINT version_key IF NOT EXISTS FOR (v:Version) REQUIRE (v.org, v.kind, v.id, v.validFrom) IS UNIQUE`,
	`CREATE CONSTRAINT snapshot_key IF NOT EXISTS FOR (s:Snapshot) REQUIRE (s.org, s.at) IS UNIQUE`,
	`CREATE CONSTRAINT event_key IF NOT EXISTS FOR (e:Event) REQUIRE (e.org, e.id) IS UNIQUE`,
	`CREATE CONSTRAINT audit_key IF NOT EXISTS FOR (a:Audit) REQUIRE (a.org, a.id) IS UNIQUE`,
	`CREATE CONSTRAINT actor_key IF NOT EXISTS FOR (a:Actor) REQUIRE (a.org, a.name) IS UNIQUE`,
	`CREATE CONSTRAINT workspace_key IF NOT EXISTS FOR (r:WorkspaceRev) REQUIRE (r.org, r.rev) IS UNIQUE`,
	`CREATE CONSTRAINT counter_key IF NOT EXISTS FOR (c:Counter) REQUIRE (c.org, c.name) IS UNIQUE`,
	`CREATE CONSTRAINT projection_key IF NOT EXISTS FOR (p:Projection) REQUIRE p.name IS UNIQUE`,
	// entity_key's own composite constraint covers (org, kind, id), which a range index can also serve for
	// an org-only prefix lookup - but several real queries (AsOfEntities' existence check, PurgeTenant's
	// sweep, Stats' count) match :Entity by org alone, with no kind at all, and every other frequently
	// org-scoped label here (Event, Audit) already gets its own dedicated (org, ...) index rather than
	// leaning on a composite constraint's incidental prefix support. This one closes that gap for Entity.
	`CREATE INDEX entity_org IF NOT EXISTS FOR (e:Entity) ON (e.org)`,
	`CREATE INDEX version_from IF NOT EXISTS FOR (v:Version) ON (v.org, v.validFrom)`,
	`CREATE INDEX version_to IF NOT EXISTS FOR (v:Version) ON (v.org, v.validTo)`,
	`CREATE INDEX event_at IF NOT EXISTS FOR (e:Event) ON (e.org, e.at)`,
	`CREATE INDEX event_target IF NOT EXISTS FOR (e:Event) ON (e.org, e.targetId)`,
	`CREATE INDEX audit_at IF NOT EXISTS FOR (a:Audit) ON (a.org, a.at)`,
	`CREATE INDEX audit_target IF NOT EXISTS FOR (a:Audit) ON (a.org, a.targetId)`,
	`CREATE INDEX audit_action IF NOT EXISTS FOR (a:Audit) ON (a.org, a.action)`,
}

// The temporal relationship types. They are interpolated into statements, so they are a closed list.
var relTypes = []string{"IN_CLUSTER", "RUNS_ON", "CALLS", "PATH_FROM", "PATH_TO", "CONTAINS"}

func isRelType(rt string) bool {
	for _, t := range relTypes {
		if t == rt {
			return true
		}
	}
	return false
}

func init() {
	for _, t := range relTypes {
		ddl = append(ddl, fmt.Sprintf(`CREATE INDEX rel_%s_key IF NOT EXISTS FOR ()-[r:%s]-() ON (r.org, r.ekey)`, lower(t), t))
		ddl = append(ddl, fmt.Sprintf(`CREATE INDEX rel_%s_open IF NOT EXISTS FOR ()-[r:%s]-() ON (r.org, r.validTo)`, lower(t), t))
	}
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

// Ensure creates the constraints and indexes that are missing. It is safe to run on every start.
func (c *Client) Ensure(ctx context.Context) error {
	for _, q := range ddl {
		if _, err := c.Run(ctx, Global(q, nil)); err != nil {
			return fmt.Errorf("preparing the graph: %w", err)
		}
	}
	_, err := c.Run(ctx, Global(`MERGE (m:SchemaMeta {id:'schema'}) SET m.version = $v, m.updated = datetime()`, map[string]any{"v": SchemaVersion}))
	return err
}

// Ping checks that the database answers and the credentials work.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.Run(ctx, Global(`RETURN 1`, nil))
	return err
}
