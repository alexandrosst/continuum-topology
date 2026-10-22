package fusion

import (
	"context"
	"net/http"

	"continuum/internal/httpkit"
	"continuum/internal/kernel"
	"continuum/internal/operators"
)

// API is the routes of the FUSION switch: its state, turning it on and off, and the retention of its stores. They call through the
// FusionControl; the data API, the access tokens and FUSION's own pages are the shell's.
type API struct {
	// For is the FUSION view of the organisation a request is for.
	For func(r *http.Request) *Service
	// Control is the server's control of FUSION, which is nil when it was installed without it or without the permission to scale it (the
	// routes then report it unavailable, and say why). Read for every request.
	Control func() *FusionControl
	// Org is the organisation FUSION belongs to: the only one that manages it and gives out access to it.
	Org func() string
	// PrometheusPath and GrafanaPath are where the server serves FUSION's own pages; a page that is up is offered at its path.
	PrometheusPath, GrafanaPath string
	// CentralWarnings are the doubts about the public address recorded for the central operator: what an address looks like is the
	// operators' to say, so it is handed in.
	CentralWarnings func(ctx context.Context, c operators.Central) []string
}

// CentralDoc is the central operator as the UI shows it next to the switch.
type CentralDoc struct {
	OperatorID string `json:"operatorId"`
	// Endpoint is what a sending operator is pointed at; Exposed says whether that works from another cluster.
	Endpoint string `json:"endpoint"`
	Exposed  bool   `json:"exposed"`
	Exists   bool   `json:"exists"`
	// Service and Namespace name the gateway's Service, which is what an administrator reads the address from
	// once they have exposed it (kubectl get svc <service> --namespace <namespace>).
	Service   string `json:"service"`
	Namespace string `json:"namespace"`
	// Warnings are the doubts about Endpoint when it is a public address: it is something a person typed, not something
	// seen to answer. Absent when there is nothing to say.
	Warnings []string `json:"warnings,omitempty"`
}

// PageLinks are the paths the UI opens, present only for a page that is up right now.
type PageLinks struct {
	Prometheus string `json:"prometheus,omitempty"`
	Grafana    string `json:"grafana,omitempty"`
}

// PageLinks are the pages of FUSION that are up in st, at the paths the server serves them at; nil when none is, or FUSION is unavailable.
func (f *FusionControl) PageLinks(st FusionStatus, prometheusPath, grafanaPath string) *PageLinks {
	if f == nil || !st.Available {
		return nil
	}
	var l PageLinks
	if st.ComponentUp(ComponentMetrics) {
		l.Prometheus = prometheusPath
	}
	if st.ComponentUp(ComponentGrafana) {
		l.Grafana = grafanaPath
	}
	if l == (PageLinks{}) {
		return nil
	}
	return &l
}

// Doc is the switch's state for one organisation.
type Doc struct {
	FusionStatus
	Central *CentralDoc `json:"central,omitempty"`
	// Data says whether this server serves the shared data API (and its access tokens) for this organisation: it knows
	// where the stores are, whatever the switch can or cannot do.
	Data bool `json:"data"`
	// Links are the pages FUSION has of its own (Prometheus, Grafana), present only while that page is up. They are paths on
	// this server, which proxies them for an administrator.
	Links *PageLinks `json:"links,omitempty"`
}

func (h *API) control() *FusionControl {
	if h.Control == nil {
		return nil
	}
	return h.Control()
}

func (h *API) fail(w http.ResponseWriter, r *http.Request, err error) {
	httpkit.Fail(w, h.For(r).Log, err)
}

// unavailable is the error of a switch the server has none of.
func (h *API) unavailable(r *http.Request) error {
	return kernel.Errf(kernel.KindConflict, "%s", h.control().Status(r.Context()).Message)
}

