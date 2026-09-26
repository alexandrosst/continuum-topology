package graph

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"continuum/internal/store"
)

// TenantInfo is an organisation as the graph records it. SQLite stays authoritative for tenants and
// memberships (access control must work while this database is down); the graph holds a projection
// so history and "who did what" can be queried against it.
type TenantInfo struct {
	ID, Name, CreatedBy string
	CreatedAt           time.Time
}

// MemberInfo is one person's role in a tenant.
type MemberInfo struct {
	Name, Role string
	Since      time.Time
}

// SyncTenant makes the graph's picture of one tenant and its members match the given one. A role that
// changed closes the old membership and opens a new one, so "who was an admin on Tuesday" is answerable.
func (d *DB) SyncTenant(ctx context.Context, t TenantInfo, members []MemberInfo, now time.Time) error {
	sc := d.C.For(t.ID)
	res, err := d.C.Run(ctx, sc.S(`MATCH (a:Actor {org:$org})-[m:MEMBER_OF]->(:Tenant {id:$org}) WHERE m.validTo IS NULL RETURN a.name, m.role`, nil))
	if err != nil {
		return err
	}
	have := map[string]string{}
	for _, r := range res[0].Rows {
		have[str(r[0])] = str(r[1])
	}
	want := map[string]MemberInfo{}
	for _, m := range members {
		want[m.Name] = m
	}
	var closing, opening []map[string]any
	for name, role := range have {
		if w, ok := want[name]; !ok || w.Role != role {
			closing = append(closing, map[string]any{"name": name})
		}
	}
	for name, w := range want {
		if role, ok := have[name]; ok && role == w.Role {
			continue
		}
		since := w.Since
		if _, existed := have[name]; existed || since.IsZero() {
			since = now // a role change happened about now, not when they joined
		}
		opening = append(opening, map[string]any{"name": name, "role": w.Role, "at": ts(since)})
	}
	sort.Slice(closing, func(i, j int) bool { return closing[i]["name"].(string) < closing[j]["name"].(string) })
	sort.Slice(opening, func(i, j int) bool { return opening[i]["name"].(string) < opening[j]["name"].(string) })
	stmts := []Stmt{sc.S(`MERGE (t:Tenant {id:$org}) SET t.name = $name, t.createdAt = datetime($created), t.createdBy = $by, t.deletedAt = null`,
		map[string]any{"name": t.Name, "created": ts(t.CreatedAt), "by": t.CreatedBy})}
	if len(closing) > 0 {
		stmts = append(stmts, sc.S(`UNWIND $rows AS row
MATCH (a:Actor {org:$org, name:row.name})-[m:MEMBER_OF]->(:Tenant {id:$org}) WHERE m.validTo IS NULL SET m.validTo = datetime($now)`,
			map[string]any{"rows": closing, "now": ts(now)}))
	}
	if len(opening) > 0 {
		stmts = append(stmts, sc.S(`UNWIND $rows AS row
MERGE (a:Actor {org:$org, name:row.name})
WITH a, row MATCH (t:Tenant {id:$org})
CREATE (a)-[:MEMBER_OF {role:row.role, validFrom:datetime(row.at)}]->(t)`, map[string]any{"rows": opening}))
	}
	_, err = d.C.Run(ctx, stmts...)
	return err
}

// TenantIDs lists the tenants the graph holds data for (the projection's own view, not authority).
func (d *DB) TenantIDs(ctx context.Context) ([]string, error) {
	res, err := d.C.Run(ctx, Global(`MATCH (t:Tenant) RETURN t.id`, nil))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range res[0].Rows {
		out = append(out, str(r[0]))
	}
	return out, nil
}

// forgetLock drops an org's entry from the lazily-created lock map: once purged, no write for this org
// should ever be in flight again, so keeping its mutex around would just be an unreclaimed map entry for
// the lifetime of the process (harmless at realistic org-deletion volume, but a real leak). Safe to call
// even if a write for this org is still finishing: that goroutine already holds a reference to the old
// *sync.Mutex and is unaffected by removing it from the map.
func (d *DB) forgetLock(org string) {
	d.mu.Lock()
	delete(d.locks, org)
	d.mu.Unlock()
}

