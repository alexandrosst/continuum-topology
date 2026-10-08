package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"continuum/internal/fusionapi"
	"continuum/internal/store"
)

// The shared API over FUSION: one place another system reads the metrics, logs and traces FUSION saved, signal by
// signal or joined around a trace, with a credential that can read nothing else.
//
// Two kinds of caller:
//
//   - a FUSION access token ("cnf_..."): minted here by an administrator of the server's main organisation, shown once,
//     read-only, limited to the signal types, namespaces and clusters it names, and it expires. The one a script, a
//     dashboard or a decision engine holds.
//   - a person signed in (a session, or a personal access token) as an administrator of that organisation: the full
//     read, which is what lets the UI explore the data without minting a token for itself.
//
// What a caller may read is a fusionapi.Scope; the queries and the answers are checked against it in internal/fusionapi.
// The stores themselves authenticate nothing and are not exposed outside the cluster: everything goes through here.

const (
	maxFusionTokens       = 50
	maxFusionTokenName    = 64
	defaultFusionTokenTTL = 90 * 24 * time.Hour
	minFusionTokenTTL     = time.Hour
	maxFusionTokenTTL     = 365 * 24 * time.Hour
	maxScopeEntries       = 50
	fusionRequestTimeout  = 30 * time.Second
)

var (
	namespaceRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,252}$`)
	clusterRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

// FusionTokenSpec is what a new FUSION access token is allowed.
type FusionTokenSpec struct {
	Name       string
	Signals    []string // empty = all three
	Namespaces []string // empty = every namespace
	Clusters   []string // empty = every cluster
	TTL        time.Duration
}

func cleanScopeList(what string, in []string, re *regexp.Regexp) ([]string, error) {
	var out []string
	// (An empty list means "no limit", so one that was given but cleans down to nothing is refused below.)

	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || slices.Contains(out, v) {
			continue
		}
		if !re.MatchString(v) {
			return nil, errf(KindInvalid, "%q is not a valid %s", printable(v, 40), what)
		}
		out = append(out, v)
	}
	if len(in) > 0 && len(out) == 0 {
		return nil, errf(KindInvalid, "name at least one %s, or leave the list out to allow every one", what)
	}
	if len(out) > maxScopeEntries {
		return nil, errf(KindInvalid, "a token can name at most %d %ss", maxScopeEntries, what)
	}
	return out, nil
}

// CreateFusionToken mints a FUSION access token. The secret is returned once and never stored.
func (c *Core) CreateFusionToken(ctx context.Context, actor string, spec FusionTokenSpec) (string, store.FusionToken, error) {
	name := strings.TrimSpace(spec.Name)
	if name == "" || len(name) > maxFusionTokenName || printable(name, maxFusionTokenName+1) != name {
		return "", store.FusionToken{}, errf(KindInvalid, "give the token a name of up to %d characters", maxFusionTokenName)
	}
	signals, err := fusionapi.NormalizeSignals(spec.Signals)
	if err != nil {
		return "", store.FusionToken{}, errf(KindInvalid, "%v", err)
	}
	namespaces, err := cleanScopeList("namespace", spec.Namespaces, namespaceRe)
	if err != nil {
		return "", store.FusionToken{}, err
	}
	clusters, err := cleanScopeList("cluster id", spec.Clusters, clusterRe)
	if err != nil {
		return "", store.FusionToken{}, err
	}
	ttl := spec.TTL
	if ttl == 0 {
		ttl = defaultFusionTokenTTL
	}
	if ttl < minFusionTokenTTL || ttl > maxFusionTokenTTL {
		return "", store.FusionToken{}, errf(KindInvalid, "a token lasts between an hour and %d days", int(maxFusionTokenTTL.Hours()/24))
	}
	now := c.Now()
	_ = c.Store.PurgeFusionTokens(ctx, now) // a lapsed token is of no use to anyone and should not count against the cap
	existing, err := c.Store.ListFusionTokens(ctx, c.OrgID)
	if err != nil {
		return "", store.FusionToken{}, err
	}
	if len(existing) >= maxFusionTokens {
		return "", store.FusionToken{}, errf(KindConflict, "there are already %d tokens; revoke one you no longer use first", maxFusionTokens)
	}
	secret, err := NewFusionTokenSecret()
	if err != nil {
		return "", store.FusionToken{}, errf(KindInternal, "could not generate a token")
	}
	tok := store.FusionToken{ID: newFusionTokenID(), OrgID: c.OrgID, Name: name, Signals: signals, Namespaces: namespaces, Clusters: clusters,
		CreatedBy: actor, CreatedAt: now, ExpiresAt: now.Add(ttl)}
	detail := fmt.Sprintf("%s: reads %s; namespaces %s; clusters %s; expires %s", name, strings.Join(signals, ", "), scopeWords(namespaces), scopeWords(clusters), tok.ExpiresAt.Format("2006-01-02"))
	if err := c.audited(ctx, actor, "fusion-token-created", "fusion-token", tok.ID, detail, func() error {
		return c.Store.CreateFusionToken(ctx, tok, HashSecret(secret))
	}); err != nil {
		return "", store.FusionToken{}, err
	}
	return secret, tok, nil
}

func scopeWords(v []string) string {
	if len(v) == 0 {
		return "all"
	}
	return strings.Join(v, ", ")
}

// ListFusionTokens lists this organisation's tokens, never a secret.
func (c *Core) ListFusionTokens(ctx context.Context) ([]store.FusionToken, error) {
	return c.Store.ListFusionTokens(ctx, c.OrgID)
}

// DeleteFusionToken revokes a token; it stops working at once.
func (c *Core) DeleteFusionToken(ctx context.Context, actor, id string) error {
	toks, err := c.Store.ListFusionTokens(ctx, c.OrgID)
	if err != nil {
		return err
	}
	var name string
	found := false
	for _, t := range toks {
		if t.ID == id {
			name, found = t.Name, true
		}
	}
	if !found {
		return errf(KindNotFound, "no such token")
	}
	return c.audited(ctx, actor, "fusion-token-revoked", "fusion-token", id, name, func() error {
		return c.Store.DeleteFusionToken(ctx, c.OrgID, id)
	})
}

// AuthenticateFusionToken resolves a presented secret. A malformed, unknown or expired one is the same refusal.
func (c *Core) AuthenticateFusionToken(ctx context.Context, secret string) (store.FusionToken, error) {
	deny := errf(KindUnauthenticated, "sign in required")
	if !looksLikeFusionToken(secret) {
		return store.FusionToken{}, deny
	}
	h := HashSecret(secret)
	tok, err := c.Store.LookupFusionToken(ctx, h)
	if err != nil {
		return store.FusionToken{}, deny
	}
	now := c.Now()
	if !now.Before(tok.ExpiresAt) {
		return store.FusionToken{}, deny
	}
	if tok.LastUsed == nil || now.Sub(*tok.LastUsed) > touchEvery {
		_ = c.Store.TouchFusionToken(ctx, h, now)
	}
	return tok, nil
}

// ---- token routes (administrators of the FUSION organisation) ----

type fusionTokenDoc struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Signals    []string `json:"signals"`
	Namespaces []string `json:"namespaces"`
	Clusters   []string `json:"clusters"`
	CreatedBy  string   `json:"createdBy"`
	CreatedAt  string   `json:"createdAt"`
	ExpiresAt  string   `json:"expiresAt"`
	LastUsedAt string   `json:"lastUsedAt,omitempty"`
}

func orEmpty(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func toFusionTokenDoc(t store.FusionToken) fusionTokenDoc {
	return fusionTokenDoc{ID: t.ID, Name: t.Name, Signals: orEmpty(t.Signals), Namespaces: orEmpty(t.Namespaces), Clusters: orEmpty(t.Clusters),
		CreatedBy: t.CreatedBy, CreatedAt: rfc(t.CreatedAt), ExpiresAt: rfc(t.ExpiresAt), LastUsedAt: rfcp(t.LastUsed)}
}

// fusionOrg is the organisation FUSION belongs to: the one the server was told, else the server's main organisation. (Not
// a.C.OrgID alone: the server's platform-wide core has none, which once made every request look like it came from another
// organisation.)
func (a *Admin) fusionOrg() string {
	if a.Fusion != nil && a.Fusion.Org != "" {
		return a.Fusion.Org
	}
	if a.C.OrgID != "" {
		return a.C.OrgID
	}
	return a.C.MainOrg()
}

// fusionOrgOnly refuses a token request from any organisation but the one FUSION belongs to.
func (a *Admin) fusionOrgOnly(w http.ResponseWriter, r *http.Request) bool {
	if a.Fusion == nil {
		a.fail(w, errf(KindConflict, "%s", a.Fusion.Status(r.Context()).Message))
		return false
	}
	if a.core(r).OrgID != a.fusionOrg() {
		a.fail(w, errf(KindForbidden, "FUSION belongs to this server's main organisation, which is the only one that can give out access to it"))
		return false
	}
	return true
}

func (a *Admin) listFusionTokens(w http.ResponseWriter, r *http.Request) {
	if !a.fusionOrgOnly(w, r) {
		return
	}
	toks, err := a.core(r).ListFusionTokens(r.Context())
	if err != nil {
		a.fail(w, err)
		return
	}
	out := make([]fusionTokenDoc, len(toks))
	for i, t := range toks {
		out[i] = toFusionTokenDoc(t)
	}
	writeJSON(w, 200, out)
}

// createFusionToken is the one response that ever carries the raw secret.
func (a *Admin) createFusionToken(w http.ResponseWriter, r *http.Request) {
	if !a.fusionOrgOnly(w, r) {
		return
	}
	var req struct {
		Name          string   `json:"name"`
		Signals       []string `json:"signals"`
		Namespaces    []string `json:"namespaces"`
		Clusters      []string `json:"clusters"`
		ExpiresInDays int      `json:"expiresInDays"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	if req.ExpiresInDays < 0 || req.ExpiresInDays > 3650 {
		a.fail(w, errf(KindInvalid, "expiresInDays must be between 1 and %d", int(maxFusionTokenTTL.Hours()/24)))
		return
	}
	secret, tok, err := a.core(r).CreateFusionToken(r.Context(), actor(r), FusionTokenSpec{
		Name: req.Name, Signals: req.Signals, Namespaces: req.Namespaces, Clusters: req.Clusters,
		TTL: time.Duration(req.ExpiresInDays) * 24 * time.Hour,
	})
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"token": secret, "details": toFusionTokenDoc(tok)})
}

