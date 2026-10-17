package install

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The web UI writes the commands it shows in TypeScript, and the server has to be able to write the same ones. tests/installCommands.test.ts
// pins what the UI prints and, beside each command, the spec of what it was asked to write. This test gives the same spec to the renderer
// in this package and demands the same bytes. Every case has one.
const uiGolden = "../../../tests/install_commands.golden.json"

// spec is what a case says about the command. The fields are the ones of tests/installCommands.test.ts's Spec.
type spec struct {
	Head       string        `json:"head"`
	Upgrade    *AgentUpgrade `json:"upgrade"`
	Collectors *Collectors   `json:"collectors"`
	Scope      *Scope        `json:"scope"`
	Telemetry  *Telemetry    `json:"telemetry"`
	// OperatorProcessors are the regional operator chart's extra processors.
	OperatorProcessors []Processor `json:"operatorProcessors"`
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
	if len(s.OperatorProcessors) > 0 {
		v, err := OperatorProcessors(s.OperatorProcessors)
		if err != nil {
			return "", err
		}
		values = append(values, v...)
	}
	return head + Continued(values), nil
}

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
	if ported != len(cases) {
		t.Fatalf("%d of %d cases carry a spec: every command the UI prints must say what it was asked to write", ported, len(cases))
	}
}

const uiSecretsGolden = "../../../tests/install_secrets.golden.json"

// The commands that create the Secrets the credential flags point at are pinned the same way, with the spec each was written from. An
// empty command is "nothing to create".
func TestTheCredentialSecretsTheUIPrintsAreWhatTheRendererWrites(t *testing.T) {
	raw, err := os.ReadFile(uiSecretsGolden)
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name    string `json:"name"`
		Command string `json:"command"`
		Spec    struct {
			Telemetry Telemetry `json:"telemetry"`
			Namespace string    `json:"namespace"`
		} `json:"spec"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 10 {
		t.Fatalf("only %d cases", len(cases))
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			if got := c.Spec.Telemetry.CredentialCommand(c.Spec.Namespace); got != c.Command {
				t.Fatalf("the renderer wrote\n%s\nthe UI prints\n%s", got, c.Command)
			}
		})
	}
}
