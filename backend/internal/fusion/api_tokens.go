package fusion

import (
	"net/http"
	"time"

	"continuum/internal/httpkit"
	"continuum/internal/kernel"
)

// The token routes: administrators of the organisation FUSION belongs to, and no other.

// orgOnly refuses a token request from any organisation but the one FUSION belongs to.
func (h *API) orgOnly(w http.ResponseWriter, r *http.Request) bool {
	if h.control() == nil {
		h.fail(w, r, h.unavailable(r))
		return false
	}
	if h.For(r).OrgID != h.Org() {
		h.fail(w, r, kernel.Errf(kernel.KindForbidden, "FUSION belongs to this server's main organisation, which is the only one that can give out access to it"))
		return false
	}
	return true
}

func (h *API) listTokens(w http.ResponseWriter, r *http.Request) {
	if !h.orgOnly(w, r) {
		return
	}
	toks, err := h.For(r).ListFusionTokens(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]TokenDoc, len(toks))
	for i, t := range toks {
		out[i] = toTokenDoc(t)
	}
	httpkit.WriteJSON(w, 200, out)
}

// createToken is the one response that ever carries the raw secret.
func (h *API) createToken(w http.ResponseWriter, r *http.Request) {
	if !h.orgOnly(w, r) {
		return
	}
	var req struct {
		Name          string   `json:"name"`
		Signals       []string `json:"signals"`
		Namespaces    []string `json:"namespaces"`
		Clusters      []string `json:"clusters"`
		ExpiresInDays int      `json:"expiresInDays"`
	}
	if err := httpkit.Decode(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	if req.ExpiresInDays < 0 || req.ExpiresInDays > 3650 {
		h.fail(w, r, kernel.Errf(kernel.KindInvalid, "expiresInDays must be between 1 and %d", int(maxFusionTokenTTL.Hours()/24)))
		return
	}
	secret, tok, err := h.For(r).CreateFusionToken(r.Context(), httpkit.Actor(r), TokenSpec{
		Name: req.Name, Signals: req.Signals, Namespaces: req.Namespaces, Clusters: req.Clusters,
		TTL: time.Duration(req.ExpiresInDays) * 24 * time.Hour,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpkit.WriteJSON(w, 200, map[string]any{"token": secret, "details": toTokenDoc(tok)})
}

func (h *API) deleteToken(w http.ResponseWriter, r *http.Request) {
	if !h.orgOnly(w, r) {
		return
	}
	if err := h.For(r).DeleteFusionToken(r.Context(), httpkit.Actor(r), r.PathValue("id")); err != nil {
		h.fail(w, r, err)
		return
	}
	httpkit.WriteJSON(w, 200, map[string]bool{"ok": true})
}
