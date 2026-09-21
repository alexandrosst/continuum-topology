// Package advice is the one place that decides whether a workload fits somewhere, and how far that answer can be
// trusted. It is pure: the same inputs give the same outputs, and it reads nothing but its arguments.
//
// # The rules
//
// Placement advice used to say a workload "fits" or "does not fit". That hid the difference between "I checked and
// it fits" and "I could not check". The answer here has three values, and it is computed with intervals, not points,
// wherever an input is uncertain.
//
//	fits        it fits even at the pessimistic end of every interval, and every constraint was checked
//	does not    it fails even at the optimistic end of some interval, or a constraint is definitely violated
//	can't tell  anything else: an input is unknown, or its uncertainty straddles the requirement
//
// Every input is a Fact: a value, a confidence class (measured, reported, inferred, guess, unknown), the time it was
// last confirmed, and where it came from. The rules, in order:
//
//  1. Unknown is not zero. A fact with no value has no interval. It never makes something fit and never makes
//     something not fit by itself: the answer is "can't tell", and the reason names the fact.
//  2. Each class widens the value by a fixed relative factor (Factors): measured ±5 %, reported ±15 %, inferred ±30 %,
//     guess ±50 %. The interval is [value×(1−f), value×(1+f)].
//  3. A fact last confirmed longer ago than the staleness window counts as one class worse (measured becomes
//     reported, reported becomes inferred, inferred becomes guess; a guess stays a guess). This is applied before
//     rule 2. A fact with no observation time (typed by a person) has no age.
//  4. Free capacity is derived per node as allocatable minus requested, and pooled over the nodes that could host the
//     workload. The factor is applied to the derived free value with the weakest class of its two inputs. If a
//     node's requested resources are not known its free capacity is only bounded above by its allocatable; if a
//     node's allocatable is not known the pool has no upper bound at all.
//  5. "Fits" needs lo ≥ need (the requirement fits at the pessimistic end). "Does not fit" needs hi < need (it fails
//     even at the optimistic end). Everything in between is "can't tell", with the free amount at which it would
//     become "fits" and the amount below which it would become "does not fit".
//  6. Several dimensions and checks combine by severity: any definite "does not fit" wins, otherwise any "can't tell",
//     otherwise "fits".
//  7. Confidence is the weakest class among the facts that decide the answer, never an average: measured and reported
//     are high, inferred is medium, a guess is low, unknown is none. A "can't tell" answer is never above low.
//
// What is not modelled: capacity is pooled over nodes, so a workload whose single replica is larger than any one node's
// free room may still be told "fits" if the pool is large enough (fragmentation is not checked); storage has no free
// figure anywhere in the model, so it is not a dimension; latency is a cost, not a constraint, so the quality of a
// path affects confidence but not fit.
//
// The browser mirrors these rules in src/lib/advice.ts. testdata/vectors.json is read by both test suites, which is
// what keeps the two from disagreeing.
package advice

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Class says how a value came to be known, from most to least certain. The strings are the model's own.
type Class string

const (
	Measured Class = "measured"
	Reported Class = "reported"
	Inferred Class = "inferred"
	Guess    Class = "guess"
	Unknown  Class = "unknown"
)

// Factors is the relative half-width of the interval for each class. Unknown has none: it has no interval.
var Factors = map[Class]float64{Measured: 0.05, Reported: 0.15, Inferred: 0.30, Guess: 0.50}

// Factor returns the class's factor, and false for unknown.
func Factor(c Class) (float64, bool) {
	f, ok := Factors[c]
	return f, ok
}

// Rank orders classes: higher is more certain. Anything unrecognised is unknown.
func (c Class) Rank() int {
	switch c {
	case Measured:
		return 4
	case Reported:
		return 3
	case Inferred:
		return 2
	case Guess:
		return 1
	}
	return 0
}

