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
	"sync"
	"time"

	"continuum/internal/history"
	"continuum/internal/store"
)

// ---- settings ----

// SettingsDoc is Settings as the UI sees it. Only administrators see the decider's address (it may
// carry a secret in its query); everyone else learns only that one is configured. The decider secret is
// stricter still: nobody ever sees it again once saved, administrator included, the same as a password field -
// only whether one is set is exposed.
type SettingsDoc struct {
	Settings
	DeciderConfigured bool `json:"deciderConfigured"`
	DeciderSecretSet  bool `json:"deciderSecretSet"`
	// ImageDefaults is what the server's --image-* flags say: what install commands use while this organisation's
	// own image settings are empty. Derived; the UI shows it and never sends it back.
	ImageDefaults ImageConfig `json:"imageDefaults"`
}

func settingsDoc(s Settings, admin bool) SettingsDoc {
	d := SettingsDoc{Settings: s, DeciderConfigured: s.DeciderURL != "", DeciderSecretSet: s.DeciderSecret != ""}
	d.Settings.DeciderSecret = "" // write-only, always - see SettingsDoc
	if !admin {
		d.DeciderURL = ""
	}
	return d
}

func (a *Admin) getSettings(w http.ResponseWriter, r *http.Request) {
	d := settingsDoc(a.core(r).Settings(), roleRank[principal(r).Role] >= roleRank[RoleAdmin])
	d.ImageDefaults = a.imageDefaults()
	writeJSON(w, 200, d)
}

