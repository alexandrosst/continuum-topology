package server

import (
	"context"
	"time"

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
// the same convention agentInOrg/operatorInOrg already use.
func (c *Core) quickStartBackendInOrg(id string) (QuickStartBackend, error) {
	for _, b := range c.Settings().QuickStartBackends {
		if b.ID == id {
			return b, nil
		}
	}
	return QuickStartBackend{}, errf(KindNotFound, "no such quick-start backend")
}

// MintGatewayToken mints a fresh, instance-scoped bearer secret for the Part C nginx gateway that gates
// access to one already-installed quick-start backend (see store.GatewayToken) - the same "mint
// server-side, show once, checked entirely by something the admin runs themselves" shape
// CreateOperator's receiver token already follows. ttl of zero (or out of [MinGatewayTokenTTL,
// MaxGatewayTokenTTL]) is clamped rather than rejected - see the constants' own comment.
func (c *Core) MintGatewayToken(ctx context.Context, actor, backendID string, ttl time.Duration) (store.GatewayToken, string, error) {
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
	secret, err := NewGatewayTokenSecret()
	if err != nil {
		return store.GatewayToken{}, "", err
	}
	now := c.Now()
	t := store.GatewayToken{
		ID:        newGatewayTokenID(),
		OrgID:     c.OrgID,
		BackendID: backendID,
		CreatedBy: actor,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
	}
	if err := c.audited(ctx, actor, "quick-start-gateway-token-minted", "quick-start-backend", backendID, backend.Label, func() error {
		return c.Store.CreateGatewayToken(ctx, t, HashSecret(secret))
	}); err != nil {
		return store.GatewayToken{}, "", err
	}
	return t, secret, nil
}

// LatestGatewayToken reports the most recently minted gateway token for a quick-start backend, if any -
// never the secret (see store.GatewayToken), only whether one exists and when it expires, for the admin
// UI to show without re-minting. ErrNotFound (from the store) if none was ever minted for it.
func (c *Core) LatestGatewayToken(ctx context.Context, backendID string) (store.GatewayToken, error) {
	if _, err := c.quickStartBackendInOrg(backendID); err != nil {
		return store.GatewayToken{}, err
	}
	return c.Store.LatestGatewayToken(ctx, c.OrgID, backendID)
}
