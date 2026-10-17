package install

import (
	"fmt"
	"strings"
	"unicode"
)

// Verb is what helm is asked to do with the release.
type Verb string

const (
	// Install is `helm install`: the release is new.
	Install Verb = "install"
	// Upgrade is `helm upgrade`: the release runs and is changed.
	Upgrade Verb = "upgrade"
	// UpgradeInstall is `helm upgrade --install`: change the release, or create it when it is not there.
	UpgradeInstall Verb = "upgrade --install"
)

// Layout is how the command is written. The words and their order are the same in both.
type Layout int

const (
	// OneLine is the whole command on one line: what is pasted to change one or two values.
	OneLine Layout = iota
	// Wrapped has the chart and release on the first line, the namespace flags on the second and each Helm value on a line of its
	// own, joined by line continuations: what is pasted to install, and read before it is run.
	Wrapped
)

// Value is one Helm value set on the command line.
type Value struct {
	Key   string
	Value string
	// JSON is a value that is a JSON document (`--set-json`), written with the document in quotes and the key outside them.
	JSON bool
}

// Set is a Helm value set to a string (`--set key=value`). An empty value is a deliberate "clear this", which Helm reads as an empty
// string.
func Set(key, value string) Value { return Value{Key: key, Value: value} }

// SetJSON is a Helm value set to a JSON document (`--set-json key='{...}'`).
func SetJSON(key, document string) Value { return Value{Key: key, Value: document, JSON: true} }

// Helm describes one helm command.
type Helm struct {
	Verb    Verb
	Release string
	// Chart is where the chart is: a file, a directory, an OCI reference or a repository chart name. Version is the chart version
	// to fetch; it is empty for a file, which is already one version.
	Chart   string
	Version string

	Namespace       string
	CreateNamespace bool
	// ResetThenReuse starts from the new chart's defaults and puts the values the release was installed with on top of them
	// (--reset-then-reuse-values, Helm 3.14 or newer). It is what every upgrade the server prints uses, never --reuse-values,
	// which replaces the new chart's defaults with the old release's values and so leaves a value block the chart added later missing.
	ResetThenReuse bool

	Values []Value
	Layout Layout
}

// Check says why h cannot be written as a command that does what it describes. Quoting makes every word one word, but it cannot
// make a word that Helm reads as an option into a value: a release, chart, version or namespace that begins with a dash would be
// taken for a flag (`--post-renderer=...`), and one that is empty would shift the words after it. A value may hold anything; a key
// may not be empty, hold "=" or white space (which would end it early), or begin with a dash.
func (h Helm) Check() error {
	for _, w := range []struct{ what, v string }{{"release", h.Release}, {"chart", h.Chart}, {"namespace", h.Namespace}} {
		if w.v == "" {
			return fmt.Errorf("install: the %s is empty", w.what)
		}
		if strings.HasPrefix(w.v, "-") {
			return fmt.Errorf("install: the %s %q begins with a dash, which Helm would read as an option", w.what, w.v)
		}
	}
	if strings.HasPrefix(h.Version, "-") {
		return fmt.Errorf("install: the version %q begins with a dash, which Helm would read as an option", h.Version)
	}
	for _, v := range h.Values {
		if v.Key == "" || strings.HasPrefix(v.Key, "-") || strings.ContainsFunc(v.Key, func(r rune) bool { return r == '=' || unicode.IsSpace(r) || unicode.IsControl(r) }) {
			return fmt.Errorf("install: %q is not a Helm value name", v.Key)
		}
	}
	return nil
}

// Render is the command as text, or the reason (Check) there is no command to write. For a command that passes Check, each of the
// release, the chart, the version, the namespace and the keys and values is one shell word, so a hostile value changes what is set
// and nothing else.
func (h Helm) Render() (string, error) {
	if err := h.Check(); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("helm " + string(h.Verb) + " " + Arg(h.Release) + " " + Arg(h.Chart))
	if h.Version != "" {
		b.WriteString(" --version " + Arg(h.Version))
	}
	// Wrapped, every flag after the chart is on a line of its own, after a line continuation.
	sep := " "
	if h.Layout == Wrapped {
		sep = " \\\n  "
	}
	b.WriteString(sep + "--namespace " + Arg(h.Namespace))
	if h.CreateNamespace {
		b.WriteString(" --create-namespace")
	}
	if h.ResetThenReuse {
		b.WriteString(" --reset-then-reuse-values")
	}
	for _, v := range h.Values {
		b.WriteString(sep + v.render())
	}
	return b.String(), nil
}

func (v Value) render() string {
	if v.JSON {
		return "--set-json " + Arg(v.Key) + "=" + Quote(v.Value)
	}
	return "--set " + Arg(v.Key+"="+v.Value)
}
