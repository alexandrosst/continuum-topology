package linksapi

import (
	"context"
	"errors"
	"net/http"
	"time"
	"unicode/utf8"

	"continuum/internal/connectivity"
	"continuum/internal/fusionapi"
	"continuum/internal/fusionhttp"
	"continuum/internal/httpkit"
	"continuum/internal/kernel"
)

// API is the links of the organisation as the server shows them: the latest figures of each watched pair for the Sites screen, the
// same figures for FUSION's data API, and the replacing of the credentials the throughput tests use. What it needs of the server is
// handed to it: the organisation of a request, the hub that keeps the live picture of the clusters, FUSION's data client, and the
// settings of the organisation FUSION belongs to.
type API struct {
	// For is the core of the organisation the request is for (its settings, its audit trail).
	For func(r *http.Request) *kernel.Core
	// Connectivity holds the sealed credentials of that organisation's links.
	Connectivity func(r *http.Request) *connectivity.Service
	// Hub is the organisation's hub.
	Hub func(r *http.Request) Hub
	// Data is the client for FUSION's stores, nil when the server has no FUSION; read per request, since FUSION can be switched on
	// and off while the server runs.
	Data func() *fusionapi.Client
	// FusionOrg is the organisation FUSION belongs to, and FusionSettings the links its settings ask to have watched.
	FusionOrg      func() string
	FusionSettings func(ctx context.Context) ([]kernel.LinkSetting, error)
	// Err writes a failure of the data API, which has its own statuses (fusionhttp.Guard.Err).
	Err func(w http.ResponseWriter, err error)
	Now func() time.Time

	readings readingsCache
}

// Hub is what the links need of the hub: the notes and the setup it works out from the live picture, and a push of the new
// configuration to the agents.
type Hub interface {
	LinkReport(ctx context.Context, links []kernel.LinkSetting, haveFusion bool) ([]connectivity.LinkNote, []connectivity.LinkSetup, error)
	PushConfigs()
}

// Mount mounts the two routes of the organisation's links.
func (x *API) Mount(r httpkit.Router) {
	const o = httpkit.OrgPath
	r.Handle("GET "+o+"/links/latest", httpkit.MemberRole, x.latest)
	r.Handle("POST "+o+"/links/rotate-credentials", httpkit.AdminRole, x.rotateCredentials)
}

// latest is GET /api/v1/orgs/{org}/links/latest.
func (x *API) latest(w http.ResponseWriter, r *http.Request) {
	now := x.Now
	doc := LinksLatestDoc{Pairs: []LinkPairLatest{}, Notes: []connectivity.LinkNote{}, Setup: []connectivity.LinkSetup{}, At: now().UTC()}
	core := x.For(r)
	links := core.Settings().Links
	// FUSION belongs to one organisation. Another one's administrator could otherwise save a pair with the FUSION organisation's
	// cluster ids and read its figures, and an organisation with no FUSION of its own would be told it has one.
	own := x.Data() != nil && core.OrgID == x.FusionOrg()
	notes, setup, err := x.Hub(r).LinkReport(r.Context(), links, own)
	switch {
	case err != nil:
		if errors.Is(r.Context().Err(), context.Canceled) {
			return
		}
		doc.SetupReason = "unavailable"
	default:
		if notes != nil {
			doc.Notes = notes
		}
		if setup != nil {
			doc.Setup = setup
		}
	}
	if !own {
		doc.Reason = "no-fusion"
		httpkit.WriteJSON(w, 200, doc)
		return
	}
	if len(links) == 0 {
		doc.Available = true
		httpkit.WriteJSON(w, 200, doc)
		return
	}
	pairs, sample, err := x.linkFigures(r.Context(), x.Data(), links)
	if err != nil {
		if errors.Is(r.Context().Err(), context.Canceled) {
			return
		}
		// What the store said is not for a viewer: it can name hosts. The administrator who needs it has FUSION's own page.
		doc.Reason = "unavailable"
		httpkit.WriteJSON(w, 200, doc)
		return
	}
	doc.Available = true
	doc.At = sample.At.UTC()
	doc.Stale = sample.Stale
	doc.Pairs = pairs
	doc.Notes = WithoutHintsOfMeasuredPairs(doc.Notes, pairs)
	httpkit.WriteJSON(w, 200, doc)
}

