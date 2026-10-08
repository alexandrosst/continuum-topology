package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
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

// registerFusionData mounts the data API from the one table that also describes it (fusion_ops.go), so a route cannot
// exist without being documented.
func (a *Admin) registerFusionData(api *http.ServeMux) {
	for _, op := range fusionOps(a) {
		api.Handle(op.Method+" "+fusionAPIPath+op.Path, a.fusionData(op.Handler))
		if op.AlsoPost {
			api.Handle("POST "+fusionAPIPath+op.Path, a.fusionData(op.Handler))
		}
	}
	a.registerFusionDocs(api)
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

// fusionScope is the Scope a structured list read runs with: the caller's own, narrowed to one Ikhnos application when the
// request names one (application=<id or name>). The application is resolved here, at read time, from what Ikhnos knows
// now, so changing an application changes what it shows without touching any stored telemetry. The caller's own Scope
// (who.Scope) is what everything not narrowed keeps using: reading one trace, and the fused joins around a search hit.
func (a *Admin) fusionScope(r *http.Request, who fusionCaller) (fusionapi.Scope, error) {
	ref := r.URL.Query().Get("application")
	if ref == "" {
		return who.Scope, nil
	}
	ex := a.fusionExtras()
	if ex == nil {
		return fusionapi.Scope{}, &fusionapi.Error{Status: http.StatusServiceUnavailable, Msg: "this server does not know the applications"}
	}
	groups, err := ex.Applications(r.Context())
	if err != nil {
		return fusionapi.Scope{}, err
	}
	g, err := fusionapi.FindGroup(groups, ref)
	if err != nil {
		return fusionapi.Scope{}, err
	}
	return who.Scope.FocusOn(g)
}

// fusionNoApplication refuses the application filter on a read that cannot carry it: a query written by the caller is sent
// as it is, so there is nothing to add the application's services to.
func fusionNoApplication(r *http.Request) error {
	if r.URL.Query().Has("application") {
		return &fusionapi.Error{Status: http.StatusBadRequest, Msg: "application narrows the structured filters; it cannot be combined with a query written as PromQL, LogQL or TraceQL (add the application's services to your own query)"}
	}
	return nil
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
	scope, err := a.fusionScope(r, who)
	if err != nil {
		a.fusionErr(w, r, err)
		return
	}
	apps, sources, err := c.Applications(r.Context(), scope, tr)
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
	scope, err := a.fusionScope(r, who)
	if err != nil {
		a.fusionErr(w, r, err)
		return
	}
	names, err := c.MetricNames(r.Context(), scope, metricFilter(r.URL.Query()), tr, limit)
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
	scope, err := a.fusionScope(r, who)
	if err != nil {
		a.fusionErr(w, r, err)
		return
	}
	series, err := c.Series(r.Context(), scope, metricFilter(r.URL.Query()), tr, limit)
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
	scope, err := a.fusionScope(r, who)
	if err != nil {
		a.fusionErr(w, r, err)
		return
	}
	series, truncated, err := c.MetricRange(r.Context(), scope, metricFilter(q), tr, step, limit)
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
		if err := fusionNoApplication(r); err != nil {
			a.fusionErr(w, r, err)
			return
		}
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
		if err = fusionNoApplication(r); err == nil {
			entries, truncated, err = c.RawLogQuery(r.Context(), who.Scope, raw, backward, tr, limit)
		}
	} else if scope, serr := a.fusionScope(r, who); serr != nil {
		err = serr
	} else {
		entries, truncated, err = c.Logs(r.Context(), scope, fusionapi.LogFilter{
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
	opts, fused, err := fusionapi.ParseFuseParams(q)
	if err != nil {
		a.fusionErr(w, r, err)
		return
	}
	opts.Extras = a.fusionExtras()
	stream, err := wantsStream(r)
	if err != nil {
		a.fusionErr(w, r, err)
		return
	}
	def, max := 20, 100
	if fused { // each hit is read in full, so a fused search is a smaller page
		def, max = 10, fusionapi.MaxBatch
	}
	limit, err := fusionapi.Limit(q.Get("limit"), def, max)
	if err != nil {
		a.fusionErr(w, r, err)
		return
	}
	var traces []fusionapi.TraceSummary
	if raw := q.Get("q"); raw != "" {
		if err = fusionNoApplication(r); err == nil {
			traces, err = c.RawTraceSearch(r.Context(), who.Scope, raw, tr, limit)
		}
	} else {
		var minD, maxD time.Duration
		if minD, err = fusionapi.DurationParam(q.Get("min_duration")); err == nil {
			maxD, err = fusionapi.DurationParam(q.Get("max_duration"))
		}
		var scope fusionapi.Scope
		if err == nil {
			scope, err = a.fusionScope(r, who)
		}
		if err == nil {
			traces, err = c.SearchTraces(r.Context(), scope, fusionapi.TraceFilter{
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
	if !fused {
		writeJSON(w, 200, map[string]any{"traces": traces})
		return
	}
	ids := make([]string, len(traces))
	for i, t := range traces {
		ids[i] = t.TraceID
	}
	if len(ids) > 0 && !a.fusionBulkAllowed(w, who, len(ids)) {
		return
	}
	if stream {
		a.streamBulk(w, r, c, who, ids, opts, map[string]any{"type": "traces", "traces": traces})
		return
	}
	items := c.FuseMany(r.Context(), who.Scope, ids, opts, nil)
	writeJSON(w, 200, map[string]any{"traces": traces, "results": nonNilItems(items), "summary": fusionapi.Summarise(items)})
}

// fusionTrace is one trace; with fused=true (or an include list) it is the fused object: each span carrying its log
// lines and each resource its metric series, and whatever else the options ask for.
func (a *Admin) fusionTrace(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who fusionCaller) {
	id := r.PathValue("id")
	opts, fused, err := fusionapi.ParseFuseParams(r.URL.Query())
	if err != nil {
		a.fusionErr(w, r, err)
		return
	}
	opts.Extras = a.fusionExtras()
	if !fused {
		tr, err := c.Trace(r.Context(), who.Scope, id)
		if err != nil {
			a.fusionErr(w, r, err)
			return
		}
		writeJSON(w, 200, tr)
		return
	}
	f, err := c.FuseTrace(r.Context(), who.Scope, id, opts)
	if err != nil {
		a.fusionErr(w, r, err)
		return
	}
	writeJSON(w, 200, f)
}

// maxBatchBody bounds a batch request: 25 ids and some options are far smaller.
const maxBatchBody = 64 << 10

// fusionTraceBatch reads many traces in one request. The body names the ids and any option the single read takes
// (include, pad, metric, omit, span_status ...), so a script states once what it wants from all of them.
func (a *Admin) fusionTraceBatch(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who fusionCaller) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBatchBody)
	dec := json.NewDecoder(r.Body)
	dec.UseNumber()
	var body map[string]any
	if err := dec.Decode(&body); err != nil {
		a.fusionErr(w, r, &fusionapi.Error{Status: http.StatusBadRequest, Msg: "the body must be a JSON object such as {\"ids\": [\"<trace id>\"], \"include\": [\"logs\", \"metrics\"]}"})
		return
	}
	q := r.URL.Query() // options given in the query string are defaults the body overrides
	for k, v := range body {
		if k != "ids" && k != "stream" && !slices.Contains(fusionapi.FuseParamNames, k) {
			a.fusionErr(w, r, &fusionapi.Error{Status: http.StatusBadRequest, Msg: fmt.Sprintf("unknown field %q", printable(k, 40))})
			return
		}
		if k == "fused" {
			a.fusionErr(w, r, &fusionapi.Error{Status: http.StatusBadRequest, Msg: "a batch is always fused; use include=none for the traces alone"})
			return
		}
		if k == fusionapi.ParamPromQL { // queries hold commas, so they are not a comma-joined list
			qs, err := promqlValues(v)
			if err != nil {
				a.fusionErr(w, r, &fusionapi.Error{Status: http.StatusBadRequest, Msg: fmt.Sprintf("%s: %v", k, err)})
				return
			}
			q[k] = qs
			continue
		}
		str, err := paramString(v)
		if err != nil {
			a.fusionErr(w, r, &fusionapi.Error{Status: http.StatusBadRequest, Msg: fmt.Sprintf("%s: %v", k, err)})
			return
		}
		q.Set(k, str)
	}
	if q.Has("fused") {
		a.fusionErr(w, r, &fusionapi.Error{Status: http.StatusBadRequest, Msg: "a batch is always fused; use include=none for the traces alone"})
		return
	}
	ids, err := fusionapi.NormalizeIDs(strings.Split(q.Get("ids"), ","))
	if err != nil {
		a.fusionErr(w, r, err)
		return
	}
	opts, err := fusionapi.ParseFuseOptions(q)
	if err != nil {
		a.fusionErr(w, r, err)
		return
	}
	opts.Extras = a.fusionExtras()
	stream := false
	if v := q.Get("stream"); v != "" {
		if stream, err = fusionapi.ParseBool("stream", v, false); err != nil {
			a.fusionErr(w, r, err)
			return
		}
	} else if stream, err = wantsStream(r); err != nil {
		a.fusionErr(w, r, err)
		return
	}
	if !a.fusionBulkAllowed(w, who, len(ids)) {
		return
	}
	if stream {
		a.streamBulk(w, r, c, who, ids, opts, nil)
		return
	}
	items := c.FuseMany(r.Context(), who.Scope, ids, opts, nil)
	writeJSON(w, 200, map[string]any{"results": nonNilItems(items), "summary": fusionapi.Summarise(items)})
}

// promqlValues turns the promql field of a batch body into the repeated name=expression values the shared parser reads:
// an object {"name": "expression"}, a list of "name=expression" strings, or one such string.
func promqlValues(v any) ([]string, error) {
	switch x := v.(type) {
	case string:
		return []string{x}, nil
	case []any:
		out := make([]string, len(x))
		for i, e := range x {
			s, ok := e.(string)
			if !ok {
				return nil, errors.New("a list holds only \"name=expression\" strings")
			}
			out[i] = s
		}
		return out, nil
	case map[string]any:
		names := make([]string, 0, len(x))
		for n := range x {
			names = append(names, n)
		}
		slices.Sort(names)
		out := make([]string, 0, len(x))
		for _, n := range names {
			e, ok := x[n].(string)
			if !ok {
				return nil, errors.New("an object maps each query name to its expression")
			}
			out = append(out, n+"="+e)
		}
		return out, nil
	case nil:
		return nil, nil
	}
	return nil, errors.New("must be an object of name to expression, or a list of \"name=expression\" strings")
}

// paramString turns a JSON value from a batch body into the string the shared parser reads: lists are comma-joined.
func paramString(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case bool:
		return strconv.FormatBool(x), nil
	case json.Number:
		return x.String(), nil
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			s, ok := e.(string)
			if !ok {
				return "", errors.New("a list holds only strings")
			}
			parts[i] = s
		}
		return strings.Join(parts, ","), nil
	case nil:
		return "", nil
	}
	return "", errors.New("must be a string, number, boolean or list of strings")
}

func nonNilItems(items []fusionapi.BulkItem) []fusionapi.BulkItem {
	if items == nil {
		return []fusionapi.BulkItem{}
	}
	return items
}

// fusionBulkAllowed charges the caller's rate limit for the traces beyond the first: reading 25 is 25 reads, whatever
// the number of requests.
func (a *Admin) fusionBulkAllowed(w http.ResponseWriter, who fusionCaller, n int) bool {
	for i := 1; i < n; i++ {
		if !a.fusionRL.Allow(who.Kind + "|" + who.ID) {
			w.Header().Set("Retry-After", "5")
			writeErr(w, http.StatusTooManyRequests, "too many requests, slow down; read fewer traces at a time")
			return false
		}
	}
	return true
}

// wantsStream says whether the caller asked for NDJSON: stream=true, or an Accept header that names it.
func wantsStream(r *http.Request) (bool, error) {
	if v := r.URL.Query().Get("stream"); v != "" {
		return fusionapi.ParseBool("stream", v, false)
	}
	return strings.Contains(r.Header.Get("Accept"), "application/x-ndjson"), nil
}

// streamBulk answers a bulk read as newline-delimited JSON: an optional first line (the search hits), then one line per
// trace as soon as it is read - in the order they finish, not the order asked - and a last line that counts them. A
// caller can start on the first trace while the rest are still being read, and a deadline costs it only the traces not
// yet done. The status is 200 once the first line is out; each line carries its own status.
func (a *Admin) streamBulk(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who fusionCaller, ids []string, opts fusionapi.FuseOptions, head any) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("X-Accel-Buffering", "no") // a reverse proxy must pass lines on as they come
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	fl, _ := w.(http.Flusher)
	flush := func() {
		if fl != nil {
			fl.Flush()
		}
	}
	if head != nil {
		_ = enc.Encode(head)
		flush()
	}
	type line struct {
		Type string `json:"type"`
		fusionapi.BulkItem
	}
	items := c.FuseMany(r.Context(), who.Scope, ids, opts, func(it fusionapi.BulkItem) { // (called one at a time)
		_ = enc.Encode(line{Type: "result", BulkItem: it})
		flush()
	})
	_ = enc.Encode(struct {
		Type string `json:"type"`
		fusionapi.BulkSummary
	}{"summary", fusionapi.Summarise(items)})
	flush()
}

// fusionGroups lists the Ikhnos applications the application filter accepts, each with the services it is made of, the
// namespaces and clusters they run in, and the service names its telemetry may carry. A caller limited to certain
// namespaces or clusters sees only the applications that have a service in them, and only those services.
func (a *Admin) fusionGroups(w http.ResponseWriter, r *http.Request, _ *fusionapi.Client, who fusionCaller) {
	ex := a.fusionExtras()
	if ex == nil {
		a.fusionErr(w, r, &fusionapi.Error{Status: http.StatusServiceUnavailable, Msg: "this server does not know the applications"})
		return
	}
	groups, err := ex.Applications(r.Context())
	if err != nil {
		a.fusionErr(w, r, err)
		return
	}
	type view struct {
		ID           string                `json:"id"`
		Name         string                `json:"name"`
		Description  string                `json:"description,omitempty"`
		Services     []fusionapi.AppMember `json:"services"`
		ServiceNames []string              `json:"serviceNames"`
		Namespaces   []string              `json:"namespaces"`
		Clusters     []string              `json:"clusters"`
	}
	out := []view{}
	for _, g := range groups {
		kept := g
		kept.Members = nil
		for _, m := range g.Members {
			if who.Scope.NamespaceVisible(m.Namespace) && who.Scope.ClusterVisible(m.Cluster) {
				kept.Members = append(kept.Members, m)
			}
		}
		if len(kept.Members) == 0 && len(g.Members) > 0 {
			continue // nothing of it is in the caller's scope
		}
		if kept.Members == nil {
			kept.Members = []fusionapi.AppMember{}
		}
		out = append(out, view{ID: g.ID, Name: g.Name, Description: g.Description, Services: kept.Members, ServiceNames: orEmpty(kept.ServiceNames()),
			Namespaces: orEmpty(kept.Namespaces()), Clusters: orEmpty(kept.Clusters())})
	}
	writeJSON(w, 200, map[string]any{"groups": out})
}