// PurgeTenant deletes everything the graph holds for a tenant (the organisation was deleted).
func (d *DB) PurgeTenant(ctx context.Context, org string) error {
	unlock := d.lock(org)
	defer func() {
		unlock()
		d.forgetLock(org)
	}()
	sc := d.C.For(org)
	for _, label := range []string{"Version", "Snapshot", "Event", "Audit", "Actor", "WorkspaceRev", "Counter", "Entity"} {
		for {
			r, err := d.C.Run(ctx, sc.S(`MATCH (n:`+label+` {org:$org}) WITH n LIMIT 2000 DETACH DELETE n RETURN count(*)`, nil))
			if err != nil {
				return err
			}
			if i64(r[0].Rows[0][0]) < 2000 {
				break
			}
		}
	}
	_, err := d.C.Run(ctx, sc.S(`MATCH (t:Tenant {id:$org}) DETACH DELETE t`, nil))
	return err
}

// ---- workspace revisions ----

// WorkspaceRev is one saved revision of an organisation's human layer.
type WorkspaceRev struct {
	Rev   int64           `json:"rev"`
	At    time.Time       `json:"at"`
	By    string          `json:"by"`
	Bytes int             `json:"bytes"`
	Data  json.RawMessage `json:"data,omitempty"`
}

const maxWorkspaceRevs = 500

// AppendWorkspace records a saved revision (the live copy stays in SQLite, where saving is atomic).
func (d *DB) AppendWorkspace(ctx context.Context, org string, w store.Workspace) error {
	sc := d.C.For(org)
	_, err := d.C.Run(ctx,
		sc.S(`MERGE (t:Tenant {id:$org}) RETURN 1`, nil),
		sc.S(`MERGE (r:WorkspaceRev {org:$org, rev:$rev}) SET r.at = datetime($at), r.by = $by, r.bytes = $n, r.data = $data
WITH r MATCH (t:Tenant {id:$org}) MERGE (t)-[:HAS_WORKSPACE_REV]->(r)`, map[string]any{"rev": w.Rev, "at": ts(w.UpdatedAt), "by": w.UpdatedBy, "n": len(w.Data), "data": string(w.Data)}),
		sc.S(`MATCH (r:WorkspaceRev {org:$org}) WHERE r.rev <= $old DETACH DELETE r`, map[string]any{"old": w.Rev - maxWorkspaceRevs}),
	)
	return err
}

// WorkspaceRevs lists saved revisions, newest first, without their content.
func (d *DB) WorkspaceRevs(ctx context.Context, org string, limit int) ([]WorkspaceRev, error) {
	if limit <= 0 || limit > maxWorkspaceRevs {
		limit = 50
	}
	res, err := d.C.Run(ctx, d.C.For(org).S(`MATCH (r:WorkspaceRev {org:$org}) RETURN r.rev, toString(r.at), r.by, r.bytes ORDER BY r.rev DESC LIMIT $limit`, map[string]any{"limit": limit}))
	if err != nil {
		return nil, err
	}
	out := []WorkspaceRev{}
	for _, r := range res[0].Rows {
		out = append(out, WorkspaceRev{Rev: i64(r[0]), At: tm(r[1]), By: str(r[2]), Bytes: int(i64(r[3]))})
	}
	return out, nil
}

// WorkspaceAt returns the revision that was current at `at`.
func (d *DB) WorkspaceAt(ctx context.Context, org string, at time.Time) (WorkspaceRev, error) {
	res, err := d.C.Run(ctx, d.C.For(org).S(`MATCH (r:WorkspaceRev {org:$org}) WHERE r.at <= datetime($at)
RETURN r.rev, toString(r.at), r.by, r.bytes, r.data ORDER BY r.rev DESC LIMIT 1`, map[string]any{"at": ts(at)}))
	if err != nil {
		return WorkspaceRev{}, err
	}
	if len(res[0].Rows) == 0 {
		return WorkspaceRev{}, store.ErrNotFound
	}
	r := res[0].Rows[0]
	return WorkspaceRev{Rev: i64(r[0]), At: tm(r[1]), By: str(r[2]), Bytes: int(i64(r[3])), Data: json.RawMessage(str(r[4]))}, nil
}

// ---- entity timeline ----

// Change is one field that differs between two versions of an entity.
type Change struct {
	Field string `json:"field"`
	From  any    `json:"from,omitempty"`
	To    any    `json:"to,omitempty"`
}

// TimelineVersion is one period during which an entity looked a certain way.
type TimelineVersion struct {
	From    time.Time  `json:"from"`
	To      *time.Time `json:"to,omitempty"` // nil while current
	Name    string     `json:"name"`
	Status  string     `json:"status,omitempty"`
	Changes []Change   `json:"changes"` // against the version before; empty for the first
	Doc     any        `json:"doc"`
}

