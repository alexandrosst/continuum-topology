package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"continuum/internal/pki"
	"continuum/internal/store"
)

// OperatorTLSBundle is the mTLS material minted once, alongside the receiver bearer token, when a
// regional operator is created: a server certificate for its own OTLP receiver, a client certificate
// every one of its source clusters presents when exporting into it, and the certificate of the operator's
// OWN private CA (CACertPEM) which signed both and which both sides need to verify the other
// (client_ca_file on the receiver, ca_file on the exporter). The certificates and their keys are not
// stored server-side; the operator's CA certificate and its sealed key are (store.Operator) - see
// pki.CA.NewOperatorCA, pki.IssueOperatorReceiverTLS and pki.IssueOperatorClientTLS.
type OperatorTLSBundle struct {
	ReceiverCertPEM, ReceiverKeyPEM []byte
	CACertPEM                       []byte
	// Senders are the client certificates minted for this operator's source clusters, one each, in the order the
	// clusters are listed: a cluster holds its own key and the ledger (store.OperatorCert) says which certificate went where.
	Senders []SenderCert
}

// SenderCert is the client certificate and key minted for one sender of an operator.
type SenderCert struct {
	Sender          string
	CertPEM, KeyPEM []byte
}

// forSender is the certificate minted for sender, ok false when none was.
func (b OperatorTLSBundle) forSender(sender string) (SenderCert, bool) {
	for _, s := range b.Senders {
		if s.Sender == sender {
			return s, true
		}
	}
	return SenderCert{}, false
}

// mintOperatorTLS gives a new operator its own private CA and issues, from THAT CA, the receiver server
// certificate. Its source clusters' client certificates are minted afterwards, one each (mintSenderCerts). bundle.CACertPEM is the operator CA's
// certificate (never the org CA's); caKeyPEM is the CA's private key sealed like the org CA key, for the
// caller to store - it is not part of the bundle so nothing that renders a bundle can leak it. A variable
// only so a test can make the mint fail - the one path (it cannot fail in practice) on which CreateOperator
// must fall back to a bearer-token operator.
var mintOperatorTLS = func(c *Core, operatorID string, hosts []string) (bundle OperatorTLSBundle, caKeyPEM []byte, err error) {
	issuer, caCertPEM, caKeyPEM, err := c.CA.NewOperatorCA(operatorID, c.OrgID)
	if err != nil {
		return OperatorTLSBundle{}, nil, err
	}
	bundle.CACertPEM = caCertPEM
	if bundle.ReceiverCertPEM, bundle.ReceiverKeyPEM, err = issuer.IssueOperatorReceiverTLS(operatorID, c.OrgID, hosts); err != nil {
		return OperatorTLSBundle{}, nil, err
	}
	return bundle, caKeyPEM, nil
}

// operatorReceiverHosts are the names an operator's receiver certificate carries: its Service in every spelling,
// and the stable name (the first group) that senders verify it by whatever address they actually dial.
func operatorReceiverHosts(id string) []string {
	svc := operatorServiceName(id)
	return []string{id + ".continuum-system", id + ".continuum-system.svc", id + ".continuum-system.svc.cluster.local",
		svc + ".continuum-system", svc + ".continuum-system.svc", svc + ".continuum-system.svc.cluster.local"}
}

// certNotAfter is the expiry of a certificate just issued, nil when it cannot be read (nothing is then recorded and
// the server falls back to working the date out from the creation time).
func certNotAfter(certPEM []byte) *time.Time {
	t, err := pki.NotAfter(certPEM)
	if err != nil {
		return nil
	}
	return &t
}

// maxOperatorName mirrors the enrollment token's own label limit (see CreateTokenFor) - both name the
// same kind of thing (a cluster, or here a fleet of them) for a person to recognise later.
const maxOperatorName = 80

// operatorInOrg finds a regional operator of this organisation. One belonging to another organisation is
// reported as not existing, the same convention agentInOrg uses.
func (c *Core) operatorInOrg(ctx context.Context, id string) (store.Operator, error) {
	op, err := c.Store.GetOperator(ctx, id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && op.OrgID != c.OrgID) {
		return store.Operator{}, errf(KindNotFound, "no such regional operator")
	}
	return op, err
}