func (a *Admin) deleteFusionToken(w http.ResponseWriter, r *http.Request) {
	if !a.fusionOrgOnly(w, r) {
		return
	}
	if err := a.core(r).DeleteFusionToken(r.Context(), actor(r), r.PathValue("id")); err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- the data routes ----

// fusionCaller is who is reading and what they may read.
type fusionCaller struct {
	Kind      string // token | user
	ID        string
	Name      string
	Scope     fusionapi.Scope
	ExpiresAt *time.Time
}

type fusionHandler func(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who fusionCaller)

// fusionData authenticates the caller, rate-limits it, bounds the request, and hands the handler a client for the
// stores and the Scope it may read with.
func (a *Admin) fusionData(h fusionHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.Fusion == nil { // nothing to read, so nothing to authenticate for
			writeErr(w, http.StatusServiceUnavailable, a.Fusion.Status(r.Context()).Message)
			return
		}
		who, err := a.fusionCaller(r)
		if err != nil {
			var e *Error
			if errors.As(err, &e) && e.Kind == KindForbidden {
				writeErr(w, http.StatusForbidden, e.Msg)
				return
			}
			Metrics.authFailures.Add(1)
			// Failed authentications are throttled per address, the same as the rest of the API.
			if !a.authRL.Allow(LimitKey(a.clientIP(r))) {
				writeErr(w, http.StatusTooManyRequests, "too many failed attempts, wait a minute")
				return
			}
			writeErr(w, http.StatusUnauthorized, "sign in required")
			return
		}
		if !a.fusionRL.Allow(who.Kind + "|" + who.ID) {
			w.Header().Set("Retry-After", "5")
			writeErr(w, http.StatusTooManyRequests, "too many requests, slow down")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), fusionRequestTimeout)
		defer cancel()
		h(w, r.WithContext(ctx), a.Fusion.dataClient(), who)
	})
}

