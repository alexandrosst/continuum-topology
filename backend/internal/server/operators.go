package server

import (
	"context"
	"errors"
	"strings"

	"continuum/internal/store"
)

// OperatorTLSBundle is the mTLS material minted once, alongside the receiver bearer token, when a
// regional operator is created: a server certificate for its own OTLP receiver, a client certificate
// every one of its source clusters presents when exporting into it, and this organisation's CA
// certificate both sides need to verify the other (client_ca_file on the receiver, ca_file on the
// exporter). None of it is stored server-side beyond what the token already is not - see
// pki.IssueOperatorReceiverTLS and pki.IssueOperatorClientTLS.
type OperatorTLSBundle struct {
	ReceiverCertPEM, ReceiverKeyPEM []byte
	ClientCertPEM, ClientKeyPEM     []byte
	CACertPEM                       []byte
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

// validateDestination checks the parts of a Destination that do not depend on anything else in the
// organisation. DestinationOperator (chaining to another regional operator) is deliberately rejected:
// this release only builds the mechanism for a two-tier fleet (agents feeding one regional operator that
// exports out), not live reparenting or operator-to-operator chains - see the plan's own §0 for why.
func validateDestination(dest store.Destination) error {
	switch dest.Kind {
	case store.DestinationExternal:
		if strings.TrimSpace(dest.Endpoint) == "" {
			return errf(KindInvalid, "destination.endpoint is required")
		}
		return nil
	case store.DestinationOperator:
		return errf(KindInvalid, "chaining a regional operator to another regional operator is not supported yet - point it at an external OTLP endpoint instead")
	default:
		return errf(KindInvalid, "destination.kind must be %q", store.DestinationExternal)
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

// CreateOperator registers a new regional operator and mints its receiver bearer token. The secret is
// returned once and never stored - the same rule CreateToken follows for enrollment tokens.
func (c *Core) CreateOperator(ctx context.Context, actor, name string, sourceClusterIDs []string, dest store.Destination, acceptedModalities []store.Modality) (store.Operator, string, OperatorTLSBundle, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > maxOperatorName {
		return store.Operator{}, "", OperatorTLSBundle{}, errf(KindInvalid, "name the regional operator (1-%d characters)", maxOperatorName)
	}
	if err := validateDestination(dest); err != nil {
		return store.Operator{}, "", OperatorTLSBundle{}, err
	}
	if err := c.validSourceClusters(ctx, sourceClusterIDs); err != nil {
		return store.Operator{}, "", OperatorTLSBundle{}, err
	}
	if err := validModalities(acceptedModalities); err != nil {
		return store.Operator{}, "", OperatorTLSBundle{}, err
	}
	secret, err := NewOperatorReceiverSecret()
	if err != nil {
		return store.Operator{}, "", OperatorTLSBundle{}, err
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
	// Minted alongside the bearer token, shown once the same way: the operator's own receiver server
	// cert (valid for the Service DNS name it is reachable at once installed with this chart's own
	// defaults - see operatorInstallCommand) and the client certificate every source cluster presents to
	// it. A failure here does not roll back the operator/token already persisted above: the operator is
	// still usable over its bearer token alone (mTLS is additive, see receiver.tls in the operator
	// chart), and the admin can be told plainly that the TLS material needs minting again rather than
	// silently losing the operator itself.
	hosts := []string{op.ID + ".continuum-system", op.ID + ".continuum-system.svc", op.ID + ".continuum-system.svc.cluster.local"}
	bundle := OperatorTLSBundle{CACertPEM: c.CA.CertPEM()}
	var tlsErr error
	bundle.ReceiverCertPEM, bundle.ReceiverKeyPEM, tlsErr = c.CA.IssueOperatorReceiverTLS(op.ID, c.OrgID, hosts)
	if tlsErr == nil {
		bundle.ClientCertPEM, bundle.ClientKeyPEM, tlsErr = c.CA.IssueOperatorClientTLS(op.ID, c.OrgID)
	}
	detail := name
	if err := c.audited(ctx, actor, "operator-created", "operator", op.ID, detail, func() error {
		return c.Store.CreateOperator(ctx, op, HashSecret(secret))
	}); err != nil {
		return store.Operator{}, "", OperatorTLSBundle{}, err
	}
	if tlsErr != nil {
		// The operator and its bearer token are already persisted and audited above - failing the whole
		// request now would report an operator that does not exist when it does. Surface this as a
		// warning the caller can show instead: the receiver bearer token still works on its own (mTLS is
		// additive, see receiver.tls in the operator chart), just without the extra certificate material.
		c.audit(ctx, actor, "operator-tls-mint-failed", "operator", op.ID, tlsErr.Error())
		return op, secret, OperatorTLSBundle{}, nil
	}
	return op, secret, bundle, nil
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
	if err := validateDestination(dest); err != nil {
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