// What an external destination may hold. Each value ends up in a shell command and a Helm value, so the
// character sets are the ones those names can really have rather than "anything but a quote": an endpoint is a
// host:port or an http(s) URL, a CA file is a path, the three auth fields are an HTTP header name and a Kubernetes
// Secret name and key.
var (
	destEndpointRe   = regexp.MustCompile(`^[A-Za-z0-9\[][A-Za-z0-9._~:/\[\]%+=-]{0,510}$`)
	destCAFileRe     = regexp.MustCompile(`^[A-Za-z0-9._~/+=:-]{1,255}$`)
	destHeaderNameRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	destSecretNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)
	destSecretKeyRe  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,253}$`)
)

// validExternalDestination checks the parts of an external destination that reach a command line.
func validExternalDestination(dest store.Destination) error {
	if !destEndpointRe.MatchString(dest.Endpoint) {
		return errf(KindInvalid, "destination.endpoint is a host and a port (otlp.example.com:4317) or an http(s) URL, without spaces, quotes or other shell characters")
	}
	if dest.CAFile != "" && !destCAFileRe.MatchString(dest.CAFile) {
		return errf(KindInvalid, "destination.caFile is a file path made of letters, digits and . _ ~ / + = : -")
	}
	if dest.AuthHeaderName != "" && !destHeaderNameRe.MatchString(dest.AuthHeaderName) {
		return errf(KindInvalid, "destination.authHeaderName is an HTTP header name (letters, digits and -)")
	}
	if dest.AuthSecretName != "" && !destSecretNameRe.MatchString(dest.AuthSecretName) {
		return errf(KindInvalid, "destination.authSecretName is a Kubernetes Secret name (lowercase letters, digits, - and .)")
	}
	if dest.AuthSecretKey != "" && !destSecretKeyRe.MatchString(dest.AuthSecretKey) {
		return errf(KindInvalid, "destination.authSecretKey is a Secret key (letters, digits, . _ and -)")
	}
	return nil
}

// validateDestination checks that a Destination is well-formed and, for DestinationOperator, that it
// names a currently active regional operator in this organisation. Ikhnos's fleet is two tiers: an
// agent's own telemetry intent, or a regional operator's own export, may point directly AT one regional
// operator, but nothing here builds live reparenting or operator-to-operator chaining beyond that single
// hop - the target operator's own Destination (if it is itself "operator") is not walked or re-validated
// here.
func (c *Core) validateDestination(ctx context.Context, dest store.Destination) error {
	switch dest.Kind {
	case store.DestinationExternal:
		if strings.TrimSpace(dest.Endpoint) == "" {
			return errf(KindInvalid, "destination.endpoint is required")
		}
		return validExternalDestination(dest)
	case store.DestinationOperator:
		if strings.TrimSpace(dest.TargetOperatorID) == "" {
			return errf(KindInvalid, "destination.targetOperatorId is required")
		}
		op, err := c.operatorInOrg(ctx, dest.TargetOperatorID)
		if err != nil {
			return err
		}
		if op.Status != store.OperatorActive {
			return errf(KindInvalid, "operator %q is not active", dest.TargetOperatorID)
		}
		return nil
	default:
		return errf(KindInvalid, "destination.kind must be %q or %q", store.DestinationExternal, store.DestinationOperator)
	}
}

// operatorDestination validates the destination of a regional operator: another operator (the central one, or
// any other) or an external backend. A "fusion" destination is the central operator's own, set by the server when
// FUSION is turned on (see EnsureCentralOperator) - nobody creates one by hand, because FUSION is part of the server
// and reached through its central operator.
func (c *Core) operatorDestination(ctx context.Context, dest store.Destination) (store.Destination, error) {
	if dest.Kind == store.DestinationFusion {
		return dest, errf(KindInvalid, "FUSION is reached through the central operator: choose it as the destination (kind %q), or turn FUSION on in the server's Settings", store.DestinationOperator)
	}
	return dest, c.validateDestination(ctx, dest)
}

// validModalities checks every value is a known telemetry modality. An empty list is always valid - see
// Operator.AcceptedModalities's own doc comment for what that means.
func validModalities(ms []store.Modality) error {
	for _, m := range ms {
		switch m {
		case store.ModalityMetrics, store.ModalityLogs, store.ModalityTraces:
		default:
			return errf(KindInvalid, "%q is not a telemetry modality (metrics, logs or traces)", m)
		}
	}
	return nil
}

// maxOperatorLabels bounds the labels one operator may carry: each is stamped on everything it forwards, so
// each costs storage and cardinality downstream. The agent's own tags are capped the same way.
const maxOperatorLabels = 8

var operatorLabelKey = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,62}$`)