// fusionCaller works out who is asking: a FUSION token, or a signed-in administrator of the FUSION organisation.
func (a *Admin) fusionCaller(r *http.Request) (fusionCaller, error) {
	ctx := r.Context()
	deny := errf(KindUnauthenticated, "sign in required")
	var p Principal
	var err error
	if secret, ok := sessionCookie(r); ok {
		p, err = a.C.Authenticate(ctx, secret)
	} else if secret, ok := bearerToken(r); ok {
		if looksLikeFusionToken(secret) {
			tok, err := a.C.AuthenticateFusionToken(ctx, secret)
			if err != nil || tok.OrgID != a.fusionOrg() {
				return fusionCaller{}, deny
			}
			exp := tok.ExpiresAt
			return fusionCaller{Kind: "token", ID: tok.ID, Name: tok.Name, ExpiresAt: &exp,
				Scope: fusionapi.Scope{Signals: tok.Signals, Namespaces: tok.Namespaces, Clusters: tok.Clusters}}, nil
		}
		p, err = a.C.AuthenticateAPIToken(ctx, secret)
	} else {
		return fusionCaller{}, deny
	}
	if err != nil {
		return fusionCaller{}, deny
	}
	if p.User.MustChange {
		return fusionCaller{}, errf(KindForbidden, "you must choose a new password before doing anything else")
	}
	if p, err = a.C.Member(ctx, p, a.fusionOrg()); err != nil {
		return fusionCaller{}, deny
	}
	if roleRank[p.Role] < roleRank[RoleAdmin] {
		return fusionCaller{}, errf(KindForbidden, "reading FUSION's data takes an administrator of the organisation, or a FUSION access token")
	}
	return fusionCaller{Kind: "user", ID: p.User.ID, Name: p.User.Username, Scope: fusionapi.AllSignals()}, nil
}

