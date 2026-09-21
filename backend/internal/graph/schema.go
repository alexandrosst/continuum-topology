package graph

import (
	"context"
	"fmt"
)

// SchemaVersion is bumped when the shape of the graph changes; Ensure applies what is missing.
const SchemaVersion = 1

// The graph, in one place:
//
//	(:Tenant {id, name})                              one organisation
//	(:Entity {org, kind, id, name, status})           the identity of a cluster, node, namespace,
//	   also labelled :Cluster :Node :Namespace :Service :ExternalEndpoint :Dependency :Path
//	   -[:HAS_VERSION]-> (:Version {org, kind, id, validFrom, validTo, hash, doc})
//	      one row per distinct state; validTo is null while it is current
//	(:Entity)-[:IN_CLUSTER|RUNS_ON|CALLS|PATH_FROM|PATH_TO {org, key, validFrom, validTo}]->(:Entity)
//	   relationships are temporal too, so "what ran where on Tuesday" is a query
//	(:Snapshot {org, at, fp, bytes, traffic, paths})   the moments the estate was recorded, plus the
//	   volatile counters (traffic, path quality) that are not versioned
//	(:Event {org, id, at, kind, targetKind, targetId, ...})  what changed, in words
//	(:Audit {org, id, at, action, targetKind, targetId, detail})-[:BY]->(:Actor {org, name})
//	(:Actor)-[:MEMBER_OF {role, validFrom, validTo}]->(:Tenant)
//	(:WorkspaceRev {org, rev, at, by, data})            every saved revision of the human layer
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
	`CREATE INDEX version_from IF NOT EXISTS FOR (v:Version) ON (v.org, v.validFrom)`,
	`CREATE INDEX version_to IF NOT EXISTS FOR (v:Version) ON (v.org, v.validTo)`,
	`CREATE INDEX event_at IF NOT EXISTS FOR (e:Event) ON (e.org, e.at)`,
	`CREATE INDEX event_target IF NOT EXISTS FOR (e:Event) ON (e.org, e.targetId)`,
	`CREATE INDEX audit_at IF NOT EXISTS FOR (a:Audit) ON (a.org, a.at)`,
	`CREATE INDEX audit_target IF NOT EXISTS FOR (a:Audit) ON (a.org, a.targetId)`,
	`CREATE INDEX audit_action IF NOT EXISTS FOR (a:Audit) ON (a.org, a.action)`,
}

// The temporal relationship types. They are interpolated into statements, so they are a closed list.
var relTypes = []string{"IN_CLUSTER", "RUNS_ON", "CALLS", "PATH_FROM", "PATH_TO"}

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
