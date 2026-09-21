package history

import (
	"strings"
	"testing"
	"time"

	"continuum/internal/model"
	"continuum/internal/store"
)

var t0 = time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

func cluster(id, name string) model.Cluster {
	return model.Cluster{ID: id, Name: name, Status: "healthy", Version: "v1.30.5"}
}
func node(id, cl, name, status string) model.Node {
	return model.Node{ID: id, ClusterID: cl, Name: name, Status: status, Role: "worker", Kind: "vm"}
}
func svc(id, cl, name string, replicas int32, nodes ...string) model.Service {
	return model.Service{ID: id, ClusterID: cl, Name: name, Namespace: "shop", Kind: "Deployment", Replicas: replicas, ReadyReplicas: replicas, Status: "healthy", Image: "img:1", NodeIDs: nodes}
}
func topo(cs []model.Cluster, ns []model.Node, ss []model.Service, ds ...model.Dependency) model.Topology {
	return model.Topology{Clusters: cs, Nodes: ns, Services: ss, Dependencies: ds}
}
func kinds(evs []store.Event) string {
	var k []string
	for _, e := range evs {
		k = append(k, e.Kind)
	}
	return strings.Join(k, ",")
}

func TestNoPreviousSnapshotMeansNoEvents(t *testing.T) {
	d := NewDiffer()
	if evs := d.Diff(nil, topo([]model.Cluster{cluster("c1", "a")}, nil, nil), t0); len(evs) != 0 {
		t.Fatalf("the first snapshot has nothing to compare with: %v", evs)
	}
}

func TestIdenticalTopologiesProduceNothing(t *testing.T) {
	a := topo([]model.Cluster{cluster("c1", "a")}, []model.Node{node("n1", "c1", "n1", "healthy")}, []model.Service{svc("s1", "c1", "api", 2, "n1")})
	if evs := NewDiffer().Diff(&a, a, t0); len(evs) != 0 {
		t.Fatalf("unexpected events: %v", kinds(evs))
	}
}

func TestScalingNamesTheCause(t *testing.T) {
	a := topo([]model.Cluster{cluster("c1", "a")}, nil, []model.Service{svc("s1", "c1", "api", 2), svc("s2", "c1", "db", 1)})
	b := topo([]model.Cluster{cluster("c1", "a")}, nil, []model.Service{svc("s1", "c1", "api", 4), svc("s2", "c1", "db", 3)})
	b.Services[0].Autoscaler = &model.Autoscaler{Min: 2, Max: 8}
	evs := NewDiffer().Diff(&a, b, t0)
	if len(evs) != 2 || evs[0].Kind != "service-scaled" {
		t.Fatalf("got %v", kinds(evs))
	}
	if !strings.Contains(evs[0].Detail, "2 → 4") || !strings.Contains(evs[0].Cause, "autoscaler (2–8)") {
		t.Fatalf("autoscaled: %+v", evs[0])
	}
	if !strings.Contains(evs[1].Cause, "no autoscaler") {
		t.Fatalf("a manual scale must not be blamed on an autoscaler: %+v", evs[1])
	}
}

func TestMigrationBetweenClustersIsOneEventNotTwo(t *testing.T) {
	cs := []model.Cluster{cluster("c1", "athens"), cluster("c2", "patras")}
	a := topo(cs, nil, []model.Service{svc("s-a", "c1", "mqtt", 1)})
	b := topo(cs, nil, []model.Service{svc("s-b", "c2", "mqtt", 1)})
	evs := NewDiffer().Diff(&a, b, t0)
	if len(evs) != 1 || evs[0].Kind != "service-migrated" || !strings.Contains(evs[0].Detail, "from athens to patras") {
		t.Fatalf("got %v %+v", kinds(evs), evs)
	}
}

