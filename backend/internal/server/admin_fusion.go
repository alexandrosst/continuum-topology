package server

import (
	"net/http"
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
	// seen to answer (see operatorDoc.AddressWarnings). Absent when there is nothing to say.
	Warnings []string `json:"warnings,omitempty"`
}

type fusionResponse struct {
	FusionStatus
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
		st = FusionStatus{State: "off", Reason: "other-org", Message: "FUSION is shared by everything that sends to this server, so it is managed from the server's main organisation."}
	}
	out := fusionResponse{FusionStatus: st, Data: f != nil && (f.Org == "" || core.OrgID == f.Org)}
	if out.Data {
		out.Links = f.uiLinks(st)
	}
	if f != nil && st.Available {
		_, err := core.GetOperator(r.Context(), CentralOperatorID)
		out.Central = &fusionCentralDoc{OperatorID: CentralOperatorID, Endpoint: f.CentralEndpoint(), Exposed: f.Exposed(), Exists: err == nil, Service: f.ServiceName(), Namespace: f.Namespace, Warnings: f.AddressWarnings(r.Context())}
		if out.Central.Exists && !out.Central.Exposed && st.State != "off" {
			f.DiscoverAddressSoon(core) // the next read of this screen shows it
		}
	}
	return out
}

func (a *Admin) getFusion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, a.fusionDoc(r))
}

func (a *Admin) enableFusion(w http.ResponseWriter, r *http.Request) {
	if a.Fusion == nil {
		a.fail(w, errf(KindConflict, "%s", a.Fusion.Status(r.Context()).Message))
		return
	}
	if _, err := a.Fusion.Enable(r.Context(), a.core(r), actor(r)); err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 200, a.fusionDoc(r))
}

func (a *Admin) disableFusion(w http.ResponseWriter, r *http.Request) {
	if a.Fusion == nil {
		a.fail(w, errf(KindConflict, "%s", a.Fusion.Status(r.Context()).Message))
		return
	}
	if _, err := a.Fusion.Disable(r.Context(), a.core(r), actor(r)); err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 200, a.fusionDoc(r))
}
