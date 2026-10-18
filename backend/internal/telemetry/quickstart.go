package telemetry

import (
	"context"
	"time"

	"continuum/internal/kernel"
	"continuum/internal/store"
)

// Gateway token lifetime bounds (see MintGatewayToken). DefaultGatewayTokenTTL is used when a caller asks
// for no particular lifetime; Min/MaxGatewayTokenTTL clamp whatever a caller does ask for into a sane
// range, rather than rejecting it outright - a short-lived credential pasted into a ConfigMap the admin
// controls end-to-end does not need the same strictness as, say, an enrollment token.
const (
	DefaultGatewayTokenTTL = 24 * time.Hour
	MinGatewayTokenTTL     = 5 * time.Minute
	MaxGatewayTokenTTL     = 7 * 24 * time.Hour
)

// quickStartBackendInOrg finds this organisation's own quick-start backend by id (see Settings.QuickStartBackends) -
// a backend belonging to another organisation, or no longer configured at all, is reported as not found,
// the same convention AgentInOrg/operatorInOrg already use.
func (c *Service) quickStartBackendInOrg(id string) (kernel.QuickStartBackend, error) {
	for _, b := range c.Settings().QuickStartBackends {
		if b.ID == id {
			return b, nil
		}
	}
	return kernel.QuickStartBackend{}, kernel.Errf(kernel.KindNotFound, "no such quick-start backend")
}

// MintGatewayToken mints a fresh, instance-scoped bearer secret for the Part C nginx gateway that gates
// access to one already-installed quick-start backend (see store.GatewayToken) - the same "mint
// server-side, show once, checked entirely by something the admin runs themselves" shape
// CreateOperator's receiver token already follows. ttl of zero (or out of [MinGatewayTokenTTL,
// MaxGatewayTokenTTL]) is clamped rather than rejected - see the constants' own comment.
func (c *Service) MintGatewayToken(ctx context.Context, actor, backendID string, ttl time.Duration) (store.GatewayToken, string, error) {
	backend, err := c.quickStartBackendInOrg(backendID)
	if err != nil {
		return store.GatewayToken{}, "", err
	}
	switch {
	case ttl <= 0:
		ttl = DefaultGatewayTokenTTL
	case ttl < MinGatewayTokenTTL:
		ttl = MinGatewayTokenTTL
	case ttl > MaxGatewayTokenTTL:
		ttl = MaxGatewayTokenTTL
	}
	secret, err := kernel.NewGatewayTokenSecret()
	if err != nil {
		return store.GatewayToken{}, "", err
	}
	now := c.Now()
	hash := kernel.HashSecret(secret)
	t := store.GatewayToken{
		ID:         kernel.NewGatewayTokenID(),
		OrgID:      c.OrgID,
		BackendID:  backendID,
		SecretHash: hash, // also handed to CreateGatewayToken below, so the in-memory struct matches what's persisted
		CreatedBy:  actor,
		CreatedAt:  now,
		ExpiresAt:  now.Add(ttl),
	}
	if err := c.AuditedWrite(c.Store.CreateGatewayToken(ctx, t, hash,
		store.WithAudit(c.AuditRow(c.OrgID, actor, "quick-start-gateway-token-minted", "quick-start-backend", backendID, backend.Label))), "quick-start-gateway-token-minted"); err != nil {
		return store.GatewayToken{}, "", err
	}
	return t, secret, nil
}

// LatestGatewayToken reports the most recently minted gateway token for a quick-start backend, if any -
// never the secret (see store.GatewayToken), only whether one exists and when it expires, for the admin
// UI to show without re-minting. ErrNotFound (from the store) if none was ever minted for it.
func (c *Service) LatestGatewayToken(ctx context.Context, backendID string) (store.GatewayToken, error) {
	if _, err := c.quickStartBackendInOrg(backendID); err != nil {
		return store.GatewayToken{}, err
	}
	return c.Store.LatestGatewayToken(ctx, c.OrgID, backendID)
}