// Worse returns the less certain of two classes.
func Worse(a, b Class) Class {
	if a.Rank() <= b.Rank() {
		return a.norm()
	}
	return b.norm()
}

func (c Class) norm() Class {
	if c.Rank() == 0 {
		return Unknown
	}
	return c
}

// Demote is one class worse; a guess and an unknown stay as they are.
func Demote(c Class) Class {
	switch c {
	case Measured:
		return Reported
	case Reported:
		return Inferred
	case Inferred:
		return Guess
	case Guess:
		return Guess
	}
	return Unknown
}

// Verdict is the three-valued answer.
type Verdict string

const (
	Fits       Verdict = "fits"
	CantTell   Verdict = "cantTell"
	DoesNotFit Verdict = "doesNotFit"
)

// Combine merges verdicts by severity: any DoesNotFit, else any CantTell, else Fits.
func Combine(vs ...Verdict) Verdict {
	out := Fits
	for _, v := range vs {
		switch v {
		case DoesNotFit:
			return DoesNotFit
		case CantTell:
			out = CantTell
		}
	}
	return out
}

// Level is what a person is told about how far to trust an answer.
type Level string

const (
	High   Level = "high"
	Medium Level = "medium"
	Low    Level = "low"
	None   Level = "none"
)

// LevelOf maps the weakest deciding class to a level: measured and reported are high, inferred medium, a guess low,
// unknown none.
func LevelOf(c Class) Level {
	switch c {
	case Measured, Reported:
		return High
	case Inferred:
		return Medium
	case Guess:
		return Low
	}
	return None
}

func (l Level) rank() int {
	switch l {
	case High:
		return 3
	case Medium:
		return 2
	case Low:
		return 1
	}
	return 0
}

// WeakerLevel returns the lower of two levels.
func WeakerLevel(a, b Level) Level {
	if a.rank() <= b.rank() {
		return a
	}
	return b
}

// Fact is one input: a value with its class and age. Value is nil when it is not known.
type Fact struct {
	// Attribute is the model's name for it ("cpuAllocatable"), Entity the id of what it is about.
	Attribute  string
	Entity     string
	EntityName string
	Unit       string
	Value      *float64
	Class      Class
	// Source: agent | probe | measured | inferred | declared.
	Source string
	// ObservedAt is when the source last vouched for the value. Zero means it has no age (a person typed it).
	ObservedAt time.Time
	// State is the observation state of the entity (live, stale, ...), carried for display only.
	State string
	// Evidence is the signal behind the value, or why it is unknown.
	Evidence string
}

// Known reports whether the fact has a value that can be used.
func (f Fact) Known() bool {
	return f.Value != nil && !math.IsNaN(*f.Value) && f.Class.Rank() > 0
}

// Effective is the class the fact counts as at now: one worse when it was last confirmed longer ago than window.
// aged says whether that happened. An unknown fact stays unknown.
func (f Fact) Effective(now time.Time, window time.Duration) (c Class, aged bool) {
	if !f.Known() {
		return Unknown, false
	}
	c = f.Class
	if !f.ObservedAt.IsZero() && window > 0 && now.Sub(f.ObservedAt) > window {
		return Demote(c), true
	}
	return c, false
}

// F is a convenience for tests and callers: a known value.
func F(v float64) *float64 { return &v }

// Node is one node's contribution to a pool: what it can offer and what is already asked of it.
type Node struct {
	ID, Name         string
	Allocatable      Fact
	Requested        Fact
	AllocatableAttr  string // the model's name, for the reasons ("cpuAllocatable")
	RequestedAttr    string
	RequestedMissing string // why requested is unknown, when it is
}

