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
	"strings"
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
// statements, so it comes from this list and nowhere else. The first seven are found by polling a
// topology and diffed by Record; "agent" is versioned directly by RecordEntity instead, the moment
// something about it changes, because its owner already knows that moment rather than needing to
// notice it by comparing two snapshots. Both kinds of entity share the same Entity/Version shape, so
// Timeline and AsOf do not need to know which path recorded a given one.
var kinds = []struct {
	Kind, Label string
	// Polled is true for a kind Record's own topology-poll manages end to end: it diffs a full picture
	// of every one of them against what is open every time it runs, so anything of that kind it does not
	// see any more is gone and its edges close. A kind recorded only through RecordEntity (agent today)
	// never hands Record such a full picture - RecordEntity versions one entity because its owner told it
	// to, not because a poll swept the whole estate - so Record's edge sweep must leave its edges alone
	// entirely, in every relationship type, or it would close them again the moment it next runs.
	Polled bool
}{
	{"cluster", "Cluster", true},
	{"node", "Node", true},
	{"namespace", "Namespace", true},
	{"service", "Service", true},
	{"external", "ExternalEndpoint", true},
	{"dependency", "Dependency", true},
	{"path", "Path", true},
	{"agent", "Agent", false},
	{"application", "Application", false},
}

func labelOf(kind string) string {
	for _, k := range kinds {
		if k.Kind == kind {
			return k.Label
		}
	}
	return ""
}

// polledKind says whether Record's own edge sweep owns an entity of this kind - see kinds.Polled.
func polledKind(kind string) bool {
	for _, k := range kinds {
		if k.Kind == kind {
			return k.Polled
		}
	}
	return false
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

	open, openEdge, last, err := d.openState(ctx, sc)
	if err != nil {
		return err
	}
	if !last.IsZero() && at.Before(last) {
		return fmt.Errorf("%w (%s < %s)", ErrOutOfOrder, ts(at), ts(last))
	}

	w := append([]Stmt{sc.S(`MERGE (t:Tenant {id:$org}) RETURN 1`, nil)}, diffEntities(sc, at, t, fp, size, open, openEdge)...)
	_, err = d.C.Run(ctx, w...)
	return err
}

// openState reads what Record and RecordCatchUp both need before they can diff anything: the newest
// recorded moment (to refuse an out-of-order write), every entity's currently open version hash, and
// every currently open edge's key. Pulled out so RecordCatchUp can read this exactly once for a whole
// batch of buffered snapshots rather than once per snapshot, the same reads Record itself has always made.
func (d *DB) openState(ctx context.Context, sc *Scope) (open map[string]string, openEdge map[string]bool, last time.Time, err error) {
	reads := []Stmt{
		sc.S(`MATCH (s:Snapshot {org:$org}) RETURN toString(s.at) ORDER BY s.at DESC LIMIT 1`, nil),
		sc.S(`MATCH (v:Version {org:$org}) WHERE v.validTo IS NULL RETURN v.kind, v.id, v.hash`, nil),
	}
	for _, rt := range relTypes {
		reads = append(reads, sc.S(fmt.Sprintf(`MATCH ()-[r:%s {org:$org}]->() WHERE r.validTo IS NULL RETURN r.ekey`, rt), nil))
	}
	res, err := d.C.Run(ctx, reads...)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	if len(res[0].Rows) > 0 {
		last = tm(res[0].Rows[0][0])
	}
	open = map[string]string{}
	for _, r := range res[1].Rows {
		open[vkey(str(r[0]), str(r[1]))] = str(r[2])
	}
	openEdge = map[string]bool{}
	for i := range relTypes {
		for _, r := range res[2+i].Rows {
			openEdge[str(r[0])] = true
		}
	}
	return open, openEdge, last, nil
}

// diffEntities is Record's own diff-and-build step, pulled out so RecordCatchUp can chain several
// snapshots' diffs together against state it only reads from Neo4j once, instead of once per snapshot -
// Record itself is now just openState followed by one call to this. open and openEdge are mutated in
// place to reflect the state right after this snapshot is applied, so a caller chaining several of these
// in sequence has each one diff against the last one's result: the same state Record would see if it
// re-read the database in between, simulated in memory instead of actually re-read.
func diffEntities(sc *Scope, at time.Time, t model.Topology, fp string, size int, open map[string]string, openEdge map[string]bool) []Stmt {
	vs, es, tr, pq := extract(t)

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
	var w []Stmt
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
			// Only an edge belonging to a kind Record's own poll actually covers is a candidate for
			// closing here - one RecordEntity opened (an agent's IN_CLUSTER edge) is invisible to
			// extract(t) by construction, not because it went away, and must be left exactly as it is.
			if e, ok := parseEdgeKey(k); ok && e.Type == rt && polledKind(e.FK) {
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
		// Carried into openEdge so a later snapshot in the same chained batch diffs against this one's
		// result rather than what was open before any of the batch was applied.
		for _, c := range closing {
			delete(openEdge, c["key"].(string))
		}
		for _, o := range opening {
			openEdge[o["key"].(string)] = true
		}
	}
	w = append(w, sc.S(`MERGE (s:Snapshot {org:$org, at:datetime($at)})
SET s.fp = $fp, s.bytes = $bytes, s.entities = $n, s.traffic = $traffic, s.paths = $paths
WITH s MATCH (t:Tenant {id:$org}) MERGE (t)-[:HAS_SNAPSHOT]->(s)`, map[string]any{
		"at": atS, "fp": fp, "bytes": size, "n": len(vs), "traffic": enc(tr), "paths": enc(pq),
	}))

	// Carried into open for the same reason as openEdge above.
	for k := range open {
		if _, ok := vs[k]; !ok {
			delete(open, k)
		}
	}
	for k, v := range vs {
		open[k] = v.Hash
	}

	return w
}

