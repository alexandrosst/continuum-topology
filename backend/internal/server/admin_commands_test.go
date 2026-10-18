package server

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"continuum/internal/install"
	"continuum/internal/kernel"
)

const (
	uiCommandsGolden = "../../../tests/install_commands.golden.json"
	uiSecretsGolden  = "../../../tests/install_secrets.golden.json"
	upgradePath      = "/api/v1/commands/agent-upgrade"
)

// upgradeAnswer is the endpoint's answer, read back.
type upgradeAnswer struct {
	Command       string `json:"command"`
	Upgrade       string `json:"upgrade"`
	SecretCommand string `json:"secretCommand"`
	Secrets       []struct {
		Name     string   `json:"name"`
		Variable string   `json:"variable"`
		Lanes    []string `json:"lanes"`
		Command  string   `json:"command"`
	} `json:"secrets"`
	Problems []string `json:"problems"`
}

func (a *adminRig) upgrade(t *testing.T, cookie string, body any) (int, upgradeAnswer) {
	t.Helper()
	r := a.do("POST", upgradePath, body, withCookie(cookie))
	var out upgradeAnswer
	if r.Code == 200 {
		if err := json.Unmarshal(r.Body.Bytes(), &out); err != nil {
			t.Fatalf("not JSON: %q", r.Body.String())
		}
	}
	return r.Code, out
}

// An administrator's page is not the only reader of this command: anyone who can see the agent can ask for it, and no one outside the
// organisation can.
func TestAgentUpgradeCommandIsForMembersOnly(t *testing.T) {
	a := newAdminRig(t)
	_, viewer := a.user(t, "vera", RoleViewer)
	if code, out := a.upgrade(t, viewer, map[string]any{"tier": 1}); code != 200 || !strings.Contains(out.Command, "--set access.tier=1") {
		t.Fatalf("a viewer is told the command: %d %+v", code, out)
	}
	if r := a.do("POST", upgradePath, map[string]any{"tier": 1}); r.Code != 401 {
		t.Fatalf("no session: %d", r.Code)
	}
	if r := a.do("POST", upgradePath, map[string]any{"tier": 1}, withCookie(viewer), withoutXRW()); r.Code == 200 {
		t.Fatalf("a request that is not from the page was answered: %d", r.Code)
	}
	_ = a.account(t, "stranger")
	stranger := a.login(t, "stranger", goodPW)
	if r := a.do("POST", upgradePath, map[string]any{"tier": 1}, withCookie(stranger)); r.Code < 400 {
		t.Fatalf("someone outside the organisation was answered: %d", r.Code)
	}
}

// The chart is the one this server serves, whatever the page says: the page cannot name another, and says nothing about it.
func TestAgentUpgradeCommandNamesTheChartTheServerServes(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "ed", RoleEditor)
	tier := 2

	a.a.ChartRef = ""
	_, out := a.upgrade(t, cookie, map[string]any{"tier": tier})
	if want := "helm upgrade continuum-agent ./" + a.a.chartFile() + " --namespace continuum-system --reset-then-reuse-values --set access.tier=2"; out.Command != want || out.Upgrade != want {
		t.Fatalf("a chart file: %q", out.Command)
	}

	a.a.ChartRef = "oci://registry.example.com/continuum-agent"
	_, out = a.upgrade(t, cookie, map[string]any{"tier": tier, "namespace": "ikhnos", "release": "edge-agent"})
	if !strings.Contains(out.Command, "helm upgrade edge-agent oci://registry.example.com/continuum-agent --version "+a.a.agentChartVersion()+" --namespace ikhnos ") {
		t.Fatalf("a registry chart: %q", out.Command)
	}

	// A page that tries to name a chart of its own is told it may not.
	if code, _ := a.upgrade(t, cookie, map[string]any{"tier": tier, "chartRef": "oci://evil.example.com/x"}); code != 400 {
		t.Fatalf("a chart named by the page: %d", code)
	}
}

func TestAgentUpgradeCommandRefusesWhatCannotBeTrue(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "ed", RoleEditor)
	for name, body := range map[string]any{
		"a tier below the lowest":  map[string]any{"tier": -1},
		"a tier above the highest": map[string]any{"tier": kernel.ImplementedTier + 1},
		"a field nobody knows":     map[string]any{"tier": 1, "nope": true},
		"telemetry that is a list": map[string]any{"telemetry": []int{1}},
		"not an object":            []byte(`"helm"`),
		"a tier that is text":      map[string]any{"tier": "2"},
	} {
		if code, _ := a.upgrade(t, cookie, body); code != 400 {
			t.Errorf("%s: %d, want 400", name, code)
		}
	}
	// A body past the size every request is held to is refused before it is read.
	big := `{"namespace":"` + strings.Repeat("a", 70<<10) + `"}`
	if code, _ := a.upgrade(t, cookie, []byte(big)); code != 400 && code != 413 {
		t.Errorf("a body of 70 KiB: %d", code)
	}
}

