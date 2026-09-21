package graph

import (
	"context"
	"fmt"
	"sort"
	"time"

	"continuum/internal/store"
)

// AddEvents stores change events for one tenant and returns them with their ids.
func (d *DB) AddEvents(ctx context.Context, org string, evs []store.Event) error {
	if len(evs) == 0 {
		return nil
	}
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].At.Before(evs[j].At) })
	rows := make([]map[string]any, len(evs))
	for i, e := range evs {
		rows[i] = map[string]any{
			"i": i + 1, "at": ts(e.At), "kind": e.Kind, "targetKind": e.TargetKind, "targetId": e.TargetID, "name": e.Name,
			"clusterId": e.ClusterID, "clusterName": e.ClusterName, "detail": e.Detail, "cause": e.Cause, "severity": e.Severity,
		}
	}
	sc := d.C.For(org)
	_, err := d.C.Run(ctx,
		sc.S(`MERGE (t:Tenant {id:$org}) RETURN 1`, nil),
		sc.S(`MERGE (c:Counter {org:$org, name:'event'}) SET c.n = coalesce(c.n, 0) + $k
WITH c.n - $k AS base
UNWIND $rows AS row
CREATE (e:Event {org:$org, id: base + row.i, at: datetime(row.at), kind: row.kind, targetKind: row.targetKind, targetId: row.targetId, name: row.name,
  clusterId: row.clusterId, clusterName: row.clusterName, detail: row.detail, cause: row.cause, severity: row.severity})`,
			map[string]any{"k": len(rows), "rows": rows}))
	return err
}

// Events lists change events, newest first.
func (d *DB) Events(ctx context.Context, org string, q store.EventQuery) ([]store.Event, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 200
	}
	if limit > 2000 {
		limit = 2000
	}
	p := map[string]any{"limit": limit}
	where := ""
	and := func(c string) {
		if where == "" {
			where = " WHERE " + c
		} else {
			where += " AND " + c
		}
	}
	if !q.Since.IsZero() {
		and("e.at >= datetime($since)")
		p["since"] = ts(q.Since)
	}
	if !q.Until.IsZero() {
		and("e.at <= datetime($until)")
		p["until"] = ts(q.Until)
	}
	if q.Kind != "" {
		and("e.kind = $kind")
		p["kind"] = q.Kind
	}
	if q.ClusterID != "" {
		and("e.clusterId = $cluster")
		p["cluster"] = q.ClusterID
	}
	if q.TargetID != "" {
		and("e.targetId = $target")
		p["target"] = q.TargetID
	}
	res, err := d.C.Run(ctx, d.C.For(org).S(`MATCH (e:Event {org:$org})`+where+`
RETURN e.id, toString(e.at), e.kind, e.targetKind, e.targetId, e.name, e.clusterId, e.clusterName, e.detail, e.cause, e.severity
ORDER BY e.at DESC, e.id DESC LIMIT $limit`, p))
	if err != nil {
		return nil, err
	}
	out := make([]store.Event, 0, len(res[0].Rows))
	for _, r := range res[0].Rows {
		out = append(out, store.Event{ID: i64(r[0]), At: tm(r[1]), Kind: str(r[2]), TargetKind: str(r[3]), TargetID: str(r[4]), Name: str(r[5]),
			ClusterID: str(r[6]), ClusterName: str(r[7]), Detail: str(r[8]), Cause: str(r[9]), Severity: str(r[10])})
	}
	return out, nil
}

// PruneEvents drops events older than `before` and, when keepNewest is positive, all but the newest keepNewest.
// keepNewest <= 0 means no count-based cap: only age matters.
func (d *DB) PruneEvents(ctx context.Context, org string, before time.Time, keepNewest int) error {
	sc := d.C.For(org)
	for {
		r, err := d.C.Run(ctx, sc.S(`MATCH (c:Counter {org:$org, name:'event'})
OPTIONAL MATCH (e:Event {org:$org}) WHERE e.at < datetime($before) OR ($keep > 0 AND e.id <= coalesce(c.n, 0) - $keep)
WITH e LIMIT 2000 WHERE e IS NOT NULL DETACH DELETE e RETURN count(*)`, map[string]any{"before": ts(before), "keep": keepNewest}))
		if err != nil {
			return err
		}
		if len(r[0].Rows) == 0 || i64(r[0].Rows[0][0]) < 2000 {
			return nil
		}
	}
}

