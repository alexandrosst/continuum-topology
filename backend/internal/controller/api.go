// Package controller keeps one Ikhnos agent installed in the cluster it runs in, from an IkhnosAgent resource: the resource says what
// the agent is to be (the server it enrols with, its access level, its collectors and scope), the controller installs the agent chart
// that is embedded in the same release to match, upgrades it when the resource changes, and uninstalls it (and, when asked, deletes its
// identity) when the resource is deleted.
//
// It is an optional way to ask for what the Connect page's `helm install` asks for, and nothing else about Ikhnos depends on it. The
// server stays the only owner of what is approved and trusted; this controller never talks to it and holds no credential for it. Which
// Helm values a set of choices makes is install.AgentChoices, the same code the Connect page's command uses.
package controller

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"ikhnos/internal/install"
)

const (
	Group    = "ikhnos.io"
	Version  = "v1alpha1"
	Kind     = "IkhnosAgent"
	Resource = "ikhnosagents"

	// Finalizer holds a resource until the release it owns is uninstalled.
	Finalizer = "ikhnos.io/uninstall"
	// SkipUninstall on a resource being deleted lets it go without touching the release: the way out when an uninstall cannot finish.
	SkipUninstall = "ikhnos.io/skip-uninstall"

	// ReleaseName is the Helm release the agent is installed as. The agent chart's objects have fixed names, so there is one agent
	// per namespace and in practice one per cluster.
	ReleaseName = "ikhnos-agent"

	// DefaultTier is the access tier the chart gives an install that does not say.
	DefaultTier = 2
)

// GVR is how the resource is addressed.
var GVR = schema.GroupVersionResource{Group: Group, Version: Version, Resource: Resource}

// Spec is what an IkhnosAgent asks for. The field names are the resource's (and the CRD's schema is held to them by a test).
type Spec struct {
	Server     ServerSpec     `json:"server"`
	Enrollment EnrollmentSpec `json:"enrollment"`
	// AccessTier is the access ceiling to install with: 0, 1 or 2. Absent: the chart's default (2).
	AccessTier *int `json:"accessTier,omitempty"`
	// Collectors and Scope are the Connect page's choices of the same names.
	Collectors install.Collectors `json:"collectors"`
	Scope      install.Scope      `json:"scope"`
	// PurgeIdentityOnDelete makes deleting the resource also delete the agent's identity Secret, which an uninstall keeps by default.
	PurgeIdentityOnDelete bool       `json:"purgeIdentityOnDelete,omitempty"`
	Image                 *ImageSpec `json:"image,omitempty"`
	// Telemetry is what the agent collects and where it sends it: the Telemetry panel's settings, in a resource's words. It is the whole
	// of the agent's telemetry (what it does not say is the chart's default), so a resource without it is an agent without. A
	// controller installed without allowTelemetry refuses a resource that asks for any.
	Telemetry *install.ResourceTelemetry `json:"telemetry,omitempty"`
}

// ServerSpec is the server the agent enrols with: what the Connect page prints as server.address and the CA pin in enrollment.key.
type ServerSpec struct {
	// Address is host:port of the server's agent listener.
	Address string `json:"address"`
	// CAPin is the pin of the server's CA (sha256/<base64>): the agent trusts only a server whose CA it matches.
	CAPin string `json:"caPin"`
}

// EnrollmentSpec says where the one-time enrolment token is. The token is a Secret's, never the resource's own text.
type EnrollmentSpec struct {
	// TokenSecretRef names a Secret in the resource's namespace that holds the token the Connect page showed. It is needed until the
	// agent has enrolled; after that it may be removed.
	TokenSecretRef *SecretKeyRef `json:"tokenSecretRef,omitempty"`
}

// SecretKeyRef is a key of a Secret in the same namespace.
type SecretKeyRef struct {
	Name string `json:"name"`
	// Key is the key that holds the token. Default: token.
	Key string `json:"key,omitempty"`
}

// ImageSpec overrides where the agent's image comes from, as the Connect page's image values do.
type ImageSpec struct {
	Repository string `json:"repository,omitempty"`
	Tag        string `json:"tag,omitempty"`
	Digest     string `json:"digest,omitempty"`
}