// Pool is the free capacity of a group of nodes as an interval. Hi is +Inf when nothing bounds it from above.
//
// A node whose free capacity is known contributes to Lo and Hi. A node whose allocatable amount is known but whose
// requested amount is not contributes nothing to Lo and its allocatable amount (widened) to Hi. A node whose
// allocatable amount is not known contributes nothing to Lo and +Inf to Hi.
type Pool struct {
	Lo, Hi float64
	// KnownFree is the sum of the free amounts of the nodes where it is known (KnownNodes of them). When every node
	// is known (NominalKnown) it is the pool's free capacity as reported.
	KnownFree    float64
	KnownNodes   int
	NominalKnown bool
	Nodes        int
	// Class is the weakest effective class among all the facts; Unknown when any is missing. LoClass is the weakest among
	// the nodes whose free capacity is known: a "fits" rests on those alone. HiClass is the weakest among the facts that
	// set the upper end: a "does not fit" rests on those alone.
	Class, LoClass, HiClass Class
	// Oldest is the oldest observation time among the facts (zero when none has one). Aged is true when at least one was
	// demoted for its age.
	Oldest time.Time
	Aged   bool
	// Notes say what is missing or old, for the reader.
	Notes []string

	hiKnown float64 // the part of Hi that comes from nodes whose free capacity is known
}

// NewPool derives free capacity over nodes at time now. window is the staleness window (facts older than it are one
// class worse). With no nodes the pool is entirely unknown.
func NewPool(nodes []Node, now time.Time, window time.Duration) Pool {
	if len(nodes) == 0 {
		return Pool{Hi: math.Inf(1), Class: Unknown, LoClass: Unknown, HiClass: Unknown, Notes: []string{"no nodes are known"}}
	}
	p := Pool{Class: Measured, LoClass: Measured, HiClass: Measured, NominalKnown: true, Nodes: len(nodes)}
	for _, n := range nodes {
		ca, aa := n.Allocatable.Effective(now, window)
		cr, ar := n.Requested.Effective(now, window)
		p.Aged = p.Aged || aa || ar
		for _, f := range []Fact{n.Allocatable, n.Requested} {
			if f.Known() && !f.ObservedAt.IsZero() && (p.Oldest.IsZero() || f.ObservedAt.Before(p.Oldest)) {
				p.Oldest = f.ObservedAt
			}
		}
		name := firstNonEmpty(n.Name, n.ID)
		if aa || ar {
			p.Notes = append(p.Notes, fmt.Sprintf("%s was last confirmed more than %s ago, so its figures count as one class worse", name, roundDur(window)))
		}
		switch {
		case !n.Allocatable.Known():
			p.Hi = math.Inf(1)
			p.NominalKnown = false
			p.Class, p.HiClass = Unknown, Unknown
			p.Notes = append(p.Notes, fmt.Sprintf("the allocatable resources of %s are not reported", name))
		case !n.Requested.Known():
			f, _ := Factor(ca)
			p.Hi += *n.Allocatable.Value * (1 + f)
			p.NominalKnown = false
			p.Class = Unknown
			p.HiClass = Worse(p.HiClass, ca)
			why := n.RequestedMissing
			if why == "" {
				why = "pods are not read there"
			}
			p.Notes = append(p.Notes, fmt.Sprintf("what is already requested on %s is not known (%s), so its free capacity is only bounded by what it has", name, why))
		default:
			free := math.Max(0, *n.Allocatable.Value-*n.Requested.Value)
			c := Worse(ca, cr)
			f, _ := Factor(c)
			p.Lo += free * (1 - f)
			p.hiKnown += free * (1 + f)
			p.Hi += free * (1 + f)
			p.KnownFree += free
			p.KnownNodes++
			p.Class = Worse(p.Class, c)
			p.LoClass = Worse(p.LoClass, c)
			p.HiClass = Worse(p.HiClass, c)
		}
	}
	if p.KnownNodes == 0 {
		p.LoClass = Unknown
	}
	return p
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}

func roundDur(d time.Duration) string {
	switch {
	case d < 90*time.Second:
		return fmt.Sprintf("%d s", int(d.Round(time.Second)/time.Second))
	case d < 90*time.Minute:
		return fmt.Sprintf("%d min", int(d.Round(time.Minute)/time.Minute))
	}
	return fmt.Sprintf("%d h", int(d.Round(time.Hour)/time.Hour))
}

