package server

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"continuum/internal/store"
)

// maxTelemetryIntentName mirrors maxOperatorName - both name the thing a person has to recognise later
// (here, which local-operator grant this is).
const maxTelemetryIntentName = 80

// maxTelemetrySignals bounds how many signal grants one intent may hold - generous headroom over the
// eleven known TELEMETRY_SIGNALS ids (see store.SignalGrant's own comment), the same reasoning
// maxDiagTelemetrySignals gives for the agent's own report of what it has installed.
const maxTelemetrySignals = 32

// signalModality mirrors the frontend's TELEMETRY_SIGNALS table (src/lib/install.ts) - the only place a
// SignalGrant.ID is ever minted from today - so a grant's modality can be checked against the target
// operator's own Operator.AcceptedModalities (see checkOperatorAcceptsSignals). It is deliberately a
// lookup of known ids, not a validator: store.SignalGrant's own comment says ID is opaque to this
// package, and an id this map does not recognise (a newer frontend's not-yet-known signal) must stay
// unchecked rather than be treated as a mismatch - see checkOperatorAcceptsSignals.
var signalModality = map[string]store.Modality{
	"resourceUsage":      store.ModalityMetrics,
	"energy":             store.ModalityMetrics,
	"kubernetesState":    store.ModalityMetrics,
	"nodeRuntime":        store.ModalityMetrics,
	"networkLatency":     store.ModalityMetrics,
	"applicationMetrics": store.ModalityMetrics,
	"systemLogs":         store.ModalityLogs,
	"kubernetesEvents":   store.ModalityLogs,
	"applicationLogs":    store.ModalityLogs,
	"traces":             store.ModalityTraces,
	"accelerators":       store.ModalityMetrics,
}

// checkOperatorAcceptsSignals confirms, when dest is a DestinationOperator, that every signal whose
// modality this package recognises (signalModality) is one the target operator's own
// Operator.AcceptedModalities actually takes. It is a no-op for a DestinationExternal (nothing to check
// against) and for a target operator with an empty AcceptedModalities (accepts everything, per that
// field's own doc comment). This is deliberately separate from validateDestination: that method checks a
// Destination's own shape and the target operator's existence/status, the same check Operator's own
// CreateOperator/UpdateOperatorScope need for a Destination that is not modality-specific by itself -
// only a TelemetryIntent's paired signals make the modality question meaningful, so only
// TelemetryIntent's own Core methods call this, right after validateDestination succeeds.
func (c *Core) checkOperatorAcceptsSignals(ctx context.Context, dest store.Destination, signals []store.SignalGrant) error {
	if dest.Kind != store.DestinationOperator {
		return nil
	}
	op, err := c.operatorInOrg(ctx, dest.TargetOperatorID)
	if err != nil {
		return err
	}
	if len(op.AcceptedModalities) == 0 {
		return nil
	}
	accepted := make(map[store.Modality]bool, len(op.AcceptedModalities))
	for _, m := range op.AcceptedModalities {
		accepted[m] = true
	}
	for _, sg := range signals {
		modality, known := signalModality[sg.ID]
		if !known {
			continue
		}
		if !accepted[modality] {
			return errf(KindInvalid, "%q is a %s signal, but the target operator only accepts %v", sg.ID, modality, op.AcceptedModalities)
		}
	}
	return nil
}

// checkDestinationsAcceptSignals is checkOperatorAcceptsSignals for an intent that may send signal types to
// destinations of their own: each signal is checked against where it actually goes - the route for its
// modality when there is one, the default destination otherwise.
func (c *Core) checkDestinationsAcceptSignals(ctx context.Context, dest store.Destination, routes map[store.Modality]store.Destination, signals []store.SignalGrant) error {
	for _, sg := range signals {
		m, known := signalModality[sg.ID]
		if !known {
			continue
		}
		target := dest
		if r, ok := routes[m]; ok {
			target = r
		}
		if err := c.checkOperatorAcceptsSignals(ctx, target, []store.SignalGrant{sg}); err != nil {
			return err
		}
	}
	return nil
}

