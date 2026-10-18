package server

import (
	"regexp"

	"continuum/internal/install"
)

// teardownCommands is what the owner of a cluster runs there to remove an agent's install for good: helm uninstall drops
// the ServiceAccount, the RBAC and the workloads, but not the identity Secret (the chart marks it
// helm.sh/resource-policy: keep, so an accidental uninstall cannot orphan re-enrollment or need a new CA trust); the second
// command is only needed if the Secret itself should go too, e.g. before installing fresh in the same namespace. The
// Secret's own name never depends on the release name (every chart object has a fixed name), only on the namespace.
func TeardownCommands(namespace, release string) (helm, secret string, guessed bool, err error) {
	ns, name, guessed := ReleaseTarget(namespace, release)
	if helm, err = (install.Uninstall{Release: name, Namespace: ns}).Render(); err != nil {
		return "", "", guessed, err
	}
	if secret, err = (install.DeleteSecret{Name: "continuum-agent-identity", Namespace: ns}).Render(); err != nil {
		return "", "", guessed, err
	}
	return helm, secret, guessed, nil
}

// validKubeName is a DNS-1123 label, which is what both a namespace and a Helm release name must be.
var validKubeName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// releaseTarget is the namespace and Helm release name a command should target: the agent's own, as it reported in its
// last Hello, or a guess when either is not yet known (an agent older than these fields, or one that has never
// connected to this server). Every object the chart creates has a fixed name (see agent.name in _helpers.tpl) regardless
// of the release name, so only the two `helm` commands need it; `kubectl` commands only ever need the namespace. The
// guesses are the chart's own documentation defaults, not facts, so callers that show them to a person should say so.
func ReleaseTarget(namespace, release string) (ns, name string, guessed bool) {
	// What an agent reported about itself ends up inside a command a person pastes into a shell, so anything
	// that is not a plain Kubernetes / Helm name is treated as not reported at all.
	if !validKubeName.MatchString(namespace) {
		namespace = ""
	}
	if !validKubeName.MatchString(release) {
		release = ""
	}
	ns, name = namespace, release
	if ns == "" {
		ns, guessed = "continuum-system", true
	}
	if name == "" {
		name, guessed = "continuum-agent", true
	}
	return ns, name, guessed
}
