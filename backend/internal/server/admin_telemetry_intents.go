package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"continuum/internal/agents"
	"continuum/internal/httpkit"
	"continuum/internal/install"
	"continuum/internal/kernel"
	"continuum/internal/store"
)

// telemetryIntentCommand returns the --set flags (and, for an operator destination, the kubectl secret
// command) a person runs against the intent's own agent to actually point its bundled local operator's
// export at what the intent now grants - the single-agent counterpart of operatorSourceReminders, built
// on demand rather than only once at creation/scope-update time, so it can hand out a freshly minted
// client certificate (see Core.IssueOperatorClientCert) no matter when the intent's destination was set.
// Provenance flags (telemetry.resource.orgId/clusterId/intentId - added in the chart alongside this
// intent model) are always included; an external destination gets nothing beyond them, since the
// endpoint/auth flags for that are already built client-side by the frontend's own withTelemetry().
// intentProvenanceValues are the values that stamp who an agent's telemetry belongs to on everything it sends, whatever a person adds
// to the pipeline themselves: the chart puts them in a processor that runs last.
func intentProvenanceValues(orgID, clusterID, intentID string) []install.Value {
	return []install.Value{
		install.Set("telemetry.resource.orgId", orgID),
		install.Set("telemetry.resource.clusterId", clusterID),
		install.Set("telemetry.resource.intentId", intentID),
	}
}

// intentCommandRequest is what the page may add to ask for the whole block instead of the parts: the telemetry it is editing, in the
// words of install.Telemetry. Without it the answer is the parts alone, as it always was.
type intentCommandRequest struct {
	Telemetry *install.Telemetry `json:"telemetry"`
}

// decodeOptional reads a body that may be absent: no body at all is no request, not a bad one.
func decodeOptional(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return kernel.Errf(kernel.KindInvalid, "invalid request body")
	}
	return nil
}

// operatorTelemetry is the page's telemetry as the intent has it: which destinations are regional operators is the intent's word, not
// the page's (the page may be showing settings the intent no longer holds), and how each operator's receiver authenticates comes from the
// operator itself. A page whose settings and the intent disagree about whether signal types go separately is told to start again, never
// given a command for something else.
func (a *Admin) operatorTelemetry(r *http.Request, ti store.TelemetryIntent, spec install.Telemetry) (install.Telemetry, map[string]string, error) {
	auth := map[string]string{}
	operatorOf := func(d store.Destination) (string, error) {
		if d.Kind != store.DestinationOperator {
			return "", nil
		}
		if _, ok := auth[d.TargetOperatorID]; !ok {
			op, err := a.core(r).Operators.GetOperator(r.Context(), d.TargetOperatorID)
			if err != nil {
				return "", err
			}
			auth[op.ID] = string(op.ReceiverAuth)
		}
		return d.TargetOperatorID, nil
	}
	if spec.Routes.Split != (len(ti.Routes) > 0) {
		return spec, nil, kernel.Errf(kernel.KindConflict, "these settings and the telemetry intent disagree about sending each signal type separately; save the intent again")
	}
	var err error
	if !spec.Routes.Split {
		spec.Destination.Operator, err = operatorOf(ti.Destination)
		return spec, auth, err
	}
	for _, m := range spec.ActiveLanes() {
		d, ok := ti.Routes[store.Modality(m)]
		if !ok {
			return spec, nil, kernel.Errf(kernel.KindConflict, "the telemetry intent has no destination for %s; save the intent again", m)
		}
		id, err := operatorOf(d)
		if err != nil {
			return spec, nil, err
		}
		switch m {
		case install.Metrics:
			spec.Routes.Metrics.Operator = id
		case install.Logs:
			spec.Routes.Logs.Operator = id
		case install.Traces:
			spec.Routes.Traces.Operator = id
		}
	}
	return spec, auth, nil
}