// fusionErr writes a failure from the query package (or the request's own context).
func (a *Admin) fusionErr(w http.ResponseWriter, r *http.Request, err error) {
	var fe *fusionapi.Error
	switch {
	case errors.As(err, &fe):
		writeErr(w, fe.Status, fe.Msg)
	case errors.Is(err, context.DeadlineExceeded):
		writeErr(w, http.StatusGatewayTimeout, "FUSION took too long to answer; narrow the time range or the filters")
	case errors.Is(err, context.Canceled):
		// The caller went away; nothing to tell it.
	default:
		a.fail(w, err)
	}
}

func (a *Admin) registerFusionData(api *http.ServeMux) {
	const p = "/api/v1/fusion"
	for pattern, h := range map[string]fusionHandler{
		"GET " + p + "/status":              a.fusionStatus,
		"GET " + p + "/applications":        a.fusionApplications,
		"GET " + p + "/applications/{name}": a.fusionApplication,
		"GET " + p + "/metrics/names":       a.fusionMetricNames,
		"GET " + p + "/metrics/series":      a.fusionMetricSeries,
		"GET " + p + "/metrics/range":       a.fusionMetricRange,
		"GET " + p + "/metrics/query":       a.fusionMetricRaw("query"),
		"GET " + p + "/metrics/query_range": a.fusionMetricRaw("query_range"),
		"GET " + p + "/logs":                a.fusionLogs,
		"GET " + p + "/traces":              a.fusionTraces,
		"GET " + p + "/traces/{id}":         a.fusionTrace,
	} {
		api.Handle(pattern, a.fusionData(h))
	}
}

func (a *Admin) fusionStatus(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who fusionCaller) {
	access := map[string]any{"kind": who.Kind, "name": who.Name, "signals": who.Scope.Signals,
		"namespaces": orEmpty(who.Scope.Namespaces), "clusters": orEmpty(who.Scope.Clusters), "rawQueries": who.Scope.Unrestricted()}
	if who.ExpiresAt != nil {
		access["expiresAt"] = rfc(*who.ExpiresAt)
	}
	// A token holder learns whether the stores are up, not how the cluster is set up: the full status carries workload
	// names, the namespace and Kubernetes API error text, which are for administrators. Nor does it read the cluster for
	// them: a token can poll as fast as the rate limit lets it, so it is answered from the last read (TokenStatus).
	var st FusionStatus
	if who.Kind == "token" {
		t := a.Fusion.TokenStatus(r.Context())
		st = FusionStatus{Available: t.Available, State: t.State}
	} else {
		st = a.Fusion.Status(r.Context())
	}
	writeJSON(w, 200, map[string]any{"fusion": st, "access": access})
}

// ---- parameters ----

func fusionRange(r *http.Request, now time.Time) (fusionapi.TimeRange, error) {
	q := r.URL.Query()
	return fusionapi.ParseRange(q.Get("from"), q.Get("to"), now)
}

func metricFilter(q url.Values) fusionapi.MetricFilter {
	return fusionapi.MetricFilter{Name: q.Get("name"), NameRegex: q.Get("metric"), Service: q.Get("service"), Namespace: q.Get("namespace"),
		Pod: q.Get("pod"), Node: q.Get("node"), Cluster: q.Get("cluster")}
}