// putSettings never lets a client blank the decider secret by accident: a GET never carries it (see settingsDoc),
// so a client that reads its settings and PUTs most of it back unchanged - which is exactly what the UI does -
// naturally sends no `deciderSecret` at all, or "". Both are treated as "leave it alone". A client sets a new
// secret by sending a non-empty `deciderSecret`, and removes it, explicitly, with `clearDeciderSecret: true`.
func (a *Admin) putSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Settings
		ClearDeciderSecret bool `json:"clearDeciderSecret"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s := body.Settings
	switch {
	case body.ClearDeciderSecret:
		s.DeciderSecret = ""
	case s.DeciderSecret == "":
		s.DeciderSecret = a.core(r).Settings().DeciderSecret
	}
	n, err := a.core(r).SaveSettings(r.Context(), actor(r), s)
	if err != nil {
		a.fail(w, err)
		return
	}
	d := settingsDoc(n, true)
	d.ImageDefaults = a.imageDefaults()
	writeJSON(w, 200, d)
}

// ---- history ----

func parseTime(r *http.Request, name string) (time.Time, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must be an RFC 3339 time", name)
	}
	return t, nil
}

func (a *Admin) historyIndex(w http.ResponseWriter, r *http.Request) {
	since, err1 := parseTime(r, "since")
	until, err2 := parseTime(r, "until")
	if err := errors.Join(err1, err2); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	pts, err := a.C.Store.ListHistory(r.Context(), a.core(r).OrgID, since, until)
	if err != nil {
		a.fail(w, err)
		return
	}
	type point struct {
		At    string `json:"at"`
		Bytes int    `json:"bytes"`
	}
	out := make([]point, len(pts))
	for i, p := range pts {
		out[i] = point{rfc(p.At), p.Bytes}
	}
	set := a.core(r).Settings()
	writeJSON(w, 200, map[string]any{"points": out, "snapshotMinutes": set.SnapshotMinutes, "retentionDays": set.RetentionDays})
}

func (a *Admin) historySnapshot(w http.ResponseWriter, r *http.Request) {
	at, err := parseTime(r, "at")
	if err != nil || at.IsZero() {
		writeErr(w, 400, "at must be an RFC 3339 time")
		return
	}
	p, data, err := a.C.Store.GetHistory(r.Context(), a.core(r).OrgID, at)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, 404, "nothing was recorded at or before that time")
		return
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	t, err := history.Decode(data)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"at": rfc(p.At), "topology": t})
}

// trafficCacheTTL is how long historyTraffic's SQLite slow path reuses a computed response before
// recomputing it. Long enough to absorb a dashboard auto-refreshing this endpoint every few seconds;
// short enough that a person watching it does not see badly stale numbers.
const trafficCacheTTL = 45 * time.Second

type trafficCacheEntry struct {
	hours   int
	at      time.Time
	payload map[string]any
}

// trafficCache is a tiny per-organisation cache for historyTraffic's slow path: on a plain SQLite
// deployment (no Neo4j), that path lists up to `hours` worth of history points, downsamples to 300,
// and decodes 300 full gzip+JSON topology snapshots just to read one counter out of each - too
// expensive to redo on every request a dashboard's auto-refresh makes. It lives on the organisation's
// own *Core (see Core.ForOrg), so one organisation's cached traffic can never be served to another's.
type trafficCache struct {
	mu    sync.Mutex
	entry *trafficCacheEntry
}

func (c *trafficCache) get(hours int, now time.Time) (map[string]any, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entry == nil || c.entry.hours != hours || now.Sub(c.entry.at) > trafficCacheTTL {
		return nil, false
	}
	return c.entry.payload, true
}

func (c *trafficCache) set(hours int, now time.Time, payload map[string]any) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entry = &trafficCacheEntry{hours: hours, at: now, payload: payload}
}

func (a *Admin) historyTraffic(w http.ResponseWriter, r *http.Request) {
	hours := 24
	if v := r.URL.Query().Get("hours"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 24*30 {
			writeErr(w, 400, "hours must be between 1 and 720")
			return
		}
		hours = n
	}
	now := a.C.Now()
	core := a.core(r)
	if ts, ok := a.C.Store.(interface {
		TrafficSamples(context.Context, string, time.Time, time.Time) ([]history.Sample, bool)
	}); ok {
		if samples, ok := ts.TrafficSamples(r.Context(), core.OrgID, now.Add(-time.Duration(hours)*time.Hour), time.Time{}); ok {
			const most = 300
			if len(samples) > most {
				keep := make([]history.Sample, 0, most)
				for i := 0; i < most; i++ {
					keep = append(keep, samples[i*(len(samples)-1)/(most-1)])
				}
				samples = keep
			}
			writeJSON(w, 200, map[string]any{"hours": hours, "snapshots": len(samples), "rates": history.Rates(samples)})
			return
		}
	}
	if payload, ok := core.trafficCache.get(hours, now); ok {
		writeJSON(w, 200, payload)
		return
	}
	pts, err := a.C.Store.ListHistory(r.Context(), core.OrgID, now.Add(-time.Duration(hours)*time.Hour), time.Time{})
	if err != nil {
		a.fail(w, err)
		return
	}
	// A long window holds many snapshots; a few hundred, evenly spread, give the same averages.
	const most = 300
	if len(pts) > most {
		keep := make([]store.HistoryPoint, 0, most)
		for i := 0; i < most; i++ {
			keep = append(keep, pts[i*(len(pts)-1)/(most-1)])
		}
		pts = keep
	}
	var samples []history.Sample
	for _, p := range pts {
		_, data, err := a.C.Store.GetHistory(r.Context(), core.OrgID, p.At)
		if err != nil {
			continue
		}
		t, err := history.Decode(data)
		if err != nil {
			continue
		}
		s := history.Sample{At: p.At, Bytes: map[string]uint64{}}
		for _, d := range t.Dependencies {
			s.Bytes[d.ID] = d.Bytes
		}
		samples = append(samples, s)
	}
	payload := map[string]any{"hours": hours, "snapshots": len(samples), "rates": history.Rates(samples)}
	core.trafficCache.set(hours, now, payload)
	writeJSON(w, 200, payload)
}

type eventDoc struct {
	ID          string `json:"id"`
	At          string `json:"at"`
	Kind        string `json:"kind"`
	TargetKind  string `json:"targetKind"`
	TargetID    string `json:"targetId"`
	Name        string `json:"name"`
	ClusterID   string `json:"clusterId,omitempty"`
	ClusterName string `json:"clusterName,omitempty"`
	Detail      string `json:"detail"`
	Cause       string `json:"cause,omitempty"`
	Severity    string `json:"severity"`
}

func (a *Admin) listEvents(w http.ResponseWriter, r *http.Request) {
	since, err1 := parseTime(r, "since")
	until, err2 := parseTime(r, "until")
	if err := errors.Join(err1, err2); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if since.IsZero() {
		since = a.C.Now().Add(-7 * 24 * time.Hour)
	}
	q := store.EventQuery{Since: since, Until: until, Kind: r.URL.Query().Get("kind"), ClusterID: r.URL.Query().Get("cluster"), TargetID: r.URL.Query().Get("target")}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			q.Limit = n
		}
	}
	evs, err := a.C.Store.ListEvents(r.Context(), a.core(r).OrgID, q)
	if err != nil {
		a.fail(w, err)
		return
	}
	out := make([]eventDoc, len(evs))
	for i, e := range evs {
		out[i] = eventDoc{ID: "ev-" + itoa(e.ID), At: rfc(e.At), Kind: e.Kind, TargetKind: e.TargetKind, TargetID: e.TargetID, Name: e.Name, ClusterID: e.ClusterID,
			ClusterName: e.ClusterName, Detail: e.Detail, Cause: e.Cause, Severity: e.Severity}
	}
	writeJSON(w, 200, map[string]any{"events": out})
}

func (a *Admin) recordNow(w http.ResponseWriter, r *http.Request) {
	a.tn(r).Hub.ScanNow(r.Context())
	w.WriteHeader(http.StatusNoContent)
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
		writeErr(w, 404, "no external decider is configured")
		return
	}
	host := "?"
	if u, err := url.Parse(set.DeciderURL); err == nil {
		host = u.Hostname()
	}
	size, code := 0, 0
	defer func() {
		c.audit(r.Context(), actor(r), "decide", "decider", host, fmt.Sprintf("org %s, request %d bytes, status %d", c.OrgID, size, code))
	}()
	fail := func(status int, msg string) {
		code = status
		writeErr(w, status, msg)
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
	if enriched, eerr := enrichDecision(body, m); eerr == nil {
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
		ts, sig := signDeciderRequest(set.DeciderSecret, body, c.Now())
		req.Header.Set("X-Continuum-Timestamp", ts)
		req.Header.Set("X-Continuum-Signature", "sha256="+sig)
	}
	resp, err := c.Decider.client(time.Duration(set.DeciderTimeoutSec) * time.Second).Do(req)
	if err != nil {
		if isDeciderDenied(err) {
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
		writeErr(w, 502, msg) // the audit row keeps the decider's own status
		return
	}
	if !json.Valid(out) {
		writeErr(w, 502, "the external decider did not answer with JSON")
		return
	}
	name := set.DeciderName
	if name == "" {
		name = "External decider"
	}
	writeJSON(w, 200, map[string]any{"decider": name, "result": json.RawMessage(out)})
}

// scrubURLError keeps the address (which may carry a secret in its query) out of logs.
func scrubURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}