// FusionLinks is GET /api/v1/fusion/links: the figures of the Sites screen for a script, a dashboard or a decision engine that holds
// a FUSION access token. The pairs are those of the FUSION organisation's settings. The figures are read for everyone alike, with
// the server's own scope and fixed queries (see linkFigures), and what the caller may see is then decided here by cluster id: a
// pair is shown only when the token may read both of its clusters, and a token without the metrics signal, or one limited to
// namespaces, is refused (Scope.NeedLinks).
func (x *API) FusionLinks(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who fusionhttp.Caller) {
	if err := who.Scope.NeedLinks(); err != nil {
		x.Err(w, err)
		return
	}
	q := r.URL.Query()
	for _, k := range []string{"cluster", "peer"} {
		if v := q.Get(k); q.Has(k) && (v == "" || len(v) > 253 || !utf8.ValidString(v)) {
			x.Err(w, &fusionapi.Error{Status: http.StatusBadRequest, Msg: k + " must be 1 to 253 characters of text"})
			return
		}
	}
	cluster, peer := q.Get("cluster"), q.Get("peer")
	if peer != "" && cluster == "" {
		x.Err(w, &fusionapi.Error{Status: http.StatusBadRequest, Msg: "peer needs cluster: it names the other end of the link"})
		return
	}
	links, err := x.FusionSettings(r.Context())
	if err != nil {
		x.Err(w, err)
		return
	}
	var want []kernel.LinkSetting
	for _, l := range links {
		switch {
		case !who.Scope.LinkPairVisible(l.A, l.B):
		case cluster != "" && l.A != cluster && l.B != cluster:
		case peer != "" && l.A != peer && l.B != peer:
		default:
			want = append(want, l)
		}
	}
	doc := struct {
		Pairs []fusionLinkPair `json:"pairs"`
		At    time.Time        `json:"at"`
		// Stale says At is older than the last attempt: FUSION could not be read just now and these are the previous figures.
		Stale bool `json:"stale,omitempty"`
	}{Pairs: []fusionLinkPair{}, At: x.Now().UTC()}
	if len(want) > 0 {
		pairs, sample, err := x.linkPairReadings(r.Context(), c, want)
		if err != nil {
			switch {
			case errors.Is(r.Context().Err(), context.Canceled):
			case errors.Is(err, context.DeadlineExceeded):
				x.Err(w, err)
			default: // what the store said can name hosts
				httpkit.WriteErr(w, http.StatusServiceUnavailable, "the link figures could not be read from FUSION's metrics store")
			}
			return
		}
		doc.At, doc.Stale = sample.At.UTC(), sample.Stale
		for _, p := range pairs {
			by := p.By
			if by == nil {
				by = []string{}
			}
			doc.Pairs = append(doc.Pairs, fusionLinkPair{LinkPairLatest: p.LinkPairLatest, MeasuredBy: by})
		}
	}
	httpkit.WriteJSON(w, 200, doc)
}

// linkFigures is the one place the figures are read, for the Sites screen and for FUSION's data API alike: the latest figures of
// the listed pairs, and when FUSION was read. The queries are fixed and run with an unrestricted scope of the server's own, whoever
// asks: nothing a caller supplies reaches them, and what a caller may see of the answer is decided by the caller of this function
// (the Sites screen by the organisation, the data API by the token's clusters). One read is shared by everyone for LatestEvery.
func (x *API) linkFigures(ctx context.Context, c *fusionapi.Client, links []kernel.LinkSetting) ([]LinkPairLatest, linkSample, error) {
	pairs, sample, err := x.linkPairReadings(ctx, c, links)
	if err != nil {
		return nil, sample, err
	}
	out := make([]LinkPairLatest, len(pairs))
	for i, p := range pairs {
		out[i] = p.LinkPairLatest
	}
	return out, sample, nil
}

// linkPairReadings is linkFigures with, for each pair, which clusters' series the figures came from.
func (x *API) linkPairReadings(ctx context.Context, c *fusionapi.Client, links []kernel.LinkSetting) ([]pairReading, linkSample, error) {
	readings, sample, err := x.readings.get(ctx, x.Now, func(ctx context.Context) (fusionapi.LinkReadings, error) {
		// Seven or more queries at once would take most of the slots the other readers of FUSION share; the bulk lane leaves some.
		return c.LinkReadings(fusionapi.WithBulk(ctx), fusionapi.AllSignals())
	})
	if err != nil {
		return nil, sample, err
	}
	return readPairs(links, readings, sample.At), sample, nil
}
