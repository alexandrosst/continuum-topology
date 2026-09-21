package graph

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"continuum/internal/history"
	"continuum/internal/model"
	"continuum/internal/store"
)

// ErrOutOfOrder: a snapshot older than the newest recorded one cannot be placed in the past.
var ErrOutOfOrder = errors.New("that moment is older than the newest recorded one")

// ---- what is versioned ----

// kinds are the entity kinds, with the label each carries. The label is interpolated into
// statements, so it comes from this list and nowhere else.
var kinds = []struct{ Kind, Label string }{
	{"cluster", "Cluster"},
	{"node", "Node"},
	{"namespace", "Namespace"},
	{"service", "Service"},
	{"external", "ExternalEndpoint"},
	{"dependency", "Dependency"},
	{"path", "Path"},
}

func labelOf(kind string) string {
	for _, k := range kinds {
		if k.Kind == kind {
			return k.Label
		}
	}
	return ""
}

type ver struct {
	Kind, ID, Hash, Doc, Name, Status, Cluster string
}

func vkey(kind, id string) string { return kind + "\x00" + id }

// counters are the volatile numbers that change on every window and would otherwise make every
// snapshot a new version of every dependency. They live on the snapshot instead.
type counters struct {
	Bytes uint64  `json:"b,omitempty"`
	Conns uint64  `json:"c,omitempty"`
	Bps   float64 `json:"r,omitempty"`
	Cpm   float64 `json:"m,omitempty"`
	Win   int32   `json:"w,omitempty"`
}

type pathQuality struct {
	Min, P50, P95, Loss float64
	Samples             int
	At                  string
}

func hashDoc(doc []byte) string {
	h := sha256.Sum256(doc)
	return hex.EncodeToString(h[:10])
}