// Mount puts the routes on r.
func (h *API) Mount(r httpkit.Router) {
	const o = httpkit.OrgPath
	r.Handle("GET "+o+"/fusion", httpkit.AdminRole, h.get)
	r.Handle("POST "+o+"/fusion/enable", httpkit.AdminRole, h.enable)
	r.Handle("POST "+o+"/fusion/disable", httpkit.AdminRole, h.disable)
	// FUSION access tokens: read-only credentials for the shared data API.
	r.Handle("GET "+o+"/fusion/tokens", httpkit.AdminRole, h.listTokens)
	r.Handle("POST "+o+"/fusion/tokens", httpkit.AdminRole, h.createToken)
	r.Handle("DELETE "+o+"/fusion/tokens/{id}", httpkit.AdminRole, h.deleteToken)
	r.Handle("GET "+o+"/fusion/retention", httpkit.AdminRole, h.getRetention)
	r.Handle("PUT "+o+"/fusion/retention", httpkit.AdminRole, h.setRetention)
}

// doc is the switch's state for this request's organisation.
func (h *API) doc(r *http.Request) Doc {
	svc := h.For(r)
	f := h.control()
	st := f.Status(r.Context())
	if f != nil && f.Org != "" && svc.OrgID != f.Org {
		st = FusionStatus{State: StateOff, Reason: ReasonOtherOrg, Message: "FUSION is shared by everything that sends to this server, so it is managed from the server's main organisation."}
	}
	out := Doc{FusionStatus: st, Data: f != nil && (f.Org == "" || svc.OrgID == f.Org)}
	if out.Data {
		out.Links = f.PageLinks(st, h.PrometheusPath, h.GrafanaPath)
	}
	if f != nil && st.Available {
		_, err := svc.Operators.GetOperator(r.Context(), operators.CentralOperatorID)
		out.Central = &CentralDoc{OperatorID: operators.CentralOperatorID, Endpoint: f.CentralEndpoint(), Exposed: f.Exposed(), Exists: err == nil, Service: f.ServiceName(), Namespace: f.Namespace}
		if h.CentralWarnings != nil {
			out.Central.Warnings = h.CentralWarnings(r.Context(), f.Central())
		}
		if out.Central.Exists && !out.Central.Exposed && st.State != StateOff {
			f.DiscoverAddressSoon(svc) // the next read of this screen shows it
		}
	}
	return out
}

func (h *API) get(w http.ResponseWriter, r *http.Request) {
	httpkit.WriteJSON(w, 200, h.doc(r))
}

func (h *API) enable(w http.ResponseWriter, r *http.Request) {
	if h.control() == nil {
		h.fail(w, r, h.unavailable(r))
		return
	}
	if _, err := h.control().Enable(r.Context(), h.For(r), httpkit.Actor(r)); err != nil {
		h.fail(w, r, err)
		return
	}
	httpkit.WriteJSON(w, 200, h.doc(r))
}

func (h *API) disable(w http.ResponseWriter, r *http.Request) {
	if h.control() == nil {
		h.fail(w, r, h.unavailable(r))
		return
	}
	if _, err := h.control().Disable(r.Context(), h.For(r), httpkit.Actor(r)); err != nil {
		h.fail(w, r, err)
		return
	}
	httpkit.WriteJSON(w, 200, h.doc(r))
}

// getRetention is every store's retention, volume and measured use.
func (h *API) getRetention(w http.ResponseWriter, r *http.Request) {
	if h.control() == nil {
		httpkit.WriteJSON(w, 200, UnavailableRetention(RetentionUnavailable, h.control().Status(r.Context()).Message))
		return
	}
	httpkit.WriteJSON(w, 200, h.control().Retention(r.Context(), h.For(r)))
}

// setRetention changes retention and grows volumes; the body names the stores (metrics, logs, traces) and, for
// each, the days to keep and/or the volume size to grow to.
func (h *API) setRetention(w http.ResponseWriter, r *http.Request) {
	if h.control() == nil {
		h.fail(w, r, h.unavailable(r))
		return
	}
	var req RetentionRequest
	if err := httpkit.Decode(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	doc, err := h.control().SetRetention(r.Context(), h.For(r), httpkit.Actor(r), req)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpkit.WriteJSON(w, 200, doc)
}
