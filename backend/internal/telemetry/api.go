package telemetry

import (
	"net/http"

	"continuum/internal/destination"
	"continuum/internal/httpkit"
	"continuum/internal/kernel"
	"continuum/internal/store"
)

// The HTTP side of telemetry: the intents' routes. What an intent may say, and how it is audited, is
// telemetry_intents.go's. The command that points an agent at what an intent grants is the shell's: it is built from the install settings
// and from the operators' addresses, which are not this package's.

// API is the telemetry routes. For finds the telemetry of the organisation a request is for.
type API struct {
	For func(r *http.Request) *Service
}

// Mount puts the routes on r.
func (h API) Mount(r httpkit.Router) {
	r.Handle("GET "+httpkit.OrgPath+"/telemetry-intents", httpkit.EditorRole, h.listTelemetryIntents)
	r.Handle("POST "+httpkit.OrgPath+"/telemetry-intents", httpkit.EditorRole, h.createTelemetryIntent)
	r.Handle("GET "+httpkit.OrgPath+"/telemetry-intents/{id}", httpkit.EditorRole, h.getTelemetryIntent)
	r.Handle("POST "+httpkit.OrgPath+"/telemetry-intents/{id}/scope", httpkit.EditorRole, h.updateTelemetryIntentScope)
	r.Handle("POST "+httpkit.OrgPath+"/telemetry-intents/{id}/destination", httpkit.EditorRole, h.updateTelemetryIntentDestination)
	r.Handle("POST "+httpkit.OrgPath+"/telemetry-intents/{id}/revoke", httpkit.EditorRole, h.revokeTelemetryIntent)
	r.Handle("DELETE "+httpkit.OrgPath+"/telemetry-intents/{id}", httpkit.EditorRole, h.deleteTelemetryIntent)
}

type signalGrantDoc struct {
	ID     string `json:"id"`
	Source string `json:"source"`
}

func toSignalGrantDoc(sg store.SignalGrant) signalGrantDoc {
	return signalGrantDoc{ID: sg.ID, Source: sg.Source}
}
func (d signalGrantDoc) toStore() store.SignalGrant {
	return store.SignalGrant{ID: d.ID, Source: d.Source}
}

type telemetryIntentDoc struct {
	ID          string                      `json:"id"`
	AgentID     string                      `json:"agentId"`
	Name        string                      `json:"name"`
	Status      store.TelemetryIntentStatus `json:"status"`
	Namespaces  []string                    `json:"namespaces"`
	Exclude     []string                    `json:"exclude"`
	Signals     []signalGrantDoc            `json:"signals"`
	Destination destination.Doc             `json:"destination"`
	// Routes is a destination per signal type (metrics, logs, traces) that has one of its own; absent when
	// every signal goes to Destination.
	Routes    map[string]destination.Doc `json:"routes,omitempty"`
	CreatedAt string                     `json:"createdAt"`
	CreatedBy string                     `json:"createdBy"`
	RevokedAt string                     `json:"revokedAt,omitempty"`
	Reason    string                     `json:"reason,omitempty"`
}

func toTelemetryIntentDoc(ti store.TelemetryIntent) telemetryIntentDoc {
	d := telemetryIntentDoc{
		ID: ti.ID, AgentID: ti.AgentID, Name: ti.Name, Status: ti.Status,
		Namespaces: ti.Namespaces, Exclude: ti.Exclude, Destination: destination.Of(ti.Destination),
		CreatedAt: kernel.RFC(ti.CreatedAt), CreatedBy: ti.CreatedBy, Reason: ti.Reason,
	}
	if len(ti.Routes) > 0 {
		d.Routes = map[string]destination.Doc{}
		for m, r := range ti.Routes {
			d.Routes[string(m)] = destination.Of(r)
		}
	}
	if d.Namespaces == nil {
		d.Namespaces = []string{}
	}
	if d.Exclude == nil {
		d.Exclude = []string{}
	}
	d.Signals = []signalGrantDoc{}
	for _, sg := range ti.Signals {
		d.Signals = append(d.Signals, toSignalGrantDoc(sg))
	}
	if ti.RevokedAt != nil {
		d.RevokedAt = kernel.RFC(*ti.RevokedAt)
	}
	return d
}

func signalGrantsFromDoc(in []signalGrantDoc) []store.SignalGrant {
	out := make([]store.SignalGrant, len(in))
	for i, sg := range in {
		out[i] = sg.toStore()
	}
	return out
}