func (a *Admin) fusionApplications(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who fusionCaller) {
	tr, err := fusionRange(r, a.C.Now())
	if err != nil {
		a.fusionErr(w, r, err)
		return
	}
	apps, sources, err := c.Applications(r.Context(), who.Scope, tr)
	if err != nil {
		a.fusionErr(w, r, err)
		return
	}
	if apps == nil {
		apps = []fusionapi.Application{}
	}
	writeJSON(w, 200, map[string]any{"applications": apps, "sources": sources, "from": tr.From, "to": tr.To})
}

func (a *Admin) fusionApplication(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who fusionCaller) {
	tr, err := fusionRange(r, a.C.Now())
	if err != nil {
		a.fusionErr(w, r, err)
		return
	}
	o, err := c.ApplicationOverview(r.Context(), who.Scope, r.PathValue("name"), tr)
	if err != nil {
		a.fusionErr(w, r, err)
		return
	}
	writeJSON(w, 200, o)
}

func (a *Admin) fusionMetricNames(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who fusionCaller) {
	tr, err := fusionRange(r, a.C.Now())
	if err != nil {
		a.fusionErr(w, r, err)
		return
	}
	limit, err := fusionapi.Limit(r.URL.Query().Get("limit"), 500, 5000)
	if err != nil {
		a.fusionErr(w, r, err)
		return
	}
	names, err := c.MetricNames(r.Context(), who.Scope, metricFilter(r.URL.Query()), tr, limit)
	if err != nil {
		a.fusionErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"names": orEmpty(names)})
}

func (a *Admin) fusionMetricSeries(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who fusionCaller) {
	tr, err := fusionRange(r, a.C.Now())
	if err != nil {
		a.fusionErr(w, r, err)
		return
	}
	limit, err := fusionapi.Limit(r.URL.Query().Get("limit"), 200, 2000)
	if err != nil {
		a.fusionErr(w, r, err)
		return
	}
	series, err := c.Series(r.Context(), who.Scope, metricFilter(r.URL.Query()), tr, limit)
	if err != nil {
		a.fusionErr(w, r, err)
		return
	}
	if series == nil {
		series = []map[string]string{}
	}
	writeJSON(w, 200, map[string]any{"series": series})
}

func (a *Admin) fusionMetricRange(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who fusionCaller) {
	q := r.URL.Query()
	tr, err := fusionRange(r, a.C.Now())
	if err != nil {
		a.fusionErr(w, r, err)
		return
	}
	limit, err := fusionapi.Limit(q.Get("limit"), 20, 100)
	if err != nil {
		a.fusionErr(w, r, err)
		return
	}
	req, err := fusionapi.DurationParam(q.Get("step"))
	if err != nil {
		a.fusionErr(w, r, err)
		return
	}
	step, err := fusionapi.ChooseStep(tr, req, 120)
	if err != nil {
		a.fusionErr(w, r, err)
		return
	}
	series, truncated, err := c.MetricRange(r.Context(), who.Scope, metricFilter(q), tr, step, limit)
	if err != nil {
		a.fusionErr(w, r, err)
		return
	}
	if series == nil {
		series = []fusionapi.MetricSeries{}
	}
	writeJSON(w, 200, map[string]any{"series": series, "truncated": truncated, "stepSeconds": step.Seconds(), "from": tr.From, "to": tr.To})
}