// Adjust returns the pool after workloads move in (negative delta) or out (positive delta): a known amount shifts the
// interval, and an amount that is not known (a workload with no request) leaves the pool without a floor.
func (p Pool) Adjust(delta *float64) Pool {
	if delta == nil {
		p.Lo, p.KnownFree, p.KnownNodes, p.hiKnown = 0, 0, 0, 0
		p.NominalKnown = false
		p.Class, p.LoClass = Unknown, Unknown
		p.Notes = append(append([]string(nil), p.Notes...), "a workload moved in has no request, so what it takes is not known")
		return p
	}
	p.Lo = math.Max(0, p.Lo+*delta)
	p.Hi = math.Max(0, p.Hi+*delta)
	p.hiKnown = math.Max(0, p.hiKnown+*delta)
	p.KnownFree = math.Max(0, p.KnownFree+*delta)
	return p
}

// Scaled returns the pool with the free capacity of the nodes where it is known multiplied so that it totals free. It is
// how "what would change this" is stated and checked. A pool with no such node becomes a single synthetic node of class
// reported (the fact "were known").
func (p Pool) Scaled(free float64) Pool {
	if p.KnownNodes > 0 && p.KnownFree > 0 {
		s := free / p.KnownFree
		extra := p.Hi - p.hiKnown
		p.Lo, p.hiKnown, p.KnownFree = p.Lo*s, p.hiKnown*s, free
		p.Hi = p.hiKnown + extra
		return p
	}
	c := p.LoClass
	if c.Rank() == 0 {
		c = Reported
	}
	f, _ := Factor(c)
	extra := p.Hi - p.hiKnown
	if p.KnownNodes == 0 {
		extra = 0
		p.NominalKnown = true
		p.KnownNodes = 1
	}
	p.Lo, p.hiKnown, p.KnownFree = free*(1-f), free*(1+f), free
	p.Hi = p.hiKnown + extra
	p.Class, p.LoClass, p.HiClass = c, c, c
	return p
}

// Dimension describes one resource: its name, the unit values are in, and how to write an amount.
type Dimension struct {
	Name string
	// Label is how the dimension is written in a sentence ("CPU", "memory").
	Label string
	Unit  string
	Fmt   func(float64) string
}

const gib = 1 << 30

// CPU is measured in cores, Memory in bytes (shown in GiB).
var (
	CPU    = Dimension{Name: "cpu", Label: "CPU", Unit: "cores", Fmt: func(v float64) string { return trim(v) + " cores" }}
	Memory = Dimension{Name: "memory", Label: "memory", Unit: "bytes", Fmt: func(v float64) string { return trim(v/gib) + " GiB" }}
)

func trim(v float64) string {
	switch {
	case v >= 100:
		return fmt.Sprintf("%.0f", v)
	case v >= 10:
		return strings.TrimSuffix(strings.TrimSuffix(fmt.Sprintf("%.1f", v), "0"), ".")
	}
	s := fmt.Sprintf("%.2f", v)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}

// Need is what the workload asks for in one dimension, all replicas together. Value is nil when no request is set.
type Need struct {
	Value *float64
	Class Class
}

// Change says the smallest change of one fact that would change the verdict.
type Change struct {
	Dimension string
	// Attribute is the derived fact the change is about, for example "memoryFree".
	Attribute string
	Unit      string
	// Direction is "atLeast" or "below": the verdict changes when the fact is at least, or below, Threshold.
	Direction string
	Threshold float64
	// Current is the nominal value now; nil when it is not known.
	Current *float64
	Becomes Verdict
	Text    string
}

