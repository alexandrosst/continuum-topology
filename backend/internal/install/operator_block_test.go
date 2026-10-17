package install

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The page used to write the block that points an agent at a regional operator and merge the server's part into it by pattern. The server
// writes all of it now. tests/operator_block.golden.json holds, for each case, what the page pasted, beside what the server says of the
// operators (the values it states, the Secrets it creates, how each receiver authenticates); this test demands the same bytes from the
// renderer. The page no longer writes the block, so nothing regenerates the file: the bytes are the ones the page wrote the day it stopped,
// and a change to them is a change to what a person is told to paste, made here and reviewed as one.
const operatorBlockGolden = "../../../tests/operator_block.golden.json"

type blockCase struct {
	Name         string            `json:"name"`
	ChartFile    string            `json:"chartFile"`
	Spec         Telemetry         `json:"spec"`
	Namespace    string            `json:"namespace"`
	Release      string            `json:"release"`
	Operators    map[string]string `json:"operators"`
	Fragment     []struct{ Key, Value string }
	Certificates []string `json:"certificates"`
	Block        string   `json:"block"`
}

func readBlockCases(t *testing.T) []blockCase {
	t.Helper()
	raw, err := os.ReadFile(operatorBlockGolden)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var cases []blockCase
	if err := dec.Decode(&cases); err != nil {
		t.Fatal(err)
	}
	return cases
}

func (c blockCase) render() (string, error) {
	var restated []Value
	for _, f := range c.Fragment {
		restated = append(restated, Set(f.Key, f.Value))
	}
	spec := c.Spec
	return OperatorTelemetry{
		AgentUpgrade: AgentUpgrade{ChartFile: c.ChartFile, Namespace: c.Namespace, Release: c.Release, Telemetry: &spec},
		Certificates: c.Certificates, Restated: restated, ReceiverAuth: c.Operators,
	}.Render()
}

func TestWhatThePagePastedForAnOperatorIsWhatTheRendererWrites(t *testing.T) {
	cases := readBlockCases(t)
	if len(cases) < 15 {
		t.Fatalf("only %d cases", len(cases))
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			got, err := c.render()
			if err != nil {
				t.Fatal(err)
			}
			if got != c.Block {
				t.Fatalf("the page pasted\n%s\nthe renderer writes\n%s", c.Block, got)
			}
		})
	}
}

// What the server hands out for a client certificate is a here-document, its closing word alone on its line. The block that has one in front
// of an upgrade once left the shell waiting at its > prompt; this one runs through to the helm step and stops at a Secret that fails.
func TestTheOperatorBlockRunsThroughAShellToTheHelmStep(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	secret := "kubectl apply -f - <<'CONTINUUM_SECRET'\napiVersion: v1\nkind: Secret\nstringData:\n  tls.key: |\n    -----BEGIN EC PRIVATE KEY-----\n    abc\n    -----END EC PRIVATE KEY-----\nCONTINUUM_SECRET"
	spec := Telemetry{
		Signals:     Signals{ResourceUsage: true},
		Destination: Destination{Endpoint: "op-central.continuum-system.svc:4317", Protocol: "grpc", Operator: "op-central"},
		Processing:  Processing{TracesSamplingPercent: new(float64)},
	}
	block, err := OperatorTelemetry{
		AgentUpgrade: AgentUpgrade{Telemetry: &spec}, Certificates: []string{secret},
		Restated: []Value{Set("telemetry.export.otlp.endpoint", "op-central.continuum-system.svc:4317")}, ReceiverAuth: map[string]string{"op-central": "mtls"},
	}.Render()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains("\n"+block+"\n", "\nCONTINUUM_SECRET\n") {
		t.Fatalf("the closing word is not alone on its line:\n%s", block)
	}
	run := func(applyOK bool) (string, string, error) {
		apply := "echo SECRET-APPLIED"
		if !applyOK {
			apply = "return 1"
		}
		cmd := exec.Command("bash", "-c", "kubectl() { cat > /dev/null; "+apply+"; }\nhelm() { echo HELM-RAN; }\n"+block)
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		return out.String(), errb.String(), err
	}
	out, stderr, err := run(true)
	if err != nil || stderr != "" || out != "SECRET-APPLIED\nHELM-RAN\n" {
		t.Fatalf("the shell ran %q (stderr %q, err %v)", out, stderr, err)
	}
	out, _, err = run(false)
	if err == nil || strings.Contains(out, "HELM-RAN") {
		t.Fatalf("a failed Secret did not stop the upgrade: %q, %v", out, err)
	}
}