// ---- who did what ----

// AuditRow is one recorded action.
type AuditRow struct {
	ID         int64     `json:"id"`
	At         time.Time `json:"at"`
	Actor      string    `json:"actor"`
	Action     string    `json:"action"`
	TargetKind string    `json:"targetKind,omitempty"`
	TargetID   string    `json:"targetId,omitempty"`
	Detail     string    `json:"detail,omitempty"`
}

// AuditQuery narrows a search of the audit trail. Zero values mean no filter.
type AuditQuery struct {
	Actor, Action, TargetID string
	Since, Until            time.Time
	Limit                   int
}

// serverOrg holds the server's own audit (actions taken outside any organisation). No tenant can read it.
const serverOrg = "_server"

// AddAudit projects audit rows into the graph (idempotent by id).
func (d *DB) AddAudit(ctx context.Context, rows []store.AuditEvent) error {
	byOrg := map[string][]map[string]any{}
	for _, a := range rows {
		org := a.OrgID
		if org == "" {
			org = serverOrg
		}
		actor := a.Actor
		if actor == "" {
			actor = "system"
		}
		byOrg[org] = append(byOrg[org], map[string]any{"id": a.ID, "at": ts(a.At), "actor": actor, "action": a.Action, "targetKind": a.TargetKind, "targetId": a.TargetID, "detail": a.Detail})
	}
	var stmts []Stmt
	for org, rs := range byOrg {
		sc := d.C.For(org)
		stmts = append(stmts, sc.S(`UNWIND $rows AS row
MERGE (a:Actor {org:$org, name:row.actor})
MERGE (e:Audit {org:$org, id:row.id})
SET e.at = datetime(row.at), e.action = row.action, e.targetKind = row.targetKind, e.targetId = row.targetId, e.detail = row.detail
MERGE (e)-[:BY]->(a)`, map[string]any{"rows": rs}))
	}
	_, err := d.C.Run(ctx, stmts...)
	return err
}

// Audit searches one tenant's trail, newest first.
func (d *DB) Audit(ctx context.Context, org string, q AuditQuery) ([]AuditRow, error) {
	limit := q.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	p := map[string]any{"limit": limit}
	where := ""
	and := func(c string) {
		if where == "" {
			where = " WHERE " + c
		} else {
			where += " AND " + c
		}
	}
	if q.Actor != "" {
		and("a.name = $actor")
		p["actor"] = q.Actor
	}
	if q.Action != "" {
		and("e.action STARTS WITH $action")
		p["action"] = q.Action
	}
	if q.TargetID != "" {
		and("e.targetId = $target")
		p["target"] = q.TargetID
	}
	if !q.Since.IsZero() {
		and("e.at >= datetime($since)")
		p["since"] = ts(q.Since)
	}
	if !q.Until.IsZero() {
		and("e.at <= datetime($until)")
		p["until"] = ts(q.Until)
	}
	res, err := d.C.Run(ctx, d.C.For(org).S(`MATCH (e:Audit {org:$org})-[:BY]->(a:Actor {org:$org})`+where+`
RETURN e.id, toString(e.at), a.name, e.action, e.targetKind, e.targetId, e.detail ORDER BY e.at DESC, e.id DESC LIMIT $limit`, p))
	if err != nil {
		return nil, err
	}
	out := make([]AuditRow, 0, len(res[0].Rows))
	for _, r := range res[0].Rows {
		out = append(out, AuditRow{ID: i64(r[0]), At: tm(r[1]), Actor: str(r[2]), Action: str(r[3]), TargetKind: str(r[4]), TargetID: str(r[5]), Detail: str(r[6])})
	}
	return out, nil
}

var _ = fmt.Sprint