func TestMigrationSpanningTwoComparisonsIsStillRecognised(t *testing.T) {
	cs := []model.Cluster{cluster("c1", "athens"), cluster("c2", "patras")}
	d := NewDiffer()
	a := topo(cs, nil, []model.Service{svc("s-a", "c1", "mqtt", 1)})
	mid := topo(cs, nil, nil)
	end := topo(cs, nil, []model.Service{svc("s-b", "c2", "mqtt", 1)})
	if evs := d.Diff(&a, mid, t0); kinds(evs) != "service-removed" {
		t.Fatalf("removal: %v", kinds(evs))
	}
	evs := d.Diff(&mid, end, t0.Add(10*time.Minute))
	if len(evs) != 1 || evs[0].Kind != "service-migrated" {
		t.Fatalf("the arrival is the second half of a move: %v", kinds(evs))
	}
	// an hour and a half later it is a fresh start, not a move
	d2 := NewDiffer()
	d2.Diff(&a, mid, t0)
	if evs := d2.Diff(&mid, end, t0.Add(90*time.Minute)); kinds(evs) != "service-added" {
		t.Fatalf("too old to be a move: %v", kinds(evs))
	}
}

func TestReschedulingBlamesTheNodeThatLeft(t *testing.T) {
	cs := []model.Cluster{cluster("c1", "a")}
	a := topo(cs, []model.Node{node("n1", "c1", "worker-1", "healthy"), node("n2", "c1", "worker-2", "healthy")}, []model.Service{svc("s1", "c1", "api", 1, "n1")})
	b := topo(cs, []model.Node{node("n1", "c1", "worker-1", "degraded"), node("n2", "c1", "worker-2", "healthy")}, []model.Service{svc("s1", "c1", "api", 1, "n2")})
	evs := NewDiffer().Diff(&a, b, t0)
	if kinds(evs) != "node-status,service-rescheduled" {
		t.Fatalf("got %v", kinds(evs))
	}
	if !strings.Contains(evs[1].Cause, "worker-1") || evs[0].Severity != "warning" {
		t.Fatalf("%+v %+v", evs[0], evs[1])
	}
}

func TestNewClusterIsOneEventNotOnePerChild(t *testing.T) {
	a := topo([]model.Cluster{cluster("c1", "a")}, nil, nil)
	b := topo([]model.Cluster{cluster("c1", "a"), cluster("c2", "edge-volos")},
		[]model.Node{node("n1", "c2", "n1", "healthy")}, []model.Service{svc("s1", "c2", "api", 1), svc("s2", "c2", "db", 1)})
	evs := NewDiffer().Diff(&a, b, t0)
	if len(evs) != 1 || evs[0].Kind != "cluster-added" || !strings.Contains(evs[0].Detail, "1 node and 2 services") {
		t.Fatalf("%v %+v", kinds(evs), evs)
	}
	c := topo([]model.Cluster{cluster("c1", "a")}, nil, nil)
	evs = NewDiffer().Diff(&b, c, t0)
	if len(evs) != 1 || evs[0].Kind != "cluster-removed" || evs[0].Severity != "warning" {
		t.Fatalf("%v", kinds(evs))
	}
}

func TestAgentGoingQuietIsReportedOnceAtClusterLevel(t *testing.T) {
	a := topo([]model.Cluster{cluster("c1", "a")}, []model.Node{node("n1", "c1", "n1", "healthy")}, []model.Service{svc("s1", "c1", "api", 1)})
	b := topo([]model.Cluster{cluster("c1", "a")}, []model.Node{node("n1", "c1", "n1", "healthy")}, []model.Service{svc("s1", "c1", "api", 1)})
	b.Clusters[0].Stale, b.Nodes[0].Stale, b.Services[0].Stale = true, true, true
	evs := NewDiffer().Diff(&a, b, t0)
	if kinds(evs) != "cluster-unreachable" {
		t.Fatalf("stale records are not changes: %v", kinds(evs))
	}
	if kinds(NewDiffer().Diff(&b, a, t0)) != "cluster-reachable" {
		t.Fatal("recovery")
	}
}

