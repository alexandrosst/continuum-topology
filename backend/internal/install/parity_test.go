package install

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The web UI used to write the commands it shows in TypeScript; they are moving here, one kind at a time. tests/installCommands.test.ts
// pins what the UI prints and, beside each command, the spec of what it was asked to write. This test gives the same spec to the renderer
// in this package and demands the same bytes. A command is moved when its case has a spec; the TypeScript is deleted when every case does.
const uiGolden = "../../../tests/install_commands.golden.json"

// spec is what a case says about the command. The fields are the ones of tests/installCommands.test.ts's Spec.
type spec struct {
	Head       string        `json:"head"`
	Upgrade    *AgentUpgrade `json:"upgrade"`
	Collectors *Collectors   `json:"collectors"`
	Scope      *Scope        `json:"scope"`
	Telemetry  *Telemetry    `json:"telemetry"`
}

type uiCase struct {
	Name    string `json:"name"`
	Chart   string `json:"chart"`
	Command string `json:"command"`
	Spec    *spec  `json:"spec"`
}

func readUICases(t *testing.T) []uiCase {
	t.Helper()
	raw, err := os.ReadFile(uiGolden)
	if err != nil {
		t.Fatal(err)
	}
	// A spec field the renderer does not know is an error: it would otherwise be ignored, and the case would pass without saying it.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var cases []uiCase
	if err := dec.Decode(&cases); err != nil {
		t.Fatal(err)
	}
	return cases
}

// write is the command the spec describes.
func (s spec) write() (string, error) {
	head := strings.TrimRight(s.Head, "\n ")
	if s.Upgrade != nil {
		up := *s.Upgrade
		up.Telemetry = s.Telemetry
		return up.Render()
	}
	var values []Value
	if s.Collectors != nil {
		values = append(values, s.Collectors.Values()...)
	}
	if s.Scope != nil {
		values = append(values, s.Scope.Values()...)
	}
	if s.Telemetry != nil {
		v, err := s.Telemetry.Values()
		if err != nil {
			return "", err
		}
		values = append(values, v...)
	}
	return head + Continued(values), nil
}

// portedCases is how many cases carry a spec. It only goes up: a case that loses its spec would stop being checked here.
const portedCases = 72

func TestWhatTheUIPrintsIsWhatTheRendererWrites(t *testing.T) {
	cases := readUICases(t)
	ported := 0
	for _, c := range cases {
		if c.Spec == nil {
			continue
		}
		ported++
		t.Run(c.Name, func(t *testing.T) {
			got, err := c.Spec.write()
			if err != nil {
				t.Fatal(err)
			}
			if got != c.Command {
				t.Fatalf("the renderer wrote\n%s\nthe UI prints\n%s", got, c.Command)
			}
		})
	}
	if ported < portedCases {
		t.Fatalf("%d of %d cases carry a spec, %d did before", ported, len(cases), portedCases)
	}
}
