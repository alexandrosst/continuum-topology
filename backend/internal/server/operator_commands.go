package server

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Everything below builds text a person pastes into a shell. A value that came from anywhere but a constant (an
// address, a chart reference an organisation configured, a host from the request) is quoted, so it can only ever
// be one word of the command, and a private key is never an argument (arguments are visible to every process on the
// machine and land in shell history): Secrets are applied from a manifest on standard input.

// shellSafe is what needs no quoting: it has no character a POSIX shell treats specially.
var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// shellArg is s as one shell word, quoted only when it has to be, so the commands that were already safe read as before.
func shellArg(s string) string {
	if shellSafe.MatchString(s) {
		return s
	}
	return shellQuote(s)
}

// setFlag is one `--set key=value` Helm flag with the pair as a single, safely quoted word. An empty value is a
// deliberate "clear this", which Helm reads as an empty string.
func setFlag(key, value string) string { return "--set " + shellArg(key+"="+value) }

// secretEntry is one key of a Secret and its value.
type secretEntry struct{ Key, Value string }

func secretKV(key, value string) secretEntry { return secretEntry{key, value} }

// applySecretCommand creates or updates a Secret (so a generated command can be run again: the person regenerates
// it, a first attempt failed half way, a certificate is re-issued) from a manifest on standard input, never from
// --from-literal arguments. The here-document is quoted, so the shell expands nothing inside it, and its closing
// word is chosen so that no line of the data can end it early.
func applySecretCommand(name, namespace string, entries ...secretEntry) string {
	delim := "CONTINUUM_SECRET"
	for clash := true; clash; {
		clash = false
		for _, e := range entries {
			for _, line := range strings.Split(e.Value, "\n") {
				if strings.TrimSpace(line) == delim {
					clash = true
				}
			}
		}
		if clash {
			delim += "_X"
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "kubectl apply -f - <<'%s'\napiVersion: v1\nkind: Secret\nmetadata:\n  name: %s\n  namespace: %s\ntype: Opaque\nstringData:\n", delim, name, namespace)
	for _, e := range entries {
		b.WriteString(yamlEntry(e))
	}
	b.WriteString(delim)
	return b.String()
}

// yamlEntry writes one stringData key: a multi-line value (a PEM block) as a literal block, anything else as a
// double-quoted string (JSON quoting is valid YAML).
func yamlEntry(e secretEntry) string {
	v := strings.TrimRight(e.Value, "\n")
	if !strings.Contains(v, "\n") || strings.HasPrefix(v, " ") {
		q, _ := json.Marshal(e.Value)
		return fmt.Sprintf("  %s: %s\n", e.Key, q)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "  %s: |\n", e.Key)
	for _, line := range strings.Split(v, "\n") {
		b.WriteString("    " + line + "\n")
	}
	return b.String()
}

// withNamespace puts "make sure the namespace exists" in front of a Secret command that runs before the operator's own
// install does: `helm install --create-namespace` comes last, so on a fresh cluster the first Secret would otherwise
// fail with "namespace not found". Create-or-update, so it is harmless where the namespace already exists.
func withNamespace(namespace, cmd string) string {
	return fmt.Sprintf("kubectl create namespace %s --dry-run=client -o yaml | kubectl apply -f - && \\\n%s", namespace, cmd)
}

// shellQuote wraps s in single quotes for a POSIX shell, closing and reopening the quotes around any
// single quote inside it, so a name or a label value can never break out of the command it is pasted into.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