// DimResult is the verdict for one dimension.
type DimResult struct {
	Dimension Dimension
	Verdict   Verdict
	Need      Need
	Pool      Pool
	// Class is the weakest class among what the verdict rests on: the need and the pool.
	Class Class
	// Reason is one sentence: what is known and why the verdict follows.
	Reason string
	// Margin is the share by which free capacity could be lower than reported before it stops fitting (negative: it
	// already does not, by that share). Nil when the free amount is not known.
	Margin      *float64
	WouldChange []Change
	// Fix is what a person can do to make an undecided dimension decidable; the caller sets it (it knows why the
	// fact is missing).
	Fix *Fix
}

// Fit decides one dimension. where names the place ("cluster edge-2") for the sentences.
func Fit(dim Dimension, need Need, pool Pool, where string) DimResult {
	r := DimResult{Dimension: dim, Need: need, Pool: pool}
	r.Class = Worse(pool.Class, orUnknown(need))
	switch {
	case need.Value == nil:
		r.Verdict = CantTell
		r.Reason = fmt.Sprintf("the workload sets no %s request, so what it needs is not known", dim.Label)
		r.Class = Unknown
		return r
	case *need.Value <= 0:
		r.Verdict = Fits
		r.Reason = fmt.Sprintf("it asks for no %s", dim.Label)
		r.Class = Reported
		return r
	}
	n := *need.Value
	switch {
	case pool.Hi < n:
		r.Verdict = DoesNotFit
		r.Class = Worse(pool.HiClass, orUnknown(need))
	case pool.Lo >= n:
		r.Verdict = Fits
		r.Class = Worse(pool.LoClass, orUnknown(need))
	default:
		r.Verdict = CantTell
	}
	if pool.KnownNodes > 0 && pool.KnownFree > 0 {
		m := 1 - n/pool.KnownFree
		r.Margin = &m
	}
	r.WouldChange = wouldChange(dim, n, pool, r.Verdict, where)
	r.Reason = reason(dim, n, pool, r, where)
	return r
}

func orUnknown(n Need) Class {
	if n.Value == nil {
		return Unknown
	}
	if n.Class.Rank() == 0 {
		return Reported
	}
	return n.Class
}

func pct(f float64) string { return fmt.Sprintf("%.0f %%", f*100) }

func reason(dim Dimension, n float64, p Pool, r DimResult, where string) string {
	need := dim.Fmt(n)
	l := dim.Label
	if p.NominalKnown {
		f, _ := Factor(p.Class)
		free := fmt.Sprintf("%s free (%s, ±%s)", dim.Fmt(p.KnownFree), p.Class, pct(f))
		switch r.Verdict {
		case Fits:
			return fmt.Sprintf("%s at %s: needs %s, %s; it fits even at the low end (%s)", l, where, need, free, dim.Fmt(p.Lo))
		case DoesNotFit:
			return fmt.Sprintf("not enough free %s at %s: needs %s, %s; it does not fit even at the high end (%s)", l, where, need, free, dim.Fmt(p.Hi))
		}
		return fmt.Sprintf("%s at %s is %s: needs %s, %s (%s to %s); it fits if free %s is at least %s", l, where, p.Class, need, free, dim.Fmt(p.Lo), dim.Fmt(p.Hi), l, dim.Fmt(thresholdFits(p, n)))
	}
	why := strings.Join(p.Notes, "; ")
	if why == "" {
		why = "not reported"
	}
	switch r.Verdict {
	case DoesNotFit:
		return fmt.Sprintf("not enough free %s at %s: needs %s but even all of what the nodes have (%s) is less; %s", l, where, need, dim.Fmt(p.Hi), why)
	case Fits:
		return fmt.Sprintf("%s at %s: needs %s and the nodes that report it have at least %s free; %s", l, where, need, dim.Fmt(p.Lo), why)
	}
	return fmt.Sprintf("free %s at %s is not known: %s; it needs %s", l, where, why, need)
}

