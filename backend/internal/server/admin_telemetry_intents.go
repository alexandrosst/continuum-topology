package server

import (
	"fmt"
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

// telemetryIntentCommand returns the --set flags (and, for an operator destination, the kubectl secret
// command) a person runs against the intent's own agent to actually point its bundled local operator's
// export at what the intent now grants - the single-agent counterpart of operatorSourceReminders, built
// on demand rather than only once at creation/scope-update time, so it can hand out a freshly minted
// client certificate (see Core.IssueOperatorClientCert) no matter when the intent's destination was set.
// Provenance flags (telemetry.resource.orgId/clusterId/intentId - added in the chart alongside this
// intent model) are always included; an external destination gets nothing beyond them, since the
// endpoint/auth flags for that are already built client-side by the frontend's own withTelemetry().
func (a *Admin) telemetryIntentCommand(w http.ResponseWriter, r *http.Request) {
	ti, err := a.core(r).GetTelemetryIntent(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	agent, err := a.core(r).agentInOrg(r.Context(), ti.AgentID)
	if err != nil {
		a.fail(w, err)
		return
	}
	installFragment := fmt.Sprintf(
		"--set telemetry.resource.orgId=%s --set telemetry.resource.clusterId=%s --set telemetry.resource.intentId=%s",
		a.core(r).OrgID, agent.ClusterID, ti.ID)
	secretCommands := []string{}
	// Where the agent really runs (or the chart's documented defaults when it never said): the Secret below
	// and the caller's `helm upgrade` must name the same namespace, or the release cannot find its
	// certificate. Always returned so the caller does not have to guess it.
	hub := a.tn(r).Hub
	rns, rname, _ := releaseTarget(hub.NamespaceOf(agent.ID), hub.ReleaseNameOf(agent.ID))
	resp := map[string]any{"namespace": rns, "release": rname}
	if ti.Destination.Kind == store.DestinationOperator {
		op, err := a.core(r).GetOperator(r.Context(), ti.Destination.TargetOperatorID)
		if err != nil {
			a.fail(w, err)
			return
		}
		certPEM, keyPEM, caPEM, err := a.core(r).IssueOperatorClientCert(r.Context(), actor(r), op.ID)
		if err != nil {
			a.fail(w, err)
			return
		}
		setFlags, secretCmd := operatorDestinationCommand(op, certPEM, keyPEM, caPEM, rns)
		installFragment += " " + setFlags
		// Whether the person must also supply a receiver bearer token: not for an "mtls" operator, whose
		// only gate is the client certificate in the Secret above; for a "bearer" one the flags are exactly
		// what they always were and the caller still supplies the token itself.
		resp["receiverAuth"] = string(op.ReceiverAuth)
		if secretCmd != "" {
			secretCommands = append(secretCommands, secretCmd)
		}
	}
	resp["installFragment"], resp["secretCommands"] = installFragment, secretCommands
	writeJSON(w, 200, resp)
}
