package server

import (
	"net/http"

	"continuum/internal/store"
)

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
	ID          string           `json:"id"`
	AgentID     string           `json:"agentId"`
	Name        string           `json:"name"`
	Status      string           `json:"status"`
	Namespaces  []string         `json:"namespaces"`
	Exclude     []string         `json:"exclude"`
	Signals     []signalGrantDoc `json:"signals"`
	Destination destinationDoc   `json:"destination"`
	CreatedAt   string           `json:"createdAt"`
	CreatedBy   string           `json:"createdBy"`
	RevokedAt   string           `json:"revokedAt,omitempty"`
	Reason      string           `json:"reason,omitempty"`
}

func toTelemetryIntentDoc(ti store.TelemetryIntent) telemetryIntentDoc {
	d := telemetryIntentDoc{
		ID: ti.ID, AgentID: ti.AgentID, Name: ti.Name, Status: string(ti.Status),
		Namespaces: ti.Namespaces, Exclude: ti.Exclude, Destination: toDestinationDoc(ti.Destination),
		CreatedAt: rfc(ti.CreatedAt), CreatedBy: ti.CreatedBy, Reason: ti.Reason,
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
		d.RevokedAt = rfc(*ti.RevokedAt)
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
func (a *Admin) listTelemetryIntents(w http.ResponseWriter, r *http.Request) {
	var ints []store.TelemetryIntent
	var err error
	if agentID := r.URL.Query().Get("agentId"); agentID != "" {
		ints, err = a.core(r).ListTelemetryIntentsForAgent(r.Context(), agentID)
	} else {
		ints, err = a.core(r).ListTelemetryIntents(r.Context())
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	out := []telemetryIntentDoc{}
	for _, ti := range ints {
		out = append(out, toTelemetryIntentDoc(ti))
	}
	writeJSON(w, 200, out)
}

func (a *Admin) getTelemetryIntent(w http.ResponseWriter, r *http.Request) {
	ti, err := a.core(r).GetTelemetryIntent(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 200, toTelemetryIntentDoc(ti))
}

func (a *Admin) createTelemetryIntent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AgentID     string           `json:"agentId"`
		Name        string           `json:"name"`
		Namespaces  []string         `json:"namespaces"`
		Exclude     []string         `json:"exclude"`
		Signals     []signalGrantDoc `json:"signals"`
		Destination destinationDoc   `json:"destination"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	ti, err := a.core(r).CreateTelemetryIntent(r.Context(), actor(r), req.AgentID, req.Name, req.Namespaces, req.Exclude, signalGrantsFromDoc(req.Signals), req.Destination.toStore())
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 201, toTelemetryIntentDoc(ti))
}

func (a *Admin) updateTelemetryIntentScope(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Namespaces []string         `json:"namespaces"`
		Exclude    []string         `json:"exclude"`
		Signals    []signalGrantDoc `json:"signals"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	id := r.PathValue("id")
	if err := a.core(r).UpdateTelemetryIntentScope(r.Context(), actor(r), id, req.Namespaces, req.Exclude, signalGrantsFromDoc(req.Signals)); err != nil {
		a.fail(w, err)
		return
	}
	ti, err := a.core(r).GetTelemetryIntent(r.Context(), id)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 200, toTelemetryIntentDoc(ti))
}

func (a *Admin) updateTelemetryIntentDestination(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Destination destinationDoc `json:"destination"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	id := r.PathValue("id")
	if err := a.core(r).UpdateTelemetryIntentDestination(r.Context(), actor(r), id, req.Destination.toStore()); err != nil {
		a.fail(w, err)
		return
	}
	ti, err := a.core(r).GetTelemetryIntent(r.Context(), id)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 200, toTelemetryIntentDoc(ti))
}

func (a *Admin) revokeTelemetryIntent(w http.ResponseWriter, r *http.Request) {
	a.reasoned(w, r, func(reason string) error {
		return a.core(r).RevokeTelemetryIntent(r.Context(), actor(r), r.PathValue("id"), reason)
	})
}

func (a *Admin) deleteTelemetryIntent(w http.ResponseWriter, r *http.Request) {
	if err := a.core(r).DeleteTelemetryIntent(r.Context(), actor(r), r.PathValue("id")); err != nil {
		a.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
