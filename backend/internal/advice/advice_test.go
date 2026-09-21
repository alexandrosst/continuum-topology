package advice

import (
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)

const win = 120 * time.Second

func fact(v float64, c Class, age time.Duration) Fact {
	f := Fact{Value: F(v), Class: c}
	if age >= 0 {
		f.ObservedAt = t0.Add(-age)
	}
	return f
}

func unknownFact() Fact { return Fact{Class: Unknown} }

func node(alloc, req float64, c Class, age time.Duration) Node {
	return Node{ID: "n", Name: "n", Allocatable: fact(alloc, c, age), Requested: fact(req, c, age)}
}

func need(v float64) Need { return Need{Value: F(v), Class: Reported} }

func close(a, b float64) bool {
	if math.IsInf(a, 0) || math.IsInf(b, 0) {
		return a == b
	}
	return math.Abs(a-b) <= 1e-9*math.Max(1, math.Max(math.Abs(a), math.Abs(b)))
}

func TestFactorsAreTheDocumentedOnes(t *testing.T) {
	want := map[Class]float64{Measured: 0.05, Reported: 0.15, Inferred: 0.30, Guess: 0.50}
	for c, f := range want {
		if got, ok := Factor(c); !ok || got != f {
			t.Errorf("%s: %v %v", c, got, ok)
		}
	}
	if _, ok := Factor(Unknown); ok {
		t.Error("unknown has no factor: it has no interval")
	}
}

func TestClassOrderAndDemotion(t *testing.T) {
	for _, tc := range []struct{ in, want Class }{{Measured, Reported}, {Reported, Inferred}, {Inferred, Guess}, {Guess, Guess}, {Unknown, Unknown}, {"junk", Unknown}} {
		if got := Demote(tc.in); got != tc.want {
			t.Errorf("Demote(%s) = %s, want %s", tc.in, got, tc.want)
		}
	}
	if Worse(Measured, Guess) != Guess || Worse(Unknown, Measured) != Unknown || Worse("junk", Reported) != Unknown {
		t.Error("Worse")
	}
	for c, l := range map[Class]Level{Measured: High, Reported: High, Inferred: Medium, Guess: Low, Unknown: None} {
		if LevelOf(c) != l {
			t.Errorf("LevelOf(%s) = %s", c, LevelOf(c))
		}
	}
}

func TestRules(t *testing.T) {
	type tc struct {
		name  string
		need  Need
		nodes []Node
		want  Verdict
		class Class
	}
	unk := Node{Allocatable: unknownFact(), Requested: unknownFact()}
	cases := []tc{
		{"unknown capacity is can't tell, not zero and not fits", need(1), []Node{unk}, CantTell, Unknown},
		{"no nodes: can't tell", need(1), nil, CantTell, Unknown},
		{"unknown request: can't tell", Need{}, []Node{node(8, 2, Reported, 0)}, CantTell, Unknown},
		{"fits at the pessimistic end (reported, 6 free, needs 5.1)", need(5.1), []Node{node(8, 2, Reported, 0)}, Fits, Reported},
		{"just above the pessimistic end: can't tell", need(5.2), []Node{node(8, 2, Reported, 0)}, CantTell, Reported},
		{"fails at the optimistic end (6.9)", need(6.9001), []Node{node(8, 2, Reported, 0)}, DoesNotFit, Reported},
		{"just under the optimistic end is still possible", need(6.89), []Node{node(8, 2, Reported, 0)}, CantTell, Reported},
		{"a guess widens the interval: 3 to 9", need(3.1), []Node{node(8, 2, Guess, 0)}, CantTell, Guess},
		{"the same need fits when the value is measured", need(5.6), []Node{node(8, 2, Measured, 0)}, Fits, Reported},
		{"stale reported counts as inferred (3 min old, window 2)", need(4.5), []Node{node(8, 2, Reported, 3*time.Minute)}, CantTell, Inferred},
		{"fresh reported fits the same need", need(4.5), []Node{node(8, 2, Reported, time.Minute)}, Fits, Reported},
		{"requested unknown: bounded above by allocatable, so it cannot fit", need(10), []Node{{Allocatable: fact(8, Reported, 0), Requested: unknownFact()}}, DoesNotFit, Reported},
		{"requested unknown and the need is smaller: can't tell", need(4), []Node{{Allocatable: fact(8, Reported, 0), Requested: unknownFact()}}, CantTell, Unknown},
		{"a node with no allocatable does not rule out a fit elsewhere", need(4), []Node{node(8, 2, Reported, 0), unk}, Fits, Reported},
		{"but an unbounded part prevents a definite no", need(100), []Node{node(8, 2, Reported, 0), unk}, CantTell, Unknown},
		{"over-requested node has zero room, not negative", need(1), []Node{node(4, 6, Reported, 0)}, DoesNotFit, Reported},
	}
	for _, c := range cases {
		p := NewPool(c.nodes, t0, win)
		r := Fit(CPU, c.need, p, "cluster x")
		if r.Verdict != c.want || r.Class != c.class {
			t.Errorf("%s: verdict %s class %s, want %s %s (lo %.3f hi %.3f) %s", c.name, r.Verdict, r.Class, c.want, c.class, p.Lo, p.Hi, r.Reason)
		}
		if r.Reason == "" {
			t.Errorf("%s: no reason", c.name)
		}
	}
}

