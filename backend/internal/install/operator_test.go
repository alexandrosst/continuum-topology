package install

import (
	"strings"
	"testing"
)

func TestOperatorProcessors(t *testing.T) {
	cond := []FilterCondition{{Field: "level", Op: "eq", Value: "debug"}}
	filter := Processor{ID: "p1", Kind: Filter, Name: "drop_debug", Filter: FilterConfig{Signal: "log", Conditions: cond}}
	tail := Processor{ID: "p2", Kind: TailSampling, Name: "errs", TailSampling: TailSamplingConfig{DecisionWaitSeconds: 10, Policies: []SamplingPolicy{{Name: "a", Type: "probabilistic", ProbabilisticPercent: 5}}}}
	other := Processor{ID: "p3", Kind: Transform, Name: "tag", Transform: TransformConfig{Signal: "trace", Statements: []TransformStatement{{Action: "delete", Key: "pw"}}}}

	if v, err := OperatorProcessors(nil); v != nil || err != nil {
		t.Fatalf("nothing to add: %v %v", v, err)
	}
	// Each list is stated whole, and only a list that has something in it.
	v, err := OperatorProcessors([]Processor{filter})
	if err != nil {
		t.Fatal(err)
	}
	if got := Flags(v[1:]); got != "--set processors.extraProcessorNames='{filter/drop_debug}'" {
		t.Fatalf("got %s", got)
	}
	v, err = OperatorProcessors([]Processor{tail, filter, other})
	if err != nil {
		t.Fatal(err)
	}
	if got := Flags(v[1:]); got != "--set processors.extraProcessorNames='{filter/drop_debug,transform/tag}' --set processors.extraTracesProcessorNames='{tail_sampling/errs}'" {
		t.Fatalf("got %s", got)
	}
	if !strings.HasPrefix(Flags(v[:1]), "--set-json processors.extraProcessors='{\"tail_sampling/errs\":") {
		t.Fatalf("the bodies come first, in the order added: %s", Flags(v[:1]))
	}
	// The operator chart has no telemetry root.
	if strings.Contains(Flags(v), "telemetry.") {
		t.Fatal("the operator chart's processors are not under telemetry")
	}
	if _, err := OperatorProcessors([]Processor{{ID: "p", Kind: Filter, Name: "f"}}); err == nil || !strings.Contains(err.Error(), "no conditions") {
		t.Fatalf("a processor with a problem gives no values: %v", err)
	}
}
