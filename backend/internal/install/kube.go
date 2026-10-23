package install

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// kubeName is a Kubernetes object name (a DNS-1123 subdomain): what every namespace, Secret, Service, Deployment and Helm release
// is called. It cannot begin with a dash, so a name that passes cannot be read as an option, and it holds nothing a shell or YAML
// treats specially.
var kubeName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)

// secretKey is a key of a Secret's data.
var secretKey = regexp.MustCompile(`^[-._a-zA-Z0-9]{1,253}$`)

func checkNames(names ...[2]string) error {
	for _, n := range names {
		if !kubeName.MatchString(n[1]) {
			return fmt.Errorf("install: the %s %q is not a Kubernetes name", n[0], n[1])
		}
	}
	return nil
}

// Entry is one key of a Secret and its value.
type Entry struct{ Key, Value string }

// Secret describes the command that creates or updates a Secret. The data is applied from a manifest on standard input, never from
// --from-literal arguments: arguments are visible to every process on the machine and land in shell history, and a private key is
// among them.
type Secret struct {
	Name, Namespace string
	Entries         []Entry
	// EnsureNamespace puts "make sure the namespace exists" in front, for a Secret that is applied before the install that would create
	// it (`helm install --create-namespace` comes last, so on a fresh cluster the first Secret would otherwise fail with "namespace not
	// found"). Create-or-update, so it is harmless where the namespace already exists.
	EnsureNamespace bool
}

// Render is the command. It creates or updates (so a generated command can be run again: the person regenerates it, a first attempt
// failed half way, a certificate is re-issued) with a server-side apply, not the client-side `kubectl apply -f -`: the client-side one
// records the whole manifest in the kubectl.kubernetes.io/last-applied-configuration annotation, and for a Secret that manifest holds
// the private key in the clear, readable by anyone who may `get` or `describe` the Secret and by anything that logs or backs up
// objects with their annotations. --force-conflicts takes the fields over from whichever tool set them before (a Secret first created
// by a client-side apply, or by hand): without it, rotating the content of such a Secret is refused as a field-ownership conflict.
// The Secret is this product's own, so there is nothing of anyone else's to protect.
//
// The here-document is quoted, so the shell expands nothing inside it, and its closing word is chosen so that no line of the data can
// end it early.
func (s Secret) Render() (string, error) {
	if err := checkNames([2]string{"Secret", s.Name}, [2]string{"namespace", s.Namespace}); err != nil {
		return "", err
	}
	for _, e := range s.Entries {
		if !secretKey.MatchString(e.Key) {
			return "", fmt.Errorf("install: %q is not a key of a Secret", e.Key)
		}
	}
	delim := "IKHNOS_SECRET"
	for clash := true; clash; {
		clash = false
		for _, e := range s.Entries {
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
	if s.EnsureNamespace {
		fmt.Fprintf(&b, "kubectl create namespace %s --dry-run=client -o yaml | kubectl apply -f - && \\\n", s.Namespace)
	}
	fmt.Fprintf(&b, "kubectl apply --server-side --force-conflicts -f - <<'%s'\napiVersion: v1\nkind: Secret\nmetadata:\n  name: %s\n  namespace: %s\ntype: Opaque\nstringData:\n", delim, s.Name, s.Namespace)
	for _, e := range s.Entries {
		b.WriteString(e.yaml())
	}
	b.WriteString(delim)
	return b.String(), nil
}

// yaml writes one stringData key: a multi-line value (a PEM block) as a literal block, anything else as a double-quoted string (JSON
// quoting is valid YAML).
func (e Entry) yaml() string {
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

// Uninstall is `helm uninstall`: it removes a release's workloads and RBAC, not the Secrets the chart marks to be kept.
type Uninstall struct{ Release, Namespace string }

func (u Uninstall) Render() (string, error) {
	if err := checkNames([2]string{"release", u.Release}, [2]string{"namespace", u.Namespace}); err != nil {
		return "", err
	}
	return fmt.Sprintf("helm uninstall %s --namespace %s", u.Release, u.Namespace), nil
}

// DeleteSecret is `kubectl delete secret`.
type DeleteSecret struct{ Name, Namespace string }

func (d DeleteSecret) Render() (string, error) {
	if err := checkNames([2]string{"Secret", d.Name}, [2]string{"namespace", d.Namespace}); err != nil {
		return "", err
	}
	return fmt.Sprintf("kubectl delete secret %s --namespace %s", d.Name, d.Namespace), nil
}

// RestartDeployment is `kubectl rollout restart`: how pods pick up a Secret that is read into their environment.
type RestartDeployment struct{ Deployment, Namespace string }

func (r RestartDeployment) Render() (string, error) {
	if err := checkNames([2]string{"deployment", r.Deployment}, [2]string{"namespace", r.Namespace}); err != nil {
		return "", err
	}
	return fmt.Sprintf("kubectl rollout restart deployment/%s --namespace %s", r.Deployment, r.Namespace), nil
}

// ServiceType is what a Service is made: reachable inside the cluster only, on a port of every node, or behind a load balancer.
type ServiceType string

const (
	ClusterIP    ServiceType = "ClusterIP"
	NodePort     ServiceType = "NodePort"
	LoadBalancer ServiceType = "LoadBalancer"
)

// SetServiceType is the `kubectl patch` that changes the type of a Service.
type SetServiceType struct {
	Namespace, Service string
	Type               ServiceType
}

func (p SetServiceType) Render() (string, error) {
	if err := checkNames([2]string{"Service", p.Service}, [2]string{"namespace", p.Namespace}); err != nil {
		return "", err
	}
	switch p.Type {
	case ClusterIP, NodePort, LoadBalancer:
	default:
		return "", fmt.Errorf("install: %q is not a type of Service", p.Type)
	}
	return "kubectl --namespace " + p.Namespace + " patch svc " + p.Service + ` -p '{"spec":{"type":"` + string(p.Type) + `"}}'`, nil
}
