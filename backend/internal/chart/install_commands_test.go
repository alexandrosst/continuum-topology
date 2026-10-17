package chart

import (
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The install and upgrade commands the web UI prints are built in TypeScript (src/lib/install.ts and its neighbours) as strings. The
// first half of the pin is tests/installCommands.test.ts: it writes the exact command for a matrix of settings into
// tests/install_commands.golden.json. This is the second half. For every command pinned there it gets the arguments a shell would
// pass to helm, turns the install or upgrade into a `helm template` of the chart that command is for, and requires that the chart
// accepts every value. The charts' schemas refuse an unknown key and a value of the wrong type, so a command that names a value the
// chart does not have (a key renamed on one side only) fails here, not on a person's cluster.
//
// The shell is a real one: the command is run with `helm` defined as a function that prints its arguments, so quoting, escaped
// quotes and line continuations are read by bash and not by a second reader written here that could disagree with it.
//
// The commands the server builds in Go are pinned in the same way, in internal/server/testdata/install_commands.golden.json, and are
// rendered here too (TestEveryInstallCommandTheServerPrintsRendersTheChart).
//
// Not covered: what the rendered configuration does at run time (the tests that run the real collector).
const installCommandsGolden = "../../../tests/install_commands.golden.json"

// serverCommandsGolden is the same pin for the commands the server builds in Go, written by internal/server's
// TestServerInstallCommandsArePinned. Its cases that are not helm (a Secret, a kubectl line) have no chart and are not rendered.
const serverCommandsGolden = "../server/testdata/install_commands.golden.json"

type pinnedCommand struct {
	Name    string `json:"name"`
	Chart   string `json:"chart"`
	Command string `json:"command"`
}

// helmArgs returns what `helm` would be called with by the command, as the shell passes it.
func helmArgs(t *testing.T, command string) []string {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	script := "helm() { printf '%s\\0' \"$@\"; }\n" + command + "\n"
	var out, stderr bytes.Buffer
	cmd := exec.Command(bash, "-c", script)
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("the shell could not run the command: %v\n%s\n%s", err, stderr.String(), command)
	}
	parts := strings.Split(strings.TrimSuffix(out.String(), "\x00"), "\x00")
	return parts
}

// valuesOf turns the arguments of an install or upgrade into the arguments `helm template` needs for the same values. Flags that
// belong to the release and not to the values (namespace, version, the upgrade mode) are dropped; any other flag is an error, so a
// new kind of flag in a command is noticed instead of silently left out of the check.
func valuesOf(t *testing.T, name string, args []string) (values []string, endpoints []string, enabled bool) {
	t.Helper()
	if len(args) < 3 || (args[0] != "install" && args[0] != "upgrade") {
		t.Fatalf("%s: not a helm install or upgrade: %q", name, args)
	}
	if len(args) > 1 && args[1] == "--install" {
		args = append([]string{args[0]}, args[2:]...) // `helm upgrade --install`: the release may not exist yet, which changes nothing about its values
	}
	rest := args[3:] // the verb, the release and the chart
	for i := 0; i < len(rest); i++ {
		switch f := rest[i]; f {
		case "--create-namespace", "--reset-then-reuse-values":
		case "--namespace", "-n", "--version":
			i++
		case "--set", "--set-string", "--set-json":
			if i+1 >= len(rest) {
				t.Fatalf("%s: %s has no value", name, f)
			}
			v := rest[i+1]
			i++
			values = append(values, f, v)
			key, val, _ := strings.Cut(v, "=")
			if strings.HasSuffix(key, ".enabled") && val == "true" && strings.HasPrefix(key, "telemetry.") {
				enabled = true
			}
			if f == "--set-string" && val != "" && (key == "telemetry.export.otlp.endpoint" || (strings.HasPrefix(key, "telemetry.export.routes.") && strings.HasSuffix(key, ".endpoint"))) {
				endpoints = append(endpoints, val)
			}
		default:
			t.Fatalf("%s: a flag this test does not know what to do with: %s (teach valuesOf whether it is a value or part of the release)", name, f)
		}
	}
	return values, endpoints, enabled
}

// hostOf is the part of a destination a rendered collector configuration is bound to repeat: its host, without a scheme or a path.
func hostOf(endpoint string) string {
	if strings.Contains(endpoint, "://") {
		if u, err := url.Parse(endpoint); err == nil {
			return u.Hostname()
		}
	}
	host, _, _ := strings.Cut(endpoint, "/")
	host, _, _ = strings.Cut(host, ":")
	return host
}

func TestEveryInstallCommandTheUIPrintsRendersTheChart(t *testing.T) {
	rendersTheChart(t, installCommandsGolden, "tests/install_commands.golden.json is written by tests/installCommands.test.ts", 60)
}

func TestEveryInstallCommandTheServerPrintsRendersTheChart(t *testing.T) {
	rendersTheChart(t, serverCommandsGolden, "internal/server/testdata/install_commands.golden.json is written by TestServerInstallCommandsArePinned", 100)
}

func rendersTheChart(t *testing.T, golden, writtenBy string, atLeast int) {
	raw, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (%s)", err, writtenBy)
	}
	var cases []pinnedCommand
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < atLeast {
		t.Fatalf("only %d pinned commands: the file is not what it should be (%s)", len(cases), writtenBy)
	}
	seen := map[string]int{}
	for _, c := range cases {
		c := c
		if c.Chart == "" {
			continue // not helm: a Secret or a kubectl line
		}
		seen[c.Chart]++
		t.Run(c.Name, func(t *testing.T) {
			values, endpoints, enabled := valuesOf(t, c.Name, helmArgs(t, c.Command))
			var out string
			var err error
			switch c.Chart {
			case "agent":
				out, err = helmTemplate(t, values...)
			case "regional-operator":
				out, err = operatorHelmTemplate(t, values...)
			default:
				t.Fatalf("a chart this test does not know: %q", c.Chart)
			}
			if err != nil {
				t.Fatalf("the chart refuses what this command sets:\n%s\n\n%s", out, c.Command)
			}
			// A destination that is switched on for a signal must come out in the rendered collector configuration, whatever the
			// spelling of the flags that put it there.
			if c.Chart == "agent" && enabled {
				for _, e := range endpoints {
					if h := hostOf(e); !strings.Contains(out, h) {
						t.Errorf("the destination %s (host %s) does not appear in what the chart rendered, though a signal is on", e, h)
					}
				}
			}
		})
	}
	for _, chart := range []string{"agent", "regional-operator"} {
		if seen[chart] == 0 {
			t.Errorf("no pinned command is for the %s chart", chart)
		}
	}
}