// cleanRoutes validates the per-signal-type destinations an intent is given: only the three signal types can
// have one, and each destination is as valid as the intent's own (an operator route must name an active
// operator of this organisation). Nil in, nil out - no routes is the ordinary case.
func (c *Core) cleanRoutes(ctx context.Context, routes map[store.Modality]store.Destination) (map[store.Modality]store.Destination, error) {
	if len(routes) == 0 {
		return nil, nil
	}
	out := make(map[store.Modality]store.Destination, len(routes))
	for m, d := range routes {
		switch m {
		case store.ModalityMetrics, store.ModalityLogs, store.ModalityTraces:
		default:
			return nil, errf(KindInvalid, "%q is not a signal type with a destination of its own (metrics, logs or traces)", printable(string(m), 32))
		}
		if err := c.validateDestination(ctx, d); err != nil {
			return nil, err
		}
		out[m] = d
	}
	return out, nil
}

// telemetryIntentInOrg finds a telemetry intent of this organisation. One belonging to another
// organisation is reported as not existing, the same convention operatorInOrg uses.
func (c *Core) telemetryIntentInOrg(ctx context.Context, id string) (store.TelemetryIntent, error) {
	ti, err := c.Store.GetTelemetryIntent(ctx, id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && ti.OrgID != c.OrgID) {
		return store.TelemetryIntent{}, errf(KindNotFound, "no such telemetry intent")
	}
	return ti, err
}

// approvedAgentInOrg finds an approved agent of this organisation - a telemetry intent targets one agent
// directly, the same "real and currently approved" check validSourceClusters makes per cluster for an
// operator's source clusters.
func (c *Core) approvedAgentInOrg(ctx context.Context, agentID string) (store.Agent, error) {
	a, err := c.agentInOrg(ctx, agentID)
	if err != nil {
		return a, err
	}
	if a.Status != store.StatusApproved {
		return a, errf(KindInvalid, "agent is %s: a telemetry intent can only target a currently approved agent", a.Status)
	}
	return a, nil
}

// cleanTelemetryScope validates and canonicalises the namespaces/exclude/signals a telemetry intent asks
// for. Namespace names use the same syntax consent.go's cleanConsent already checks for an agent's own
// excluded namespaces; unlike validSourceClusters's source cluster list, Namespaces is not required to be
// non-empty - empty means "every namespace the agent's own tier/consent already allows".
func cleanTelemetryScope(namespaces, exclude []string, signals []store.SignalGrant) ([]string, []string, []store.SignalGrant, error) {
	cleanNames := func(in []string) ([]string, error) {
		var out []string
		seen := map[string]bool{}
		for _, ns := range in {
			ns = strings.TrimSpace(ns)
			if ns == "" {
				continue
			}
			if !nsNameRe.MatchString(ns) {
				return nil, errf(KindInvalid, "%q is not a valid namespace name (lowercase letters, digits and dashes, at most 63 characters)", printable(ns, 64))
			}
			if !seen[ns] {
				seen[ns] = true
				out = append(out, ns)
			}
		}
		if len(out) > maxExcluded {
			return nil, errf(KindInvalid, "at most %d namespaces can be listed here", maxExcluded)
		}
		return out, nil
	}
	ns, err := cleanNames(namespaces)
	if err != nil {
		return nil, nil, nil, err
	}
	exc, err := cleanNames(exclude)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(signals) > maxTelemetrySignals {
		return nil, nil, nil, errf(KindInvalid, "at most %d telemetry signals can be granted here", maxTelemetrySignals)
	}
	seen := map[string]bool{}
	out := make([]store.SignalGrant, 0, len(signals))
	for _, sg := range signals {
		id := strings.TrimSpace(sg.ID)
		if id == "" {
			return nil, nil, nil, errf(KindInvalid, "a telemetry signal needs an id")
		}
		if seen[id] {
			return nil, nil, nil, errf(KindInvalid, "signal %q is listed twice", printable(id, 64))
		}
		seen[id] = true
		out = append(out, store.SignalGrant{ID: printable(id, 64), Source: printable(strings.TrimSpace(sg.Source), 64)})
	}
	return ns, exc, out, nil
}

// CreateTelemetryIntent grants one agent's local operator (the telemetry extractors bundled in its own
// continuum-agent install) a scope to collect and where to export it. At most one active intent may
// exist per agent at a time: a second one is refused with KindConflict rather than layering on top of the
// first, since the resulting pair would otherwise have to be merged by whoever reads them later - the
// caller updates the existing intent's scope/destination instead.
func (c *Core) CreateTelemetryIntent(ctx context.Context, actor, agentID, name string, namespaces, exclude []string, signals []store.SignalGrant, dest store.Destination) (store.TelemetryIntent, error) {
	return c.CreateTelemetryIntentWithRoutes(ctx, actor, agentID, name, namespaces, exclude, signals, dest, nil)
}

