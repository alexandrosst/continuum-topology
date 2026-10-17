package install

import (
	"regexp"
	"strings"
)

// CredentialVar is the environment variable a destination's credential is read from by the command that creates its Secret. The page's own
// hint and the command can never disagree about it, because both come from here.
const CredentialVar = "TELEMETRY_EXPORT_TOKEN"

// CredentialSecret is a Secret that holds a destination's credential, and the command that creates it.
type CredentialSecret struct {
	// Name is the Secret's name, and Variable the environment variable its credential is read from.
	Name     string
	Variable string
	// Lanes are the kinds of signal whose destination reads this Secret (none for the single destination).
	Lanes   []Modality
	Command string
}

// namespaceOr is the namespace a command targets: the given one when it is a plain Kubernetes name, otherwise the chart's default. It
// comes from what the agent reported and ends up in a shell line, so anything else is dropped and not pasted.
func namespaceOr(ns string) string {
	if plainName.MatchString(ns) {
		return ns
	}
	return DefaultNamespace
}

// CredentialSecrets are the Secrets the telemetry's credential flags point at, each with the command that creates it, to run just before
// the upgrade: one for the single destination, or, sending each kind of signal separately, one per Secret name the routes use. Two routes
// that name the same Secret share it (one command, one variable): that is how one credential serves a backend that takes metrics and
// logs. A route that needs a different credential under the same Secret name is a different key in it, read from its own variable.
//
// The credential itself never passes through the page or the server: the command reads it from a variable the person sets in their own
// shell, and `:?` makes the shell stop with a message instead of creating an empty Secret when it is unset. Create-or-update, so that
// running it again does not fail on the Secret already existing. Nothing is returned for telemetry that has no command (none on, or
// something wrong with it), and a name or key that is not a plain Kubernetes name is never pasted into a shell line.
func (t Telemetry) CredentialSecrets(namespace string) []CredentialSecret {
	if !t.Active() || len(t.Problems()) > 0 {
		return nil
	}
	ns := namespaceOr(namespace)
	type use struct {
		lane Modality // empty for the single destination
		name string
		key  string
	}
	var uses []use
	if t.Routes.Split {
		for _, m := range t.ActiveLanes() {
			uses = append(uses, use{m, strings.TrimSpace(t.Routes.lane(m).AuthSecret), keyOrToken(t.Routes.lane(m).AuthKey)})
		}
	} else {
		uses = []use{{"", strings.TrimSpace(t.Destination.AuthSecret), keyOrToken(t.Destination.AuthKey)}}
	}
	type entry struct {
		key, variable string
		lanes         []Modality
	}
	byName := map[string][]*entry{}
	var order []string
	for _, u := range uses {
		if u.name == "" || !secretName.MatchString(u.name) || !secretKeyName.MatchString(u.key) {
			continue
		}
		if _, seen := byName[u.name]; !seen {
			order = append(order, u.name)
		}
		var same *entry
		for _, e := range byName[u.name] {
			if e.key == u.key {
				same = e
			}
		}
		switch {
		case same != nil:
			if u.lane != "" {
				same.lanes = append(same.lanes, u.lane)
			}
		default:
			e := &entry{key: u.key, variable: CredentialVar}
			if u.lane != "" {
				e.variable = CredentialVar + "_" + strings.ToUpper(string(u.lane))
				e.lanes = []Modality{u.lane}
			}
			byName[u.name] = append(byName[u.name], e)
		}
	}
	var out []CredentialSecret
	for _, name := range order {
		entries := byName[name]
		var literals []string
		var lanes []Modality
		for _, e := range entries {
			literals = append(literals, "--from-literal="+Arg(e.key)+`="${`+e.variable+":?set "+e.variable+` to the credential first}"`)
			lanes = append(lanes, e.lanes...)
		}
		out = append(out, CredentialSecret{
			Name: name, Variable: entries[0].variable, Lanes: lanes,
			Command: "kubectl create secret generic " + Arg(name) + " --namespace " + Arg(ns) + " " + strings.Join(literals, " ") + " --dry-run=client -o yaml | kubectl apply -f -",
		})
	}
	return out
}

func keyOrToken(key string) string {
	if key = strings.TrimSpace(key); key != "" {
		return key
	}
	return "token"
}

// CredentialCommand is the commands that create the credential Secrets as one block to paste, or empty when there is nothing to create.
func (t Telemetry) CredentialCommand(namespace string) string {
	secrets := t.CredentialSecrets(namespace)
	cmds := make([]string, len(secrets))
	for i, s := range secrets {
		cmds[i] = s.Command
	}
	return Chain(cmds)
}

var heredoc = regexp.MustCompile(`<<-?\s*['"]?[A-Za-z_]\w*['"]?`)

// Chain joins shell commands into one block to paste, each running only if the one before it worked (a failed Secret stops the upgrade that
// needs it).
//
// A command that ends in a here-document (`kubectl apply -f - <<'X' ... X`) cannot simply be followed by ` && \`: the closing word must be
// alone on its line, or the shell never sees the document end and waits at its `>` prompt for more. Such a command is wrapped in braces
// instead, and the `&&` follows the closing brace. Only the commands before the last need it.
func Chain(commands []string) string {
	last := len(commands) - 1
	parts := make([]string, len(commands))
	for i, c := range commands {
		if i < last && heredoc.MatchString(c) {
			c = "{ " + c + "\n}"
		}
		parts[i] = c
	}
	return strings.Join(parts, " && \\\n")
}
