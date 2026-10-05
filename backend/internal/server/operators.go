package server

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
	ClientCertPEM, ClientKeyPEM     []byte
	CACertPEM                       []byte
}

// mintOperatorTLS gives a new operator its own private CA and issues, from THAT CA, the receiver server
// certificate and the client certificate its source clusters present. bundle.CACertPEM is the operator CA's
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
	if bundle.ClientCertPEM, bundle.ClientKeyPEM, err = issuer.IssueOperatorClientTLS(operatorID, c.OrgID); err != nil {
		return OperatorTLSBundle{}, nil, err
	}
	return bundle, caKeyPEM, nil
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

// validateDestination checks that a Destination is well-formed and, for DestinationOperator, that it
// names a currently active regional operator in this organisation. Continuum's fleet is two tiers: an
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
		return nil
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

// validSourceClusters checks that every id names a currently approved agent's cluster in this
// organisation - the same source of truth the wizard itself reads from, re-checked here since the
// client's own list can be stale by the time it submits.
func (c *Core) validSourceClusters(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return errf(KindInvalid, "pick at least one source cluster")
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
	name = strings.TrimSpace(name)
	if name == "" || len(name) > maxOperatorName {
		return store.Operator{}, "", OperatorTLSBundle{}, "", errf(KindInvalid, "name the regional operator (1-%d characters)", maxOperatorName)
	}
	if err := c.validateDestination(ctx, dest); err != nil {
		return store.Operator{}, "", OperatorTLSBundle{}, "", err
	}
	if err := c.validSourceClusters(ctx, sourceClusterIDs); err != nil {
		return store.Operator{}, "", OperatorTLSBundle{}, "", err
	}
	if err := validModalities(acceptedModalities); err != nil {
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
	hosts := []string{op.ID + ".continuum-system", op.ID + ".continuum-system.svc", op.ID + ".continuum-system.svc.cluster.local"}
	bundle, caKeyPEM, tlsErr := mintOperatorTLS(c, op.ID, hosts)
	var secret string
	var tokenHash []byte
	if tlsErr == nil {
		op.ReceiverAuth = store.ReceiverAuthMTLS
		op.ClientCACertPEM, op.ClientCAKeyPEM = bundle.CACertPEM, caKeyPEM
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
	return op, secret, bundle, hbSecret, nil
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
	if _, err := c.operatorInOrg(ctx, id); err != nil {
		return err
	}
	if err := c.validateDestination(ctx, dest); err != nil {
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

func (c *Core) RevokeOperator(ctx context.Context, actor, id, reason string) error {
	if _, err := c.operatorInOrg(ctx, id); err != nil {
		return err
	}
	reason = printable(reason, maxReason)
	return c.audited(ctx, actor, "operator-revoked", "operator", id, reason, func() error {
		if err := c.Store.RevokeOperator(ctx, id, reason, c.Now()); err != nil {
			if errors.Is(err, store.ErrBadState) {
				return errf(KindConflict, "operator is not active")
			}
			return err
		}
		return nil
	})
}

func (c *Core) DeleteOperator(ctx context.Context, actor, id string) error {
	if _, err := c.operatorInOrg(ctx, id); err != nil {
		return err
	}
	return c.audited(ctx, actor, "operator-deleted", "operator", id, "", func() error {
		if err := c.Store.DeleteOperator(ctx, id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return errf(KindNotFound, "no such regional operator")
			}
			return err
		}
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
	issuer, err := c.CA.OpenOperatorCA(op.ClientCACertPEM, keyPEM)
	if err != nil {
		return nil, nil, err
	}
	return issuer, issuer.CertPEM(), nil
}

// IssueOperatorClientCert mints a fresh mTLS client certificate for an existing operator's receiver,
// on demand - for a cluster granted a TelemetryIntent pointing at this operator after its creation,
// which never received the one shared client cert minted (and shown once, never stored) at CreateOperator
// time. Safe to call repeatedly: the operator's receiver trusts its CA via client_ca_file, not one pinned
// certificate, so every certificate this mints validates identically. The CA is the operator's own private
// one when it has one (see operatorIssuer) and the returned caPEM is that CA's certificate, the one the
// receiver trusts - never the org CA's for such an operator. Not stored server-side, same rule every
// certificate/secret in this app follows - returned once, to be put directly into a Kubernetes Secret the
// admin creates.
func (c *Core) IssueOperatorClientCert(ctx context.Context, actor, operatorID string) (certPEM, keyPEM, caPEM []byte, err error) {
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
	certPEM, keyPEM, err = issuer.IssueOperatorClientTLS(op.ID, c.OrgID)
	if err != nil {
		return nil, nil, nil, err
	}
	// Side-effect-free beyond the mint above: nothing is stored, so this is recorded with the
	// fire-and-forget c.audit rather than c.audited (which wraps a do func() error for a store mutation
	// that must not happen unseen - see CreateOperator's own "operator-tls-mint-failed" for the same
	// reasoning when a TLS mint itself is what is being logged). Only which CA signed it is recorded
	// ("operator" or the legacy "org"), never any key material.
	scope := op.ClientCAScope()
	if scope == "" {
		scope = store.ClientCAScopeOrg // a bearer operator's optional client cert is signed by the org CA
	}
	c.audit(ctx, actor, "operator-client-cert-reissued", "operator", op.ID, "ca="+scope)
	return certPEM, keyPEM, caPEM, nil
}
