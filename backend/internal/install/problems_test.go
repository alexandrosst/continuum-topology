package install

import (
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

const uiProblemsGolden = "../../../tests/telemetry_problems.golden.json"

// The page says what is wrong with a draft as it is edited and the server says it again for the draft it is sent. They must say the same:
// a page that lets a draft through that the server refuses shows a person a command that is not there, and one that blocks a draft the
// server would write is a step that cannot be taken. tests/telemetry_problems.golden.json holds what the page says of each draft, beside the
// spec the server is sent.
//
// The page knows one more thing than the server: which backends carry which signals (Jaeger takes only traces), from the catalog of
// backends it offers. That advice is the page's, and is set aside here by its shape; everything else must be the same words in the same order.
var pageOnlyAdvice = regexp.MustCompile(`^(.+) only carries (.+?)( - turn off the other signals, or send everything somewhere else|, so it cannot be where .+ go)$`)

func isPageOnly(problem string) bool {
	m := pageOnlyAdvice.FindStringSubmatch(problem)
	// "Zipkin only carries traces..." is about the protocol, which the server knows as well; a backend is named with what it is.
	return m != nil && m[1] != "Zipkin"
}

func TestWhatThePageSaysIsWrongWithADraftIsWhatTheServerSays(t *testing.T) {
	raw, err := os.ReadFile(uiProblemsGolden)
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name     string   `json:"name"`
		Problems []string `json:"problems"`
		Spec     struct {
			Telemetry Telemetry `json:"telemetry"`
		} `json:"spec"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 50 {
		t.Fatalf("only %d cases", len(cases))
	}
	advice, plain := 0, 0
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			var want []string
			for _, p := range c.Problems {
				if isPageOnly(p) {
					advice++
					continue
				}
				want = append(want, p)
			}
			if len(want) == 0 {
				plain++
			}
			if got := c.Spec.Telemetry.Problems(); strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("the page says\n  %s\nthe server says\n  %s", strings.Join(want, "\n  "), strings.Join(got, "\n  "))
			}
		})
	}
	// The advice that is set aside is some of the pinned problems, not all of them: a pattern that matched everything would hide any difference.
	if advice == 0 || advice > 8 {
		t.Fatalf("%d pieces of the page's backend advice were set aside", advice)
	}
	if plain < 8 {
		t.Fatalf("only %d drafts without a problem", plain)
	}
}

// What is set aside is only advice about a backend.
func TestOnlyBackendAdviceIsSetAside(t *testing.T) {
	for _, p := range []string{
		"Jaeger (traces only) only carries traces - turn off the other signals, or send everything somewhere else",
		"Jaeger (traces only) only carries traces, so it cannot be where metrics go",
	} {
		if !isPageOnly(p) {
			t.Errorf("%q is a backend's advice", p)
		}
	}
	for _, p := range []string{
		"Zipkin only carries traces - turn off metrics, or send everything somewhere that speaks OTLP",
		"Zipkin only carries traces, so it cannot be where metrics go",
		"Traces sampling must be between 0 and 100",
		"An export endpoint is required once any telemetry signal is on",
		"Metrics needs a destination",
	} {
		if isPageOnly(p) {
			t.Errorf("%q is not a backend's advice", p)
		}
	}
}