// validOperatorLabels trims and checks an operator's labels: at most maxOperatorLabels, each key once, a key
// an OpenTelemetry attribute can carry, never the reserved continuum. prefix (the operator's own id and
// name, and the agent's provenance, live there), and a short printable value.
func validOperatorLabels(in []store.OperatorLabel) ([]store.OperatorLabel, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > maxOperatorLabels {
		return nil, errf(KindInvalid, "an operator takes at most %d labels", maxOperatorLabels)
	}
	seen := make(map[string]bool, len(in))
	out := make([]store.OperatorLabel, 0, len(in))
	for _, l := range in {
		k, v := strings.TrimSpace(l.Key), strings.TrimSpace(l.Value)
		switch {
		case !operatorLabelKey.MatchString(k):
			return nil, errf(KindInvalid, "label key %q is not valid (letters, digits, . _ / -, up to 63 characters)", k)
		case strings.HasPrefix(strings.ToLower(k), "continuum."):
			return nil, errf(KindInvalid, "label key %q uses the continuum. prefix, which is reserved", k)
		case seen[k]:
			return nil, errf(KindInvalid, "label %q is listed twice", k)
		case v == "" || len(v) > 64:
			return nil, errf(KindInvalid, "label %q needs a value of 1-64 characters", k)
		}
		for _, r := range v {
			if r < 0x20 || r == 0x7f {
				return nil, errf(KindInvalid, "label %q has a control character in its value", k)
			}
		}
		seen[k] = true
		out = append(out, store.OperatorLabel{Key: k, Value: v})
	}
	return out, nil
}

// validSourceClusters checks that every id names a currently approved agent's cluster in this
// organisation - the same source of truth the wizard itself reads from, re-checked here since the
// client's own list can be stale by the time it submits.
func (c *Core) validSourceClusters(ctx context.Context, ids []string) error {
	// No source is a valid operator: it is a place to send to, and clusters are pointed at it later (a telemetry intent issues
	// each one its own client certificate). Requiring one here forced a cluster to be named before anything could aggregate.
	if len(ids) == 0 {
		return nil
	}
	agents, err := c.Store.ListAgents(ctx, c.OrgID)
	if err != nil {
		return err
	}
	approved := make(map[string]bool, len(agents))
	for _, a := range agents {
		if a.Status == store.StatusApproved && a.ClusterID != "" {
			approved[a.ClusterID] = true
		}
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			return errf(KindInvalid, "source cluster %q is listed twice", id)
		}
		seen[id] = true
		if !approved[id] {
			return errf(KindInvalid, "%q is not the cluster of a currently approved agent in this organisation", id)
		}
	}
	return nil
}

// CreateOperator registers a new regional operator. For an mTLS operator (the normal case - see
// CreateOperatorWithHeartbeat) the returned receiver secret is "": there is no bearer token. For a bearer
// operator the secret is returned once and never stored - the same rule CreateToken follows. The operator
// is created without a heartbeat; see CreateOperatorWithHeartbeat.
func (c *Core) CreateOperator(ctx context.Context, actor, name string, sourceClusterIDs []string, dest store.Destination, acceptedModalities []store.Modality) (store.Operator, string, OperatorTLSBundle, error) {
	op, secret, bundle, _, err := c.CreateOperatorWithHeartbeat(ctx, actor, name, sourceClusterIDs, dest, acceptedModalities, false)
	return op, secret, bundle, err
}

// CreateOperatorWithHeartbeat is CreateOperator that can also mint the operator's heartbeat secret in the
// same step (heartbeat true): returned once as the fourth value, "" when heartbeat is false. The heartbeat
// is opt-in everywhere - see EnableOperatorHeartbeat for what it is.
func (c *Core) CreateOperatorWithHeartbeat(ctx context.Context, actor, name string, sourceClusterIDs []string, dest store.Destination, acceptedModalities []store.Modality, heartbeat bool) (store.Operator, string, OperatorTLSBundle, string, error) {
	return c.CreateOperatorWithOptions(ctx, actor, name, sourceClusterIDs, dest, acceptedModalities, OperatorOptions{Heartbeat: heartbeat})
}

// OperatorOptions are the optional parts of creating an operator, so a new one does not change the
// signature every caller already uses.
type OperatorOptions struct {
	// Heartbeat mints the heartbeat secret in the same step (see EnableOperatorHeartbeat).
	Heartbeat bool
	// Labels are stamped on everything the operator forwards (see store.OperatorLabel and validOperatorLabels).
	Labels []store.OperatorLabel
	// Exposure is how the Service is exposed ("cluster", "loadbalancer", "nodeport"; "" = not asked), kept so the
	// page can say what is left to do. See store.Operator.Exposure.
	Exposure string
}

