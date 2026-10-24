package install

import (
	"encoding/json"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// The patch of an IkhnosAgent carries text a person typed (an endpoint, a Secret's name, a tag) inside a shell command. This holds it to
// being one word, whatever the text holds: the command is run with `kubectl` defined as a function that prints the words it was given, and
// the words must be the ones the patch describes, with the payload the JSON that says what was typed. The seeds run with every `go test`;
// `go test -fuzz=FuzzResourcePatchIsOneQuotedPayload` looks for more. It needs `sh`.
func FuzzResourcePatchIsOneQuotedPayload(f *testing.F) {
	f.Add("collector.example.com:4317", "otlp-token", "team", "it's", "ikhnos", uint8(1), true)
	f.Add("$(touch /tmp/pwned)", "`id`", "k", "'; echo hi; '", "ns", uint8(2), true)
	f.Add("a\nb\\\n--type=x", "x y", "", "~root", "ikhnos-system", uint8(0), false)
	f.Add("", "", "k", "{\"a\":\"b c\"}", "x", uint8(3), true)
	f.Fuzz(func(t *testing.T, endpoint, secret, tagKey, tagValue, ns string, tier uint8, on bool) {
		sh, err := exec.LookPath("sh")
		if err != nil {
			t.Skip("sh is not installed")
		}
		for _, s := range []string{endpoint, secret, tagKey, tagValue, ns} {
			if strings.ContainsRune(s, 0) || !utf8.ValidString(s) {
				t.Skip("a NUL cannot be in a shell word, and JSON cannot hold text that is not UTF-8")
			}
		}
		p := ResourcePatch{Namespace: ns, SetTelemetry: true}
		if tier%4 != 3 {
			n := int(tier % 4)
			p.Tier = &n
		}
		if on {
			p.Telemetry = &ResourceTelemetry{
				Signals:     ResourceSignals{ResourceUsage: true},
				Destination: &ResourceDestination{Endpoint: endpoint, AuthSecret: secret},
				Processing:  &ResourceProcessing{Tags: []Tag{{Key: tagKey, Value: tagValue}}},
			}
		}
		cmd, err := p.Render()
		if err != nil {
			return // refused: a namespace that is not a plain name, a tier out of range
		}
		out, err := exec.Command(sh, "-c", "kubectl() { printf '%s\\0' \"$@\"; }\n"+cmd).Output()
		if err != nil {
			t.Fatalf("the shell refused the command: %v\n%s", err, cmd)
		}
		got := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
		if len(got) != 9 || !slices.Equal(got[:5], []string{"--namespace", ns, "patch", "ikhnosagents.ikhnos.io", ControllerResourceName}) || got[5] != "--type" || got[7] != "-p" {
			t.Fatalf("the shell read %q\ncommand:\n%s", got, cmd)
		}
		// The payload is what was typed, to the letter.
		var ops []struct {
			Op    string          `json:"op"`
			Path  string          `json:"path"`
			Value json.RawMessage `json:"value"`
		}
		switch got[6] {
		case "json":
			if err := json.Unmarshal([]byte(got[8]), &ops); err != nil {
				t.Fatalf("the payload is not JSON: %v\n%q", err, got[8])
			}
			last := ops[len(ops)-1]
			if last.Path != "/spec/telemetry" {
				t.Fatalf("the last operation is %q", last.Path)
			}
			var rt ResourceTelemetry
			if err := json.Unmarshal(last.Value, &rt); err != nil {
				t.Fatal(err)
			}
			if rt.Destination.Endpoint != endpoint || rt.Destination.AuthSecret != secret || rt.Processing == nil || len(rt.Processing.Tags) != 1 || rt.Processing.Tags[0] != (Tag{Key: tagKey, Value: tagValue}) {
				t.Fatalf("the payload says %+v, not what was typed", rt)
			}
		case "merge":
			var m map[string]any
			if err := json.Unmarshal([]byte(got[8]), &m); err != nil {
				t.Fatalf("the payload is not JSON: %v\n%q", err, got[8])
			}
			if on {
				t.Fatalf("telemetry on was written as a merge patch: %q", got[8])
			}
		default:
			t.Fatalf("the patch type is %q", got[6])
		}
	})
}