// Problems say why the spec cannot be installed from, each worded for the person who wrote it.
func (s Spec) Problems() []string {
	var p []string
	if host, port, err := net.SplitHostPort(s.Server.Address); err != nil || host == "" {
		p = append(p, "spec.server.address must be host:port")
	} else if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		p = append(p, "spec.server.address must have a port between 1 and 65535")
	}
	if pin := s.Server.CAPin; pin == "" || strings.ContainsAny(pin, ". \t\r\n") {
		p = append(p, "spec.server.caPin is the server CA's pin (sha256/...); it has no dots or spaces")
	}
	if s.AccessTier != nil && (*s.AccessTier < 0 || *s.AccessTier > 2) {
		p = append(p, "spec.accessTier must be 0, 1 or 2")
	}
	if r := s.Enrollment.TokenSecretRef; r != nil && r.Name == "" {
		p = append(p, "spec.enrollment.tokenSecretRef.name is empty")
	}
	if _, err := install.AgentChoices(s.tier(), s.Collectors, s.Scope, s.PurgeIdentityOnDelete); err != nil {
		if pe, ok := err.(*install.ProblemsError); ok {
			p = append(p, pe.Problems...)
		} else {
			p = append(p, err.Error())
		}
	}
	if s.Telemetry != nil {
		for _, m := range s.Telemetry.Problems(s.tier(), s.Collectors.Measurements) {
			p = append(p, "spec.telemetry: "+m)
		}
	}
	return p
}

// AsksForTelemetry is whether the resource turns any telemetry signal on.
func (s Spec) AsksForTelemetry() bool {
	return s.Telemetry != nil && s.Telemetry.Signals != (install.ResourceSignals{})
}

func (s Spec) tier() int {
	if s.AccessTier != nil {
		return *s.AccessTier
	}
	return DefaultTier
}

// TokenKey is the key of the token Secret that holds the token.
func (r SecretKeyRef) TokenKey() string {
	if r.Key != "" {
		return r.Key
	}
	return "token"
}

// ValidToken says whether a token can be given to the chart: one word, with no dot (the chart splits "<pin>.<token>" at it) and nothing
// that is white space or a control character.
func ValidToken(t string) error {
	if t == "" || strings.ContainsAny(t, ". \t\r\n") || strings.ContainsFunc(t, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return fmt.Errorf("the token is empty, or holds a dot, a space or a control character")
	}
	return nil
}

// Values are the Helm values the agent chart is installed with for this spec and token (the token is empty once the agent has
// enrolled and the Secret is gone: the chart then has none, as its own comment says it may). The spec is checked first.
func (s Spec) Values(token string) (map[string]any, error) {
	if p := s.Problems(); len(p) > 0 {
		return nil, &install.ProblemsError{Problems: p}
	}
	vs := []install.Value{install.Set("server.address", s.Server.Address), install.SetString("server.caPin", s.Server.CAPin)}
	if token != "" {
		if err := ValidToken(token); err != nil {
			return nil, &install.ProblemsError{Problems: []string{err.Error()}}
		}
		vs = append(vs, install.SetString("enrollment.token", token))
	}
	if s.AccessTier != nil {
		vs = append(vs, install.Set("access.tier", strconv.Itoa(*s.AccessTier)))
	}
	if im := s.Image; im != nil {
		for _, kv := range [][2]string{{"image.repository", im.Repository}, {"image.tag", im.Tag}, {"image.digest", im.Digest}} {
			if kv[1] != "" {
				vs = append(vs, install.SetString(kv[0], kv[1]))
			}
		}
	}
	choices, err := install.AgentChoices(s.tier(), s.Collectors, s.Scope, s.PurgeIdentityOnDelete)
	if err != nil {
		return nil, err
	}
	vs = append(vs, choices...)
	if s.Telemetry != nil {
		tv, err := s.Telemetry.Values(s.tier(), s.Collectors.Measurements)
		if err != nil {
			return nil, err
		}
		vs = append(vs, tv...)
	}
	return install.HelmValues(vs)
}