// CatchUpPoint is one buffered snapshot waiting to be recorded, as Store's own drainHistory already has
// it in hand once it decodes one: the same at/topology/fingerprint/size Record's own last four parameters
// carry, bundled so RecordCatchUp can take several without an unwieldy signature.
type CatchUpPoint struct {
	At   time.Time
	Topo model.Topology
	FP   string
	Size int
}

// RecordCatchUp applies several buffered snapshots, oldest first, as one Neo4j transaction rather than
// one Record call - and therefore one round trip pair - per point. drainHistory exists specifically to
// replay a backlog built up while Neo4j was unreachable, and replaying it one point at a time serialises
// a full read-then-write round trip per buffered point behind the very per-org lock a live sync is also
// waiting on; a backlog of a few hundred points made that a few hundred round trips where one pair would
// do. The graph ends up exactly as it would from calling Record once per point in order: openState is
// read once, and each point's diff is computed (via diffEntities) against the state the previous point in
// this same batch left, the same state Record would see by re-reading the database in between - just
// carried forward in memory instead of actually re-read, and all applied in the one transaction Run
// already gives any set of statements passed to it together.
//
// A point at or before the newest moment already recorded - in the database, or earlier in this same
// batch - is skipped rather than failing the whole batch, exactly as a lone out-of-order Record call
// returns ErrOutOfOrder without touching anything rather than erroring the caller out of recording
// anything newer; dropped reports which timestamps were skipped so the caller can log them the way it
// already does for a single out-of-order Record call. An empty points slice is a safe no-op that makes no
// request at all.
func (d *DB) RecordCatchUp(ctx context.Context, org string, points []CatchUpPoint) (applied int, dropped []time.Time, err error) {
	if len(points) == 0 {
		return 0, nil, nil
	}
	sort.Slice(points, func(i, j int) bool { return points[i].At.Before(points[j].At) })
	defer d.lock(org)()
	sc := d.C.For(org)

	open, openEdge, last, err := d.openState(ctx, sc)
	if err != nil {
		return 0, nil, err
	}

	w := []Stmt{sc.S(`MERGE (t:Tenant {id:$org}) RETURN 1`, nil)}
	for _, p := range points {
		at := p.At.UTC().Truncate(time.Second)
		if !last.IsZero() && at.Before(last) {
			dropped = append(dropped, p.At)
			continue
		}
		w = append(w, diffEntities(sc, at, p.Topo, p.FP, p.Size, open, openEdge)...)
		last = at
		applied++
	}
	if applied == 0 {
		return 0, dropped, nil
	}
	if _, err := d.C.Run(ctx, w...); err != nil {
		return 0, dropped, err
	}
	return applied, dropped, nil
}

// RecordEntity versions one entity's state outside the periodic topology scan: for state whose owner
// already knows the exact moment and reason it changed (an agent's tier changed, it was revoked) rather
// than noticing it by comparing two snapshots. It writes the same Version/HAS_VERSION shape Record uses
// for a whole topology, one entity at a time, so Timeline and AsOf treat every kind alike regardless of
// which path recorded it. If cluster is not empty, this entity's single IN_CLUSTER edge is opened or
// moved to match, the same invariant Record keeps for the kinds it polls (at most one open edge of a
// given type per entity). Idempotent: recording the same doc again is a no-op, so a caller can call this
// unconditionally after anything that might have changed the entity, without tracking what actually did.
func (d *DB) RecordEntity(ctx context.Context, org string, at time.Time, kind, id, name, status, cluster string, doc any) error {
	label := labelOf(kind)
	if label == "" {
		return fmt.Errorf("graph: %q is not an entity kind", kind)
	}
	at = at.UTC().Truncate(time.Second)
	defer d.lock(org)()
	sc := d.C.For(org)

	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	hash := hashDoc(raw)

	res, err := d.C.Run(ctx, sc.S(`MATCH (v:Version {org:$org, kind:$kind, id:$id}) WHERE v.validTo IS NULL RETURN v.hash, v.cluster`, map[string]any{"kind": kind, "id": id}))
	if err != nil {
		return err
	}
	hadOpen := len(res[0].Rows) > 0
	if hadOpen && str(res[0].Rows[0][0]) == hash && str(res[0].Rows[0][1]) == cluster {
		return nil // unchanged, including which cluster it belongs to: nothing to version, nothing to move
	}

	atS := ts(at)
	w := []Stmt{sc.S(`MERGE (t:Tenant {id:$org}) RETURN 1`, nil)}
	if hadOpen {
		// A version opened at this very instant is being replaced within it: drop it rather than leave a
		// zero-length version behind, exactly as Record does for the kinds it polls.
		w = append(w,
			sc.S(`MATCH (v:Version {org:$org, kind:$kind, id:$id}) WHERE v.validTo IS NULL AND v.validFrom = datetime($at)
DETACH DELETE v`, map[string]any{"kind": kind, "id": id, "at": atS}),
			sc.S(`MATCH (v:Version {org:$org, kind:$kind, id:$id}) WHERE v.validTo IS NULL
SET v.validTo = datetime($at)`, map[string]any{"kind": kind, "id": id, "at": atS}),
		)
	}
	w = append(w, sc.S(fmt.Sprintf(`MERGE (e:Entity {org:$org, kind:$kind, id:$id})
ON CREATE SET e.firstSeen = datetime($at)
SET e:%s, e.name = $name, e.status = $status, e.cluster = $cluster, e.gone = null
CREATE (v:Version {org:$org, kind:$kind, id:$id, validFrom:datetime($at), hash:$hash, doc:$doc, name:$name, status:$status, cluster:$cluster})
CREATE (e)-[:HAS_VERSION]->(v)`, label), map[string]any{"kind": kind, "id": id, "at": atS, "name": name, "status": status, "cluster": cluster, "hash": hash, "doc": string(raw)}))

	if cluster != "" {
		// Kept in step the same way a polled kind's IN_CLUSTER edge is: at most one open, closed and
		// reopened elsewhere if the entity moves. If the cluster itself has not been recorded yet (a very
		// new one, not yet scanned), this quietly records no edge; the next call that finds it will.
		ekey := edge{Type: "IN_CLUSTER", FK: kind, FID: id, TK: "cluster", TID: cluster}.ekey()
		ep := map[string]any{"kind": kind, "id": id, "cluster": cluster, "ekey": ekey, "at": atS}
		w = append(w,
			sc.S(`MATCH (:Entity {org:$org, kind:$kind, id:$id})-[r:IN_CLUSTER {org:$org}]->() WHERE r.validTo IS NULL AND r.ekey <> $ekey
SET r.validTo = datetime($at)`, ep),
			sc.S(`MATCH (a:Entity {org:$org, kind:$kind, id:$id})
MATCH (b:Cluster:Entity {org:$org, kind:'cluster', id:$cluster})
MERGE (a)-[r:IN_CLUSTER {org:$org, ekey:$ekey}]->(b)
ON CREATE SET r.validFrom = datetime($at)`, ep),
		)
	}
	_, err = d.C.Run(ctx, w...)
	return err
}

