package install

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

// The page used to write the block that points an agent at a regional operator and merge the server's part into it by pattern. The server
// writes all of it now. tests/installCommands.test.ts pins what the page wrote for each case, beside what the server says of the operators
// (the values it states, the Secrets it creates, how each receiver authenticates), and this test demands the same bytes from the renderer.
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
