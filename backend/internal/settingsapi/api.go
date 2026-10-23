// Package settingsapi is the HTTP face of an organisation's settings: reading them, and replacing them under the revision an edit
// started from. The settings themselves (and what changing them audits and applies) are the kernel's.
package settingsapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"ikhnos/internal/accounts"
	"ikhnos/internal/httpkit"
	"ikhnos/internal/kernel"
)

// API is the settings' routes. For is the Core of the organisation a request is for; ImageDefaults is what the server's --image-*
// flags say, which the document shows beside the organisation's own image settings.
type API struct {
	For           func(r *http.Request) *kernel.Core
	ImageDefaults func() kernel.ImageConfig
}

// Mount puts the routes on r.
func (h *API) Mount(r httpkit.Router) {
	r.Handle("GET "+httpkit.OrgPath+"/settings", httpkit.MemberRole, h.getSettings)
	r.Handle("PUT "+httpkit.OrgPath+"/settings", httpkit.AdminRole, h.putSettings)
}

// SettingsDoc is Settings as the UI sees it. Only administrators see the decider's address (it may
// carry a secret in its query); everyone else learns only that one is configured. The decider secret is
// stricter still: nobody ever sees it again once saved, administrator included, the same as a password field -
// only whether one is set is exposed.
type SettingsDoc struct {
	kernel.Settings
	DeciderConfigured bool `json:"deciderConfigured"`
	DeciderSecretSet  bool `json:"deciderSecretSet"`
	// ImageDefaults is what the server's --image-* flags say: what install commands use while this organisation's
	// own image settings are empty. Derived; the UI shows it and never sends it back.
	ImageDefaults kernel.ImageConfig `json:"imageDefaults"`
	// Rev is the revision of this document: 0 until it was first saved, then up by one with every save. Derived; a save
	// names the revision its edit started from in If-Match instead (see putSettings), and a body that carries `rev` is refused.
	Rev int64 `json:"rev"`
}

func settingsDoc(s kernel.Settings, rev int64, admin bool) SettingsDoc {
	d := SettingsDoc{Settings: s, Rev: rev, DeciderConfigured: s.DeciderURL != "", DeciderSecretSet: s.DeciderSecret != ""}
	d.Settings.DeciderSecret = "" // write-only, always - see SettingsDoc
	if !admin {
		d.DeciderURL = ""
	}
	return d
}

func (h *API) getSettings(w http.ResponseWriter, r *http.Request) {
	cur, rev := h.For(r).SettingsAt()
	d := settingsDoc(cur, rev, accounts.Allows(httpkit.Principal(r), accounts.ActAdminister))
	d.ImageDefaults = h.ImageDefaults()
	httpkit.WriteJSON(w, 200, d)
}

// putSettings never lets a client blank the decider secret by accident: a GET never carries it (see settingsDoc),
// so a client that reads its settings and PUTs most of it back unchanged - which is exactly what the UI does -
// naturally sends no `deciderSecret` at all, or "". Both are treated as "leave it alone". A client sets a new
// secret by sending a non-empty `deciderSecret`, and removes it, explicitly, with `clearDeciderSecret: true`.
//
// The document is replaced as a whole, so a client that edits an old copy would silently undo what someone else saved
// in between. If-Match carries the revision (the document's `rev`) the client's edit started from; when another save
// has come in since, nothing is stored and the answer is 409 with the current document, which the client shows
// and edits again. A request without If-Match is still accepted and replaces whatever is there (scripts, older UIs).
func (h *API) putSettings(w http.ResponseWriter, r *http.Request) {
	var expect *int64
	if m := strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), `"`); m != "" {
		v, err := strconv.ParseInt(m, 10, 64)
		if err != nil || v < 0 {
			httpkit.WriteErr(w, 400, "If-Match is the revision of the settings you started from, a number")
			return
		}
		expect = &v
	}
	var body saveSettingsRequest
	if err := httpkit.Decode(r, &body); err != nil {
		httpkit.WriteErr(w, 400, err.Error())
		return
	}
	s := body.Settings
	switch {
	case body.ClearDeciderSecret:
		s.DeciderSecret = ""
	case s.DeciderSecret == "":
		s.DeciderSecret = h.For(r).Settings().DeciderSecret
	}
	// Which cluster the server runs in is changed where it is shown (the central operator's address picker), not here.
	s.ServerClusterID = h.For(r).Settings().ServerClusterID
	n, rev, err := h.For(r).SaveSettingsAt(r.Context(), httpkit.Actor(r), expect, s)
	var ce *kernel.Error
	if errors.As(err, &ce) && ce.Kind == kernel.KindConflict {
		d := settingsDoc(n, rev, true)
		d.ImageDefaults = h.ImageDefaults()
		httpkit.WriteJSON(w, http.StatusConflict, settingsConflictDoc{d, ce.Msg})
		return
	}
	if err != nil {
		httpkit.Fail(w, h.For(r).Log, err)
		return
	}
	d := settingsDoc(n, rev, true)
	d.ImageDefaults = h.ImageDefaults()
	httpkit.WriteJSON(w, 200, d)
}