// thresholdFits is the free amount (of the nodes where it is known) at which the pool's lower end reaches n. A pool
// with no such node is judged as if its free amount were known and reported.
func thresholdFits(p Pool, n float64) float64 {
	if p.KnownNodes > 0 && p.KnownFree > 0 && p.Lo > 0 {
		return p.KnownFree * n / p.Lo
	}
	f, _ := Factor(p.LoClass)
	if f == 0 {
		f = Factors[Reported]
	}
	return n / (1 - f)
}

// thresholdDoesNot is the free amount below which even the pool's upper end falls short of n; ok is false when scaling
// the known nodes cannot get there (the unbounded or unknown part alone reaches n, or no node's free amount is known:
// then only a change to what the nodes have could rule it in, and that is not a fact this package derives).
func thresholdDoesNot(p Pool, n float64) (float64, bool) {
	extra := p.Hi - p.hiKnown
	if math.IsInf(extra, 1) || extra >= n {
		return 0, false
	}
	if p.KnownNodes > 0 && p.KnownFree > 0 && p.hiKnown > 0 {
		return p.KnownFree * (n - extra) / p.hiKnown, true
	}
	return 0, false
}

func wouldChange(dim Dimension, n float64, p Pool, v Verdict, where string) []Change {
	attr := dim.Name + "Free"
	var cur *float64
	if p.KnownNodes > 0 {
		c := p.KnownFree
		cur = &c
	}
	mk := func(dir string, t float64, becomes Verdict, text string) Change {
		return Change{Dimension: dim.Name, Unit: dim.Unit, Attribute: attr, Direction: dir, Threshold: t, Current: cur, Becomes: becomes, Text: text}
	}
	fit := thresholdFits(p, n)
	dnf, dnfOK := thresholdDoesNot(p, n)
	switch v {
	case Fits:
		return []Change{mk("below", fit, CantTell, fmt.Sprintf("if free %s at %s is below %s it can no longer be said to fit", dim.Label, where, dim.Fmt(fit)))}
	case CantTell:
		if p.KnownNodes == 0 {
			return []Change{mk("atLeast", fit, Fits, fmt.Sprintf("if free %s at %s were reported, it would fit at %s or more", dim.Label, where, dim.Fmt(fit)))}
		}
		out := []Change{mk("atLeast", fit, Fits, fmt.Sprintf("if free %s at %s is at least %s it fits", dim.Label, where, dim.Fmt(fit)))}
		if dnfOK {
			out = append(out, mk("below", dnf, DoesNotFit, fmt.Sprintf("if free %s at %s is below %s it does not fit", dim.Label, where, dim.Fmt(dnf))))
		}
		return out
	}
	if !dnfOK {
		return nil
	}
	return []Change{
		mk("atLeast", dnf, CantTell, fmt.Sprintf("if free %s at %s reaches %s it is no longer ruled out", dim.Label, where, dim.Fmt(dnf))),
		mk("atLeast", fit, Fits, fmt.Sprintf("if free %s at %s reaches %s it fits", dim.Label, where, dim.Fmt(fit))),
	}
}

// Apply reports the verdict this dimension would have if the derived free fact were delta away from a change's threshold
// (positive delta is more free): the check that a suggested change really changes the answer.
func (r DimResult) Apply(c Change, delta float64) Verdict {
	return Fit(r.Dimension, r.Need, r.Pool.Scaled(c.Threshold+delta), "").Verdict
}

// SensitivityText is a short readout of how fragile the verdict is.
func (r DimResult) SensitivityText() string {
	if r.Margin == nil {
		return ""
	}
	m := *r.Margin
	f, _ := Factor(r.Pool.Class)
	switch r.Verdict {
	case Fits:
		return fmt.Sprintf("still fits if free %s is off by up to %s", r.Dimension.Label, pct(math.Min(m, 1)))
	case DoesNotFit:
		return fmt.Sprintf("would need %s more free %s than reported", pct(-m), r.Dimension.Label)
	}
	if m > 0 {
		return fmt.Sprintf("fits only if the reported free %s is off by less than %s; the source allows ±%s", r.Dimension.Label, pct(m), pct(f))
	}
	return fmt.Sprintf("needs %s more free %s than reported to fit; the source allows ±%s", pct(-m), r.Dimension.Label, pct(f))
}