// CreateTelemetryIntentWithRoutes is CreateTelemetryIntent for an intent that sends signal types to destinations of
// their own: every granted signal is checked against where it goes (its route, or dest), not all against dest.
func (c *Core) CreateTelemetryIntentWithRoutes(ctx context.Context, actor, agentID, name string, namespaces, exclude []string, signals []store.SignalGrant, dest store.Destination, routes map[store.Modality]store.Destination) (store.TelemetryIntent, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > maxTelemetryIntentName {
		return store.TelemetryIntent{}, errf(KindInvalid, "name the telemetry intent (1-%d characters)", maxTelemetryIntentName)
	}
	agent, err := c.approvedAgentInOrg(ctx, agentID)
	if err != nil {
		return store.TelemetryIntent{}, err
	}
	if err := c.validateDestination(ctx, dest); err != nil {
		return store.TelemetryIntent{}, err
	}
	routes, err = c.cleanRoutes(ctx, routes)
	if err != nil {
		return store.TelemetryIntent{}, err
	}
	ns, exc, sig, err := cleanTelemetryScope(namespaces, exclude, signals)
	if err != nil {
		return store.TelemetryIntent{}, err
	}
	if err := c.checkDestinationsAcceptSignals(ctx, dest, routes, sig); err != nil {
		return store.TelemetryIntent{}, err
	}
	existing, err := c.Store.ListTelemetryIntentsByAgent(ctx, agent.ID)
	if err != nil {
		return store.TelemetryIntent{}, err
	}
	for _, e := range existing {
		if e.Status == store.TelemetryIntentActive {
			return store.TelemetryIntent{}, errf(KindConflict, "agent %q already has an active telemetry intent (%s); update it instead of creating a second one", agent.Name, e.ID)
		}
	}
	ti := store.TelemetryIntent{
		ID: newTelemetryIntentID(), OrgID: c.OrgID, AgentID: agent.ID, Name: name, Status: store.TelemetryIntentActive,
		Namespaces: ns, Exclude: exc, Signals: sig, Destination: dest, Routes: routes, CreatedBy: actor, CreatedAt: c.Now(),
	}
	detail := fmt.Sprintf("%q for agent %q", name, agent.Name)
	if err := c.audited(ctx, actor, "telemetry-intent-created", "telemetry-intent", ti.ID, detail, func() error {
		return c.Store.CreateTelemetryIntent(ctx, ti)
	}); err != nil {
		return store.TelemetryIntent{}, err
	}
	return ti, nil
}

func (c *Core) GetTelemetryIntent(ctx context.Context, id string) (store.TelemetryIntent, error) {
	return c.telemetryIntentInOrg(ctx, id)
}

// ListTelemetryIntentsForAgent lists one agent's intents (active or revoked); the agent itself must
// belong to this organisation, the same org-scoped not-found convention as everything else here.
func (c *Core) ListTelemetryIntentsForAgent(ctx context.Context, agentID string) ([]store.TelemetryIntent, error) {
	if _, err := c.agentInOrg(ctx, agentID); err != nil {
		return nil, err
	}
	return c.Store.ListTelemetryIntentsByAgent(ctx, agentID)
}

func (c *Core) ListTelemetryIntents(ctx context.Context) ([]store.TelemetryIntent, error) {
	return c.Store.ListTelemetryIntents(ctx, c.OrgID)
}