func TestUnknownIsNeverAFit(t *testing.T) {
	// for every class that is not unknown, an entirely unknown capacity never yields Fits, whatever the need
	for _, n := range []float64{0.001, 1, 1e9} {
		r := Fit(CPU, need(n), NewPool([]Node{{Allocatable: unknownFact(), Requested: unknownFact()}}, t0, win), "x")
		if r.Verdict != CantTell {
			t.Errorf("need %v: %s", n, r.Verdict)
		}
	}
}

func TestExactlyAtWindowIsNotStale(t *testing.T) {
	f := fact(1, Reported, win)
	if c, aged := f.Effective(t0, win); c != Reported || aged {
		t.Errorf("at the window: %s %v", c, aged)
	}
	if c, aged := f.Effective(t0.Add(time.Nanosecond), win); c != Inferred || !aged {
		t.Errorf("just past it: %s %v", c, aged)
	}
	if c, aged := fact(1, Reported, -1).Effective(t0.Add(365*24*time.Hour), win); c != Reported || aged {
		t.Errorf("a fact with no observation time has no age: %s %v", c, aged)
	}
	if c, aged := fact(1, Guess, 10*time.Minute).Effective(t0, win); c != Guess || !aged {
		t.Errorf("a stale guess stays a guess but is marked old: %s %v", c, aged)
	}
}

func TestPoolNotesSayWhatIsMissingOrOld(t *testing.T) {
	p := NewPool([]Node{{Name: "edge-1", Allocatable: fact(8, Reported, 0), Requested: unknownFact(), RequestedMissing: "pods are not read at this access tier"}}, t0, win)
	if !strings.Contains(strings.Join(p.Notes, ";"), "edge-1") || !strings.Contains(strings.Join(p.Notes, ";"), "access tier") {
		t.Errorf("%v", p.Notes)
	}
	old := NewPool([]Node{node(8, 2, Reported, 10*time.Minute)}, t0, win)
	if !old.Aged || len(old.Notes) == 0 || old.Oldest.IsZero() {
		t.Errorf("%+v", old)
	}
}

func TestVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Factors map[Class]float64 `json:"factors"`
		Cases   []struct {
			Name      string `json:"name"`
			Dimension string `json:"dimension"`
			WindowSec int    `json:"windowSec"`
			Need      struct {
				Value *float64 `json:"value"`
				Class Class    `json:"class"`
			} `json:"need"`
			Nodes []struct {
				Alloc, Requested vfact
			} `json:"nodes"`
			Adjust []*float64 `json:"adjust"`
			Expect struct {
				Verdict   Verdict  `json:"verdict"`
				Class     Class    `json:"class"`
				Lo        *float64 `json:"lo"`
				Hi        *float64 `json:"hi"`
				Known     *bool    `json:"known"`
				KnownFree *float64 `json:"knownFree"`
				Margin    *float64 `json:"margin"`
				Changes   []struct {
					Direction string  `json:"direction"`
					Threshold float64 `json:"threshold"`
					Becomes   Verdict `json:"becomes"`
				} `json:"changes"`
			} `json:"expect"`
		} `json:"cases"`
		Assess []struct {
			Name  string `json:"name"`
			Parts []struct {
				Verdict Verdict `json:"verdict"`
				Class   Class   `json:"class"`
			} `json:"parts"`
			Expect struct {
				Verdict    Verdict `json:"verdict"`
				Class      Class   `json:"class"`
				Confidence Level   `json:"confidence"`
			} `json:"expect"`
		} `json:"assess"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for c, f := range doc.Factors {
		if Factors[c] != f {
			t.Errorf("factor %s: vectors say %v, code says %v", c, f, Factors[c])
		}
	}
	if len(doc.Cases) < 20 || len(doc.Assess) < 5 {
		t.Fatalf("the vector file is suspiciously small: %d cases", len(doc.Cases))
	}
	for _, c := range doc.Cases {
		dim := CPU
		if c.Dimension == "memory" {
			dim = Memory
		}
		var nodes []Node
		for _, n := range c.Nodes {
			nodes = append(nodes, Node{Name: "n", Allocatable: n.Alloc.fact(), Requested: n.Requested.fact()})
		}
		p := NewPool(nodes, t0, time.Duration(c.WindowSec)*time.Second)
		for _, a := range c.Adjust {
			p = p.Adjust(a)
		}
		nd := Need{Value: c.Need.Value, Class: c.Need.Class}
		r := Fit(dim, nd, p, "x")
		e := c.Expect
		if r.Verdict != e.Verdict || r.Class != e.Class {
			t.Errorf("%s: got %s/%s, want %s/%s (%s)", c.Name, r.Verdict, r.Class, e.Verdict, e.Class, r.Reason)
		}
		if e.Lo != nil && !close(p.Lo, *e.Lo) {
			t.Errorf("%s: lo %v, want %v", c.Name, p.Lo, *e.Lo)
		}
		if e.Lo != nil {
			if e.Hi == nil && !math.IsInf(p.Hi, 1) || e.Hi != nil && !close(p.Hi, *e.Hi) {
				t.Errorf("%s: hi %v, want %v", c.Name, p.Hi, e.Hi)
			}
			if *e.Known != p.NominalKnown {
				t.Errorf("%s: known %v", c.Name, p.NominalKnown)
			}
			if e.KnownFree != nil && !close(p.KnownFree, *e.KnownFree) {
				t.Errorf("%s: free %v, want %v", c.Name, p.KnownFree, *e.KnownFree)
			}
			if (e.Margin == nil) != (r.Margin == nil) || e.Margin != nil && !close(*r.Margin, *e.Margin) {
				t.Errorf("%s: margin %v, want %v", c.Name, r.Margin, e.Margin)
			}
		}
		if len(r.WouldChange) != len(e.Changes) {
			t.Errorf("%s: %d changes, want %d: %+v", c.Name, len(r.WouldChange), len(e.Changes), r.WouldChange)
			continue
		}
		for i, ch := range r.WouldChange {
			w := e.Changes[i]
			if ch.Direction != w.Direction || ch.Becomes != w.Becomes || !close(ch.Threshold, w.Threshold) {
				t.Errorf("%s: change %d is %+v, want %+v", c.Name, i, ch, w)
			}
		}
	}
	for _, a := range doc.Assess {
		var checks []Check
		for _, p := range a.Parts {
			checks = append(checks, Check{Name: "x", Verdict: p.Verdict, Class: p.Class})
		}
		got := Assess(nil, checks)
		if got.Verdict != a.Expect.Verdict || got.Class != a.Expect.Class || got.Confidence != a.Expect.Confidence {
			t.Errorf("%s: got %s/%s/%s, want %s/%s/%s", a.Name, got.Verdict, got.Class, got.Confidence, a.Expect.Verdict, a.Expect.Class, a.Expect.Confidence)
		}
	}
}

type vfact struct {
	Value  *float64 `json:"value"`
	Class  Class    `json:"class"`
	AgeSec *int     `json:"ageSec"`
}

func (v vfact) fact() Fact {
	f := Fact{Value: v.Value, Class: v.Class}
	if v.AgeSec != nil {
		f.ObservedAt = t0.Add(-time.Duration(*v.AgeSec) * time.Second)
	}
	return f
}

// The point of "what would change this": applying the suggested change to the fact must actually give the verdict the
// suggestion promises, and one step short of it must not.
func TestSuggestedChangesReallyFlipTheVerdict(t *testing.T) {
	pools := map[string][]Node{
		"reported":       {node(8, 2, Reported, 0)},
		"guess":          {node(8, 2, Guess, 0)},
		"mixed classes":  {node(8, 2, Measured, 0), node(4, 1, Reported, 0)},
		"stale":          {node(8, 2, Reported, 5*time.Minute)},
		"partial":        {node(8, 2, Reported, 0), {Allocatable: unknownFact(), Requested: unknownFact()}},
		"bounded":        {node(8, 2, Reported, 0), {Allocatable: fact(4, Reported, 0), Requested: unknownFact()}},
		"nothing known":  {{Allocatable: unknownFact(), Requested: unknownFact()}},
		"requested only": {{Allocatable: fact(8, Reported, 0), Requested: unknownFact()}},
	}
	checked := 0
	for name, nodes := range pools {
		for _, n := range []float64{0.5, 2, 3.5, 5, 6, 6.5, 7, 8, 9.5, 12, 20} {
			p := NewPool(nodes, t0, win)
			r := Fit(CPU, need(n), p, "x")
			for _, ch := range r.WouldChange {
				checked++
				// past the threshold, in the direction the change describes
				past, short := 1e-6*math.Max(1, ch.Threshold), -1e-6*math.Max(1, ch.Threshold)
				if ch.Direction == "below" {
					past, short = short, past
				}
				if got := r.Apply(ch, past); got != ch.Becomes {
					t.Errorf("%s need %v: %q: at %.6g the verdict is %s, not %s", name, n, ch.Text, ch.Threshold+past, got, ch.Becomes)
				}
				if got := r.Apply(ch, short); got != r.Verdict && ch.Becomes == Fits {
					// one step short of "fits" leaves it not fitting (it may already be can't tell or does not fit)
					if got == Fits {
						t.Errorf("%s need %v: %q: just short of the threshold it already fits", name, n, ch.Text)
					}
				}
				if got := r.Apply(ch, short); ch.Direction == "below" && got == ch.Becomes {
					t.Errorf("%s need %v: %q: just on the safe side of the threshold the verdict already changed to %s", name, n, ch.Text, got)
				}
			}
		}
	}
	if checked < 40 {
		t.Fatalf("only %d changes were checked", checked)
	}
}

func TestSuggestedChangeTextNamesTheAmount(t *testing.T) {
	r := Fit(Memory, need(3.2*gib), NewPool([]Node{{Name: "edge-2", Allocatable: fact(8*gib, Inferred, 0), Requested: fact(4*gib, Inferred, 0)}}, t0, win), "cluster edge-2")
	if r.Verdict != CantTell {
		t.Fatalf("%s: %s", r.Verdict, r.Reason)
	}
	if !strings.Contains(r.Reason, "inferred") || !strings.Contains(r.Reason, "3.2 GiB") || !strings.Contains(r.Reason, "4 GiB") {
		t.Errorf("reason: %s", r.Reason)
	}
	joined := ""
	for _, c := range r.WouldChange {
		joined += c.Text + "|"
	}
	if !strings.Contains(joined, "GiB") || !strings.Contains(joined, "memory") || !strings.Contains(joined, "cluster edge-2") {
		t.Errorf("changes: %s", joined)
	}
}

func TestSensitivity(t *testing.T) {
	fits := Fit(CPU, need(3), NewPool([]Node{node(8, 2, Reported, 0)}, t0, win), "x")
	if fits.Verdict != Fits || fits.Margin == nil || !close(*fits.Margin, 0.5) {
		t.Fatalf("%+v", fits)
	}
	if got := fits.SensitivityText(); !strings.Contains(got, "50 %") {
		t.Errorf("fits: %s", got)
	}
	// the margin is honest: at exactly that error it just about stops fitting the nominal value
	p := NewPool([]Node{node(8, 2, Reported, 0)}, t0, win)
	scaled := p.Scaled(6 * (1 - *fits.Margin))
	if !close(scaled.KnownFree, 3) {
		t.Errorf("scaled free %v", scaled.KnownFree)
	}
	tight := Fit(CPU, need(5.5), NewPool([]Node{node(8, 2, Reported, 0)}, t0, win), "x")
	if tight.Verdict != CantTell || !strings.Contains(tight.SensitivityText(), "less than 8 %") || !strings.Contains(tight.SensitivityText(), "±15 %") {
		t.Errorf("%s / %s", tight.Verdict, tight.SensitivityText())
	}
	no := Fit(CPU, need(9), NewPool([]Node{node(8, 2, Reported, 0)}, t0, win), "x")
	if no.Verdict != DoesNotFit || !strings.Contains(no.SensitivityText(), "50 % more") {
		t.Errorf("%s / %s", no.Verdict, no.SensitivityText())
	}
	unk := Fit(CPU, need(1), NewPool(nil, t0, win), "x")
	if unk.Margin != nil || unk.SensitivityText() != "" {
		t.Error("an unknown free amount has no margin")
	}
}

func TestAssessWeakestFactDecidesNotTheAverage(t *testing.T) {
	good := Fit(CPU, need(1), NewPool([]Node{node(8, 2, Measured, 0)}, t0, win), "x")
	mem := Fit(Memory, need(gib), NewPool([]Node{{Allocatable: fact(8*gib, Guess, 0), Requested: fact(2*gib, Guess, 0)}}, t0, win), "x")
	if good.Verdict != Fits || mem.Verdict != Fits {
		t.Fatalf("%s %s", good.Verdict, mem.Verdict)
	}
	a := Assess([]DimResult{good, mem}, []Check{{Name: "arch", Verdict: Fits, Class: Reported}})
	if a.Verdict != Fits || a.Class != Guess || a.Confidence != Low {
		t.Errorf("one guess among three good inputs must show: %+v", a)
	}
	// a can't-tell caused by an unknown is "none", and a fix travels with it
	fix := &Fix{Action: "raise-agent-tier", Text: "let the agent read pods", Link: "/agents"}
	b := Assess([]DimResult{good}, []Check{{Name: "requested", Verdict: CantTell, Class: Unknown, Reason: "requested is not known", Fix: fix}})
	if b.Verdict != CantTell || b.Confidence != None || len(b.Fixes) != 1 || b.Fixes[0].Link != "/agents" || len(b.Reasons) != 1 {
		t.Errorf("%+v", b)
	}
	// a definite no is a definite no, whatever else is unknown
	c := Assess([]DimResult{Fit(CPU, need(100), NewPool([]Node{node(8, 2, Reported, 0)}, t0, win), "x")}, []Check{{Name: "residency", Verdict: CantTell, Class: Unknown}})
	if c.Verdict != DoesNotFit || c.Confidence != High {
		t.Errorf("%+v", c)
	}
}

func TestDeterministic(t *testing.T) {
	build := func() string {
		nodes := []Node{node(8, 2, Reported, 0), node(4, 1, Guess, 0), {Allocatable: fact(4, Reported, 0), Requested: unknownFact()}}
		p := NewPool(nodes, t0, win)
		a := Assess([]DimResult{Fit(CPU, need(3), p, "x"), Fit(Memory, need(2*gib), p, "x")}, []Check{{Name: "b", Verdict: CantTell, Reason: "b", Class: Unknown}, {Name: "a", Verdict: CantTell, Reason: "a", Class: Guess}})
		raw, _ := json.Marshal(a.Document([]FactDoc{FreeFact(CPU, p, "cl", "c", "live", "e")}))
		return string(raw)
	}
	first := build()
	for i := 0; i < 20; i++ {
		if build() != first {
			t.Fatal("the same inputs gave different output")
		}
	}
}

func TestDocumentHasTheFieldsADeciderNeeds(t *testing.T) {
	p := NewPool([]Node{{Allocatable: fact(8, Reported, 0), Requested: unknownFact()}}, t0, win)
	a := Assess([]DimResult{Fit(CPU, need(2), p, "cluster c")}, nil)
	raw, err := json.Marshal(a.Document([]FactDoc{FreeFact(CPU, p, "cl-1", "c", "live", "allocatable minus requested"), NeedFact(CPU, need(2), "sv-1", "api", "live", "")}))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(raw, &got)
	for _, k := range []string{"verdict", "confidence", "weakestClass", "facts", "wouldChange", "dimensions", "reasons"} {
		if _, ok := got[k]; !ok {
			t.Errorf("missing %s in %s", k, raw)
		}
	}
	facts := got["facts"].([]any)
	free := facts[0].(map[string]any)
	if v, present := free["value"]; !present || v != nil {
		t.Errorf("an unknown free amount must be null, not absent and not zero: %v", free)
	}
	if free["confidence"] != "unknown" || free["unit"] != "cores" || free["attribute"] != "cpuFree" {
		t.Errorf("%v", free)
	}
	if strings.Contains(string(raw), "Inf") || strings.Contains(string(raw), "NaN") {
		t.Errorf("non-finite number: %s", raw)
	}
}