// CreateOperatorWithOptions is CreateOperatorWithHeartbeat with the options named.
func (c *Core) CreateOperatorWithOptions(ctx context.Context, actor, name string, sourceClusterIDs []string, dest store.Destination, acceptedModalities []store.Modality, opts OperatorOptions) (store.Operator, string, OperatorTLSBundle, string, error) {
	c.depMu.RLock() // see Core.depMu
	defer c.depMu.RUnlock()
	heartbeat := opts.Heartbeat
	name = strings.TrimSpace(name)
	if name == "" || len(name) > maxOperatorName {
		return store.Operator{}, "", OperatorTLSBundle{}, "", errf(KindInvalid, "name the regional operator (1-%d characters)", maxOperatorName)
	}
	dest, err := c.operatorDestination(ctx, dest)
	if err != nil {
		return store.Operator{}, "", OperatorTLSBundle{}, "", err
	}
	if err := c.validSourceClusters(ctx, sourceClusterIDs); err != nil {
		return store.Operator{}, "", OperatorTLSBundle{}, "", err
	}
	if err := validModalities(acceptedModalities); err != nil {
		return store.Operator{}, "", OperatorTLSBundle{}, "", err
	}
	labels, err := validOperatorLabels(opts.Labels)
	if err != nil {
		return store.Operator{}, "", OperatorTLSBundle{}, "", err
	}
	if _, err := serviceTypeFor(opts.Exposure); err != nil {
		return store.Operator{}, "", OperatorTLSBundle{}, "", err
	}
	op := store.Operator{
		ID:                 newOperatorID(),
		OrgID:              c.OrgID,
		Name:               name,
		Status:             store.OperatorActive,
		SourceClusterIDs:   sourceClusterIDs,
		Destination:        dest,
		AcceptedModalities: acceptedModalities,
		Labels:             labels,
		Exposure:           opts.Exposure,
		CreatedBy:          actor,
		CreatedAt:          c.Now(),
	}
	var hbSecret string
	if heartbeat {
		var err error
		if hbSecret, err = NewOperatorHeartbeatSecret(); err != nil {
			return store.Operator{}, "", OperatorTLSBundle{}, "", err
		}
		op.HeartbeatHash = HashSecret(hbSecret)
		now := c.Now()
		op.HeartbeatEnabledAt = &now
	}
	// The operator's own receiver server cert (valid for the Service DNS name it is reachable at once
	// installed with this chart's own defaults - see operatorInstallCommand) and the client certificate
	// every source cluster presents to it, shown once the same way a secret is.
	//
	// What gates the receiver is decided by whether that mint worked. If it did, the operator is
	// ReceiverAuthMTLS: TLS with a required client certificate signed by THIS operator's own private CA is
	// its ONLY gate (no other operator's certificate, and not the org CA's, is accepted), and no receiver bearer token is minted at all (an agent pointed at it with the per-agent
	// command presents only a client certificate, so a token on top could never be satisfied). If the mint
	// failed, the operator falls back to ReceiverAuthBearer and a token IS minted, so the receiver is never
	// left with no gate whatever happens here. The operator is still created either way - failing the
	// request would report an operator that does not exist when it does.
	bundle, caKeyPEM, tlsErr := mintOperatorTLS(c, op.ID, operatorReceiverHosts(op.ID))
	var secret string
	var tokenHash []byte
	if tlsErr == nil {
		op.ReceiverAuth = store.ReceiverAuthMTLS
		op.ClientCACertPEM, op.ClientCAKeyPEM = bundle.CACertPEM, caKeyPEM
		op.ReceiverNotAfter = certNotAfter(bundle.ReceiverCertPEM)
		op.ClientNotAfter = op.ReceiverNotAfter // client certificates are minted with it, for the same time
	} else {
		var err error
		if secret, err = NewOperatorReceiverSecret(); err != nil {
			return store.Operator{}, "", OperatorTLSBundle{}, "", err
		}
		tokenHash = HashSecret(secret)
		op.ReceiverAuth = store.ReceiverAuthBearer
	}
	detail := name
	if heartbeat {
		// Only the fact, never the secret: the audit trail must hold no credential material.
		detail += " (heartbeat enabled)"
	}
	if err := c.audited(ctx, actor, "operator-created", "operator", op.ID, detail, func() error {
		return c.Store.CreateOperator(ctx, op, tokenHash)
	}); err != nil {
		return store.Operator{}, "", OperatorTLSBundle{}, "", err
	}
	if tlsErr != nil {
		// The operator (as a bearer-token one - see above) is already persisted and audited: failing the
		// whole request now would report an operator that does not exist when it does. The caller gets no
		// certificate material and the bearer token that gates the receiver instead.
		c.audit(ctx, actor, "operator-tls-mint-failed", "operator", op.ID, tlsErr.Error())
		return op, secret, OperatorTLSBundle{}, hbSecret, nil
	}
	if err := c.recordOperatorCert(ctx, actor, op, store.OperatorCertReceiver, "", bundle.ReceiverCertPEM); err != nil {
		c.Log.Error("receiver certificate not recorded in the ledger", "operator", op.ID, "err", err)
	}
	bundle.Senders, _ = c.mintSenderCerts(ctx, actor, op, op.SourceClusterIDs)
	return op, secret, bundle, hbSecret, nil
}

