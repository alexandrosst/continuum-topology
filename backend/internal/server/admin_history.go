package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"continuum/internal/accounts"
	"continuum/internal/decisionloop"
	"continuum/internal/httpkit"
	"continuum/internal/kernel"
)

// ---- settings ----

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

func (a *Admin) getSettings(w http.ResponseWriter, r *http.Request) {
	cur, rev := a.core(r).SettingsAt()
	d := settingsDoc(cur, rev, accounts.Allows(httpkit.Principal(r), accounts.ActAdminister))
	d.ImageDefaults = a.ImageDefaults()
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
func (a *Admin) putSettings(w http.ResponseWriter, r *http.Request) {
	var expect *int64
	if h := strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), `"`); h != "" {
		v, err := strconv.ParseInt(h, 10, 64)
		if err != nil || v < 0 {
			httpkit.WriteErr(w, 400, "If-Match is the revision of the settings you started from, a number")
			return
		}
		expect = &v
	}
	var body struct {
		kernel.Settings
		ClearDeciderSecret bool `json:"clearDeciderSecret"`
	}
	if err := httpkit.Decode(r, &body); err != nil {
		httpkit.WriteErr(w, 400, err.Error())
		return
	}
	s := body.Settings
	switch {
	case body.ClearDeciderSecret:
		s.DeciderSecret = ""
	case s.DeciderSecret == "":
		s.DeciderSecret = a.core(r).Settings().DeciderSecret
	}
	// Which cluster the server runs in is changed where it is shown (the central operator's address picker), not here.
	s.ServerClusterID = a.core(r).Settings().ServerClusterID
	n, rev, err := a.core(r).SaveSettingsAt(r.Context(), httpkit.Actor(r), expect, s)
	var ce *kernel.Error
	if errors.As(err, &ce) && ce.Kind == kernel.KindConflict {
		d := settingsDoc(n, rev, true)
		d.ImageDefaults = a.ImageDefaults()
		httpkit.WriteJSON(w, http.StatusConflict, struct {
			SettingsDoc
			Error string `json:"error"`
		}{d, ce.Msg})
		return
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	d := settingsDoc(n, rev, true)
	d.ImageDefaults = a.ImageDefaults()
	httpkit.WriteJSON(w, 200, d)
}

// ---- external decider ----

const (
	maxDecideRequest  = 8 << 20
	maxDecideResponse = 4 << 20
)

// decide forwards a decision request (the topology facts a decider needs) to the decider an
// administrator configured, and returns its answer untouched for the UI to validate. The address is
// never taken from the request. Only people who can edit may call it (it makes the server send the
// topology to another system), and every call is written to the audit trail: who, which organisation,
// the decider's host (never the rest of the address, which may hold a secret), how large the request was
// and what came back.
func (a *Admin) decide(w http.ResponseWriter, r *http.Request) {
	c := a.core(r)
	set := c.Settings()
	if set.DeciderURL == "" {
		httpkit.WriteErr(w, 404, "no external decider is configured")
		return
	}
	host := "?"
	if u, err := url.Parse(set.DeciderURL); err == nil {
		host = u.Hostname()
	}
	size, code := 0, 0
	defer func() {
		c.Audit(r.Context(), httpkit.Actor(r), "decide", "decider", host, fmt.Sprintf("org %s, request %d bytes, status %d", c.OrgID, size, code))
	}()
	fail := func(status int, msg string) {
		code = status
		httpkit.WriteErr(w, status, msg)
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxDecideRequest+1))
	size = len(body)
	if err != nil || len(body) > maxDecideRequest {
		fail(413, "the decision request is too large")
		return
	}
	if !json.Valid(body) {
		fail(400, "the decision request must be JSON")
		return
	}
	// The decider is only ever offered live targets: the request is checked against the effective model here, so
	// it does not depend on what the browser believed, and it says which clusters were left out and why.
	m, _, merr := a.tn(r).Hub.Model(r.Context())
	if merr != nil {
		fail(500, "the effective model could not be built")
		return
	}
	if enriched, eerr := decisionloop.EnrichDecision(body, m); eerr == nil {
		body = enriched
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(set.DeciderTimeoutSec)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, set.DeciderURL, bytes.NewReader(body))
	if err != nil {
		fail(502, "the decider address is not valid")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "continuum-server/"+a.Version)
	if set.DeciderSecret != "" {
		ts, sig := decisionloop.SignDeciderRequest(set.DeciderSecret, body, c.Now())
		req.Header.Set("X-Continuum-Timestamp", ts)
		req.Header.Set("X-Continuum-Signature", "sha256="+sig)
	}
	resp, err := c.Decider.Client(time.Duration(set.DeciderTimeoutSec) * time.Second).Do(req)
	if err != nil {
		if kernel.IsDeciderDenied(err) {
			c.Log.Warn("external decider refused by the address policy", "org", c.OrgID, "host", host, "err", err)
			fail(502, "the server is not allowed to connect to the external decider's address: it is private, loopback or link-local. An operator can allow a range with --decider-allow-cidrs")
			return
		}
		c.Log.Warn("external decider unreachable", "org", c.OrgID, "host", host, "err", scrubURLError(err))
		fail(502, "the external decider could not be reached or did not answer in time")
		return
	}
	defer resp.Body.Close()
	code = resp.StatusCode
	out, err := io.ReadAll(io.LimitReader(resp.Body, maxDecideResponse+1))
	if err != nil || len(out) > maxDecideResponse {
		fail(502, "the external decider's answer was too large")
		return
	}
	if resp.StatusCode != http.StatusOK {
		msg := fmt.Sprintf("the external decider answered with status %d", resp.StatusCode)
		httpkit.WriteErr(w, 502, msg) // the audit row keeps the decider's own status
		return
	}
	if !json.Valid(out) {
		httpkit.WriteErr(w, 502, "the external decider did not answer with JSON")
		return
	}
	name := set.DeciderName
	if name == "" {
		name = "External decider"
	}
	httpkit.WriteJSON(w, 200, map[string]any{"decider": name, "result": json.RawMessage(out)})
}

// scrubURLError keeps the address (which may carry a secret in its query) out of logs.
func scrubURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}
