package install

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// What the Connect page's controller steps are called. They are the names the controller chart's own notes use, so a person who reads
// those and these is reading one set.
const (
	// ControllerRelease is the Helm release of the controller; its namespace is the agent's (DefaultNamespace), because the controller
	// watches only its own namespace and installs the agent there.
	ControllerRelease = "ikhnos-controller"
	// ControllerResourceName is what the IkhnosAgent resource is called: there is one agent a cluster.
	ControllerResourceName = "this-cluster"
	// ControllerTokenSecret is the Secret that holds the one-time token, and ControllerTokenKey the key of it that does.
	ControllerTokenSecret = "ikhnos-enrollment"
	ControllerTokenKey    = "token"

	// ResourceAPIVersion and ResourceKind are the resource's own (the controller package holds the same two and a test holds them equal).
	ResourceAPIVersion = "ikhnos.io/v1alpha1"
	ResourceKind       = "IkhnosAgent"
)

// ResourceImage is the agent image an IkhnosAgent names, as the Connect page's command names it with image.repository, image.tag and
// image.digest.
type ResourceImage struct{ Repository, Tag, Digest string }

// AgentResource is an IkhnosAgent resource and the `kubectl apply` that creates it: the same choices the Connect page's `helm install`
// makes, written as a resource. The token is not in it. It is a Secret's (the resource only names the Secret), so the manifest can be
// kept in a repository.
type AgentResource struct {
	// Server is host:port of the agent listener, and CAPin the pin of the server's CA.
	Server, CAPin string
	// Tier is the access ceiling; the chart's default (2) is left unsaid, as the helm command leaves it.
	Tier int
	// Collectors, Scope and PurgeIdentity are the choices of the same names.
	Collectors    Collectors
	Scope         Scope
	PurgeIdentity bool
	// Image is nil when the chart's own image names apply.
	Image *ResourceImage
}

// yamlText is s as a YAML scalar: JSON quoting is valid YAML, and holds nothing a YAML parser could read as anything but the text.
func yamlText(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func yamlList(items []string) string {
	b, _ := json.Marshal(items)
	return string(b)
}

// Check says why a resource cannot be written for these choices: what the controller would refuse it for.
func (a AgentResource) Check() error {
	if host, port, err := net.SplitHostPort(a.Server); err != nil || host == "" {
		return fmt.Errorf("install: the server address %q is not host:port", a.Server)
	} else if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("install: the server address %q has no port between 1 and 65535", a.Server)
	}
	if a.CAPin == "" || strings.ContainsAny(a.CAPin, ". \t\r\n") {
		return fmt.Errorf("install: the CA pin %q is empty or holds a dot or a space", a.CAPin)
	}
	if a.Tier < 0 || a.Tier > 2 {
		return fmt.Errorf("install: the access tier %d is not 0, 1 or 2", a.Tier)
	}
	if _, err := AgentChoices(a.Tier, a.Collectors, a.Scope, a.PurgeIdentity); err != nil {
		return err
	}
	return nil
}

// Manifest is the resource as YAML.
func (a AgentResource) Manifest(namespace string) (string, error) {
	if err := a.Check(); err != nil {
		return "", err
	}
	if err := checkNames([2]string{"namespace", namespace}); err != nil {
		return "", err
	}
	var b strings.Builder
	line := func(indent int, format string, args ...any) {
		b.WriteString(strings.Repeat("  ", indent) + fmt.Sprintf(format, args...) + "\n")
	}
	line(0, "apiVersion: %s", ResourceAPIVersion)
	line(0, "kind: %s", ResourceKind)
	line(0, "metadata:")
	line(1, "name: %s", ControllerResourceName)
	line(1, "namespace: %s", namespace)
	line(0, "spec:")
	line(1, "server:")
	line(2, "address: %s", yamlText(a.Server))
	line(2, "caPin: %s", yamlText(a.CAPin))
	line(1, "enrollment:")
	line(2, "tokenSecretRef:")
	line(3, "name: %s", ControllerTokenSecret)
	if a.Tier != 2 {
		line(1, "accessTier: %d", a.Tier)
	}
	if c := a.Collectors; c.NodeProbe || c.FlowObserver || c.Measurements {
		line(1, "collectors:")
		for _, f := range []struct {
			on   bool
			name string
		}{{c.NodeProbe, "nodeProbe"}, {c.FlowObserver, "flowObserver"}, {c.Measurements, "measurements"}} {
			if f.on {
				line(2, "%s: true", f.name)
			}
		}
	}
	if s := a.Scope; len(s.Namespaces) > 0 || len(s.Exclude) > 0 || s.Selector != "" {
		line(1, "scope:")
		if len(s.Namespaces) > 0 {
			line(2, "namespaces: %s", yamlList(s.Namespaces))
		}
		if len(s.Exclude) > 0 {
			line(2, "exclude: %s", yamlList(s.Exclude))
		}
		if s.Selector != "" {
			line(2, "selector: %s", yamlText(s.Selector))
		}
	}
	if a.PurgeIdentity {
		line(1, "purgeIdentityOnDelete: true")
	}
	if im := a.Image; im != nil && (im.Repository != "" || im.Tag != "" || im.Digest != "") {
		line(1, "image:")
		for _, kv := range [][2]string{{"repository", im.Repository}, {"tag", im.Tag}, {"digest", im.Digest}} {
			if kv[1] != "" {
				line(2, "%s: %s", kv[0], yamlText(kv[1]))
			}
		}
	}
	return b.String(), nil
}

// Apply is the command that creates (or updates) the resource. The here-document is quoted, so the shell expands nothing in it.
func (a AgentResource) Apply(namespace string) (string, error) {
	m, err := a.Manifest(namespace)
	if err != nil {
		return "", err
	}
	return "kubectl apply -f - <<'YAML'\n" + m + "YAML", nil
}

// ControllerInstall is the `helm install` of the controller: the chart it is installed from, the access tier it is given permission for
// (and no more, because Kubernetes lets nobody grant what they do not hold), and the image registry the controller and the agents it
// installs pull from.
type ControllerInstall struct {
	Chart, Version string
	// MaxTier is the highest access tier the controller may install an agent at, and the most its permissions cover.
	MaxTier int
	// Registry is the image registry (the one place the controller's and the agents' images are), or "" for the chart's own names.
	Registry string
}

// Render is the command.
func (c ControllerInstall) Render() (string, error) {
	if c.MaxTier < 0 || c.MaxTier > 2 {
		return "", fmt.Errorf("install: the access tier %d is not 0, 1 or 2", c.MaxTier)
	}
	values := []Value{Set("maxAccessTier", strconv.Itoa(c.MaxTier))}
	if r := strings.TrimRight(c.Registry, "/"); r != "" {
		values = append(values,
			Set("image.repository", r+"/controller"),
			Set("agent.image.repository", r+"/ikhnos"),
			Set("agent.linkImage.repository", r+"/link"))
	}
	return Helm{Verb: Install, Release: ControllerRelease, Chart: c.Chart, Version: c.Version, Namespace: DefaultNamespace, CreateNamespace: true,
		Values: values, Layout: Wrapped}.Render()
}

// ControllerTokenSecretCommand is the command that creates the Secret the resource names, holding the token. The token is read from the
// manifest on standard input, never from an argument (see Secret).
func ControllerTokenSecretCommand(token string) (string, error) {
	return Secret{Name: ControllerTokenSecret, Namespace: DefaultNamespace, Entries: []Entry{{Key: ControllerTokenKey, Value: token}}}.Render()
}