// LinkEntities keeps kind/id's own open outgoing edges of relType, to entities of targetKind, in step
// with exactly the ids given: whichever are open but not listed close, whichever are listed but not yet
// open, open. It is RecordEntity's counterpart for a many-target relationship (an application's CONTAINS
// to its member services) the way RecordEntity's own cluster parameter is for a single one (an agent's
// IN_CLUSTER): built on the same edge-key convention Record's own topology sweep uses, so a future query
// can walk any relationship in the graph uniformly regardless of which path wrote it. relType must be one
// of relTypes; targetIDs may be empty (closes everything currently open, opens nothing) to retire an
// entity's membership without retiring the entity itself.
func (d *DB) LinkEntities(ctx context.Context, org string, at time.Time, relType, kind, id, targetKind string, targetIDs []string) error {
	if !isRelType(relType) {
		return fmt.Errorf("graph: %q is not a relationship type", relType)
	}
	at = at.UTC().Truncate(time.Second)
	defer d.lock(org)()
	sc := d.C.For(org)

	want := map[string]edge{}
	for _, tid := range targetIDs {
		e := edge{Type: relType, FK: kind, FID: id, TK: targetKind, TID: tid}
		want[e.ekey()] = e
	}
	res, err := d.C.Run(ctx, sc.S(fmt.Sprintf(`MATCH (:Entity {org:$org, kind:$kind, id:$id})-[r:%s {org:$org}]->() WHERE r.validTo IS NULL RETURN r.ekey`, relType),
		map[string]any{"kind": kind, "id": id}))
	if err != nil {
		return err
	}
	type row = map[string]any
	var closing, opening []row
	for _, r := range res[0].Rows {
		k := str(r[0])
		if _, still := want[k]; still {
			delete(want, k) // already open: nothing to do
		} else {
			closing = append(closing, row{"key": k})
		}
	}
	for _, e := range want {
		opening = append(opening, row{"key": e.ekey(), "fk": e.FK, "fid": e.FID, "tk": e.TK, "tid": e.TID})
	}
	sort.Slice(closing, func(i, j int) bool { return closing[i]["key"].(string) < closing[j]["key"].(string) })
	sort.Slice(opening, func(i, j int) bool { return opening[i]["key"].(string) < opening[j]["key"].(string) })

	var w []Stmt
	if len(closing) > 0 {
		w = append(w, sc.S(fmt.Sprintf(`UNWIND $rows AS row
MATCH ()-[r:%s {org:$org, ekey:row.key}]->() WHERE r.validTo IS NULL
SET r.validTo = datetime($at)`, relType), map[string]any{"rows": closing, "at": ts(at)}))
	}
	if len(opening) > 0 {
		w = append(w, sc.S(fmt.Sprintf(`UNWIND $rows AS row
MATCH (a:Entity {org:$org, kind:row.fk, id:row.fid})
MATCH (b:Entity {org:$org, kind:row.tk, id:row.tid})
CREATE (a)-[:%s {org:$org, ekey:row.key, validFrom:datetime($at)}]->(b)`, relType), map[string]any{"rows": opening, "at": ts(at)}))
	}
	if len(w) == 0 {
		return nil
	}
	_, err = d.C.Run(ctx, w...)
	return err
}

// EntityRecord is one entity to version via RecordEntities - the batch counterpart of RecordEntity's own
// positional id/name/status/cluster/doc arguments, bundled so many entities of the same kind can be
// checked and written in two round trips total instead of two per entity.
type EntityRecord struct {
	ID      string
	Name    string
	Status  string
	Cluster string
	Doc     any
}