// listTelemetryIntents lists one agent's intents when ?agentId= is given, or every intent in the
// organisation otherwise - the same optional-filter convention admin_history.go's own query endpoints use.
func (h API) listTelemetryIntents(w http.ResponseWriter, r *http.Request) {
	s := h.For(r)
	var ints []store.TelemetryIntent
	var err error
	if agentID := r.URL.Query().Get("agentId"); agentID != "" {
		ints, err = s.ListTelemetryIntentsForAgent(r.Context(), agentID)
	} else {
		ints, err = s.ListTelemetryIntents(r.Context())
	}
	if err != nil {
		httpkit.Fail(w, s.Log, err)
		return
	}
	out := []telemetryIntentDoc{}
	for _, ti := range ints {
		out = append(out, toTelemetryIntentDoc(ti))
	}
	httpkit.WriteJSON(w, 200, out)
}

func (h API) getTelemetryIntent(w http.ResponseWriter, r *http.Request) {
	s := h.For(r)
	ti, err := s.GetTelemetryIntent(r.Context(), r.PathValue("id"))
	if err != nil {
		httpkit.Fail(w, s.Log, err)
		return
	}
	httpkit.WriteJSON(w, 200, toTelemetryIntentDoc(ti))
}

func (h API) createTelemetryIntent(w http.ResponseWriter, r *http.Request) {
	s := h.For(r)
	var req createTelemetryIntentRequest
	if err := httpkit.Decode(r, &req); err != nil {
		httpkit.Fail(w, s.Log, err)
		return
	}
	var routes map[store.Modality]store.Destination
	if len(req.Routes) > 0 {
		routes = make(map[store.Modality]store.Destination, len(req.Routes))
		for m, d := range req.Routes {
			routes[store.Modality(m)] = d.ToStore()
		}
	}
	ti, err := s.CreateTelemetryIntentWithRoutes(r.Context(), httpkit.Actor(r), req.AgentID, req.Name, req.Namespaces, req.Exclude, signalGrantsFromDoc(req.Signals), req.Destination.ToStore(), routes)
	if err != nil {
		httpkit.Fail(w, s.Log, err)
		return
	}
	httpkit.WriteJSON(w, 201, toTelemetryIntentDoc(ti))
}

func (h API) updateTelemetryIntentScope(w http.ResponseWriter, r *http.Request) {
	s := h.For(r)
	var req telemetryIntentScopeRequest
	if err := httpkit.Decode(r, &req); err != nil {
		httpkit.Fail(w, s.Log, err)
		return
	}
	id := r.PathValue("id")
	if err := s.UpdateTelemetryIntentScope(r.Context(), httpkit.Actor(r), id, req.Namespaces, req.Exclude, signalGrantsFromDoc(req.Signals)); err != nil {
		httpkit.Fail(w, s.Log, err)
		return
	}
	ti, err := s.GetTelemetryIntent(r.Context(), id)
	if err != nil {
		httpkit.Fail(w, s.Log, err)
		return
	}
	httpkit.WriteJSON(w, 200, toTelemetryIntentDoc(ti))
}

func (h API) updateTelemetryIntentDestination(w http.ResponseWriter, r *http.Request) {
	s := h.For(r)
	var req telemetryIntentDestinationRequest
	if err := httpkit.Decode(r, &req); err != nil {
		httpkit.Fail(w, s.Log, err)
		return
	}
	id := r.PathValue("id")
	var err error
	if req.Routes != nil {
		routes := make(map[store.Modality]store.Destination, len(req.Routes))
		for m, d := range req.Routes {
			routes[store.Modality(m)] = d.ToStore()
		}
		err = s.UpdateTelemetryIntentDestinations(r.Context(), httpkit.Actor(r), id, req.Destination.ToStore(), routes)
	} else {
		err = s.UpdateTelemetryIntentDestination(r.Context(), httpkit.Actor(r), id, req.Destination.ToStore())
	}
	if err != nil {
		httpkit.Fail(w, s.Log, err)
		return
	}
	ti, err := s.GetTelemetryIntent(r.Context(), id)
	if err != nil {
		httpkit.Fail(w, s.Log, err)
		return
	}
	httpkit.WriteJSON(w, 200, toTelemetryIntentDoc(ti))
}

func (h API) revokeTelemetryIntent(w http.ResponseWriter, r *http.Request) {
	s := h.For(r)
	httpkit.Reasoned(w, r, s.Log, func(reason string) error {
		return s.RevokeTelemetryIntent(r.Context(), httpkit.Actor(r), r.PathValue("id"), reason)
	})
}

func (h API) deleteTelemetryIntent(w http.ResponseWriter, r *http.Request) {
	s := h.For(r)
	if err := s.DeleteTelemetryIntent(r.Context(), httpkit.Actor(r), r.PathValue("id")); err != nil {
		httpkit.Fail(w, s.Log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