// mintSenderCerts issues each of clusters its own client certificate from op's CA. One that cannot be issued is left out
// and audited, not fatal: the operator exists, and Renew certificates issues them again.
func (c *Core) mintSenderCerts(ctx context.Context, actor string, op store.Operator, clusters []string) (out []SenderCert, caPEM []byte) {
	for _, cl := range clusters {
		certPEM, keyPEM, ca, err := c.IssueOperatorClientCertFor(ctx, actor, op.ID, cl, "cluster="+cl)
		if err != nil {
			c.audit(ctx, actor, "operator-tls-mint-failed", "operator", op.ID, "client certificate for "+cl+": "+err.Error())
			continue
		}
		out, caPEM = append(out, SenderCert{Sender: cl, CertPEM: certPEM, KeyPEM: keyPEM}), ca
	}
	return out, caPEM
}

func (c *Core) GetOperator(ctx context.Context, id string) (store.Operator, error) {
	return c.operatorInOrg(ctx, id)
}

func (c *Core) ListOperators(ctx context.Context) ([]store.Operator, error) {
	return c.Store.ListOperators(ctx, c.OrgID)
}

// UpdateOperatorScope replaces which clusters feed this operator and where it exports to. There is no
// live reparenting here (see the plan): applying the corresponding change to each source cluster's own
// agent release remains a manual step, printed as a reminder by operatorInstallCommand.
func (c *Core) UpdateOperatorScope(ctx context.Context, actor, id string, sourceClusterIDs []string, dest store.Destination, acceptedModalities []store.Modality) error {
	c.depMu.RLock() // see Core.depMu
	defer c.depMu.RUnlock()
	if err := guardCentral(id); err != nil {
		return err
	}
	if _, err := c.operatorInOrg(ctx, id); err != nil {
		return err
	}
	dest, err := c.operatorDestination(ctx, dest)
	if err != nil {
		return err
	}
	if err := c.validSourceClusters(ctx, sourceClusterIDs); err != nil {
		return err
	}
	if err := validModalities(acceptedModalities); err != nil {
		return err
	}
	return c.audited(ctx, actor, "operator-scope-changed", "operator", id, "", func() error {
		if err := c.Store.UpdateOperatorScope(ctx, id, sourceClusterIDs, dest, acceptedModalities); err != nil {
			if errors.Is(err, store.ErrBadState) {
				return errf(KindConflict, "only an active operator's scope can be changed")
			}
			return err
		}
		return nil
	})
}

// DefaultOperatorPort is the OTLP/gRPC port a regional operator receives on.
const DefaultOperatorPort = 4317

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// isClusterLocalName says whether host is a Kubernetes Service name that only resolves in its own cluster.
func isClusterLocalName(host string) bool {
	return host == "svc" || strings.HasSuffix(host, ".svc") || strings.HasSuffix(host, ".cluster.local")
}