func (a *Admin) telemetryIntentCommand(w http.ResponseWriter, r *http.Request) {
	var ask intentCommandRequest
	if err := decodeOptional(r, &ask); err != nil {
		a.fail(w, err)
		return
	}
	ti, err := a.core(r).Telemetry.GetTelemetryIntent(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	// A revoked intent grants nothing: handing out a command (and a freshly issued client certificate) for it would
	// quietly restore what was taken away.
	if ti.Status != store.TelemetryIntentActive {
		a.fail(w, kernel.Errf(kernel.KindConflict, "this telemetry intent is %s; create a new one to get a command", ti.Status))
		return
	}
	agent, err := a.core(r).AgentInOrg(r.Context(), ti.AgentID)
	if err != nil {
		a.fail(w, err)
		return
	}
	values := intentProvenanceValues(a.core(r).OrgID, agent.ClusterID, ti.ID) // the Helm values of the fragment, written once at the end
	secretCommands := []string{}
	forWhat := fmt.Sprintf("intent=%s cluster=%s", ti.ID, agent.ClusterID) // what the audit entry of each certificate says it was for
	// Where the agent really runs (or the chart's documented defaults when it never said): the Secret below
	// and the caller's `helm upgrade` must name the same namespace, or the release cannot find its
	// certificate. Always returned so the caller does not have to guess it.
	hub := a.tn(r).Hub
	rns, rname, _ := agents.ReleaseTarget(hub.NamespaceOf(agent.ID), hub.ReleaseNameOf(agent.ID))
	resp := map[string]any{"namespace": rns, "release": rname}
	// With the page's telemetry the answer is the whole block. What is wrong with that telemetry is found before anything is issued: a
	// certificate is minted and audited for a command that will be written, not for one that cannot be.
	var spec install.Telemetry
	var receiverAuth map[string]string
	if ask.Telemetry != nil {
		var err error
		spec, receiverAuth, err = a.operatorTelemetry(r, ti, *ask.Telemetry)
		if err != nil {
			a.fail(w, err)
			return
		}
		if problems := spec.TowardOperators(receiverAuth).Problems(); len(problems) > 0 {
			resp["installFragment"], resp["secretCommands"], resp["problems"] = "", []string{}, problems
			httpkit.WriteJSON(w, 200, resp)
			return
		}
	}
	// What the printed endpoints are only taken on trust for (see targetWarnings), once per operator.
	var warnings []string
	warned := map[string]bool{}
	warn := func(op store.Operator, d store.Destination) {
		if key := op.ID + "|" + d.Address; !warned[key] {
			warned[key] = true
			warnings = append(warnings, a.destWarnings(r.Context(), op, d)...)
		}
	}
	if len(ti.Routes) > 0 {
		// Each signal type with its own destination. Only the routes to a regional operator need anything from
		// the server (a client certificate); the others are built by the caller from what it holds. An operator
		// that several signal types export to is issued ONE certificate, in ONE Secret, however many routes
		// name it - the Secret every one of those routes reads.
		issued := map[string]bool{}
		auth := map[string]string{}
		// issue mints the operator's client certificate once and queues the Secret that holds it.
		issue := func(op store.Operator) error {
			if issued[op.ID] {
				return nil
			}
			certPEM, keyPEM, caPEM, err := a.core(r).Operators.IssueOperatorClientCertFor(r.Context(), httpkit.Actor(r), op.ID, agent.ClusterID, forWhat)
			if err != nil {
				return err
			}
			issued[op.ID] = true
			auth[op.ID] = string(op.ReceiverAuth)
			if len(certPEM) > 0 {
				secretCmd, err := operatorClientSecretCommand(op, certPEM, keyPEM, caPEM, rns, false)
				if err != nil {
					return err
				}
				secretCommands = append(secretCommands, secretCmd)
			}
			return nil
		}
		var unrouted []store.Modality
		for _, m := range routeModalities {
			d, ok := ti.Routes[m]
			if !ok {
				unrouted = append(unrouted, m)
				continue
			}
			if d.Kind != store.DestinationOperator {
				continue
			}
			op, err := a.core(r).Operators.GetOperator(r.Context(), d.TargetOperatorID)
			if err != nil {
				a.fail(w, err)
				return
			}
			op = a.advertised(op)
			if err := issue(op); err != nil {
				a.fail(w, err)
				return
			}
			warn(op, d)
			values = append(values, operatorRouteValues(op, a.destEndpoint(op, d), m)...)
		}
		if len(unrouted) > 0 {
			// The signal types without a route of their own go to the intent's default destination. When that is a
			// regional operator it is stated in full here, like a route, and the routes these types may still carry from
			// an earlier command are cleared: a route outranks the default.
			if ti.Destination.Kind == store.DestinationOperator {
				op, err := a.core(r).Operators.GetOperator(r.Context(), ti.Destination.TargetOperatorID)
				if err != nil {
					a.fail(w, err)
					return
				}
				op = a.advertised(op)
				if err := issue(op); err != nil {
					a.fail(w, err)
					return
				}
				warn(op, ti.Destination)
				values = append(values, exportBlock("telemetry.export.otlp", op, a.destEndpoint(op, ti.Destination), true)...)
			}
			values = append(values, clearRoutes(unrouted...)...)
		}
		if len(auth) > 0 {
			resp["operators"] = auth
		}
	} else if ti.Destination.Kind == store.DestinationOperator {
		op, err := a.core(r).Operators.GetOperator(r.Context(), ti.Destination.TargetOperatorID)
		if err != nil {
			a.fail(w, err)
			return
		}
		op = a.advertised(op)
		certPEM, keyPEM, caPEM, err := a.core(r).Operators.IssueOperatorClientCertFor(r.Context(), httpkit.Actor(r), op.ID, agent.ClusterID, forWhat)
		if err != nil {
			a.fail(w, err)
			return
		}
		warn(op, ti.Destination)
		destination, secretCmd, err := operatorDestinationCommand(op, a.destEndpoint(op, ti.Destination), certPEM, keyPEM, caPEM, rns)
		if err != nil {
			a.fail(w, a.installConfigError(err))
			return
		}
		// Every signal type goes to this one destination, so no per-signal route may stay: a route outranks the default,
		// and one an earlier command set (--reset-then-reuse-values keeps it) would keep sending to the old destination.
		values = append(append(values, destination...), clearRoutes(routeModalities...)...)
		// Whether the person must also supply a receiver bearer token: not for an "mtls" operator, whose
		// only gate is the client certificate in the Secret above; for a "bearer" one the flags are exactly
		// what they always were and the caller still supplies the token itself.
		resp["receiverAuth"] = string(op.ReceiverAuth)
		resp["operators"] = map[string]string{op.ID: string(op.ReceiverAuth)}
		resp["endpoint"] = a.destEndpoint(op, ti.Destination)
		if secretCmd != "" {
			secretCommands = append(secretCommands, secretCmd)
		}
	}
	resp["installFragment"], resp["secretCommands"] = install.Flags(values), secretCommands
	if ask.Telemetry != nil {
		img := a.images(a.core(r))
		block, err := install.OperatorTelemetry{
			AgentUpgrade: install.AgentUpgrade{ChartRef: a.AgentRef(img), ChartVersion: a.AgentVersion(), ChartFile: a.AgentChartFile(), Namespace: rns, Release: rname, Telemetry: &spec},
			Certificates: secretCommands, Restated: values, ReceiverAuth: receiverAuth,
		}.Render()
		if err != nil {
			a.fail(w, a.installConfigError(err))
			return
		}
		resp["command"], resp["problems"] = block, []string{}
	}
	if len(warnings) > 0 {
		resp["warnings"] = warnings
	}
	httpkit.WriteJSON(w, 200, resp)
}