// RecordEntities is RecordEntity for many entities of the same kind at once: one read checking every
// entity's current hash/cluster, then one write for whichever actually changed, instead of a read and a
// write per entity (which is what repeatedly calling RecordEntity in a loop costs - this is purely a
// call-site batching of the same semantics, not a new transport: Client.Run already carries any number of
// statements in one transaction). Semantics match RecordEntity exactly, entity by entity: idempotent, the
// same version-replace-within-the-same-instant handling, the same IN_CLUSTER edge upkeep.
func (d *DB) RecordEntities(ctx context.Context, org string, at time.Time, kind string, recs []EntityRecord) error {
	if len(recs) == 0 {
		return nil
	}
	label := labelOf(kind)
	if label == "" {
		return fmt.Errorf("graph: %q is not an entity kind", kind)
	}
	at = at.UTC().Truncate(time.Second)
	defer d.lock(org)()
	sc := d.C.For(org)

	type prepared struct {
		rec  EntityRecord
		hash string
		raw  string
	}
	byID := make(map[string]*prepared, len(recs))
	ids := make([]string, 0, len(recs))
	for _, r := range recs {
		raw, err := json.Marshal(r.Doc)
		if err != nil {
			return err
		}
		byID[r.ID] = &prepared{rec: r, hash: hashDoc(raw), raw: string(raw)}
		ids = append(ids, r.ID)
	}

	res, err := d.C.Run(ctx, sc.S(`MATCH (v:Version {org:$org, kind:$kind}) WHERE v.validTo IS NULL AND v.id IN $ids RETURN v.id, v.hash, v.cluster`,
		map[string]any{"kind": kind, "ids": ids}))
	if err != nil {
		return err
	}
	type openRow struct{ hash, cluster string }
	open := map[string]openRow{}
	for _, row := range res[0].Rows {
		open[str(row[0])] = openRow{str(row[1]), str(row[2])}
	}

	atS := ts(at)
	w := []Stmt{sc.S(`MERGE (t:Tenant {id:$org}) RETURN 1`, nil)}
	var toClose, toCreate, clusterRows []map[string]any
	for _, id := range ids {
		p := byID[id]
		o, hadOpen := open[id]
		if hadOpen && o.hash == p.hash && o.cluster == p.rec.Cluster {
			continue // unchanged, including which cluster it belongs to: nothing to version, nothing to move
		}
		if hadOpen {
			toClose = append(toClose, map[string]any{"id": id})
		}
		toCreate = append(toCreate, map[string]any{"id": id, "name": p.rec.Name, "status": p.rec.Status, "cluster": p.rec.Cluster, "hash": p.hash, "doc": p.raw})
		if p.rec.Cluster != "" {
			ekey := edge{Type: "IN_CLUSTER", FK: kind, FID: id, TK: "cluster", TID: p.rec.Cluster}.ekey()
			clusterRows = append(clusterRows, map[string]any{"id": id, "cluster": p.rec.Cluster, "ekey": ekey})
		}
	}
	if len(toCreate) == 0 {
		return nil
	}
	if len(toClose) > 0 {
		w = append(w,
			sc.S(`UNWIND $rows AS row
MATCH (v:Version {org:$org, kind:$kind, id:row.id}) WHERE v.validTo IS NULL AND v.validFrom = datetime($at)
DETACH DELETE v`, map[string]any{"kind": kind, "rows": toClose, "at": atS}),
			sc.S(`UNWIND $rows AS row
MATCH (v:Version {org:$org, kind:$kind, id:row.id}) WHERE v.validTo IS NULL
SET v.validTo = datetime($at)`, map[string]any{"kind": kind, "rows": toClose, "at": atS}),
		)
	}
	// label is the same for every row of one kind (labelOf(kind) is a function of kind alone, like
	// RecordEntity's own single-entity write), so one literal :%s applies to the whole UNWIND.
	w = append(w, sc.S(fmt.Sprintf(`UNWIND $rows AS row
MERGE (e:Entity {org:$org, kind:$kind, id:row.id})
ON CREATE SET e.firstSeen = datetime($at)
SET e:%s, e.name = row.name, e.status = row.status, e.cluster = row.cluster, e.gone = null
CREATE (v:Version {org:$org, kind:$kind, id:row.id, validFrom:datetime($at), hash:row.hash, doc:row.doc, name:row.name, status:row.status, cluster:row.cluster})
CREATE (e)-[:HAS_VERSION]->(v)`, label), map[string]any{"kind": kind, "at": atS, "rows": toCreate}))
	if len(clusterRows) > 0 {
		w = append(w,
			sc.S(`UNWIND $rows AS row
MATCH (:Entity {org:$org, kind:$kind, id:row.id})-[r:IN_CLUSTER {org:$org}]->() WHERE r.validTo IS NULL AND r.ekey <> row.ekey
SET r.validTo = datetime($at)`, map[string]any{"kind": kind, "rows": clusterRows, "at": atS}),
			sc.S(`UNWIND $rows AS row
MATCH (a:Entity {org:$org, kind:$kind, id:row.id})
MATCH (b:Cluster:Entity {org:$org, kind:'cluster', id:row.cluster})
MERGE (a)-[r:IN_CLUSTER {org:$org, ekey:row.ekey}]->(b)
ON CREATE SET r.validFrom = datetime($at)`, map[string]any{"kind": kind, "rows": clusterRows, "at": atS}),
		)
	}
	_, err = d.C.Run(ctx, w...)
	return err
}

// MemberSet is one entity's desired member-id set for LinkEntitiesBatch - the batch counterpart of
// LinkEntities' own id/targetIDs arguments.
type MemberSet struct {
	ID        string
	TargetIDs []string
}

