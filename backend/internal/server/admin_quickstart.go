package server

import (
	"errors"
	"net/http"
	"time"

	"continuum/internal/store"
)

// ---- quick-start gateway tokens (Part B of the quick-start gateway: see QuickStartBackend and
// MintGatewayToken) ----

// mintGatewayToken mints a fresh gateway token for one quick-start backend instance and returns the
// plaintext once - never stored, never logged, the same convention createOperator's receiver token and
// createToken's enrollment token both already follow. TTLSeconds is optional; zero (or out of range) is
// clamped server-side, see MintGatewayToken's own comment.
func (a *Admin) mintGatewayToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TTLSeconds int `json:"ttlSeconds"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	t, secret, err := a.core(r).MintGatewayToken(r.Context(), actor(r), r.PathValue("id"), time.Duration(req.TTLSeconds)*time.Second)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{
		"token":     secret,
		"createdAt": rfc(t.CreatedAt),
		"expiresAt": rfc(t.ExpiresAt),
	})
}

// getGatewayToken reports whether a gateway token has been minted for this backend and, if so, when it
// expires - never the secret itself, which is shown once, at mint time, and nowhere else. No token minted
// yet is not an error: it is simply {"active": false}, the normal state for a backend nobody has gated yet.
func (a *Admin) getGatewayToken(w http.ResponseWriter, r *http.Request) {
	t, err := a.core(r).LatestGatewayToken(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, 200, map[string]any{"active": false})
		return
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"active":    true,
		"expired":   !a.core(r).Now().Before(t.ExpiresAt),
		"createdAt": rfc(t.CreatedAt),
		"expiresAt": rfc(t.ExpiresAt),
	})
}
