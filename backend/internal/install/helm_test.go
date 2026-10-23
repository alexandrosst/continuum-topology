package install

import (
	"bytes"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func render(t *testing.T, h Helm) string {
	t.Helper()
	got, err := h.Render()
	if err != nil {
		t.Fatalf("%+v: %v", h, err)
	}
	return got
}

// The three shapes the server prints, as they were before there was a renderer (copied from the commands pinned in
// internal/server/testdata/install_commands.golden.json).
func TestTheThreeShapesTheServerPrints(t *testing.T) {
	cases := []struct {
		name string
		h    Helm
		want string
	}{
		{"an agent install, wrapped", Helm{Verb: Install, Release: "ikhnos-agent", Chart: "oci://registry.example.com/team/ikhnos-agent", Version: "0.0.0-agent",
			Namespace: "ikhnos-system", CreateNamespace: true, Layout: Wrapped,
			Values: []Value{Set("server.address", "srv.example.com:8443"), Set("enrollment.key", "sha256/AAAA=.ikh_SECRET"), Set("access.tier", "3")}},
			"helm install ikhnos-agent oci://registry.example.com/team/ikhnos-agent --version 0.0.0-agent \\\n" +
				"  --namespace ikhnos-system --create-namespace \\\n" +
				"  --set server.address=srv.example.com:8443 \\\n" +
				"  --set enrollment.key=sha256/AAAA=.ikh_SECRET \\\n" +
				"  --set access.tier=3"},
		{"an operator that is changed in place, wrapped", Helm{Verb: UpgradeInstall, Release: "athens", Chart: "./ikhnos-regional-operator-0.0.0-operator.tgz",
			Namespace: "ikhnos-system", CreateNamespace: true, ResetThenReuse: true, Layout: Wrapped,
			Values: []Value{Set("export.otlp.tls.caFile", ""), SetJSON("operator", `{"id":"athens","name":"it's","labels":[]}`)}},
			"helm upgrade --install athens ./ikhnos-regional-operator-0.0.0-operator.tgz \\\n" +
				"  --namespace ikhnos-system --create-namespace --reset-then-reuse-values \\\n" +
				"  --set export.otlp.tls.caFile= \\\n" +
				`  --set-json operator='{"id":"athens","name":"it'\''s","labels":[]}'`},
		{"an upgrade of one value, on one line", Helm{Verb: Upgrade, Release: "obs-agent", Chart: "oci://registry-1.docker.io/myteam/ikhnos-agent", Version: "0.0.0-agent",
			Namespace: "apps-team", ResetThenReuse: true, Values: []Value{Set("access.tier", "2")}},
			"helm upgrade obs-agent oci://registry-1.docker.io/myteam/ikhnos-agent --version 0.0.0-agent --namespace apps-team --reset-then-reuse-values --set access.tier=2"},
	}
	for _, c := range cases {
		if got := render(t, c.h); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

// A value is one word whatever it holds. The command is read by a real shell, with `helm` a function that prints its arguments, and the
// arguments must be exactly the ones described: nothing a value holds ends the command, adds a word or runs.
func TestEveryValueIsExactlyOneWordToARealShell(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	hostile := []string{"", "plain", "has space", "it's", `"double"`, "$(touch /tmp/pwned)", "`id`", "a;b", "a && b", "a | b", "a\nb", `back\slash`, "-n", "--post-renderer=/x",
		"*", "~", "#", "!", "{a,b}", "é世界", "'; touch /tmp/pwned; '", "$HOME", "EOF\nIKHNOS_SECRET"}
	for _, layout := range []Layout{OneLine, Wrapped} {
		for _, verb := range []Verb{Install, Upgrade, UpgradeInstall} {
			var values []Value
			var wantArgs []string
			for i, v := range hostile {
				k := "a.b" + string(rune('A'+i))
				values = append(values, Set(k, v))
				wantArgs = append(wantArgs, "--set", k+"="+v)
				values = append(values, SetString("s."+k, v))
				wantArgs = append(wantArgs, "--set-string", "s."+k+"="+v)
			}
			values = append(values, Strings("names", []string{"a", "it's $(id)", "x y"}), Strings("none", nil))
			wantArgs = append(wantArgs, "--set", "names={a,it's $(id),x y}", "--set-json", "none=[]")
			values = append(values, SetJSON("operator", `{"name":"it's $(id)"}`))
			wantArgs = append(wantArgs, "--set-json", `operator={"name":"it's $(id)"}`)
			h := Helm{Verb: verb, Release: "rel", Chart: "oci://r.example.com/c", Version: "1.2.3", Namespace: "ns", CreateNamespace: true, ResetThenReuse: true, Values: values, Layout: layout}
			cmd := render(t, h)
			var out, stderr bytes.Buffer
			sh := exec.Command(bash, "-c", "helm() { printf '%s\\0' \"$@\"; }\n"+cmd+"\n")
			sh.Stdout, sh.Stderr = &out, &stderr
			if err := sh.Run(); err != nil {
				t.Fatalf("%v: %v\n%s\n%s", verb, err, stderr.String(), cmd)
			}
			got := strings.Split(strings.TrimSuffix(out.String(), "\x00"), "\x00")
			want := append(strings.Fields(string(verb)), "rel", "oci://r.example.com/c", "--version", "1.2.3", "--namespace", "ns", "--create-namespace", "--reset-then-reuse-values")
			want = append(want, wantArgs...)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("layout %d, %s: the shell read other words than the ones described.\n got %q\nwant %q\n%s", layout, verb, got, want, cmd)
			}
		}
	}
}

// A word Helm would read as an option, or one that is missing, is refused: quoting cannot turn it into a value.
func TestAHelmCommandThatWouldNotDoWhatItSaysIsRefused(t *testing.T) {
	ok := Helm{Verb: Install, Release: "r", Chart: "./c.tgz", Namespace: "ns"}
	if _, err := ok.Render(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*Helm){
		"no release":             func(h *Helm) { h.Release = "" },
		"no chart":               func(h *Helm) { h.Chart = "" },
		"no namespace":           func(h *Helm) { h.Namespace = "" },
		"release is an option":   func(h *Helm) { h.Release = "--post-renderer=/x" },
		"chart is an option":     func(h *Helm) { h.Chart = "--kubeconfig=/x" },
		"namespace is a flag":    func(h *Helm) { h.Namespace = "-A" },
		"version is an option":   func(h *Helm) { h.Version = "--devel" },
		"empty key":              func(h *Helm) { h.Values = []Value{Set("", "x")} },
		"key holds =":            func(h *Helm) { h.Values = []Value{Set("a=b", "x")} },
		"key holds a space":      func(h *Helm) { h.Values = []Value{Set("a b", "x")} },
		"key holds a newline":    func(h *Helm) { h.Values = []Value{Set("a\nb", "x")} },
		"key begins with a dash": func(h *Helm) { h.Values = []Value{Set("-f", "x")} },
		"list name holds comma":  func(h *Helm) { h.Values = []Value{Strings("l", []string{"a,b"})} },
		"list name holds brace":  func(h *Helm) { h.Values = []Value{Strings("l", []string{"a}"})} },
		"list name holds slash":  func(h *Helm) { h.Values = []Value{Strings("l", []string{`a\b`})} },
		"list name is empty":     func(h *Helm) { h.Values = []Value{Strings("l", []string{"a", ""})} },
	} {
		h := ok
		mut(&h)
		if got, err := h.Render(); err == nil {
			t.Errorf("%s: rendered %q", name, got)
		}
	}
}

// Flags is the values of a command as a fragment for a caller that writes the rest, and each is one word to a real shell too.
func TestFlagsAreTheValuesOnOneLine(t *testing.T) {
	values := []Value{Set("a.b", "plain"), Set("c.d", ""), Set("e.f", "it's $(id)"), SetJSON("g", `{"x":"y z"}`)}
	if got, want := Flags(values), `--set a.b=plain --set c.d= --set 'e.f=it'\''s $(id)' --set-json g='{"x":"y z"}'`; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
	if Flags(nil) != "" {
		t.Fatal("no values, no fragment")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	var out bytes.Buffer
	sh := exec.Command(bash, "-c", "f() { printf '%s\\n' \"$@\"; }\nf "+Flags(values))
	sh.Stdout = &out
	if err := sh.Run(); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "--set\na.b=plain\n--set\nc.d=\n--set\ne.f=it's $(id)\n--set-json\ng={\"x\":\"y z\"}\n"; got != want {
		t.Fatalf("the shell read %q, want %q", got, want)
	}
}

// A value that is not a shell word is quoted whole, so an address or a chart reference from a setting can only ever be one argument
// (these cases are the ones the server's own test of its old flag writer held).
func TestAValueThatIsNotAShellWordIsQuotedWhole(t *testing.T) {
	for _, c := range []struct{ key, val, want string }{
		{"a.b", "host:4317", "--set a.b=host:4317"},
		{"a.b", "", "--set a.b="},
		{"a.b", "x; rm -rf /", "--set 'a.b=x; rm -rf /'"},
		{"a.b", "it's $(id)", `--set 'a.b=it'\''s $(id)'`},
		{"a.b", "a\nb", "--set 'a.b=a\nb'"},
	} {
		if got := Flags([]Value{Set(c.key, c.val)}); got != c.want {
			t.Errorf("Set(%q, %q) = %s, want %s", c.key, c.val, got, c.want)
		}
	}
}

// The kinds of value are written the way the commands always were: a string stays a string, a list is Helm's own, and an empty list is
// JSON because `{}` is a list holding one empty string.
func TestTheKindsOfValue(t *testing.T) {
	got := Flags([]Value{Set("a.on", "true"), SetString("a.name", "x y"), SetString("a.cleared", ""), Strings("a.list", []string{"p", "q"}), Strings("a.empty", nil)})
	want := `--set a.on=true --set-string a.name='x y' --set-string a.cleared= --set a.list='{p,q}' --set-json a.empty='[]'`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

// Continued puts each value on a line of its own after the head, which is how a short command that sets many things reads.
func TestContinuedLinesFollowAHead(t *testing.T) {
	if got, want := "helm x"+Continued([]Value{Set("a", "1"), SetString("b", "2")}), "helm x \\\n  --set a=1 \\\n  --set-string b=2"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if Continued(nil) != "" {
		t.Fatal("no values, nothing appended")
	}
}