// LinkEntitiesBatch is LinkEntities for many entities sharing the same kind/relType/targetKind at once:
// one read covering every entity's currently open edges, then one write for whichever edges actually need
// to open or close, instead of a read and a write per entity. Semantics match LinkEntities exactly, entity
// by entity.
func (d *DB) LinkEntitiesBatch(ctx context.Context, org string, at time.Time, relType, kind, targetKind string, sets []MemberSet) error {
	if !isRelType(relType) {
		return fmt.Errorf("graph: %q is not a relationship type", relType)
	}
	if len(sets) == 0 {
		return nil
	}
	at = at.UTC().Truncate(time.Second)
	defer d.lock(org)()
	sc := d.C.For(org)

	ids := make([]string, len(sets))
	want := make(map[string]map[string]edge, len(sets))
	for i, s := range sets {
		ids[i] = s.ID
		m := make(map[string]edge, len(s.TargetIDs))
		for _, tid := range s.TargetIDs {
			e := edge{Type: relType, FK: kind, FID: s.ID, TK: targetKind, TID: tid}
			m[e.ekey()] = e
		}
		want[s.ID] = m
	}

	res, err := d.C.Run(ctx, sc.S(fmt.Sprintf(`MATCH (a:Entity {org:$org, kind:$kind})-[r:%s {org:$org}]->() WHERE r.validTo IS NULL AND a.id IN $ids RETURN a.id, r.ekey`, relType),
		map[string]any{"kind": kind, "ids": ids}))
	if err != nil {
		return err
	}
	type row = map[string]any
	var closing, opening []row
	for _, r := range res[0].Rows {
		id, k := str(r[0]), str(r[1])
		if m, ok := want[id]; ok {
			if _, still := m[k]; still {
				delete(m, k) // already open: nothing to do
				continue
			}
		}
		closing = append(closing, row{"key": k})
	}
	for _, m := range want {
		for _, e := range m {
			opening = append(opening, row{"key": e.ekey(), "fk": e.FK, "fid": e.FID, "tk": e.TK, "tid": e.TID})
		}
	}
	sort.Slice(closing, func(i, j int) bool { return closing[i]["key"].(string) < closing[j]["key"].(string) })
	sort.Slice(opening, func(i, j int) bool { return opening[i]["key"].(string) < opening[j]["key"].(string) })

	var w []Stmt
	if len(closing) > 0 {
		w = append(w, sc.S(fmt.Sprintf(`UNWIND $rows AS row
MATCH ()-[r:%s {org:$org, ekey:row.key}]->() WHERE r.validTo IS NULL
SET r.validTo = datetime($at)`, relType), map[string]any{"rows": closing, "at": ts(at)}))
	}
	if len(opening) > 0 {
		w = append(w, sc.S(fmt.Sprintf(`UNWIND $rows AS row
MATCH (a:Entity {org:$org, kind:row.fk, id:row.fid})
MATCH (b:Entity {org:$org, kind:row.tk, id:row.tid})
CREATE (a)-[:%s {org:$org, ekey:row.key, validFrom:datetime($at)}]->(b)`, relType), map[string]any{"rows": opening, "at": ts(at)}))
	}
	if len(w) == 0 {
		return nil
	}
	_, err = d.C.Run(ctx, w...)
	return err
}

// CloseMissingEntities closes the still-open version of every entity of `kind` in this org that is not
// in keepIDs, and marks it gone - the same "this poll's full picture says so-and-so no longer exists"
// Record's own diff already does for the seven polled kinds, for the rare RecordEntity-driven kind whose
// owner really can enumerate everything that currently exists (an application, declared in one document)
// rather than only ever learning about one entity changing at a time. Returns the ids it closed, so a
// caller can also retire whatever those entities were linked to (see LinkEntities) - closing an entity
// here does not by itself touch its edges.
func (d *DB) CloseMissingEntities(ctx context.Context, org string, at time.Time, kind string, keepIDs []string) ([]string, error) {
	at = at.UTC().Truncate(time.Second)
	defer d.lock(org)()
	sc := d.C.For(org)
	res, err := d.C.Run(ctx, sc.S(`MATCH (e:Entity {org:$org, kind:$kind}) WHERE e.gone IS NULL AND NOT e.id IN $keep
OPTIONAL MATCH (e)-[:HAS_VERSION]->(v:Version {org:$org}) WHERE v.validTo IS NULL
SET e.gone = datetime($at), v.validTo = datetime($at)
RETURN DISTINCT e.id`, map[string]any{"kind": kind, "keep": keepIDs, "at": ts(at)}))
	if err != nil {
		return nil, err
	}
	out := make([]string, len(res[0].Rows))
	for i, r := range res[0].Rows {
		out[i] = str(r[0])
	}
	sort.Strings(out)
	return out, nil
}