// ---- categorical checks and the combined advice ----

// Fix is what a person can do to turn "can't tell" into an answer.
type Fix struct {
	// Action is a stable key: raise-agent-tier | set-requests | declare-residency | declare-trust-zone | connect-agent.
	Action string `json:"action"`
	Text   string `json:"text"`
	// Link is a path in the app that leads there ("/agents").
	Link string `json:"link,omitempty"`
}

// Check is a yes/no constraint that may also be unknown: architecture, data residency, trust zone, node selectors.
type Check struct {
	Name    string
	Verdict Verdict
	// Class of the facts the check rests on (declared values are reported).
	Class  Class
	Reason string
	Fix    *Fix
}

// Advice is the combined answer for one workload at one place.
type Advice struct {
	Verdict Verdict
	// Class is the weakest class among what decides the verdict; Confidence is that as a level (capped at low when the
	// verdict is "can't tell").
	Class      Class
	Confidence Level
	Dims       []DimResult
	Checks     []Check
	// Reasons: one line for everything that is not a plain pass, worst first.
	Reasons []string
	Fixes   []Fix
	Changes []Change
}

// Assess combines dimensions and checks. The confidence class is the weakest among the parts that decide: for "fits" all
// of them, for "does not fit" the parts that fail, for "can't tell" the parts that could not be decided.
func Assess(dims []DimResult, checks []Check) Advice {
	a := Advice{Dims: dims, Checks: checks}
	vs := make([]Verdict, 0, len(dims)+len(checks))
	for _, d := range dims {
		vs = append(vs, d.Verdict)
	}
	for _, c := range checks {
		vs = append(vs, c.Verdict)
	}
	a.Verdict = Combine(vs...)
	a.Class = Reported
	seen := false
	take := func(c Class) {
		a.Class = Worse(a.Class, c)
		seen = true
	}
	for _, d := range dims {
		if decides(a.Verdict, d.Verdict) {
			take(d.Class)
		}
	}
	for _, c := range checks {
		if decides(a.Verdict, c.Verdict) {
			take(c.Class)
		}
	}
	if !seen {
		a.Class = Reported
	}
	a.Confidence = LevelOf(a.Class)
	if a.Verdict == CantTell {
		a.Confidence = WeakerLevel(a.Confidence, Low)
	}
	type line struct {
		v Verdict
		s string
	}
	var lines []line
	for _, d := range dims {
		if d.Verdict != Fits {
			lines = append(lines, line{d.Verdict, d.Reason})
			if d.Fix != nil {
				a.Fixes = appendFix(a.Fixes, *d.Fix)
			}
		}
		if decides(a.Verdict, d.Verdict) {
			a.Changes = append(a.Changes, d.WouldChange...)
		}
	}
	for _, c := range checks {
		if c.Verdict != Fits {
			lines = append(lines, line{c.Verdict, c.Reason})
			if c.Fix != nil {
				a.Fixes = appendFix(a.Fixes, *c.Fix)
			}
		}
	}
	sort.SliceStable(lines, func(i, j int) bool { return sev(lines[i].v) > sev(lines[j].v) })
	for _, l := range lines {
		a.Reasons = append(a.Reasons, l.s)
	}
	return a
}

func decides(overall, part Verdict) bool { return overall == part }

func sev(v Verdict) int {
	switch v {
	case DoesNotFit:
		return 2
	case CantTell:
		return 1
	}
	return 0
}

func appendFix(fs []Fix, f Fix) []Fix {
	for _, x := range fs {
		if x.Action == f.Action && x.Text == f.Text {
			return fs
		}
	}
	return append(fs, f)
}
