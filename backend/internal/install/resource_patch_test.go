package install

import (
	"strings"
	"testing"
)

func TestThePatchThatChangesAResourceReplacesTelemetryWholeAndRemovesItByNull(t *testing.T) {
	tel := &ResourceTelemetry{Signals: ResourceSignals{ResourceUsage: true}, Destination: &ResourceDestination{Endpoint: "otel.example.com:4317"}}
	two := 1
	got, err := ResourcePatch{Namespace: "ikhnos-system", SetTelemetry: true, Telemetry: tel}.Render()
	if err != nil {
		t.Fatal(err)
	}
	want := `kubectl --namespace ikhnos-system patch ikhnosagents.ikhnos.io this-cluster --type json -p '[{"op":"add","path":"/spec/telemetry","value":{"signals":{"resourceUsage":true},"destination":{"endpoint":"otel.example.com:4317"}}}]'`
	if got != want {
		t.Errorf("\n%s\nwant\n%s", got, want)
	}
	got, _ = ResourcePatch{Namespace: "ikhnos-system", Tier: &two, SetTelemetry: true, Telemetry: tel}.Render()
	if !strings.Contains(got, `[{"op":"add","path":"/spec/accessTier","value":1},{"op":"add","path":"/spec/telemetry"`) {
		t.Errorf("tier and telemetry: %s", got)
	}
	got, _ = ResourcePatch{Namespace: "ikhnos-system", SetTelemetry: true}.Render()
	if want := `--type merge -p '{"spec":{"telemetry":null}}'`; !strings.HasSuffix(got, want) {
		t.Errorf("off: %s", got)
	}
	got, _ = ResourcePatch{Namespace: "ikhnos-system", Tier: &two, SetTelemetry: true}.Render()
	if want := `--type merge -p '{"spec":{"accessTier":1,"telemetry":null}}'`; !strings.HasSuffix(got, want) {
		t.Errorf("tier and off: %s", got)
	}
	got, _ = ResourcePatch{Namespace: "ikhnos-system", Tier: &two}.Render()
	if want := `--type merge -p '{"spec":{"accessTier":1}}'`; !strings.HasSuffix(got, want) {
		t.Errorf("tier only: %s", got)
	}
}

func TestThePatchRefusesWhatItCannotSay(t *testing.T) {
	bad := 3
	for name, p := range map[string]ResourcePatch{
		"nothing to change":          {Namespace: "ikhnos-system"},
		"a namespace that is not":    {Namespace: "x; rm -rf /", SetTelemetry: true},
		"a tier that does not exist": {Namespace: "ikhnos-system", Tier: &bad},
	} {
		if s, err := p.Render(); err == nil {
			t.Errorf("%s: %s", name, s)
		}
	}
}

// A value in the telemetry is one JSON string inside one quoted word: whatever it holds, the command is the one command.
func TestAValueInThePatchCannotEndTheCommand(t *testing.T) {
	tel := &ResourceTelemetry{Signals: ResourceSignals{ResourceUsage: true}, Destination: &ResourceDestination{Endpoint: "a'; touch /tmp/x; '"}}
	got, err := ResourcePatch{Namespace: "ikhnos-system", SetTelemetry: true, Telemetry: tel}.Render()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(got, "\n") != 0 || !strings.Contains(got, `'\''`) {
		t.Errorf("the quote was not closed and reopened around the value: %s", got)
	}
}
