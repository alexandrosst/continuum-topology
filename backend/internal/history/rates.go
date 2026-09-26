package history

import (
	"sort"
	"time"
)

// Sample is the cumulative byte count of every observed dependency at one moment.
type Sample struct {
	At    time.Time
	Bytes map[string]uint64
}

// Rate is how much a dependency carried over a period, worked out from consecutive snapshots.
type Rate struct {
	ID      string  `json:"id"`
	AvgBps  float64 `json:"avgBytesPerSec"`
	PeakBps float64 `json:"peakBytesPerSec"`
	Seconds float64 `json:"seconds"` // the period the average covers
}

// minPeakInterval keeps a snapshot taken a moment after another (because something changed) from
// turning a few bytes into a huge "peak".
const minPeakInterval = 30 * time.Second

// Rates derives average and peak throughput per dependency. Counters that go backwards (a flow
// table that was reset) restart from zero instead of producing a negative rate.
func Rates(samples []Sample) []Rate {
	sort.Slice(samples, func(i, j int) bool { return samples[i].At.Before(samples[j].At) })
	type acc struct{ bytes, peak float64 }
	all := map[string]*acc{}
	var total float64
	for i := 1; i < len(samples); i++ {
		a, b := samples[i-1], samples[i]
		dt := b.At.Sub(a.At)
		if dt <= 0 {
			continue
		}
		total += dt.Seconds()
		for id, nb := range b.Bytes {
			pb := a.Bytes[id]
			var delta uint64
			if nb >= pb {
				delta = nb - pb
			} else {
				delta = nb
			}
			x := all[id]
			if x == nil {
				x = &acc{}
				all[id] = x
			}
			x.bytes += float64(delta)
			if dt >= minPeakInterval {
				if r := float64(delta) / dt.Seconds(); r > x.peak {
					x.peak = r
				}
			}
		}
	}
	out := make([]Rate, 0, len(all))
	for id, x := range all {
		avg := 0.0
		if total > 0 {
			avg = x.bytes / total
		}
		out = append(out, Rate{ID: id, AvgBps: avg, PeakBps: x.peak, Seconds: total})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
