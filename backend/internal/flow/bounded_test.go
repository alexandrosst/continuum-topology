package flow

import (
	"fmt"
	"testing"

	continuumv1 "continuum/gen/continuumv1"
)

func edge(i int) *continuumv1.Flow {
	return &continuumv1.Flow{
		Src:         &continuumv1.FlowEndpoint{Kind: continuumv1.FlowEndpoint_WORKLOAD, Ref: fmt.Sprintf("ns/Deployment/a%d", i)},
		Dst:         &continuumv1.FlowEndpoint{Kind: continuumv1.FlowEndpoint_EXTERNAL, Ip: "203.0.113.7"},
		Port:        443,
		Protocol:    "tcp",
		Connections: 1,
	}
}

// While the agent cannot reach the server nothing flushes the aggregator. It must not grow without bound: the oldest edges go first,
// and it counts what it dropped so that the agent can say the figures are incomplete.
func TestTheAggregatorIsBoundedWhileNothingFlushesIt(t *testing.T) {
	a := NewAggregator()
	a.SetMax(100)
	for i := 0; i < 1000; i++ {
		a.Add(edge(i))
		if h := a.Held(); h > 100+10 {
			t.Fatalf("holding %d edges after %d additions with a cap of 100", h, i+1)
		}
	}
	if a.Held() > 110 || a.Held() < 90 {
		t.Fatalf("held %d", a.Held())
	}
	if d := a.Dropped(); d < 890 || d > 910 {
		t.Fatalf("dropped %d of 1000 with room for about 100", d)
	}
	// The oldest go first: the most recent edges are still held.
	fb := a.Flush()
	have := map[string]bool{}
	for _, f := range fb.Flows {
		have[f.Src.Ref] = true
	}
	for i := 990; i < 1000; i++ {
		if !have[fmt.Sprintf("ns/Deployment/a%d", i)] {
			t.Errorf("recent edge %d was dropped", i)
		}
	}
	if have["ns/Deployment/a0"] {
		t.Error("the oldest edge survived")
	}
	if a.Dropped() < 890 {
		t.Error("the count of drops was reset by a flush")
	}
}

func TestATouchedEdgeIsNotTheOldest(t *testing.T) {
	a := NewAggregator()
	a.SetMax(10)
	for i := 0; i < 10; i++ {
		a.Add(edge(i))
	}
	a.Add(edge(0)) // edge 0 is seen again: it is now the newest
	for i := 10; i < 12; i++ {
		a.Add(edge(i))
	}
	fb := a.Flush()
	kept := false
	for _, f := range fb.Flows {
		kept = kept || f.Src.Ref == "ns/Deployment/a0"
	}
	if !kept {
		t.Fatal("an edge that was just seen was evicted as the oldest")
	}
}

func TestAPausedAggregatorTakesNothingAndFlushesNothing(t *testing.T) {
	a := NewAggregator()
	a.Add(edge(1))
	a.Seen("n1", "ebpf", true)
	a.SetPaused(true)
	if a.Held() != 0 {
		t.Fatal("pausing must forget what was held")
	}
	if n, _ := a.Presence(); n != 0 {
		t.Fatal("pausing must forget the collectors")
	}
	a.Add(edge(2))
	a.Seen("n1", "ebpf", true)
	a.AddLost(5)
	if a.Held() != 0 || a.Flush() != nil {
		t.Fatal("a paused aggregator took something in")
	}
	a.SetPaused(false)
	a.Add(edge(3))
	if a.Held() != 1 || a.Flush() == nil {
		t.Fatal("resuming did not restore collection")
	}
}
