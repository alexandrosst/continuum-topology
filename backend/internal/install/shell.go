// Package install writes the commands a person pastes into a shell to put Ikhnos on a cluster: the helm command that installs or
// upgrades a chart, and (later) the Secret and kubectl lines that go with it.
//
// Nothing else in the server builds such a command. A command is described as a value (what to install, where, with which Helm
// values), and the one renderer in this package turns it into text. That is where quoting is decided, once: every word that came
// from anywhere but a constant is one shell word whatever it holds, so no value can end the command it is in or start another.
package install

import (
	"regexp"
	"strings"
)

// safe is what needs no quoting: it has no character a POSIX shell treats specially.
var safe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// Arg is s as one shell word, quoted only when it has to be, so a command whose words were already safe reads as it always did.
func Arg(s string) string {
	if safe.MatchString(s) {
		return s
	}
	return Quote(s)
}

// Quote wraps s in single quotes for a POSIX shell, closing and reopening the quotes around any single quote inside it.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