// validOperatorAddress normalises the host:port an operator is reachable at from other clusters: a DNS name or
// an IP address, and the port (4317 when left out). An empty string is valid and clears it. Nothing that could never be reached
// from another cluster (loopback, unspecified, link-local, "localhost") and no scheme, path or credentials.
func validOperatorAddress(in string) (string, error) {
	s := strings.TrimSpace(in)
	if s == "" {
		return "", nil
	}
	bad := errf(KindInvalid, "the address is a host and a port, such as otlp.example.com:4317 or 203.0.113.7:4317 (no scheme, no path)")
	if len(s) > 261 || strings.ContainsAny(s, " \t/\\?#@,;\"'`$&|<>(){}") {
		return "", bad
	}
	host, port, err := net.SplitHostPort(s)
	var ae *net.AddrError
	if errors.As(err, &ae) && strings.Contains(ae.Err, "missing port") {
		// A bare host, such as the name a load balancer reports: the receiver's own port is the only one it has.
		host, port, err = net.SplitHostPort(net.JoinHostPort(strings.Trim(s, "[]"), strconv.Itoa(DefaultOperatorPort)))
	}
	if err != nil || host == "" {
		return "", bad
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", errf(KindInvalid, "the port must be a number from 1 to 65535")
	}
	port = strconv.Itoa(n) // "+4317" and "04317" parse as 4317 and are stored as 4317
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsMulticast() {
			return "", errf(KindInvalid, "%s cannot be reached from another cluster; give the address a LoadBalancer, NodePort or Ingress exposes", host)
		}
		return net.JoinHostPort(ip.String(), port), nil
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if len(host) > 253 || host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return "", errf(KindInvalid, "%s cannot be reached from another cluster", host)
	}
	if isClusterLocalName(host) {
		// What `kubectl get svc` prints in its NAME column, or a Service's DNS name: it resolves inside one cluster only,
		// so another cluster cannot use it however well it is spelled. (clusterset.local, the multi-cluster Services
		// name, does resolve across clusters and is allowed.)
		return "", errf(KindInvalid, "%s is a name that resolves only inside its own cluster; give the address a LoadBalancer, NodePort or Ingress exposes", host)
	}
	for _, l := range strings.Split(host, ".") {
		if !dnsLabel.MatchString(l) {
			return "", bad
		}
	}
	return net.JoinHostPort(host, port), nil
}

// SetOperatorAddress records where clusters other than the operator's own reach its receiver. It is allowed for the
// central operator too (the gateway in front of FUSION): that is where its public address is set from the UI. Nothing about
// the operator's certificates changes: callers verify it by its stable in-cluster name (operatorServerName),
// so the address can be changed, or an IP can move, without reissuing anything. Empty clears it.
func (c *Core) SetOperatorAddress(ctx context.Context, actor, id, address string) (string, error) {
	if _, err := c.operatorInOrg(ctx, id); err != nil {
		return "", err
	}
	addr, err := validOperatorAddress(address)
	if err != nil {
		return "", err
	}
	detail := addr
	if detail == "" {
		detail = "cleared"
	}
	return addr, c.audited(ctx, actor, "operator-address-changed", "operator", id, detail, func() error {
		if err := c.Store.SetOperatorAddress(ctx, id, addr); err != nil {
			if errors.Is(err, store.ErrBadState) {
				return errf(KindConflict, "only an active operator's address can be changed")
			}
			return err
		}
		return nil
	})
}

// errAddressRecorded is what RecordOperatorAddressIfUnset's write finds when a person recorded an address after it
// had looked: they win, and the audit trail says the server's attempt did not take effect.
var errAddressRecorded = errors.New("an address was recorded in the meantime and was kept")

// RecordOperatorAddressIfUnset is SetOperatorAddress for the server's own discovery: it writes only while the operator
// has no address, as one compare-and-set in the store, so what an administrator types between the server's look and its
// write is never replaced. wrote is false (and err nil) when an address is already there; current is then what it is.
func (c *Core) RecordOperatorAddressIfUnset(ctx context.Context, actor, id, address string) (current string, wrote bool, err error) {
	op, err := c.operatorInOrg(ctx, id)
	if err != nil {
		return "", false, err
	}
	if op.Address != "" {
		return op.Address, false, nil
	}
	addr, err := validOperatorAddress(address)
	if err != nil || addr == "" {
		return "", false, err
	}
	err = c.audited(ctx, actor, "operator-address-changed", "operator", id, addr, func() error {
		ok, err := c.Store.SetOperatorAddressIfEmpty(ctx, id, addr)
		switch {
		case errors.Is(err, store.ErrBadState):
			return errf(KindConflict, "only an active operator's address can be changed")
		case err != nil:
			return err
		case !ok:
			return errAddressRecorded
		}
		return nil
	})
	if errors.Is(err, errAddressRecorded) {
		again, gerr := c.Store.GetOperator(ctx, id)
		return again.Address, false, gerr
	}
	if err != nil {
		return "", false, err
	}
	return addr, true, nil
}