func TestObservedDependenciesAppearAndGoQuiet(t *testing.T) {
	cs := []model.Cluster{cluster("c1", "a"), cluster("c2", "b")}
	ss := []model.Service{svc("s1", "c1", "web", 1), svc("s2", "c2", "api", 1)}
	dep := model.Dependency{ID: "d1", From: "s1", FromKind: "service", To: "s2", ToKind: "service", Sources: []string{"observed"}, Protocol: "TCP", Port: 8080, CrossCluster: true}
	a := topo(cs, nil, ss)
	b := topo(cs, nil, ss, dep)
	evs := NewDiffer().Diff(&a, b, t0)
	if len(evs) != 1 || evs[0].Kind != "dependency-seen" || evs[0].Severity != "notice" || !strings.Contains(evs[0].Detail, "web → api (TCP 8080)") {
		t.Fatalf("%+v", evs)
	}
	stale := dep
	stale.Stale = true
	c := topo(cs, nil, ss, stale)
	if kinds(NewDiffer().Diff(&b, c, t0)) != "dependency-quiet" {
		t.Fatal("quiet")
	}
	// noise and declared-only links are not news
	noisy := dep
	noisy.Noise = "dns"
	if evs := NewDiffer().Diff(&a, topo(cs, nil, ss, noisy), t0); len(evs) != 0 {
		t.Fatalf("noise: %v", kinds(evs))
	}
}

func TestImageAndRestartsAndStatus(t *testing.T) {
	cs := []model.Cluster{cluster("c1", "a")}
	a := topo(cs, nil, []model.Service{svc("s1", "c1", "api", 2)})
	s := svc("s1", "c1", "api", 2)
	s.Image, s.Restarts, s.Status, s.ReadyReplicas = "img:2", 5, "degraded", 1
	evs := NewDiffer().Diff(&a, topo(cs, nil, []model.Service{s}), t0)
	if kinds(evs) != "service-image,service-status,service-restarts" {
		t.Fatalf("%v", kinds(evs))
	}
}

func TestAFloodOfChangesIsCapped(t *testing.T) {
	cs := []model.Cluster{cluster("c1", "a")}
	var many []model.Service
	for i := 0; i < 500; i++ {
		many = append(many, svc("s"+string(rune('a'+i%26))+strings.Repeat("x", i/26), "c1", "w", 1))
	}
	a := topo(cs, nil, nil)
	b := topo(cs, nil, many)
	evs := NewDiffer().Diff(&a, b, t0)
	if len(evs) != maxEventsPerDiff+1 || evs[len(evs)-1].Kind != "many-changes" {
		t.Fatalf("got %d events, last %s", len(evs), evs[len(evs)-1].Kind)
	}
}

func TestEncodeDecodeAndFingerprint(t *testing.T) {
	a := topo([]model.Cluster{cluster("c1", "a")}, nil, []model.Service{svc("s1", "c1", "api", 2)})
	a.Services[0].Labels = map[string]string{"big": "label"}
	a.Services[0].Evidence = map[string]model.Evidence{"x": {Signal: "y"}}
	c := Compact(a)
	if c.Services[0].Labels != nil || c.Services[0].Evidence != nil {
		t.Fatal("compact keeps only what a snapshot needs")
	}
	data, fp, err := Encode(c)
	if err != nil {
		t.Fatal(err)
	}
	back, err := Decode(data)
	if err != nil || len(back.Services) != 1 || back.Services[0].Name != "api" {
		t.Fatalf("%v %+v", err, back)
	}
	// volatile fields do not change the fingerprint; real changes do
	b := c
	b.Services = append([]model.Service{}, c.Services...)
	b.Services[0].LastSeen = "later"
	if Fingerprint(b) != fp {
		t.Fatal("a newer last-seen stamp is not a change")
	}
	b.Services[0].Replicas = 9
	if Fingerprint(b) == fp {
		t.Fatal("a scale is a change")
	}
	if _, err := Decode([]byte("not gzip")); err == nil {
		t.Fatal("garbage must not decode")
	}
}

