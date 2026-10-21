package decisionloop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"continuum/internal/httpkit"
	"continuum/internal/kernel"
)

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
func (h API) decide(w http.ResponseWriter, r *http.Request) {
	c := h.For(r)
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
	m, merr := h.Model(r)
	if merr != nil {
		fail(500, "the effective model could not be built")
		return
	}
	if enriched, eerr := EnrichDecision(body, m); eerr == nil {
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
	req.Header.Set("User-Agent", "continuum-server/"+h.Version)
	if set.DeciderSecret != "" {
		ts, sig := SignDeciderRequest(set.DeciderSecret, body, c.Now())
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
	httpkit.WriteJSON(w, 200, externalDecisionDoc{Decider: name, Result: json.RawMessage(out)})
}

// scrubURLError keeps the address (which may carry a secret in its query) out of logs.
func scrubURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}