// LinkEventChanges connects each of this batch's events to the version of its target that was created at
// the same moment, if there is one: the graph's own record of what an event explains, not just that they
// happened close together in time. Events with no single target (a "many changes" summary, for instance)
// are skipped - there is nothing for them to point at. Safe to call whether or not a version was actually
// created for every event's target: an event with nothing to link to simply links to nothing.
func (d *DB) LinkEventChanges(ctx context.Context, org string, at time.Time, evs []store.Event) error {
	at = at.UTC().Truncate(time.Second)
	seen := map[string]bool{}
	var rows []map[string]any
	for _, e := range evs {
		if e.TargetKind == "" || e.TargetID == "" {
			continue
		}
		k := e.TargetKind + "\x00" + e.TargetID
		if seen[k] {
			continue
		}
		seen[k] = true
		rows = append(rows, map[string]any{"kind": e.TargetKind, "id": e.TargetID})
	}
	if len(rows) == 0 {
		return nil
	}
	// Events carry their own full-precision instant; the version they explain was written with its
	// validFrom truncated to the second (see Record and RecordEntity). Both fall in the same one-second
	// window starting at the truncated instant, since truncation only ever moves a moment earlier.
	_, err := d.C.Run(ctx, d.C.For(org).S(`UNWIND $rows AS row
MATCH (ev:Event {org:$org, targetKind:row.kind, targetId:row.id})
WHERE ev.at >= datetime($at) AND ev.at < datetime($at) + duration({seconds: 1})
MATCH (v:Version {org:$org, kind:row.kind, id:row.id, validFrom:datetime($at)})
MERGE (ev)-[:EXPLAINS]->(v)`, map[string]any{"rows": rows, "at": ts(at)}))
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

// EntitySnapshot is one entity's versioned state as of a moment, independent of what kind of
// thing it is. It is the schema-agnostic foundation everything that reads the past is built on:
// AsOf projects it into the typed model.Topology shape the UI and the existing history API expect,
// while a kind-agnostic consumer (an external API, an LLM) can read it directly without this
// package knowing anything about that consumer. A new entity kind added later -- versioned through
// either Record or RecordEntity -- shows up here with no change to this method; only a typed
// projection like AsOf needs updating to expose it in a shape its callers expect.
type EntitySnapshot struct {
	Kind    string          `json:"kind"`
	ID      string          `json:"id"`
	Name    string          `json:"name,omitempty"`
	Status  string          `json:"status,omitempty"`
	Cluster string          `json:"cluster,omitempty"`
	Doc     json.RawMessage `json:"doc"`
}

// AsOfEntities returns every entity's Version doc valid at `at`, whatever kind it is, alongside the
// instant it was matched against (truncated to the second, the precision every Version is written
// at). It is the schema-agnostic foundation everything that reads the past can be built on: AsOf
// projects it into the typed model.Topology shape the UI and the existing history API expect, while a
// kind-agnostic consumer -- an external API, an LLM walking the graph -- can read it directly without
// this package knowing anything about that consumer. A new entity kind added later, versioned through
// either Record or RecordEntity, shows up here with no change to this method.
//
// Unlike AsOf, this does not round down to the nearest full-topology poll. An entity recorded through
// RecordEntity changes the moment its owner acts on it, not on the topology poll's schedule, so asking
// "what was true at this instant" should see that change right away rather than waiting for the next
// poll to catch up. AsOf keeps rounding, because the seven kinds it projects only ever change together
// in lockstep with a poll, so "the last poll at or before this instant" is the question that matters
// for them; AsOfEntities makes no such assumption about a kind it has never heard of.
func (d *DB) AsOfEntities(ctx context.Context, org string, at time.Time) (time.Time, []EntitySnapshot, error) {
	at = at.UTC().Truncate(time.Second)
	sc := d.C.For(org)

	exists, err := d.C.Run(ctx, sc.S(`MATCH (e:Entity {org:$org}) WHERE e.firstSeen <= datetime($at) RETURN 1 LIMIT 1`, map[string]any{"at": ts(at)}))
	if err != nil {
		return time.Time{}, nil, err
	}
	if len(exists[0].Rows) == 0 {
		return time.Time{}, nil, store.ErrNotFound
	}

	docs, err := d.C.Run(ctx, sc.S(`MATCH (v:Version {org:$org}) WHERE v.validFrom <= datetime($at) AND (v.validTo IS NULL OR v.validTo > datetime($at))
RETURN v.kind, v.id, v.name, v.status, v.cluster, v.doc`, map[string]any{"at": ts(at)}))
	if err != nil {
		return time.Time{}, nil, err
	}
	out := make([]EntitySnapshot, 0, len(docs[0].Rows))
	for _, row := range docs[0].Rows {
		out = append(out, EntitySnapshot{
			Kind:    str(row[0]),
			ID:      str(row[1]),
			Name:    str(row[2]),
			Status:  str(row[3]),
			Cluster: str(row[4]),
			Doc:     json.RawMessage(str(row[5])),
		})
	}
	return at, out, nil
}

// AsOf reconstructs the estate as of the newest recorded moment at or before `at`, as the typed
// model.Topology the UI and the existing history API already know. It is a projection of
// AsOfEntities: the kinds it understands are unmarshaled into their model type, and anything else
// (an "agent" version, or a future kind this function has not been taught about) is left out here --
// present in AsOfEntities, absent from this narrower view, by design.
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

	_, entities, err := d.AsOfEntities(ctx, org, sat)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return Snapshot{}, err
	}
	seen := ts(sat)
	t := model.Topology{Clusters: []model.Cluster{}, Nodes: []model.Node{}, Namespaces: []model.Namespace{}, Services: []model.Service{}, Suggestions: []model.Suggestion{}, Dependencies: []model.Dependency{}, ExternalEndpoints: []model.ExternalEndpoint{}, Paths: []model.Path{}}
	for _, es := range entities {
		doc := []byte(es.Doc)
		switch es.Kind {
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

// ---- multi-hop traversal ----

// maxWalkHops bounds how far Dependents and Dependencies will walk. Clamped here rather than trusted
// from a caller, the same discipline relType interpolation already follows against the closed relTypes
// list.
const maxWalkHops = 8

// maxWalkResults bounds how many entities a single walk will ever report. Past this many, the walk
// stops opening further hops and returns what it already found: a hub node (a shared cluster, a
// heavily-called service) can otherwise turn a handful of hops into an unbounded fan-out, and a walk
// exists to answer "what's connected", not to enumerate an entire estate.
const maxWalkResults = 4000

// Reached is one entity a graph walk found, alongside how many hops away it was. An entity reachable
// by more than one path through the estate is reported once, at its shortest distance.
type Reached struct {
	EntitySnapshot
	Hops int `json:"hops"`
}

// Dependents returns everything that would be affected, directly or transitively, if kind/id became
// unavailable at `at`: every entity reached by walking the graph's relationship edges backward from
// it, up to hops steps (clamped to [1, maxWalkHops]). Every relationship type this schema ever writes
// points from the dependent thing to the thing it depends on - a service CALLS the service it queries,
// a service RUNS_ON the node it is scheduled on, anything IN_CLUSTER the cluster that hosts it, a path
// PATH_FROM/PATH_TO the cluster it measures, an application CONTAINS the services it groups - so
// walking backward from the entity that failed finds exactly what would notice: whoever calls the
// failed service, whatever was scheduled on the failed node, whatever the failed cluster hosted, any
// path that measured it, any application it belonged to, and, one more hop out, whatever in turn
// depended on those. This is a structural query, not a numeric estimate: it says what is connected,
// not how badly each one would be hurt.
func (d *DB) Dependents(ctx context.Context, org string, at time.Time, kind, id string, hops int) (time.Time, []Reached, error) {
	return d.walk(ctx, org, at, kind, id, hops, false)
}

// Dependencies returns everything kind/id itself relies on to do its job at `at`: the same walk as
// Dependents, in the opposite direction - what it calls, what it runs on, what cluster it belongs to,
// what paths measure it, which application(s) contain it - out to hops steps. Where Dependents answers
// "what breaks if this does," Dependencies answers "what does this need in order to keep working."
func (d *DB) Dependencies(ctx context.Context, org string, at time.Time, kind, id string, hops int) (time.Time, []Reached, error) {
	return d.walk(ctx, org, at, kind, id, hops, true)
}

// vk is the (kind, id) pair a walk moves between - cheap to hold thousands of in memory, and the same
// shape a level's frontier is sent back to Neo4j as for the next one.
type vk struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// walk finds everything connected to kind/id by the graph's relationship edges, out to hops steps, as
// of `at`, moving level by level (breadth-first) rather than asking Neo4j to enumerate every path with
// a single *1..hops variable-length pattern. This schema keeps every historical edge - a relationship
// is closed (validTo set) when it changes, never deleted - so a node with real fan-out, or simply a
// long history behind it, can carry far more edges than it has distinct neighbors; a variable-length
// pattern walks every one of those edges as a separate path before min(size(rels)) can collapse them
// back down to "reached, at its shortest distance", and the cost of that is exponential in the branching
// it meets at each hop. One single-hop query per level, over only the entities the previous level
// reached and not already visited, costs work proportional to the frontier's actual size instead - the
// trade is up to `hops` round trips to the database instead of one, which is cheap next to a query that
// might not return. maxWalkResults is the second, independent backstop: even a frontier that keeps
// growing every level stops being followed once the estate found gets implausibly large for one answer.
// walkTimeout bounds the whole of one Dependents/Dependencies call, across every one of the up to
// maxWalkHops+2 round trips it now makes to Neo4j (see walk's own doc comment for why there are several).
// Each individual call to the client is already bounded on its own (Client's http.Client carries its own
// request timeout), but that bounds one round trip, not the operation: a walk that is unlucky on every
// level could otherwise take that per-call timeout multiplied by the hop count. Shorter than the client's
// own per-request timeout, so it is this budget, not that one, that actually governs a slow walk - and
// once it fires, the in-flight call fails immediately rather than running out its own longer allowance.
// A var, not a const, so a test can shrink it to prove the wiring actually cancels a walk rather than
// just trusting that context.WithTimeout was spelled correctly.
var walkTimeout = 20 * time.Second

func (d *DB) walk(ctx context.Context, org string, at time.Time, kind, id string, hops int, forward bool) (time.Time, []Reached, error) {
	ctx, cancel := context.WithTimeout(ctx, walkTimeout)
	defer cancel()
	at = at.UTC().Truncate(time.Second)
	if hops < 1 {
		hops = 1
	}
	if hops > maxWalkHops {
		hops = maxWalkHops
	}
	sc := d.C.For(org)
	atS := ts(at)

	start, err := d.C.Run(ctx, sc.S(`MATCH (e:Entity {org:$org, kind:$kind, id:$id}) RETURN 1 LIMIT 1`, map[string]any{"kind": kind, "id": id}))
	if err != nil {
		return time.Time{}, nil, err
	}
	if len(start[0].Rows) == 0 {
		return time.Time{}, nil, store.ErrNotFound
	}

	step := fmt.Sprintf(`UNWIND $frontier AS fr
MATCH (f:Entity {org:$org, kind:fr.kind, id:fr.id})<-[rel:%s]-(n:Entity {org:$org})
WHERE rel.validFrom <= datetime($at) AND (rel.validTo IS NULL OR rel.validTo > datetime($at))
RETURN DISTINCT n.kind AS kind, n.id AS id`, strings.Join(relTypes, "|"))
	if forward {
		step = fmt.Sprintf(`UNWIND $frontier AS fr
MATCH (f:Entity {org:$org, kind:fr.kind, id:fr.id})-[rel:%s]->(n:Entity {org:$org})
WHERE rel.validFrom <= datetime($at) AND (rel.validTo IS NULL OR rel.validTo > datetime($at))
RETURN DISTINCT n.kind AS kind, n.id AS id`, strings.Join(relTypes, "|"))
	}

	visited := map[string]int{vkey(kind, id): 0} // hop distance of everything already placed; the start is hop 0 and never reported
	frontier := []vk{{Kind: kind, ID: id}}
	var order []vk // discovery order: hop order first, which is what the final sort needs to be stable against

	for h := 1; h <= hops && len(frontier) > 0 && len(visited) <= maxWalkResults; h++ {
		fr := make([]map[string]any, len(frontier))
		for i, f := range frontier {
			fr[i] = map[string]any{"kind": f.Kind, "id": f.ID}
		}
		rows, err := d.C.Run(ctx, sc.S(step, map[string]any{"frontier": fr, "at": atS}))
		if err != nil {
			return time.Time{}, nil, err
		}
		next := make([]vk, 0, len(rows[0].Rows))
		for _, r := range rows[0].Rows {
			nk, nid := str(r[0]), str(r[1])
			key := vkey(nk, nid)
			if _, seen := visited[key]; seen {
				continue
			}
			visited[key] = h
			v := vk{Kind: nk, ID: nid}
			next = append(next, v)
			order = append(order, v)
			if len(visited) >= maxWalkResults {
				break // the cap bites mid-level: what is already placed stands, nothing later this level joins it
			}
		}
		frontier = next
	}

	if len(order) == 0 {
		return at, []Reached{}, nil
	}

	pairs := make([]map[string]any, len(order))
	for i, o := range order {
		pairs[i] = map[string]any{"kind": o.Kind, "id": o.ID}
	}
	res, err := d.C.Run(ctx, sc.S(`UNWIND $pairs AS p
MATCH (e:Entity {org:$org, kind:p.kind, id:p.id})-[:HAS_VERSION]->(v:Version {org:$org})
WHERE v.validFrom <= datetime($at) AND (v.validTo IS NULL OR v.validTo > datetime($at))
RETURN v.kind, v.id, v.name, v.status, v.cluster, v.doc`, map[string]any{"pairs": pairs, "at": atS}))
	if err != nil {
		return time.Time{}, nil, err
	}
	out := make([]Reached, 0, len(res[0].Rows))
	for _, row := range res[0].Rows {
		k, i := str(row[0]), str(row[1])
		out = append(out, Reached{
			EntitySnapshot: EntitySnapshot{Kind: k, ID: i, Name: str(row[2]), Status: str(row[3]), Cluster: str(row[4]), Doc: json.RawMessage(str(row[5]))},
			Hops:           visited[vkey(k, i)],
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Hops != out[j].Hops {
			return out[i].Hops < out[j].Hops
		}
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].ID < out[j].ID
	})
	return at, out, nil
}

// ---- structural diff ----

// EntityDiff describes how one entity looked different between two moments: which fields moved, from
// what to what - the same per-field Change Timeline already reports between an entity's own successive
// versions (see diffDocs), generalized here to compare two arbitrary instants rather than two adjacent
// ones.
type EntityDiff struct {
	Kind    string   `json:"kind"`
	ID      string   `json:"id"`
	Name    string   `json:"name,omitempty"`
	Changes []Change `json:"changes"`
}

// StructuralDiff is what changed across the whole estate between two moments: entities that came into
// existence, entities that were retired, and entities present at both moments but that looked
// different by the second one - down to which fields moved. Built from two AsOfEntities reads rather
// than the event log, so it still answers precisely even across a span the event log has since pruned,
// or for a kind (an application) that events were never written for.
type StructuralDiff struct {
	From    time.Time        `json:"from"`
	To      time.Time        `json:"to"`
	Added   []EntitySnapshot `json:"added"`
	Removed []EntitySnapshot `json:"removed"`
	Changed []EntityDiff     `json:"changed"`
}

// DiffEntities compares the estate as of `from` against the estate as of `to`, entity by entity. One
// absent at `from` and present at `to` is Added; the reverse is Removed; one present at both, with a
// doc that reads differently, is Changed, down to which fields moved. Neither moment needs anything to
// have been recorded yet - an org with no history before `from` simply reports everything at `to` as
// Added.
func (d *DB) DiffEntities(ctx context.Context, org string, from, to time.Time) (StructuralDiff, error) {
	fat, froms, err := d.AsOfEntities(ctx, org, from)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return StructuralDiff{}, err
	}
	tat, tos, err := d.AsOfEntities(ctx, org, to)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return StructuralDiff{}, err
	}

	byFrom := make(map[string]EntitySnapshot, len(froms))
	for _, e := range froms {
		byFrom[vkey(e.Kind, e.ID)] = e
	}
	byTo := make(map[string]EntitySnapshot, len(tos))
	for _, e := range tos {
		byTo[vkey(e.Kind, e.ID)] = e
	}

	out := StructuralDiff{From: fat, To: tat, Added: []EntitySnapshot{}, Removed: []EntitySnapshot{}, Changed: []EntityDiff{}}
	for k, e := range byTo {
		if _, ok := byFrom[k]; !ok {
			out.Added = append(out.Added, e)
		}
	}
	for k, e := range byFrom {
		if _, ok := byTo[k]; !ok {
			out.Removed = append(out.Removed, e)
		}
	}
	for k, oe := range byFrom {
		ne, ok := byTo[k]
		if !ok {
			continue
		}
		var od, nd map[string]any
		_ = json.Unmarshal(oe.Doc, &od)
		_ = json.Unmarshal(ne.Doc, &nd)
		if ch := diffDocs(od, nd); len(ch) > 0 {
			out.Changed = append(out.Changed, EntityDiff{Kind: ne.Kind, ID: ne.ID, Name: ne.Name, Changes: ch})
		}
	}
	less := func(a, b EntitySnapshot) bool {
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.ID < b.ID
	}
	sort.Slice(out.Added, func(i, j int) bool { return less(out.Added[i], out.Added[j]) })
	sort.Slice(out.Removed, func(i, j int) bool { return less(out.Removed[i], out.Removed[j]) })
	sort.Slice(out.Changed, func(i, j int) bool {
		if out.Changed[i].Kind != out.Changed[j].Kind {
			return out.Changed[i].Kind < out.Changed[j].Kind
		}
		return out.Changed[i].ID < out.Changed[j].ID
	})
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