// UpdateTelemetryIntentScope replaces which namespaces (and which telemetry signals) a local operator is
// granted. There is no live reparenting here, the same as UpdateOperatorScope: applying the corresponding
// change inside the agent's own cluster remains whatever this release's agent-side wiring does with it.
func (c *Core) UpdateTelemetryIntentScope(ctx context.Context, actor, id string, namespaces, exclude []string, signals []store.SignalGrant) error {
	ti, err := c.telemetryIntentInOrg(ctx, id)
	if err != nil {
		return err
	}
	ns, exc, sig, err := cleanTelemetryScope(namespaces, exclude, signals)
	if err != nil {
		return err
	}
	if err := c.checkDestinationsAcceptSignals(ctx, ti.Destination, ti.Routes, sig); err != nil {
		return err
	}
	detail := fmt.Sprintf("%q: scope changed", ti.Name)
	return c.audited(ctx, actor, "telemetry-intent-scope-changed", "telemetry-intent", id, detail, func() error {
		if err := c.Store.UpdateTelemetryIntentScope(ctx, id, ns, exc, sig); err != nil {
			if errors.Is(err, store.ErrBadState) {
				return errf(KindConflict, "only an active telemetry intent's scope can be changed")
			}
			return err
		}
		return nil
	})
}

// UpdateTelemetryIntentDestination replaces where a local operator exports to, revalidated the same way
// CreateTelemetryIntent's own destination is.
func (c *Core) UpdateTelemetryIntentDestination(ctx context.Context, actor, id string, dest store.Destination) error {
	ti, err := c.telemetryIntentInOrg(ctx, id)
	if err != nil {
		return err
	}
	if err := c.validateDestination(ctx, dest); err != nil {
		return err
	}
	if err := c.checkDestinationsAcceptSignals(ctx, dest, ti.Routes, ti.Signals); err != nil {
		return err
	}
	detail := fmt.Sprintf("%q: destination changed", ti.Name)
	return c.audited(ctx, actor, "telemetry-intent-destination-changed", "telemetry-intent", id, detail, func() error {
		if err := c.Store.UpdateTelemetryIntentDestination(ctx, id, dest); err != nil {
			if errors.Is(err, store.ErrBadState) {
				return errf(KindConflict, "only an active telemetry intent's destination can be changed")
			}
			return err
		}
		return nil
	})
}

// UpdateTelemetryIntentDestinations replaces the default destination and the per-signal-type routes together,
// each revalidated like a new intent's, and every granted signal re-checked against where it now goes. An empty
// routes clears them: the intent sends everything to dest again.
func (c *Core) UpdateTelemetryIntentDestinations(ctx context.Context, actor, id string, dest store.Destination, routes map[store.Modality]store.Destination) error {
	ti, err := c.telemetryIntentInOrg(ctx, id)
	if err != nil {
		return err
	}
	if err := c.validateDestination(ctx, dest); err != nil {
		return err
	}
	routes, err = c.cleanRoutes(ctx, routes)
	if err != nil {
		return err
	}
	if err := c.checkDestinationsAcceptSignals(ctx, dest, routes, ti.Signals); err != nil {
		return err
	}
	detail := fmt.Sprintf("%q: destination changed", ti.Name)
	if len(routes) > 0 {
		detail = fmt.Sprintf("%q: destinations changed (%d signal types routed separately)", ti.Name, len(routes))
	}
	return c.audited(ctx, actor, "telemetry-intent-destination-changed", "telemetry-intent", id, detail, func() error {
		if err := c.Store.UpdateTelemetryIntentDestinations(ctx, id, dest, routes); err != nil {
			if errors.Is(err, store.ErrBadState) {
				return errf(KindConflict, "only an active telemetry intent's destination can be changed")
			}
			return err
		}
		return nil
	})
}

func (c *Core) RevokeTelemetryIntent(ctx context.Context, actor, id, reason string) error {
	if _, err := c.telemetryIntentInOrg(ctx, id); err != nil {
		return err
	}
	reason = printable(reason, maxReason)
	return c.audited(ctx, actor, "telemetry-intent-revoked", "telemetry-intent", id, reason, func() error {
		if err := c.Store.RevokeTelemetryIntent(ctx, id, reason, c.Now()); err != nil {
			if errors.Is(err, store.ErrBadState) {
				return errf(KindConflict, "telemetry intent is not active")
			}
			return err
		}
		return nil
	})
}

func (c *Core) DeleteTelemetryIntent(ctx context.Context, actor, id string) error {
	if _, err := c.telemetryIntentInOrg(ctx, id); err != nil {
		return err
	}
	return c.audited(ctx, actor, "telemetry-intent-deleted", "telemetry-intent", id, "", func() error {
		if err := c.Store.DeleteTelemetryIntent(ctx, id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return errf(KindNotFound, "no such telemetry intent")
			}
			return err
		}
		return nil
	})
}
