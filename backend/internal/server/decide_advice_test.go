package server

import (
	"encoding/json"
	"flag"
	"os"
	"reflect"
	"strings"
	"testing"

	"continuum/internal/advice"
	"continuum/internal/twin"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden files")

const advGib = float64(1 << 30)

func advNum(v float64, unit string, c twin.Confidence, at string) twin.Attr {
	return twin.Attr{Value: v, Unit: unit, Source: twin.FromAgent, Confidence: c, ObservedAt: at, State: twin.Live}
}
func advUnk(unit, why string) twin.Attr {
	return twin.Attr{Value: nil, Unit: unit, Source: twin.FromAgent, Confidence: twin.Unknown, Evidence: why, State: twin.Live}
}

const advFresh = "2026-09-21T11:59:30Z"

func advNode(id, cluster string, cpu, cpuReq, mem, memReq float64, reqKnown bool) twin.Entity {
	e := twin.Entity{Kind: "node", ID: id, Name: id, ClusterID: cluster, Origin: "observed", State: twin.Live, Attributes: map[string]twin.Attr{
		"cpuAllocatable":    advNum(cpu, "cores", twin.Reported, advFresh),
		"memoryAllocatable": advNum(mem*advGib, "bytes", twin.Reported, advFresh),
	}}
	if reqKnown {
		e.Attributes["cpuRequested"] = advNum(cpuReq, "cores", twin.Reported, advFresh)
		e.Attributes["memoryRequested"] = advNum(memReq*advGib, "bytes", twin.Reported, advFresh)
	} else {
		e.Attributes["cpuRequested"] = advUnk("cores", twin.WhyPodsNotRead)
		e.Attributes["memoryRequested"] = advUnk("bytes", twin.WhyPodsNotRead)
	}
	return e
}

func advCluster(id string, st twin.State, attrs map[string]twin.Attr) twin.Entity {
	e := twin.Entity{Kind: "cluster", ID: id, Name: id, Origin: "observed", State: st, Attributes: attrs}
	if st == twin.Stale {
		e.StateReason = "stale for 2 h"
	}
	return e
}

// adviceModel is a small estate with one cluster of every interesting kind.
func adviceModel() twin.Model {
	known := map[string]twin.Attr{"cpuAllocatable": advNum(1, "cores", twin.Reported, advFresh)}
	blind := map[string]twin.Attr{"cpuAllocatable": advUnk("cores", twin.WhyNodesNotRead), "cpuRequested": advUnk("cores", twin.WhyNodesNotRead)}
	noreq := map[string]twin.Attr{"cpuAllocatable": advNum(8, "cores", twin.Reported, advFresh), "cpuRequested": advUnk("cores", "pods are not read for every node (access tier below 2)")}
	declNode := twin.Entity{Kind: "node", ID: "nd-decl", Name: "decl-node", ClusterID: "cl-decl", Origin: "declared", State: twin.Declared, Attributes: map[string]twin.Attr{
		"allocatable": {Value: map[string]any{"cpu": 8.0, "memoryGb": 32.0}, Source: twin.FromDeclared, Confidence: twin.Reported, State: twin.Declared},
	}}
	svc := twin.Entity{Kind: "service", ID: "sv-api", Name: "api", ClusterID: "cl-home", Origin: "observed", State: twin.Live, Attributes: map[string]twin.Attr{
		"replicas":      advNum(2, "count", twin.Reported, advFresh),
		"cpuRequest":    advNum(0.5, "cores", twin.Reported, advFresh),
		"memoryRequest": advNum(advGib, "bytes", twin.Reported, advFresh),
	}}
	noReqSvc := twin.Entity{Kind: "service", ID: "sv-bare", Name: "bare", ClusterID: "cl-home", Origin: "observed", State: twin.Live, Attributes: map[string]twin.Attr{
		"replicas":      advNum(1, "count", twin.Reported, advFresh),
		"cpuRequest":    advUnk("cores", "no CPU request is set, so nothing is reserved and the need is not known"),
		"memoryRequest": advUnk("bytes", "no memory request is set, so nothing is reserved and the need is not known"),
	}}
	return twin.Model{Contract: twin.Contract, ModelVersion: 9, GeneratedAt: "2026-09-21T12:00:00Z", Observation: twin.ObservationDoc{StaleAfterSeconds: 120}, Entities: []twin.Entity{
		advCluster("cl-home", twin.Live, known), advNode("nd-home", "cl-home", 4, 1, 16, 4, true),
		advCluster("cl-big", twin.Live, known), advNode("nd-big1", "cl-big", 16, 4, 64, 8, true), advNode("nd-big2", "cl-big", 16, 6, 64, 8, true),
		advCluster("cl-tight", twin.Live, known), advNode("nd-tight", "cl-tight", 4, 3.5, 16, 4, true),
		advCluster("cl-blind", twin.Live, blind),
		advCluster("cl-noreq", twin.Live, noreq), advNode("nd-noreq", "cl-noreq", 8, 0, 32, 0, false),
		advCluster("cl-old", twin.Stale, map[string]twin.Attr{}),
		{Kind: "cluster", ID: "cl-decl", Name: "cl-decl", Origin: "declared", State: twin.Declared, Attributes: map[string]twin.Attr{}}, declNode,
		svc, noReqSvc,
	}}
}

const adviceBody = `{"schema":1,"question":"placement","generatedAt":"2026-09-21T12:00:00Z","policy":{"latency":1},
"clusters":[{"id":"cl-home","name":"cl-home","tier":"edge","freeCpu":3},{"id":"cl-big","name":"cl-big","tier":"cloud"},{"id":"cl-tight","name":"cl-tight"},{"id":"cl-blind","name":"cl-blind"},{"id":"cl-noreq","name":"cl-noreq"},{"id":"cl-old","name":"cl-old"},{"id":"cl-decl","name":"cl-decl"}],
"links":[],
"services":[
 {"id":"sv-api","name":"api","cluster":"cl-home","mobility":"free","candidates":["cl-big","cl-tight","cl-blind","cl-noreq","cl-old","cl-decl"],"constraints":[]},
 {"id":"sv-bare","name":"bare","cluster":"cl-home","mobility":"careful","candidates":["cl-big"],"constraints":["x"]},
 {"id":"sv-unknown","name":"not in the model","cluster":"cl-home","mobility":"free","candidates":["cl-big","cl-tight"]},
 {"id":"sv-pin","name":"pinned","cluster":"cl-home","mobility":"pinned","candidates":[]}],
"flows":[]}`

func decodeAny(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestDecisionPayloadGolden(t *testing.T) {
	out, err := enrichDecision([]byte(adviceBody), adviceModel())
	if err != nil {
		t.Fatal(err)
	}
	var pretty map[string]any
	_ = json.Unmarshal(out, &pretty)
	got, _ := json.MarshalIndent(pretty, "", " ")
	got = append(got, '\n')
	const path = "testdata/decide_enriched.golden.json"
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no golden file (run with -update): %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("the enriched decision request changed; run `go test ./internal/server -run TestDecisionPayloadGolden -update` if that is intended, and update docs/model-api.md")
		_ = os.WriteFile(path+".actual", got, 0o644)
	}
}

// Everything the client sent is still there, with the same values, except the candidates the server removed. What was added is
// only additions.
func TestDecisionPayloadKeepsWhatWasSent(t *testing.T) {
	out, err := enrichDecision([]byte(adviceBody), adviceModel())
	if err != nil {
		t.Fatal(err)
	}
	before, after := decodeAny(t, []byte(adviceBody)), decodeAny(t, out)
	var contained func(path string, a, b any)
	contained = func(path string, a, b any) {
		switch av := a.(type) {
		case map[string]any:
			bv, ok := b.(map[string]any)
			if !ok {
				t.Errorf("%s: no longer an object", path)
				return
			}
			for k, x := range av {
				y, present := bv[k]
				if !present {
					t.Errorf("%s.%s was removed", path, k)
					continue
				}
				if k == "candidates" {
					continue // filtered, checked below
				}
				contained(path+"."+k, x, y)
			}
		case []any:
			bv, ok := b.([]any)
			if !ok || len(bv) != len(av) {
				t.Errorf("%s: the list changed length", path)
				return
			}
			for i := range av {
				contained(path+"[]", av[i], bv[i])
			}
		default:
			if !reflect.DeepEqual(a, b) && !strings.HasSuffix(path, "stateReason") {
				t.Errorf("%s changed: %v -> %v", path, a, b)
			}
		}
	}
	contained("$", before, after)
	svcs := after["services"].([]any)
	cands := func(i int) []any { return svcs[i].(map[string]any)["candidates"].([]any) }
	if got := cands(0); len(got) != 1 || got[0] != "cl-big" {
		t.Errorf("api: only the cluster where the server can say it fits stays a candidate, got %v", got)
	}
	if got := cands(1); len(got) != 0 {
		t.Errorf("bare has no requests, so nowhere can be certified: %v", got)
	}
	if got := cands(2); len(got) != 2 {
		t.Errorf("a workload the model does not know is not judged, only the exclusions apply: %v", got)
	}
	for _, k := range []string{"modelVersion", "excluded"} {
		if _, ok := after[k]; !ok {
			t.Errorf("missing added field %s", k)
		}
	}
	if _, ok := before["modelVersion"]; ok {
		t.Error("test body should not have carried modelVersion")
	}
}

type capOut struct {
	Cluster     string
	Verdict     string
	Confidence  string
	Reasons     []string
	Facts       []map[string]any
	Fixes       []advice.Fix
	Dimensions  []map[string]any
	WouldChange []map[string]any
}

func capacityFor(t *testing.T, sid string) map[string]capOut {
	t.Helper()
	out, err := enrichDecision([]byte(adviceBody), adviceModel())
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Services []struct {
			ID       string
			Capacity []capOut
		}
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	res := map[string]capOut{}
	for _, s := range got.Services {
		if s.ID == sid {
			for _, c := range s.Capacity {
				res[c.Cluster] = c
			}
		}
	}
	return res
}

func TestDecisionCapacityVerdicts(t *testing.T) {
	c := capacityFor(t, "sv-api")
	want := map[string]struct{ verdict, conf string }{
		"cl-big":   {"fits", "high"},
		"cl-tight": {"doesNotFit", "high"},
		"cl-blind": {"cantTell", "none"},
		"cl-noreq": {"cantTell", "none"},
		"cl-decl":  {"cantTell", "none"},
	}
	for id, w := range want {
		got, ok := c[id]
		if !ok || got.Verdict != w.verdict || got.Confidence != w.conf {
			t.Errorf("%s: %+v, want %v", id, got, w)
		}
	}
	if _, ok := c["cl-old"]; ok {
		t.Error("a cluster that is not live is excluded, not judged")
	}
	if _, ok := c["cl-home"]; ok {
		t.Error("the cluster it runs in is not a target")
	}
	// every answer says what it used and what would change it
	big := c["cl-big"]
	if len(big.Facts) < 4 || len(big.WouldChange) == 0 {
		t.Errorf("facts %d, wouldChange %d", len(big.Facts), len(big.WouldChange))
	}
	for _, f := range big.Facts {
		for _, k := range []string{"attribute", "value", "unit", "source", "confidence", "observedAt", "state"} {
			if _, ok := f[k]; !ok && !(k == "observedAt" && f["attribute"] == "cpuRequest") {
				t.Errorf("fact %v lacks %s", f["attribute"], k)
			}
		}
	}
	// the fix for each kind of "can't tell" is the one that would actually help
	if f := c["cl-blind"].Fixes; len(f) != 1 || f[0].Action != "raise-agent-tier" || f[0].Link != "/agents" || !strings.Contains(f[0].Text, "tier to 1") {
		t.Errorf("blind: %+v", f)
	}
	if f := c["cl-noreq"].Fixes; len(f) != 1 || !strings.Contains(f[0].Text, "tier to 2") {
		t.Errorf("noreq: %+v", f)
	}
	if f := c["cl-decl"].Fixes; len(f) != 1 || f[0].Action != "declare-capacity" {
		t.Errorf("declared: %+v", f)
	}
	if len(c["cl-blind"].Reasons) == 0 || !strings.Contains(strings.Join(c["cl-blind"].Reasons, " "), "not known") {
		t.Errorf("reasons: %v", c["cl-blind"].Reasons)
	}
	bare := capacityFor(t, "sv-bare")
	if b := bare["cl-big"]; b.Verdict != "cantTell" || len(b.Fixes) == 0 || b.Fixes[0].Action != "set-requests" {
		t.Errorf("a workload with no requests: %+v", b)
	}
	if len(capacityFor(t, "sv-pin")) != 0 {
		t.Error("a pinned workload is not judged for capacity")
	}
}

func TestDecisionClustersCarryFreeFacts(t *testing.T) {
	out, _ := enrichDecision([]byte(adviceBody), adviceModel())
	var got struct {
		Clusters []struct {
			ID   string
			Free map[string]struct {
				Value      *float64
				Unit       string
				Confidence string
				Source     string
				Low, High  *float64
			}
		}
		Excluded []struct {
			Cluster, Kind, Reason string
			Fix                   *advice.Fix
		}
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	free := map[string]float64{}
	for _, c := range got.Clusters {
		if c.ID == "cl-big" {
			if f := c.Free["cpu"]; f.Value == nil || f.Unit != "cores" || f.Confidence != "reported" || f.Low == nil || f.High == nil {
				t.Errorf("cl-big cpu: %+v", f)
			} else {
				free["cpu"] = *f.Value
			}
			if f := c.Free["memory"]; f.Value == nil || f.Unit != "bytes" {
				t.Errorf("cl-big memory: %+v", f)
			}
		}
		if c.ID == "cl-noreq" {
			if f := c.Free["cpu"]; f.Value != nil || f.Confidence != "unknown" || f.High == nil {
				t.Errorf("free is null when requested is unknown, but bounded above by what the node has: %+v", f)
			}
		}
		if c.ID == "cl-blind" {
			if f := c.Free["cpu"]; f.Value != nil || f.High != nil || f.Confidence != "unknown" {
				t.Errorf("no nodes: %+v", f)
			}
		}
	}
	if free["cpu"] != 22 { // 16-4 + 16-6
		t.Errorf("free cpu %v", free["cpu"])
	}
	kinds := map[string]string{}
	for _, e := range got.Excluded {
		kinds[e.Cluster] = e.Kind
		if e.Cluster == "cl-blind" && (e.Fix == nil || e.Fix.Action != "raise-agent-tier") {
			t.Errorf("a capacity-unknown exclusion names the fix: %+v", e)
		}
	}
	if kinds["cl-old"] != "not-live" || kinds["cl-blind"] != "capacity-unknown" || len(kinds) != 2 {
		t.Errorf("kinds: %v", kinds)
	}
}

func TestDecisionCapacityIsDeterministic(t *testing.T) {
	first, _ := enrichDecision([]byte(adviceBody), adviceModel())
	for i := 0; i < 20; i++ {
		again, _ := enrichDecision([]byte(adviceBody), adviceModel())
		if string(again) != string(first) {
			t.Fatal("the same request and model gave a different document")
		}
	}
}

func TestDecisionAdviceAgesFactsFromTheModelClock(t *testing.T) {
	// a node last confirmed 10 minutes before the model was generated counts one class worse, so the same need that fits
	// when advFresh can no longer be certified
	m := adviceModel()
	for i, e := range m.Entities {
		if e.ID == "nd-big1" || e.ID == "nd-big2" {
			for k, a := range e.Attributes {
				a.ObservedAt = "2026-09-21T11:50:00Z"
				m.Entities[i].Attributes[k] = a
			}
		}
	}
	body := strings.Replace(adviceBody, `"mobility":"free","candidates":["cl-big","cl-tight","cl-blind","cl-noreq","cl-old","cl-decl"]`, `"mobility":"free","candidates":["cl-big"]`, 1)
	out, _ := enrichDecision([]byte(body), m)
	var got struct{ Services []struct{ Capacity []capOut } }
	_ = json.Unmarshal(out, &got)
	c := got.Services[0].Capacity
	var big capOut
	for _, x := range c {
		if x.Cluster == "cl-big" {
			big = x
		}
	}
	// free is 22 cores and the need is 1: still fits even when inferred (22*0.7 = 15.4), but it is medium confidence now
	if big.Verdict != "fits" || big.Confidence != "medium" {
		t.Errorf("stale facts: %s / %s", big.Verdict, big.Confidence)
	}
	aged := false
	for _, f := range big.Facts {
		if f["aged"] == true {
			aged = true
		}
	}
	if !aged {
		t.Errorf("the facts do not say they are old: %v", big.Facts)
	}
}

var _ = twin.Live
