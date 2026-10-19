package server

import (
	"net/http"

	"continuum/internal/fusion"
	"continuum/internal/httpkit"
	"continuum/internal/kernel"
	"continuum/internal/operators"
	"continuum/internal/operatorsapi"
)

// fusionCentralDoc is the central operator as the UI shows it next to the switch.
type fusionCentralDoc struct {
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
	// seen to answer (see operatorsapi.Doc.AddressWarnings). Absent when there is nothing to say.
	Warnings []string `json:"warnings,omitempty"`
}

type fusionResponse struct {
	fusion.FusionStatus
	Central *fusionCentralDoc `json:"central,omitempty"`
	// Data says whether this server serves the shared data API (and its access tokens) for this organisation: it knows
	// where the stores are, whatever the switch can or cannot do.
	Data bool `json:"data"`
	// Links are the pages FUSION has of its own (Prometheus, Grafana), present only while that page is up. They are paths on
	// this server, which proxies them for an administrator.
	Links *fusionUILinks `json:"links,omitempty"`
}

// fusionDoc is the switch's state for this request's organisation.
func (a *Admin) fusionDoc(r *http.Request) fusionResponse {
	core := a.core(r)
	f := a.Fusion
	st := f.Status(r.Context())
	if f != nil && f.Org != "" && core.OrgID != f.Org {
		st = fusion.FusionStatus{State: "off", Reason: "other-org", Message: "FUSION is shared by everything that sends to this server, so it is managed from the server's main organisation."}
	}
	out := fusionResponse{FusionStatus: st, Data: f != nil && (f.Org == "" || core.OrgID == f.Org)}
	if out.Data {
		out.Links = uiLinksOf(f, st)
	}
	if f != nil && st.Available {
		_, err := core.Operators.GetOperator(r.Context(), operators.CentralOperatorID)
		out.Central = &fusionCentralDoc{OperatorID: operators.CentralOperatorID, Endpoint: f.CentralEndpoint(), Exposed: f.Exposed(), Exists: err == nil, Service: f.ServiceName(), Namespace: f.Namespace, Warnings: operatorsapi.CentralAddressWarnings(r.Context(), f.Central())}
		if out.Central.Exists && !out.Central.Exposed && st.State != "off" {
			f.DiscoverAddressSoon(core.Fusion) // the next read of this screen shows it
		}
	}
	return out
}

func (a *Admin) getFusion(w http.ResponseWriter, r *http.Request) {
	httpkit.WriteJSON(w, 200, a.fusionDoc(r))
}

func (a *Admin) enableFusion(w http.ResponseWriter, r *http.Request) {
	if a.Fusion == nil {
		a.fail(w, kernel.Errf(kernel.KindConflict, "%s", a.Fusion.Status(r.Context()).Message))
		return
	}
	if _, err := a.Fusion.Enable(r.Context(), a.core(r).Fusion, httpkit.Actor(r)); err != nil {
		a.fail(w, err)
		return
	}
	httpkit.WriteJSON(w, 200, a.fusionDoc(r))
}

func (a *Admin) disableFusion(w http.ResponseWriter, r *http.Request) {
	if a.Fusion == nil {
		a.fail(w, kernel.Errf(kernel.KindConflict, "%s", a.Fusion.Status(r.Context()).Message))
		return
	}
	if _, err := a.Fusion.Disable(r.Context(), a.core(r).Fusion, httpkit.Actor(r)); err != nil {
		a.fail(w, err)
		return
	}
	httpkit.WriteJSON(w, 200, a.fusionDoc(r))
}

// getFusionRetention is every store's retention, volume and measured use.
func (a *Admin) getFusionRetention(w http.ResponseWriter, r *http.Request) {
	if a.Fusion == nil {
		httpkit.WriteJSON(w, 200, fusion.UnavailableRetention("unavailable", a.Fusion.Status(r.Context()).Message))
		return
	}
	httpkit.WriteJSON(w, 200, a.Fusion.Retention(r.Context(), a.core(r).Fusion))
}

// setFusionRetention changes retention and grows volumes; the body names the stores (metrics, logs, traces) and, for
// each, the days to keep and/or the volume size to grow to.
func (a *Admin) setFusionRetention(w http.ResponseWriter, r *http.Request) {
	if a.Fusion == nil {
		a.fail(w, kernel.Errf(kernel.KindConflict, "%s", a.Fusion.Status(r.Context()).Message))
		return
	}
	var req fusion.RetentionRequest
	if err := httpkit.Decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	doc, err := a.Fusion.SetRetention(r.Context(), a.core(r).Fusion, httpkit.Actor(r), req)
	if err != nil {
		a.fail(w, err)
		return
	}
	httpkit.WriteJSON(w, 200, doc)
}

// central is the central operator as the server runs it, or nil when the server has no FUSION to run it.
func (a *Admin) central() operators.Central { return a.Fusion.Central() }