// operatorServerName is the name on an operator's receiver certificate that never changes (the SANs
// CreateOperator asks for): callers that reach the operator at an advertised address verify against this.
func operatorServerName(op store.Operator) string { return op.ID + ".continuum-system.svc" }

// operatorServiceName is the name of the Kubernetes Service the regional-operator chart creates for a release named
// after the operator (what the install command does): the chart's own "operator.name" rule, which appends
// "-regional-operator" unless the release name already contains it, cut to 63 characters. This is the DNS name a
// sender in the same cluster dials; it is NOT the operator's id, and it is not what the certificate is verified by
// (operatorServerName is).
func operatorServiceName(id string) string {
	name := id
	if !strings.Contains(id, "regional-operator") {
		name = id + "-regional-operator"
	}
	if len(name) > 63 {
		name = name[:63]
	}
	return strings.TrimRight(name, "-")
}

// operatorInClusterEndpoint is where a sender inside the same cluster dials a regional operator: its Service in the
// namespace the install commands use.
func operatorInClusterEndpoint(op store.Operator) string {
	return fmt.Sprintf("%s.continuum-system.svc:%d", operatorServiceName(op.ID), DefaultOperatorPort)
}

// operatorNeedsServerName says whether a sender must verify op's certificate by its stable name instead of the name it
// dials. That is every regional operator (its Service name is not on the certificate, its stable name is) and the
// central operator once it is reached at a public address; the central operator inside its own cluster is dialled by
// a name its certificate carries.
func operatorNeedsServerName(op store.Operator) bool {
	return op.ID != CentralOperatorID || op.Address != ""
}

// RevokeOperator revokes an operator nothing depends on; see RevokeOperatorForce.
func (c *Core) RevokeOperator(ctx context.Context, actor, id, reason string) error {
	return c.RevokeOperatorForce(ctx, actor, id, reason, false)
}

// RevokeOperatorForce revokes an operator. Revoking does not stop its data plane (the receiver keeps running and
// trusting its CA until it is uninstalled), but it does orphan whatever is configured to send to it: another
// operator exporting into it, a telemetry intent granting a cluster export to it. While anything does, this refuses
// with KindConflict naming what depends on it, unless force is set; the audit entry then records the counts, so the
// trail says what was knowingly left behind.
func (c *Core) RevokeOperatorForce(ctx context.Context, actor, id, reason string, force bool) error {
	if err := guardCentral(id); err != nil {
		return err
	}
	c.depMu.Lock() // the dependents check and the revoke are one step: see Core.depMu
	defer c.depMu.Unlock()
	if _, err := c.operatorInOrg(ctx, id); err != nil {
		return err
	}
	reason = printable(reason, maxReason)
	detail, err := c.dependentsDetail(ctx, id, force, reason)
	if err != nil {
		return err
	}
	return c.audited(ctx, actor, "operator-revoked", "operator", id, detail, func() error {
		if err := c.Store.RevokeOperator(ctx, id, reason, c.Now()); err != nil {
			if errors.Is(err, store.ErrBadState) {
				return errf(KindConflict, "operator is not active")
			}
			return err
		}
		c.opCAs.forget(id) // the sealed key was erased with it; do not keep the opened one
		return nil
	})
}

// DeleteOperator deletes an operator nothing depends on; see DeleteOperatorForce.
func (c *Core) DeleteOperator(ctx context.Context, actor, id string) error {
	return c.DeleteOperatorForce(ctx, actor, id, false)
}

// DeleteOperatorForce deletes an operator, under the same dependents rule as RevokeOperatorForce.
func (c *Core) DeleteOperatorForce(ctx context.Context, actor, id string, force bool) error {
	if err := guardCentral(id); err != nil {
		return err
	}
	c.depMu.Lock() // the dependents check and the delete are one step: see Core.depMu
	defer c.depMu.Unlock()
	if _, err := c.operatorInOrg(ctx, id); err != nil {
		return err
	}
	detail, err := c.dependentsDetail(ctx, id, force, "")
	if err != nil {
		return err
	}
	return c.audited(ctx, actor, "operator-deleted", "operator", id, detail, func() error {
		if err := c.Store.DeleteOperator(ctx, id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return errf(KindNotFound, "no such regional operator")
			}
			return err
		}
		c.opCAs.forget(id)
		return nil
	})
}

