package install

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// The promise of Render is that each of the release, the chart, the version, the namespace and every key and value is one shell word,
// "so a hostile value changes what is set and nothing else". This holds it to that with a real shell: the command is run with `helm`
// defined as a function that prints the words it was given, and the words must be the ones the description names, whatever the strings
// hold. The seeds run with every `go test`; `go test -fuzz=FuzzRenderedCommandIsTheWordsItDescribes` looks for more. It needs `sh`.
func FuzzRenderedCommandIsTheWordsItDescribes(f *testing.F) {
	f.Add("ikhnos", "oci://ghcr.io/x/chart", "0.1.0", "ns", "image.tag", "v1", "a,b", false, uint8(0))
	f.Add("r", "c", "", "ns", "a.b", "it's", "x y", true, uint8(1))
	f.Add("r", "c", "1", "ns", "k", "$(touch /tmp/pwned)", "`id`", false, uint8(2))
	f.Add("r", "c", "1", "ns", "k", "a\nb\\\n--post-renderer=x", "'; echo hi; '", true, uint8(3))
	f.Add("r", "c", "1", "ns", "k", "~root", "*", false, uint8(1))
	f.Add("r", "c", "1", "ns", "k", "", "{\"a\":\"b c\"}", false, uint8(2))
	f.Fuzz(func(t *testing.T, release, chart, version, ns, key, value, item string, create bool, mode uint8) {
		sh, err := exec.LookPath("sh")
		if err != nil {
			t.Skip("sh is not installed")
		}
		for _, s := range []string{release, chart, version, ns, key, value, item} {
			if strings.ContainsRune(s, 0) {
				t.Skip("a NUL cannot be in a shell word")
			}
		}
		h := Helm{Verb: "upgrade", Release: release, Chart: chart, Version: version, Namespace: ns, CreateNamespace: create, ResetThenReuse: mode&4 != 0, Layout: Layout(mode & 1)}
		want := []string{"upgrade", release, chart}
		if version != "" {
			want = append(want, "--version", version)
		}
		want = append(want, "--namespace", ns)
		if create {
			want = append(want, "--create-namespace")
		}
		if h.ResetThenReuse {
			want = append(want, "--reset-then-reuse-values")
		}
		h.Values = []Value{Set(key, value), SetString(key, value), SetJSON(key, value), Strings(key, []string{item, value})}
		want = append(want, "--set", key+"="+value, "--set-string", key+"="+value, "--set-json", key+"="+value)
		if item != "" && value != "" && !strings.ContainsAny(item+value, ",{}\\") {
			want = append(want, "--set", key+"={"+item+","+value+"}")
		} else {
			h.Values = h.Values[:3] // a name Helm would read as syntax is refused, which Check says
		}
		cmd, err := h.Render()
		if err != nil {
			return // Check refused it: no command was written, and that is the right answer for a word Helm would read as an option
		}
		out, err := exec.Command(sh, "-c", "helm() { printf '%s\\0' \"$@\"; }\n"+cmd).Output()
		if err != nil {
			t.Fatalf("the shell refused the command: %v\n%s", err, cmd)
		}
		got := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
		if !slices.Equal(got, want) {
			t.Fatalf("the shell read %q, the description says %q\ncommand:\n%s", got, want, cmd)
		}
	})
}