// Timeline is everything the graph knows about one entity over time: its versions, the events that
// were about it and the actions people took on it.
type Timeline struct {
	Kind     string            `json:"kind"`
	ID       string            `json:"id"`
	Versions []TimelineVersion `json:"versions"` // newest first
	Events   []store.Event     `json:"events"`
	Audit    []AuditRow        `json:"audit"`
}

var ignoredFields = map[string]bool{"orgId": true, "revision": true, "lastSeen": true}

func diffDocs(older, newer map[string]any) []Change {
	keys := map[string]bool{}
	for k := range older {
		keys[k] = true
	}
	for k := range newer {
		keys[k] = true
	}
	var out []Change
	for k := range keys {
		if ignoredFields[k] {
			continue
		}
		a, _ := json.Marshal(older[k])
		b, _ := json.Marshal(newer[k])
		if string(a) != string(b) {
			out = append(out, Change{Field: k, From: older[k], To: newer[k]})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Field < out[j].Field })
	return out
}

// Timeline gathers the history of one entity.
func (d *DB) Timeline(ctx context.Context, org, kind, id string, limit int) (Timeline, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	sc := d.C.For(org)
	res, err := d.C.Run(ctx, sc.S(`MATCH (v:Version {org:$org, kind:$kind, id:$id})
RETURN toString(v.validFrom), toString(v.validTo), v.name, v.status, v.doc ORDER BY v.validFrom DESC LIMIT $limit`, map[string]any{"kind": kind, "id": id, "limit": limit}))
	if err != nil {
		return Timeline{}, err
	}
	if len(res[0].Rows) == 0 {
		return Timeline{}, store.ErrNotFound
	}
	tl := Timeline{Kind: kind, ID: id, Versions: []TimelineVersion{}}
	docs := make([]map[string]any, len(res[0].Rows))
	for i, r := range res[0].Rows {
		v := TimelineVersion{From: tm(r[0]), Name: str(r[2]), Status: str(r[3]), Changes: []Change{}}
		if to := tm(r[1]); !to.IsZero() {
			v.To = &to
		}
		var doc map[string]any
		_ = json.Unmarshal([]byte(str(r[4])), &doc)
		docs[i] = doc
		v.Doc = doc
		tl.Versions = append(tl.Versions, v)
	}
	for i := 0; i+1 < len(docs); i++ {
		tl.Versions[i].Changes = diffDocs(docs[i+1], docs[i])
	}
	if tl.Events, err = d.Events(ctx, org, store.EventQuery{TargetID: id, Limit: 100}); err != nil {
		return Timeline{}, err
	}
	if tl.Audit, err = d.Audit(ctx, org, AuditQuery{TargetID: id, Limit: 100}); err != nil {
		return Timeline{}, err
	}
	return tl, nil
}

// ---- how much is stored ----

// Stats says how much the graph holds for one tenant.
type Stats struct {
	Snapshots int        `json:"snapshots"`
	Versions  int        `json:"versions"`
	Entities  int        `json:"entities"`
	Events    int        `json:"events"`
	Audit     int        `json:"audit"`
	Oldest    *time.Time `json:"oldest,omitempty"`
	Newest    *time.Time `json:"newest,omitempty"`
}

func (d *DB) Stats(ctx context.Context, org string) (Stats, error) {
	sc := d.C.For(org)
	res, err := d.C.Run(ctx,
		sc.S(`MATCH (s:Snapshot {org:$org}) RETURN count(s), toString(min(s.at)), toString(max(s.at))`, nil),
		sc.S(`MATCH (v:Version {org:$org}) RETURN count(v)`, nil),
		sc.S(`MATCH (e:Entity {org:$org}) RETURN count(e)`, nil),
		sc.S(`MATCH (e:Event {org:$org}) RETURN count(e)`, nil),
		sc.S(`MATCH (a:Audit {org:$org}) RETURN count(a)`, nil),
	)
	if err != nil {
		return Stats{}, err
	}
	st := Stats{Snapshots: int(i64(res[0].Rows[0][0])), Versions: int(i64(res[1].Rows[0][0])), Entities: int(i64(res[2].Rows[0][0])), Events: int(i64(res[3].Rows[0][0])), Audit: int(i64(res[4].Rows[0][0]))}
	if t := tm(res[0].Rows[0][1]); !t.IsZero() {
		st.Oldest = &t
	}
	if t := tm(res[0].Rows[0][2]); !t.IsZero() {
		st.Newest = &t
	}
	return st, nil
}