// A namespace or release is whatever the agent reported and ends up in a shell line: the command never carries one that is not a plain name.
func TestAgentUpgradeCommandNeverPastesAHostileName(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "ed", RoleEditor)
	for _, hostile := range []string{"x; rm -rf /", "$(id)", "a b", "`id`", "A", "--set x=1", "x\ny"} {
		_, out := a.upgrade(t, cookie, map[string]any{"tier": 1, "namespace": hostile, "release": hostile})
		if strings.Contains(out.Command, hostile) || !strings.Contains(out.Command, "helm upgrade continuum-agent ") || !strings.Contains(out.Command, "--namespace continuum-system ") {
			t.Errorf("%q reached the command: %q", hostile, out.Command)
		}
	}
}

// Telemetry that is not fit to write a command from is told as problems, with no command: the page shows them where it would show the block.
func TestAgentUpgradeCommandSaysWhatIsWrongInsteadOfAPartialCommand(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "ed", RoleEditor)
	// A signal on with nowhere to send it.
	body := map[string]any{"telemetry": map[string]any{"signals": map[string]any{"resourceUsage": true}}}
	code, out := a.upgrade(t, cookie, body)
	if code != 200 || out.Command != "" || out.Upgrade != "" || len(out.Problems) == 0 {
		t.Fatalf("%d %+v", code, out)
	}
	if !strings.Contains(strings.Join(out.Problems, ";"), "export endpoint") {
		t.Fatalf("problems: %v", out.Problems)
	}
	// Problems are also what the renderer says of the same draft.
	var tel install.Telemetry
	j, _ := json.Marshal(body["telemetry"])
	if err := json.Unmarshal(j, &tel); err != nil {
		t.Fatal(err)
	}
	if want := tel.Problems(); strings.Join(out.Problems, "|") != strings.Join(want, "|") {
		t.Fatalf("the endpoint says %v, the renderer %v", out.Problems, want)
	}
}

type goldenCase struct {
	Name    string `json:"name"`
	Command string `json:"command"`
	Spec    struct {
		Upgrade   json.RawMessage `json:"upgrade"`
		Telemetry json.RawMessage `json:"telemetry"`
	} `json:"spec"`
}

func readGolden(t *testing.T, file string, into any) {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatal(err)
	}
}

// Every telemetry the page's tests describe is sent to the endpoint as the page would send it, and the endpoint answers what the renderer
// writes for it: the HTTP layer adds nothing and loses nothing (a field it did not read would show here as a difference).
func TestAgentUpgradeCommandIsWhatTheRendererWritesForEveryPinnedTelemetry(t *testing.T) {
	a := newAdminRig(t)
	a.a.ChartRef = ""
	_, cookie := a.user(t, "ed", RoleEditor)
	var cases []goldenCase
	readGolden(t, uiCommandsGolden, &cases)
	n := 0
	for _, c := range cases {
		if len(c.Spec.Telemetry) == 0 || string(c.Spec.Telemetry) == "null" {
			continue
		}
		n++
		t.Run(c.Name, func(t *testing.T) {
			var tel install.Telemetry
			if err := json.Unmarshal(c.Spec.Telemetry, &tel); err != nil {
				t.Fatal(err)
			}
			up := install.AgentUpgrade{ChartFile: a.a.chartFile(), Namespace: "ikhnos", Release: "edge-agent", Telemetry: &tel}
			want, werr := up.Render()
			code, got := a.upgrade(t, cookie, map[string]any{"namespace": "ikhnos", "release": "edge-agent", "telemetry": c.Spec.Telemetry})
			if code != 200 {
				t.Fatalf("status %d", code)
			}
			if werr != nil {
				pe, ok := werr.(*install.ProblemsError)
				if !ok {
					t.Fatalf("the renderer fails with %v, which is not the person's to fix", werr)
				}
				if got.Command != "" || strings.Join(got.Problems, "|") != strings.Join(pe.Problems, "|") {
					t.Fatalf("the renderer refuses with %v; the endpoint answered %+v", pe.Problems, got)
				}
				return
			}
			if got.Upgrade != want || len(got.Problems) != 0 {
				t.Fatalf("the renderer wrote\n%s\nthe endpoint\n%s\n%v", want, got.Upgrade, got.Problems)
			}
			if wantSecrets := tel.CredentialCommand("ikhnos"); got.SecretCommand != wantSecrets {
				t.Fatalf("secrets: the renderer wrote\n%s\nthe endpoint\n%s", wantSecrets, got.SecretCommand)
			}
			block := want
			if got.SecretCommand != "" {
				block = install.Chain(append(secretCommands(tel, "ikhnos"), want))
			}
			if got.Command != block {
				t.Fatalf("the block is\n%s\nwant\n%s", got.Command, block)
			}
		})
	}
	if n < 50 {
		t.Fatalf("only %d telemetry cases", n)
	}
}