// operatorIssuer returns the CA that signs op's client certificates and that CA's certificate PEM, which is
// what goes in the Secret next to the client certificate. An operator with its own CA (op.ClientCAScope()
// "operator") signs with it: its sealed key is read, opened with the org CA's passphrase, and used in memory
// only. Any other operator - a legacy mTLS one, or a bearer one - signs with the org CA, which is exactly what
// it always did and what its receiver already trusts.
func (c *Core) operatorIssuer(ctx context.Context, op store.Operator) (*pki.CA, []byte, error) {
	if len(op.ClientCACertPEM) == 0 {
		return c.CA, c.CA.CertPEM(), nil
	}
	keyPEM, err := c.Store.GetOperatorClientCAKey(ctx, op.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("the operator's CA key is not available: %w", err)
	}
	issuer, err := c.opCAs.open(ctx, c, op.ID, op.ClientCACertPEM, keyPEM)
	if err != nil {
		return nil, nil, err
	}
	return issuer, issuer.CertPEM(), nil
}

// IssueOperatorClientCertFor mints a fresh mTLS client certificate for ONE sender of an existing operator's receiver
// (sender: the cluster, or the operator, that will hold the key; it is named in the certificate), on demand: for a
// cluster granted a TelemetryIntent pointing at this operator, a cluster added to its scope, or an operator that exports
// to it. Safe to call repeatedly: the receiver trusts the operator's CA via client_ca_file, not one pinned certificate,
// so every certificate this mints validates identically. The CA is the operator's own private one when it has one (see
// operatorIssuer) and caPEM is that CA's certificate, the one the receiver trusts. The certificate and key are returned
// once, to be put into a Kubernetes Secret, and never stored; what IS stored is the ledger entry (store.OperatorCert:
// serial, subject, sender, dates, by whom), and a certificate that cannot be recorded is not handed out. forWhat is
// extra words for the audit entry ("intent=ti-1 cluster=cl-a").
func (c *Core) IssueOperatorClientCertFor(ctx context.Context, actor, operatorID, sender, forWhat string) (certPEM, keyPEM, caPEM []byte, err error) {
	op, err := c.operatorInOrg(ctx, operatorID)
	if err != nil {
		return nil, nil, nil, err
	}
	if op.Status != store.OperatorActive {
		return nil, nil, nil, errf(KindConflict, "operator is not active")
	}
	issuer, caPEM, err := c.operatorIssuer(ctx, op)
	if err != nil {
		return nil, nil, nil, err
	}
	certPEM, keyPEM, err = issuer.IssueOperatorClientTLS(op.ID, c.OrgID, sender)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := c.recordOperatorCert(ctx, actor, op, store.OperatorCertClient, sender, certPEM); err != nil {
		return nil, nil, nil, err
	}
	// Only which CA signed it is audited ("operator" or the legacy "org"), never any key material.
	scope := op.ClientCAScope()
	if scope == "" {
		scope = store.ClientCAScopeOrg // a bearer operator's optional client cert is signed by the org CA
	}
	detail := "ca=" + scope
	if forWhat != "" {
		detail += " " + forWhat
	}
	c.audit(ctx, actor, "operator-client-cert-reissued", "operator", op.ID, detail)
	return certPEM, keyPEM, caPEM, nil
}

// recordOperatorCert writes the ledger entry for a certificate just issued: what it is, whose, and when it ends.
func (c *Core) recordOperatorCert(ctx context.Context, actor string, op store.Operator, kind store.OperatorCertKind, sender string, certPEM []byte) error {
	cert, err := pki.ParseCertificate(certPEM)
	if err != nil {
		return fmt.Errorf("the certificate just issued could not be read back: %w", err)
	}
	return c.Store.AddOperatorCert(ctx, store.OperatorCert{
		Serial: cert.SerialNumber.Text(16), OrgID: c.OrgID, OperatorID: op.ID, Kind: kind, Subject: cert.Subject.CommonName,
		Sender: sender, IssuedBy: actor, IssuedAt: c.Now(), NotBefore: cert.NotBefore, NotAfter: cert.NotAfter,
	})
}

// OperatorCertificates is the ledger of what was issued for an operator, newest first.
func (c *Core) OperatorCertificates(ctx context.Context, id string) ([]store.OperatorCert, error) {
	if _, err := c.operatorInOrg(ctx, id); err != nil {
		return nil, err
	}
	return c.Store.ListOperatorCerts(ctx, id)
}
