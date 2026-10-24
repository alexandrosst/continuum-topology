package fusionapi

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// A series with nothing to summarise says nothing: a zero would read as an idle pod where the pod stopped reporting.
func TestASeriesWithNoPointsHasNoSummary(t *testing.T) {
	empty := MetricSeries{Name: "m", Labels: map[string]string{}}
	empty.summarise()
	b, _ := json.Marshal(empty)
	for _, k := range []string{`"min"`, `"max"`, `"avg"`, `"last"`} {
		if strings.Contains(string(b), k) {
			t.Errorf("an empty series reports %s: %s", k, b)
		}
	}
	idle := MetricSeries{Name: "m", Labels: map[string]string{}, Points: []Point{{1, 0}, {2, 0}}}
	idle.summarise()
	b, _ = json.Marshal(idle)
	for _, k := range []string{`"min":0`, `"max":0`, `"avg":0`, `"last":0`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("a series that measured zero must say so (%s): %s", k, b)
		}
	}
	// Summarising again from fewer points does not keep the old figures.
	idle.Points = nil
	idle.summarise()
	if idle.Last != nil {
		t.Errorf("a stale summary survived: %v", *idle.Last)
	}
}

// got4 is a series' summary as min, max, avg, last; a missing figure is NaN, which equals nothing.
func got4(m MetricSeries) [4]float64 {
	v := func(p *float64) float64 {
		if p == nil {
			return math.NaN()
		}
		return *p
	}
	return [4]float64{v(m.Min), v(m.Max), v(m.Avg), v(m.Last)}
}