func TestRetentionThinsOldSnapshots(t *testing.T) {
	now := t0
	var pts []store.HistoryPoint
	// every 10 minutes for 10 days
	for i := 0; i < 10*24*6; i++ {
		pts = append(pts, store.HistoryPoint{At: now.Add(-time.Duration(i) * 10 * time.Minute), Bytes: 100})
	}
	// oldest first as the store returns them
	for i, j := 0, len(pts)-1; i < j; i, j = i+1, j-1 {
		pts[i], pts[j] = pts[j], pts[i]
	}
	del := Retention(pts, now, 30, 0)
	gone := map[time.Time]bool{}
	for _, d := range del {
		gone[d] = true
	}
	last24, hourly := 0, map[string]int{}
	for _, p := range pts {
		if gone[p.At] {
			continue
		}
		age := now.Sub(p.At)
		if age <= 24*time.Hour {
			last24++
		} else if age <= 7*24*time.Hour {
			hourly[p.At.Format("2006-01-02T15")]++
		}
	}
	if last24 != 24*6+1 && last24 != 24*6 {
		t.Fatalf("everything from the last day is kept, got %d", last24)
	}
	for h, n := range hourly {
		if n != 1 {
			t.Fatalf("hour %s keeps %d snapshots", h, n)
		}
	}
	if gone[pts[len(pts)-1].At] {
		t.Fatal("the newest snapshot is never deleted")
	}
	// beyond retention everything goes
	old := []store.HistoryPoint{{At: now.Add(-40 * 24 * time.Hour), Bytes: 1}, {At: now, Bytes: 1}}
	if d := Retention(old, now, 30, 0); len(d) != 1 || !d[0].Equal(old[0].At) {
		t.Fatalf("%v", d)
	}
	// a size cap deletes the oldest first, never the newest
	small := []store.HistoryPoint{{At: now.Add(-3 * time.Hour), Bytes: 60}, {At: now.Add(-2 * time.Hour), Bytes: 60}, {At: now, Bytes: 60}}
	if d := Retention(small, now, 30, 100); len(d) != 2 || !d[0].Equal(small[0].At) || !d[1].Equal(small[1].At) {
		t.Fatalf("%v", d)
	}
}

func TestRates(t *testing.T) {
	s := []Sample{
		{At: t0, Bytes: map[string]uint64{"d1": 1000}},
		{At: t0.Add(60 * time.Second), Bytes: map[string]uint64{"d1": 7000}},          // 100 B/s
		{At: t0.Add(61 * time.Second), Bytes: map[string]uint64{"d1": 7000 + 5000}},   // a burst right after an event snapshot: not a peak
		{At: t0.Add(121 * time.Second), Bytes: map[string]uint64{"d1": 3, "d2": 600}}, // counter reset for d1; d2 is new
	}
	got := map[string]Rate{}
	for _, r := range Rates(s) {
		got[r.ID] = r
	}
	d1 := got["d1"]
	// 6000 + 5000 + 3 bytes over 121 s
	if d1.AvgBps < 90 || d1.AvgBps > 92 {
		t.Fatalf("avg %v", d1.AvgBps)
	}
	if d1.PeakBps < 99 || d1.PeakBps > 101 {
		t.Fatalf("peak %v (the 1 s interval must not count)", d1.PeakBps)
	}
	if got["d2"].AvgBps < 4.9 || got["d2"].AvgBps > 5.1 {
		t.Fatalf("d2 %v", got["d2"])
	}
	if len(Rates(nil)) != 0 || len(Rates(s[:1])) != 0 {
		t.Fatal("fewer than two samples has no rate")
	}
}