func enc(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

type edge struct {
	Type, FK, FID, TK, TID, Key string
	Port                        int
	Proto                       string
}

func (e edge) ekey() string {
	return e.Type + "|" + e.FK + "|" + e.FID + "|" + e.TK + "|" + e.TID + "|" + e.Key
}

// extract turns a topology into the versions, edges and volatile counters the graph keeps.
func extract(t model.Topology) (vs map[string]ver, es map[string]edge, tr map[string]counters, pq map[string]pathQuality) {
	vs = map[string]ver{}
	es = map[string]edge{}
	tr = map[string]counters{}
	pq = map[string]pathQuality{}
	put := func(kind, id, name, status, cluster string, doc any) {
		raw, _ := json.Marshal(doc)
		vs[vkey(kind, id)] = ver{Kind: kind, ID: id, Hash: hashDoc(raw), Doc: string(raw), Name: name, Status: status, Cluster: cluster}
	}
	has := func(kind, id string) bool { _, ok := vs[vkey(kind, id)]; return ok }

	for _, c := range t.Clusters {
		c.LastSeen, c.Revision = "", 0
		c.ClearObservation()
		put("cluster", c.ID, c.Name, c.Status, c.ID, c)
	}
	for _, n := range t.Nodes {
		n.LastSeen, n.Revision = "", 0
		n.ClearObservation()
		put("node", n.ID, n.Name, n.Status, n.ClusterID, n)
	}
	for _, n := range t.Namespaces {
		n.LastSeen, n.Revision = "", 0
		n.ClearObservation()
		put("namespace", n.ID, n.Name, "", n.ClusterID, n)
	}
	for _, s := range t.Services {
		s.LastSeen, s.Revision = "", 0
		s.ClearObservation()
		put("service", s.ID, s.Name, s.Status, s.ClusterID, s)
	}
	for _, e := range t.ExternalEndpoints {
		e.LastSeen, e.Revision = "", 0
		e.ClearObservation()
		name := e.Host
		if e.Port > 0 {
			name += ":" + strconv.Itoa(e.Port)
		}
		put("external", e.ID, name, "", "", e)
	}
	for _, d := range t.Dependencies {
		if d.Bytes > 0 || d.Connections > 0 || d.Stats != nil {
			c := counters{Bytes: d.Bytes, Conns: d.Connections}
			if d.Stats != nil {
				c.Bps, c.Cpm, c.Win = d.Stats.BytesPerSec, d.Stats.ConnectionsPerMin, d.Stats.WindowSec
			}
			tr[d.ID] = c
		}
		d.LastSeen, d.Bytes, d.Connections, d.Stats = "", 0, 0, nil
		name := d.Label
		if name == "" {
			name = d.From + " → " + d.To
		}
		put("dependency", d.ID, name, "", "", d)
	}
	for _, p := range t.Paths {
		pq[p.ID] = pathQuality{Min: p.RTTMin, P50: p.RTTP50, P95: p.RTTP95, Loss: p.LossPct, Samples: p.Samples, At: p.At}
		p.RTTMin, p.RTTP50, p.RTTP95, p.LossPct, p.Samples, p.At = 0, 0, 0, 0, 0, ""
		put("path", p.ID, p.Host+":"+strconv.Itoa(p.Port), "", p.FromCluster, p)
	}

	add := func(e edge) {
		if has(e.FK, e.FID) && has(e.TK, e.TID) {
			es[e.ekey()] = e
		}
	}
	for _, n := range t.Nodes {
		add(edge{Type: "IN_CLUSTER", FK: "node", FID: n.ID, TK: "cluster", TID: n.ClusterID})
	}
	for _, n := range t.Namespaces {
		add(edge{Type: "IN_CLUSTER", FK: "namespace", FID: n.ID, TK: "cluster", TID: n.ClusterID})
	}
	for _, s := range t.Services {
		add(edge{Type: "IN_CLUSTER", FK: "service", FID: s.ID, TK: "cluster", TID: s.ClusterID})
		for _, n := range s.NodeIDs {
			add(edge{Type: "RUNS_ON", FK: "service", FID: s.ID, TK: "node", TID: n})
		}
	}
	for _, d := range t.Dependencies {
		fk, tk := d.FromKind, d.ToKind
		if fk == "" {
			fk = "service"
		}
		if tk == "" {
			tk = "service"
		}
		add(edge{Type: "CALLS", FK: fk, FID: d.From, TK: tk, TID: d.To, Key: d.ID, Port: d.Port, Proto: d.Protocol})
	}
	for _, p := range t.Paths {
		add(edge{Type: "PATH_FROM", FK: "path", FID: p.ID, TK: "cluster", TID: p.FromCluster})
		if p.ToCluster != "" {
			add(edge{Type: "PATH_TO", FK: "path", FID: p.ID, TK: "cluster", TID: p.ToCluster})
		}
	}
	return
}

// ---- the recorder ----

// DB is the graph operations. It serialises writes per tenant so a diff is always made against the
// state its own predecessor left.
type DB struct {
	C *Client

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func NewDB(c *Client) *DB { return &DB{C: c, locks: map[string]*sync.Mutex{}} }

func (d *DB) lock(org string) func() {
	d.mu.Lock()
	m := d.locks[org]
	if m == nil {
		m = &sync.Mutex{}
		d.locks[org] = m
	}
	d.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// Record makes the graph describe the estate as it is at `at`: entities and links that appeared are
// opened, ones that changed get a new version, ones that went away are closed. Only differences are
// written, so an unchanged estate costs one snapshot row. `at` must not be older than the newest
// recorded moment (an equal one is a harmless repeat).
func (d *DB) Record(ctx context.Context, org string, at time.Time, t model.Topology, fp string, size int) error {
	at = at.UTC().Truncate(time.Second)
	defer d.lock(org)()
	sc := d.C.For(org)

	vs, es, tr, pq := extract(t)

	// What is open now.
	reads := []Stmt{
		sc.S(`MATCH (s:Snapshot {org:$org}) RETURN toString(s.at) ORDER BY s.at DESC LIMIT 1`, nil),
		sc.S(`MATCH (v:Version {org:$org}) WHERE v.validTo IS NULL RETURN v.kind, v.id, v.hash`, nil),
	}
	for _, rt := range relTypes {
		reads = append(reads, sc.S(fmt.Sprintf(`MATCH ()-[r:%s {org:$org}]->() WHERE r.validTo IS NULL RETURN r.ekey`, rt), nil))
	}
	res, err := d.C.Run(ctx, reads...)
	if err != nil {
		return err
	}
	if len(res[0].Rows) > 0 {
		if last := tm(res[0].Rows[0][0]); at.Before(last) {
			return fmt.Errorf("%w (%s < %s)", ErrOutOfOrder, ts(at), ts(last))
		}
	}
	open := map[string]string{}
	for _, r := range res[1].Rows {
		open[vkey(str(r[0]), str(r[1]))] = str(r[2])
	}
	openEdge := map[string]bool{}
	for i := range relTypes {
		for _, r := range res[2+i].Rows {
			openEdge[str(r[0])] = true
		}
	}

	// What must change.
	type row = map[string]any
	var closeRows, goneRows []row
	created := map[string][]row{}
	for k, o := range open {
		v, ok := vs[k]
		kind, id := splitKey(k)
		switch {
		case !ok:
			closeRows = append(closeRows, row{"kind": kind, "id": id})
			goneRows = append(goneRows, row{"kind": kind, "id": id})
		case v.Hash != o:
			closeRows = append(closeRows, row{"kind": kind, "id": id})
		}
	}
	for k, v := range vs {
		if o, ok := open[k]; ok && o == v.Hash {
			continue
		}
		created[v.Kind] = append(created[v.Kind], row{"kind": v.Kind, "id": v.ID, "hash": v.Hash, "doc": v.Doc, "name": v.Name, "status": v.Status, "cluster": v.Cluster})
	}
	sortRows := func(rs []row) {
		sort.Slice(rs, func(i, j int) bool {
			if a, b := rs[i]["kind"].(string), rs[j]["kind"].(string); a != b {
				return a < b
			}
			return rs[i]["id"].(string) < rs[j]["id"].(string)
		})
	}
	sortRows(closeRows)
	sortRows(goneRows)

	atS := ts(at)
	w := []Stmt{sc.S(`MERGE (t:Tenant {id:$org}) RETURN 1`, nil)}
	if len(closeRows) > 0 {
		// A version that began at this very instant is being replaced within it: drop it rather than
		// leave a zero-length version behind (the moment is recorded once).
		w = append(w, sc.S(`UNWIND $rows AS row
MATCH (v:Version {org:$org, kind:row.kind, id:row.id}) WHERE v.validTo IS NULL AND v.validFrom = datetime($at)
DETACH DELETE v`, map[string]any{"rows": closeRows, "at": atS}))
		w = append(w, sc.S(`UNWIND $rows AS row
MATCH (v:Version {org:$org, kind:row.kind, id:row.id}) WHERE v.validTo IS NULL
SET v.validTo = datetime($at)`, map[string]any{"rows": closeRows, "at": atS}))
	}
	if len(goneRows) > 0 {
		w = append(w, sc.S(`UNWIND $rows AS row
MATCH (e:Entity {org:$org, kind:row.kind, id:row.id}) SET e.gone = datetime($at)`, map[string]any{"rows": goneRows, "at": atS}))
	}
	for _, k := range kinds {
		rows := created[k.Kind]
		if len(rows) == 0 {
			continue
		}
		sortRows(rows)
		w = append(w, sc.S(fmt.Sprintf(`UNWIND $rows AS row
MERGE (e:Entity {org:$org, kind:row.kind, id:row.id})
ON CREATE SET e.firstSeen = datetime($at)
SET e:%s, e.name = row.name, e.status = row.status, e.cluster = row.cluster, e.gone = null
CREATE (v:Version {org:$org, kind:row.kind, id:row.id, validFrom:datetime($at), hash:row.hash, doc:row.doc, name:row.name, status:row.status, cluster:row.cluster})
CREATE (e)-[:HAS_VERSION]->(v)`, k.Label), map[string]any{"rows": rows, "at": atS}))
	}
	// Links.
	for _, rt := range relTypes {
		var closing, opening []row
		for k := range openEdge {
			if e, ok := parseEdgeKey(k); ok && e.Type == rt {
				if _, still := es[k]; !still {
					closing = append(closing, row{"key": k})
				}
			}
		}
		for k, e := range es {
			if e.Type == rt && !openEdge[k] {
				opening = append(opening, row{"key": k, "fk": e.FK, "fid": e.FID, "tk": e.TK, "tid": e.TID, "port": e.Port, "proto": e.Proto})
			}
		}
		sort.Slice(closing, func(i, j int) bool { return closing[i]["key"].(string) < closing[j]["key"].(string) })
		sort.Slice(opening, func(i, j int) bool { return opening[i]["key"].(string) < opening[j]["key"].(string) })
		if len(closing) > 0 {
			w = append(w, sc.S(fmt.Sprintf(`UNWIND $rows AS row
MATCH ()-[r:%s {org:$org, ekey:row.key}]->() WHERE r.validTo IS NULL
SET r.validTo = datetime($at)`, rt), map[string]any{"rows": closing, "at": atS}))
		}
		if len(opening) > 0 {
			w = append(w, sc.S(fmt.Sprintf(`UNWIND $rows AS row
MATCH (a:Entity {org:$org, kind:row.fk, id:row.fid})
MATCH (b:Entity {org:$org, kind:row.tk, id:row.tid})
CREATE (a)-[:%s {org:$org, ekey:row.key, validFrom:datetime($at), port:row.port, protocol:row.proto}]->(b)`, rt), map[string]any{"rows": opening, "at": atS}))
		}
	}
	w = append(w, sc.S(`MERGE (s:Snapshot {org:$org, at:datetime($at)})
SET s.fp = $fp, s.bytes = $bytes, s.entities = $n, s.traffic = $traffic, s.paths = $paths
WITH s MATCH (t:Tenant {id:$org}) MERGE (t)-[:HAS_SNAPSHOT]->(s)`, map[string]any{
		"at": atS, "fp": fp, "bytes": size, "n": len(vs), "traffic": enc(tr), "paths": enc(pq),
	}))
	_, err = d.C.Run(ctx, w...)
	return err
}

func splitKey(k string) (string, string) {
	for i := 0; i < len(k); i++ {
		if k[i] == 0 {
			return k[:i], k[i+1:]
		}
	}
	return k, ""
}

func parseEdgeKey(k string) (edge, bool) {
	var parts [6]string
	n, start := 0, 0
	for i := 0; i <= len(k) && n < 6; i++ {
		if i == len(k) || k[i] == '|' {
			parts[n] = k[start:i]
			n++
			start = i + 1
		}
	}
	if n < 5 {
		return edge{}, false
	}
	return edge{Type: parts[0], FK: parts[1], FID: parts[2], TK: parts[3], TID: parts[4], Key: parts[5]}, true
}

// ---- reading the past ----

// Snapshot is the estate as it was at one recorded moment.
type Snapshot struct {
	At       time.Time
	Bytes    int
	Topology model.Topology
}

// AsOf reconstructs the estate as of the newest recorded moment at or before `at`.
func (d *DB) AsOf(ctx context.Context, org string, at time.Time) (Snapshot, error) {
	sc := d.C.For(org)
	res, err := d.C.Run(ctx, sc.S(`MATCH (s:Snapshot {org:$org}) WHERE s.at <= datetime($at)
RETURN toString(s.at), s.bytes, s.traffic, s.paths ORDER BY s.at DESC LIMIT 1`, map[string]any{"at": ts(at)}))
	if err != nil {
		return Snapshot{}, err
	}
	if len(res[0].Rows) == 0 {
		return Snapshot{}, store.ErrNotFound
	}
	r := res[0].Rows[0]
	sat := tm(r[0])
	out := Snapshot{At: sat, Bytes: int(i64(r[1]))}
	var tr map[string]counters
	var pq map[string]pathQuality
	_ = json.Unmarshal([]byte(str(r[2])), &tr)
	_ = json.Unmarshal([]byte(str(r[3])), &pq)

	docs, err := d.C.Run(ctx, sc.S(`MATCH (v:Version {org:$org}) WHERE v.validFrom <= datetime($at) AND (v.validTo IS NULL OR v.validTo > datetime($at))
RETURN v.kind, v.doc`, map[string]any{"at": ts(sat)}))
	if err != nil {
		return Snapshot{}, err
	}
	seen := ts(sat)
	t := model.Topology{Clusters: []model.Cluster{}, Nodes: []model.Node{}, Namespaces: []model.Namespace{}, Services: []model.Service{}, Suggestions: []model.Suggestion{}, Dependencies: []model.Dependency{}, ExternalEndpoints: []model.ExternalEndpoint{}, Paths: []model.Path{}}
	for _, row := range docs[0].Rows {
		doc := []byte(str(row[1]))
		switch str(row[0]) {
		case "cluster":
			var x model.Cluster
			if json.Unmarshal(doc, &x) == nil {
				x.LastSeen = seen
				t.Clusters = append(t.Clusters, x)
			}
		case "node":
			var x model.Node
			if json.Unmarshal(doc, &x) == nil {
				x.LastSeen = seen
				t.Nodes = append(t.Nodes, x)
			}
		case "namespace":
			var x model.Namespace
			if json.Unmarshal(doc, &x) == nil {
				x.LastSeen = seen
				t.Namespaces = append(t.Namespaces, x)
			}
		case "service":
			var x model.Service
			if json.Unmarshal(doc, &x) == nil {
				x.LastSeen = seen
				t.Services = append(t.Services, x)
			}
		case "external":
			var x model.ExternalEndpoint
			if json.Unmarshal(doc, &x) == nil {
				x.LastSeen = seen
				t.ExternalEndpoints = append(t.ExternalEndpoints, x)
			}
		case "dependency":
			var x model.Dependency
			if json.Unmarshal(doc, &x) == nil {
				if !x.Stale {
					x.LastSeen = seen
				}
				if c, ok := tr[x.ID]; ok {
					x.Bytes, x.Connections = c.Bytes, c.Conns
					if c.Bps > 0 || c.Cpm > 0 || c.Win > 0 {
						x.Stats = &model.DependencyStats{BytesPerSec: c.Bps, ConnectionsPerMin: c.Cpm, WindowSec: c.Win}
					}
				}
				t.Dependencies = append(t.Dependencies, x)
			}
		case "path":
			var x model.Path
			if json.Unmarshal(doc, &x) == nil {
				if q, ok := pq[x.ID]; ok {
					x.RTTMin, x.RTTP50, x.RTTP95, x.LossPct, x.Samples, x.At = q.Min, q.P50, q.P95, q.Loss, q.Samples, q.At
				}
				t.Paths = append(t.Paths, x)
			}
		}
	}
	sort.Slice(t.Clusters, func(i, j int) bool { return t.Clusters[i].ID < t.Clusters[j].ID })
	sort.Slice(t.Nodes, func(i, j int) bool { return t.Nodes[i].ID < t.Nodes[j].ID })
	sort.Slice(t.Namespaces, func(i, j int) bool { return t.Namespaces[i].ID < t.Namespaces[j].ID })
	sort.Slice(t.Services, func(i, j int) bool { return t.Services[i].ID < t.Services[j].ID })
	sort.Slice(t.Dependencies, func(i, j int) bool { return t.Dependencies[i].ID < t.Dependencies[j].ID })
	sort.Slice(t.ExternalEndpoints, func(i, j int) bool { return t.ExternalEndpoints[i].ID < t.ExternalEndpoints[j].ID })
	sort.Slice(t.Paths, func(i, j int) bool { return t.Paths[i].ID < t.Paths[j].ID })
	out.Topology = t
	return out, nil
}

// Points lists the recorded moments between since and until (zero = open), oldest first.
func (d *DB) Points(ctx context.Context, org string, since, until time.Time) ([]store.HistoryPoint, error) {
	sc := d.C.For(org)
	p := map[string]any{"since": "", "until": ""}
	q := `MATCH (s:Snapshot {org:$org})`
	cond := ""
	if !since.IsZero() {
		cond += ` s.at >= datetime($since)`
		p["since"] = ts(since)
	}
	if !until.IsZero() {
		if cond != "" {
			cond += " AND"
		}
		cond += ` s.at <= datetime($until)`
		p["until"] = ts(until)
	}
	if cond != "" {
		q += " WHERE" + cond
	}
	q += ` RETURN toString(s.at), s.bytes ORDER BY s.at`
	res, err := d.C.Run(ctx, sc.S(q, p))
	if err != nil {
		return nil, err
	}
	out := make([]store.HistoryPoint, 0, len(res[0].Rows))
	for _, r := range res[0].Rows {
		out = append(out, store.HistoryPoint{At: tm(r[0]), Bytes: int(i64(r[1]))})
	}
	return out, nil
}

// DeleteSnapshots forgets recorded moments (retention thins old ones), then discards the versions
// and links that ended before the oldest moment still remembered, since no remaining moment can
// show them.
func (d *DB) DeleteSnapshots(ctx context.Context, org string, ats []time.Time) error {
	defer d.lock(org)()
	sc := d.C.For(org)
	if len(ats) > 0 {
		s := make([]string, len(ats))
		for i, a := range ats {
			s[i] = ts(a.UTC().Truncate(time.Second))
		}
		if _, err := d.C.Run(ctx, sc.S(`UNWIND $ats AS a MATCH (s:Snapshot {org:$org, at:datetime(a)}) DETACH DELETE s`, map[string]any{"ats": s})); err != nil {
			return err
		}
	}
	res, err := d.C.Run(ctx, sc.S(`MATCH (s:Snapshot {org:$org}) RETURN toString(min(s.at))`, nil))
	if err != nil {
		return err
	}
	if len(res[0].Rows) == 0 || str(res[0].Rows[0][0]) == "" {
		return nil
	}
	oldest := str(res[0].Rows[0][0])
	for {
		r, err := d.C.Run(ctx, sc.S(`MATCH (v:Version {org:$org}) WHERE v.validTo IS NOT NULL AND v.validTo <= datetime($old)
WITH v LIMIT 2000 DETACH DELETE v RETURN count(*)`, map[string]any{"old": oldest}))
		if err != nil {
			return err
		}
		if i64(r[0].Rows[0][0]) < 2000 {
			break
		}
	}
	for _, rt := range relTypes {
		for {
			r, err := d.C.Run(ctx, sc.S(fmt.Sprintf(`MATCH ()-[r:%s {org:$org}]->() WHERE r.validTo IS NOT NULL AND r.validTo <= datetime($old)
WITH r LIMIT 2000 DELETE r RETURN count(*)`, rt), map[string]any{"old": oldest}))
			if err != nil {
				return err
			}
			if i64(r[0].Rows[0][0]) < 2000 {
				break
			}
		}
	}
	_, err = d.C.Run(ctx, sc.S(`MATCH (e:Entity {org:$org}) WHERE NOT (e)-[:HAS_VERSION]->() DETACH DELETE e`, nil))
	return err
}

// TrafficSamples reads the cumulative byte counters of every recorded moment in a window without
// rebuilding each moment's topology (the counters live on the snapshot for exactly this reason).
func (d *DB) TrafficSamples(ctx context.Context, org string, since, until time.Time) ([]history.Sample, error) {
	p := map[string]any{"since": ts(since)}
	q := `MATCH (s:Snapshot {org:$org}) WHERE s.at >= datetime($since)`
	if !until.IsZero() {
		q += ` AND s.at <= datetime($until)`
		p["until"] = ts(until)
	}
	q += ` RETURN toString(s.at), s.traffic ORDER BY s.at`
	res, err := d.C.Run(ctx, d.C.For(org).S(q, p))
	if err != nil {
		return nil, err
	}
	out := make([]history.Sample, 0, len(res[0].Rows))
	for _, r := range res[0].Rows {
		var tr map[string]counters
		_ = json.Unmarshal([]byte(str(r[1])), &tr)
		s := history.Sample{At: tm(r[0]), Bytes: make(map[string]uint64, len(tr))}
		for id, c := range tr {
			s.Bytes[id] = c.Bytes
		}
		out = append(out, s)
	}
	return out, nil
}