// fusionMetricRaw is PromQL as written, answered the way Prometheus itself answers.
func (a *Admin) fusionMetricRaw(endpoint string) fusionHandler {
	return func(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who fusionCaller) {
		data, err := c.RawMetricQuery(r.Context(), who.Scope, endpoint, r.URL.Query())
		if err != nil {
			a.fusionErr(w, r, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":`))
		_, _ = w.Write(data)
		_, _ = w.Write([]byte(`}`))
	}
}

func (a *Admin) fusionLogs(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who fusionCaller) {
	q := r.URL.Query()
	tr, err := fusionRange(r, a.C.Now())
	if err != nil {
		a.fusionErr(w, r, err)
		return
	}
	limit, err := fusionapi.Limit(q.Get("limit"), 200, 2000)
	if err != nil {
		a.fusionErr(w, r, err)
		return
	}
	var order string
	switch order = q.Get("order"); order {
	case "", "newest", "oldest":
	default:
		a.fusionErr(w, r, &fusionapi.Error{Status: http.StatusBadRequest, Msg: "order must be newest or oldest"})
		return
	}
	backward := order != "oldest"
	var entries []fusionapi.LogEntry
	var truncated bool
	if raw := q.Get("query"); raw != "" {
		entries, truncated, err = c.RawLogQuery(r.Context(), who.Scope, raw, backward, tr, limit)
	} else {
		entries, truncated, err = c.Logs(r.Context(), who.Scope, fusionapi.LogFilter{
			Service: q.Get("service"), Namespace: q.Get("namespace"), Pod: q.Get("pod"), Cluster: q.Get("cluster"),
			TraceID: q.Get("trace_id"), SpanID: q.Get("span_id"), Severity: q.Get("severity"), Contains: q.Get("contains"), Backward: backward,
		}, tr, limit)
	}
	if err != nil {
		a.fusionErr(w, r, err)
		return
	}
	if entries == nil {
		entries = []fusionapi.LogEntry{}
	}
	writeJSON(w, 200, map[string]any{"entries": entries, "truncated": truncated})
}

func (a *Admin) fusionTraces(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who fusionCaller) {
	q := r.URL.Query()
	tr, err := fusionRange(r, a.C.Now())
	if err != nil {
		a.fusionErr(w, r, err)
		return
	}
	limit, err := fusionapi.Limit(q.Get("limit"), 20, 100)
	if err != nil {
		a.fusionErr(w, r, err)
		return
	}
	var traces []fusionapi.TraceSummary
	if raw := q.Get("q"); raw != "" {
		traces, err = c.RawTraceSearch(r.Context(), who.Scope, raw, tr, limit)
	} else {
		var minD, maxD time.Duration
		if minD, err = fusionapi.DurationParam(q.Get("min_duration")); err == nil {
			maxD, err = fusionapi.DurationParam(q.Get("max_duration"))
		}
		if err == nil {
			traces, err = c.SearchTraces(r.Context(), who.Scope, fusionapi.TraceFilter{
				Service: q.Get("service"), Namespace: q.Get("namespace"), Cluster: q.Get("cluster"), Name: q.Get("name"), Status: q.Get("status"),
				MinDuration: minD, MaxDuration: maxD,
			}, tr, limit)
		}
	}
	if err != nil {
		a.fusionErr(w, r, err)
		return
	}
	if traces == nil {
		traces = []fusionapi.TraceSummary{}
	}
	writeJSON(w, 200, map[string]any{"traces": traces})
}

// fusionTrace is one trace; with include=logs,metrics it is the fused object, each span carrying its log lines and
// each resource its metric series.
func (a *Admin) fusionTrace(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who fusionCaller) {
	q := r.URL.Query()
	id := r.PathValue("id")
	var opts fusionapi.FuseOptions
	fused := false
	for _, part := range strings.Split(q.Get("include"), ",") {
		switch strings.TrimSpace(part) {
		case "":
		case "logs":
			opts.Logs, fused = true, true
		case "metrics":
			opts.Metrics, fused = true, true
		default:
			a.fusionErr(w, r, &fusionapi.Error{Status: http.StatusBadRequest, Msg: "include takes logs and/or metrics"})
			return
		}
	}
	if !fused {
		tr, err := c.Trace(r.Context(), who.Scope, id)
		if err != nil {
			a.fusionErr(w, r, err)
			return
		}
		writeJSON(w, 200, tr)
		return
	}
	pad, err := fusionapi.DurationParam(q.Get("pad"))
	if err != nil {
		a.fusionErr(w, r, err)
		return
	}
	spanPad, err := fusionapi.DurationParam(q.Get("span_pad"))
	if err != nil {
		a.fusionErr(w, r, err)
		return
	}
	opts.Pad, opts.SpanPad, opts.MetricRegex = pad, spanPad, q.Get("metric")
	for _, p := range []struct {
		key string
		dst *int
		max int
	}{{"max_logs", &opts.MaxLogs, 2000}, {"max_series", &opts.MaxSeries, 100}, {"points", &opts.Points, 500}} {
		if v := q.Get(p.key); v != "" {
			n, err := fusionapi.Limit(v, 0, p.max)
			if err != nil {
				a.fusionErr(w, r, err)
				return
			}
			*p.dst = n
		}
	}
	f, err := c.FuseTrace(r.Context(), who.Scope, id, opts)
	if err != nil {
		a.fusionErr(w, r, err)
		return
	}
	writeJSON(w, 200, f)
}