func secretCommands(t install.Telemetry, ns string) []string {
	var out []string
	for _, s := range t.CredentialSecrets(ns) {
		out = append(out, s.Command)
	}
	return out
}

// The commands the page has always printed for an upgrade are what this endpoint now writes, byte for byte.
func TestAgentUpgradeCommandIsWhatTheUIPinsForAnUpgrade(t *testing.T) {
	a := newAdminRig(t)
	a.a.ChartRef = ""
	_, cookie := a.user(t, "ed", RoleEditor)
	var cases []goldenCase
	readGolden(t, uiCommandsGolden, &cases)
	checked := 0
	for _, c := range cases {
		if len(c.Spec.Upgrade) == 0 || string(c.Spec.Upgrade) == "null" {
			continue
		}
		var u install.AgentUpgrade
		if err := json.Unmarshal(c.Spec.Upgrade, &u); err != nil {
			t.Fatal(err)
		}
		// Only the cases that name no chart: a chart is the server's to name.
		if u.ChartRef != "" || u.ChartVersion != "" || u.ChartFile != "" || len(c.Spec.Telemetry) > 0 {
			continue
		}
		checked++
		t.Run(c.Name, func(t *testing.T) {
			// The page pins the command for a chart file it was not told the name of; this server knows the name of the one it serves.
			want := strings.Replace(c.Command, "./"+install.DefaultChartFile, "./"+a.a.chartFile(), 1)
			code, got := a.upgrade(t, cookie, map[string]any{"tier": u.Tier, "namespace": u.Namespace, "release": u.Release})
			if code != 200 || got.Command != want {
				t.Fatalf("%d: the endpoint wrote\n%s\nthe page pins\n%s", code, got.Command, want)
			}
		})
	}
	if checked < 3 {
		t.Fatalf("only %d upgrade cases", checked)
	}
}

// The Secrets the page has always printed are what this endpoint writes, and each is told apart (name, variable, lanes) for the page to explain.
func TestAgentUpgradeCommandWritesTheSecretsTheUIPins(t *testing.T) {
	a := newAdminRig(t)
	a.a.ChartRef = ""
	_, cookie := a.user(t, "ed", RoleEditor)
	var cases []struct {
		Name    string `json:"name"`
		Command string `json:"command"`
		Spec    struct {
			Telemetry json.RawMessage `json:"telemetry"`
			Namespace string          `json:"namespace"`
		} `json:"spec"`
	}
	readGolden(t, uiSecretsGolden, &cases)
	if len(cases) < 10 {
		t.Fatalf("only %d cases", len(cases))
	}
	withSecrets := 0
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			code, got := a.upgrade(t, cookie, map[string]any{"namespace": c.Spec.Namespace, "telemetry": c.Spec.Telemetry})
			if code != 200 {
				t.Fatalf("status %d", code)
			}
			if got.SecretCommand != c.Command {
				t.Fatalf("the endpoint wrote\n%s\nthe page pins\n%s", got.SecretCommand, c.Command)
			}
			if c.Command == "" {
				if len(got.Secrets) != 0 {
					t.Fatalf("secrets without a command: %+v", got.Secrets)
				}
				return
			}
			withSecrets++
			var joined []string
			for _, s := range got.Secrets {
				if s.Name == "" || s.Variable == "" || s.Command == "" || s.Lanes == nil {
					t.Fatalf("a secret left half told: %+v", s)
				}
				joined = append(joined, s.Command)
			}
			if install.Chain(joined) != c.Command {
				t.Fatalf("the secrets do not add up to the command:\n%s", strings.Join(joined, "\n"))
			}
			// The credential itself is read from the person's shell, not carried here.
			if !bytes.Contains([]byte(got.Command), []byte(":?set ")) || !strings.HasSuffix(got.Command, got.Upgrade) {
				t.Fatalf("the block does not create the Secrets and then upgrade:\n%s", got.Command)
			}
		})
	}
	if withSecrets < 5 {
		t.Fatalf("only %d cases with secrets", withSecrets)
	}
}
