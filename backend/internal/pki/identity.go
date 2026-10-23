package pki

import (
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// A certificate this server issues says, in a URI name, what kind of holder it was issued to and whose it is:
//
//	spiffe://ikhnos/org/<organisation>/agent/<agent>
//	spiffe://ikhnos/org/<organisation>/operator/<operator>/receiver   (the server certificate of an operator's receiver)
//	spiffe://ikhnos/org/<organisation>/operator/<operator>/sender     (the client certificate a sender presents to it)
//
// The common name and organisation say the same, but they are free text and several kinds of holder share the one CA of a server that
// has legacy operators: the kind used to be worked out from how a name was spelled. With the URI the kind is stated, the server compares it
// with the name before it believes either, and a certificate of one kind is refused where another is expected.
//
// The form is a SPIFFE ID so that anything that already understands those (a mesh, a policy engine) reads it without a translation.
// The trust domain is fixed: it names this product's issuer, not a host, and does not change when the server is moved or renamed.

// TrustDomain is the host part of every identity URI.
const TrustDomain = "ikhnos"

// RequireIdentityURI refuses a certificate that has no identity URI. It is off while certificates issued before the URI existed are still
// in use (an agent's lasts AgentCertTTL, an operator's OperatorTLSTTL, a legacy operator's LegacyOperatorTLSTTL); it is turned on in the
// release after the one that first issues them.
var RequireIdentityURI = false

// HolderKind says what a certificate was issued to.
type HolderKind string

const (
	HolderAgent    HolderKind = "agent"
	HolderReceiver HolderKind = "operator-receiver"
	HolderSender   HolderKind = "operator-sender"
)

// Identity is what the URI of a certificate states.
type Identity struct {
	Kind HolderKind
	Org  string
	// ID is the agent's id, or the operator's.
	ID string
}

// URI is the identity as it is written into a certificate.
func (i Identity) URI() (*url.URL, error) {
	if i.Org == "" || i.ID == "" {
		return nil, errors.New("pki: an identity needs an organisation and an id")
	}
	if strings.ContainsAny(i.Org+i.ID, "/?#\x00") {
		return nil, errors.New("pki: an organisation or id may not contain / ? # or NUL")
	}
	var path string
	switch i.Kind {
	case HolderAgent:
		path = "/org/" + url.PathEscape(i.Org) + "/agent/" + url.PathEscape(i.ID)
	case HolderReceiver:
		path = "/org/" + url.PathEscape(i.Org) + "/operator/" + url.PathEscape(i.ID) + "/receiver"
	case HolderSender:
		path = "/org/" + url.PathEscape(i.Org) + "/operator/" + url.PathEscape(i.ID) + "/sender"
	default:
		return nil, fmt.Errorf("pki: unknown holder kind %q", i.Kind)
	}
	return &url.URL{Scheme: "spiffe", Host: TrustDomain, Path: path}, nil
}

// IdentityOf reads the identity a certificate states. present is false for a certificate with none (one issued before they existed). A
// certificate whose URI is not ours, is malformed, is not alone, or disagrees with the certificate's own name is an error: it is never
// read as "no identity", which would let a badly formed one through as a legacy certificate.
func IdentityOf(c *x509.Certificate) (id Identity, present bool, err error) {
	switch len(c.URIs) {
	case 0:
		return Identity{}, false, nil
	case 1:
	default:
		return Identity{}, true, errors.New("pki: the certificate has more than one URI name")
	}
	u := c.URIs[0]
	if u.Scheme != "spiffe" || u.Host != TrustDomain || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return Identity{}, true, errors.New("pki: the certificate's URI name is not one this server issues")
	}
	parts := strings.Split(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
	unesc := func(s string) (string, bool) {
		v, err := url.PathUnescape(s)
		return v, err == nil && v != "" && !strings.ContainsAny(v, "/\x00")
	}
	bad := errors.New("pki: the certificate's URI name is malformed")
	if len(parts) < 4 || parts[0] != "org" {
		return Identity{}, true, bad
	}
	org, ok := unesc(parts[1])
	if !ok {
		return Identity{}, true, bad
	}
	switch {
	case len(parts) == 4 && parts[2] == "agent":
		id.Kind = HolderAgent
	case len(parts) == 5 && parts[2] == "operator" && parts[4] == "receiver":
		id.Kind = HolderReceiver
	case len(parts) == 5 && parts[2] == "operator" && parts[4] == "sender":
		id.Kind = HolderSender
	default:
		return Identity{}, true, bad
	}
	hid, ok := unesc(parts[3])
	if !ok {
		return Identity{}, true, bad
	}
	id.Org, id.ID = org, hid

	// It must agree with what the certificate says in its name.
	if len(c.Subject.Organization) != 1 || c.Subject.Organization[0] != id.Org {
		return Identity{}, true, errors.New("pki: the URI name and the organisation of the certificate disagree")
	}
	cn := c.Subject.CommonName
	switch id.Kind {
	case HolderAgent, HolderReceiver:
		if cn != id.ID {
			return Identity{}, true, errors.New("pki: the URI name and the common name of the certificate disagree")
		}
	case HolderSender:
		if cn != id.ID+"-export" && !strings.HasPrefix(cn, id.ID+"-export-") {
			return Identity{}, true, errors.New("pki: the URI name and the common name of the certificate disagree")
		}
	}
	// And with what it may be used for.
	want := x509.ExtKeyUsageClientAuth
	if id.Kind == HolderReceiver {
		want = x509.ExtKeyUsageServerAuth
	}
	if len(c.ExtKeyUsage) != 1 || c.ExtKeyUsage[0] != want {
		return Identity{}, true, errors.New("pki: the URI name and the permitted use of the certificate disagree")
	}
	return id, true, nil
}

// Holder is IdentityOf for a caller that expects a kind: a certificate that states another kind is refused, and one that states nothing
// is refused when RequireIdentityURI is on and otherwise passes with present false (the caller then goes by the name, as before).
func Holder(c *x509.Certificate, want HolderKind) (id Identity, present bool, err error) {
	id, present, err = IdentityOf(c)
	switch {
	case err != nil:
		return Identity{}, present, err
	case !present && RequireIdentityURI:
		return Identity{}, false, errors.New("pki: the certificate states no identity")
	case present && id.Kind != want:
		return Identity{}, true, fmt.Errorf("pki: the certificate was issued to a %s, not a %s", id.Kind, want)
	}
	return id, present, nil
}
