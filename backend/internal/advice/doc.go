package advice

import (
	"math"
	"time"
)

// FactDoc is a fact as it is sent to a decider and shown in the "Why" panel: what was used, where it came from, how
// sure it is and how old it is.
type FactDoc struct {
	Attribute  string `json:"attribute"`
	Entity     string `json:"entity,omitempty"`
	EntityName string `json:"entityName,omitempty"`
	// Value is null when the fact is not known: unknown is never zero.
	Value  *float64 `json:"value"`
	Unit   string   `json:"unit,omitempty"`
	Source string   `json:"source,omitempty"`
	// Confidence is the class the fact counts as now (after demotion for age).
	Confidence Class `json:"confidence"`
	// Reported is the class the source gave it, present only when age demoted it.
	Reported   Class  `json:"reportedConfidence,omitempty"`
	ObservedAt string `json:"observedAt,omitempty"`
	State      string `json:"state,omitempty"`
	Evidence   string `json:"evidence,omitempty"`
	// Low and High are the ends of the interval when the fact is a quantity with one.
	Low  *float64 `json:"low,omitempty"`
	High *float64 `json:"high,omitempty"`
	// Aged is true when the fact is older than the staleness window and was demoted a class.
	Aged bool `json:"aged,omitempty"`
}

// ChangeDoc is a Change as sent: the smallest change of one fact that would flip the verdict.
type ChangeDoc struct {
	Dimension string   `json:"dimension,omitempty"`
	Attribute string   `json:"attribute"`
	Direction string   `json:"direction"`
	Threshold float64  `json:"threshold"`
	Unit      string   `json:"unit,omitempty"`
	Current   *float64 `json:"current"`
	Becomes   Verdict  `json:"becomes"`
	Text      string   `json:"text"`
}

// DimDoc is one dimension's result as sent.
type DimDoc struct {
	Dimension string   `json:"dimension"`
	Unit      string   `json:"unit"`
	Verdict   Verdict  `json:"verdict"`
	Need      *float64 `json:"need"`
	FreeLow   *float64 `json:"freeLow"`
	FreeHigh  *float64 `json:"freeHigh"`
	// FreeNominal is the free amount as reported, null when it is not known.
	FreeNominal *float64 `json:"free"`
	Confidence  Class    `json:"confidence"`
	Margin      *float64 `json:"margin,omitempty"`
	Reason      string   `json:"reason"`
}

// Doc is an Advice as sent to a decider and to the browser.
type Doc struct {
	Verdict     Verdict     `json:"verdict"`
	Confidence  Level       `json:"confidence"`
	Weakest     Class       `json:"weakestClass"`
	Dimensions  []DimDoc    `json:"dimensions,omitempty"`
	Reasons     []string    `json:"reasons,omitempty"`
	Facts       []FactDoc   `json:"facts"`
	WouldChange []ChangeDoc `json:"wouldChange"`
	Fixes       []Fix       `json:"fixes,omitempty"`
}

// finite drops infinities (no bound) and rounds to six decimals, so 3.4499999999999997 is sent as 3.45 and whole numbers such as byte counts are untouched.
func finite(v float64) *float64 {
	if math.IsInf(v, 0) || math.IsNaN(v) {
		return nil
	}
	v = round(v)
	return &v
}

func round(v float64) float64 { return math.Round(v*1e6) / 1e6 }

// FreeFact is the derived free capacity of a pool as a fact, for the "Why" list.
func FreeFact(dim Dimension, p Pool, entity, name, state string, evidence string) FactDoc {
	d := FactDoc{Attribute: dim.Name + "Free", Entity: entity, EntityName: name, Unit: dim.Unit, Source: "inferred", Confidence: p.Class, State: state, Evidence: evidence}
	if p.NominalKnown {
		d.Value = finite(p.KnownFree)
	}
	d.Low = finite(p.Lo)
	d.High = finite(p.Hi)
	if !p.Oldest.IsZero() {
		d.ObservedAt = p.Oldest.UTC().Format(time.RFC3339)
	}
	d.Aged = p.Aged
	return d
}

// NeedFact is what the workload asks for, as a fact.
func NeedFact(dim Dimension, n Need, entity, name, state, observedAt string) FactDoc {
	d := FactDoc{Attribute: dim.Name + "Request", Entity: entity, EntityName: name, Unit: dim.Unit, Source: "agent", Confidence: orUnknown(n), State: state, ObservedAt: observedAt}
	if n.Value != nil {
		d.Value = finite(*n.Value)
	} else {
		d.Evidence = "no " + dim.Label + " request is set, so what the workload needs is not known"
	}
	return d
}

// Document turns an Advice into what is sent: the verdict, the confidence, the facts used and what would change it.
// facts are the facts the caller assembled (free capacity, needs, anything else that was used); the dimensions and
// changes are taken from a itself.
func (a Advice) Document(facts []FactDoc) Doc {
	d := Doc{Verdict: a.Verdict, Confidence: a.Confidence, Weakest: a.Class, Reasons: a.Reasons, Facts: facts, Fixes: a.Fixes, WouldChange: []ChangeDoc{}}
	if d.Facts == nil {
		d.Facts = []FactDoc{}
	}
	for _, r := range a.Dims {
		dd := DimDoc{Dimension: r.Dimension.Name, Unit: r.Dimension.Unit, Verdict: r.Verdict, Confidence: r.Class, Reason: r.Reason}
		if r.Need.Value != nil {
			dd.Need = finite(*r.Need.Value)
		}
		if r.Margin != nil {
			dd.Margin = finite(*r.Margin)
		}
		dd.FreeLow, dd.FreeHigh = finite(r.Pool.Lo), finite(r.Pool.Hi)
		if r.Pool.NominalKnown {
			dd.FreeNominal = finite(r.Pool.KnownFree)
		}
		d.Dimensions = append(d.Dimensions, dd)
	}
	for _, c := range a.Changes {
		cd := ChangeDoc{Dimension: c.Dimension, Attribute: c.Attribute, Direction: c.Direction, Threshold: round(c.Threshold), Unit: c.Unit, Becomes: c.Becomes, Text: c.Text}
		if c.Current != nil {
			cd.Current = finite(*c.Current)
		}
		d.WouldChange = append(d.WouldChange, cd)
	}
	return d
}
