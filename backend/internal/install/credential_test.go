package install

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

func TestCredentialSecretsOfRoutes(t *testing.T) {
	tel := valid()
	tel.Signals = Signals{ResourceUsage: true, SystemLogs: true, Traces: true}
	tel.Routes.Split = true
	tel.Routes.Metrics = Destination{Endpoint: "m:1", Protocol: "grpc", AuthSecret: "shared", AuthKey: "a"}
	tel.Routes.Logs = Destination{Endpoint: "l:1", Protocol: "grpc", AuthSecret: "shared", AuthKey: "b"}
	tel.Routes.Traces = Destination{Endpoint: "t:1", Protocol: "grpc", AuthSecret: "tempo"}
	got := tel.CredentialSecrets("")
	if len(got) != 2 || got[0].Name != "shared" || got[1].Name != "tempo" {
		t.Fatalf("%+v", got)
	}
	// One Secret under two keys is one command with both, each read from its own variable.
	if want := `kubectl create secret generic shared --namespace ikhnos-system --from-literal=a="${TELEMETRY_EXPORT_TOKEN_METRICS:?set TELEMETRY_EXPORT_TOKEN_METRICS to the credential first}" --from-literal=b="${TELEMETRY_EXPORT_TOKEN_LOGS:?set TELEMETRY_EXPORT_TOKEN_LOGS to the credential first}" --dry-run=client -o yaml | kubectl apply -f -`; got[0].Command != want {
		t.Errorf("got\n%s\nwant\n%s", got[0].Command, want)
	}
	if got[0].Variable != "TELEMETRY_EXPORT_TOKEN_METRICS" || len(got[0].Lanes) != 2 || got[1].Variable != "TELEMETRY_EXPORT_TOKEN_TRACES" {
		t.Errorf("%+v", got)
	}
	// Two routes that name the same Secret and key share one variable and one literal.
	tel.Routes.Logs.AuthKey = "a"
	got = tel.CredentialSecrets("")
	if strings.Count(got[0].Command, "--from-literal") != 1 || len(got[0].Lanes) != 2 {
		t.Errorf("%+v", got[0])
	}
}

func TestCredentialSecretsOfOneDestination(t *testing.T) {
	tel := valid()
	if got := tel.CredentialSecrets(""); got != nil || tel.CredentialCommand("") != "" {
		t.Fatalf("no Secret named: %v", got)
	}
	tel.Destination.AuthSecret = "tok"
	got := tel.CredentialSecrets("ikhnos")
	if len(got) != 1 || got[0].Variable != CredentialVar || got[0].Lanes != nil || !strings.Contains(got[0].Command, "--namespace ikhnos --from-literal=token=") {
		t.Fatalf("%+v", got)
	}
	// A namespace that is not a name is dropped for the default, not pasted.
	if c := tel.CredentialCommand("x; rm -rf /"); !strings.Contains(c, "--namespace ikhnos-system") || strings.Contains(c, "rm") {
		t.Fatal(c)
	}
	// Nothing is created for telemetry that is off or has a problem: its upgrade command carries no credential flags either.
	off := tel
	off.Signals = Signals{}
	bad := tel
	bad.Destination.Endpoint = ""
	badName := tel
	badName.Destination.AuthSecret = "x; rm -rf /"
	// A destination as installed is not checked, so a name in it is not known to be one: it is not made into a command.
	keptBad := badName
	keptBad.Kept = []string{KeptDestination}
	for name, c := range map[string]Telemetry{"off": off, "a problem": bad, "a bad name": badName, "a bad name as installed": keptBad} {
		if c.CredentialSecrets("") != nil {
			t.Errorf("%s: a Secret is created", name)
		}
	}
}

// The command reads the credential from the shell's environment, and does nothing when it is not set. Run for real, with kubectl a
// function that behaves as the real one does at the two ends of the pipe: `create --dry-run` prints what it was given, and `apply` refuses
// an empty input, as the real one does ("no objects passed to apply").
func TestTheCredentialIsReadFromTheEnvironmentAndAnUnsetVariableCreatesNothing(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	tel := valid()
	tel.Destination.AuthSecret = "tok"
	cmd := tel.CredentialCommand("")
	kubectl := `kubectl() { if [ "$1" = apply ]; then in=$(cat); [ -n "$in" ] || { echo "error: no objects passed to apply" >&2; return 1; }; echo "applied: $in" >&2; else printf '%s\n' "$@"; fi; }` + "\n"
	run := func(env string) (string, error) {
		var out, stderr bytes.Buffer
		sh := exec.Command(bash, "-c", env+kubectl+cmd)
		sh.Stdout, sh.Stderr = &out, &stderr
		err := sh.Run()
		return out.String() + stderr.String(), err
	}
	out, err := run("export TELEMETRY_EXPORT_TOKEN='s3cr3t $(id)'\n")
	if err != nil || !strings.Contains(out, "--from-literal=token=s3cr3t $(id)") {
		t.Fatalf("%v: %s", err, out)
	}
	out, err = run("")
	if err == nil || !strings.Contains(out, "set TELEMETRY_EXPORT_TOKEN to the credential first") || strings.Contains(out, "applied:") {
		t.Fatalf("an unset variable must create nothing and fail: %v %s", err, out)
	}
}

func TestChain(t *testing.T) {
	if Chain(nil) != "" || Chain([]string{"a"}) != "a" {
		t.Fatal("one command, or none, is not joined")
	}
	if got, want := Chain([]string{"a", "b", "c"}), "a && \\\nb && \\\nc"; got != want {
		t.Fatalf("got %q", got)
	}
	// A here-document cannot be followed by ` && \`: the closing word has to be alone on its line, so the command is wrapped in braces.
	heredoc := "kubectl apply -f - <<'EOF'\nx: 1\nEOF"
	if got, want := Chain([]string{heredoc, "next"}), "{ "+heredoc+"\n} && \\\nnext"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	// Only the commands before the last need it.
	if got := Chain([]string{"first", heredoc}); got != "first && \\\n"+heredoc {
		t.Fatalf("got %q", got)
	}
	for _, c := range []string{`cat <<EOF`, `cat <<-"EOF"`, `cat << 'EOF'`} {
		if !strings.HasPrefix(Chain([]string{c, "x"}), "{ ") {
			t.Errorf("%s is a here-document", c)
		}
	}
	if strings.HasPrefix(Chain([]string{"echo 1<<2", "x"}), "{ ") {
		t.Error("a shift is not a here-document")
	}
}
