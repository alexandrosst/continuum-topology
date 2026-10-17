package install

import (
	"strconv"
	"strings"
)

// Values are the Helm values that set the telemetry: the export target, then every signal's switch, explicitly true or false, not
// just the ones turned on. That is deliberate: the same values also change an install that has telemetry (helm upgrade
// --reset-then-reuse-values), where Helm changes only what a value actually names, so a signal left unmentioned because it was merely
// unchecked would keep running. Stating every signal makes unchecking one actually turn it off, and costs nothing on a fresh install
// (an explicit false for a signal that was going to default to false is a no-op).
//
// Without a signal on there is nothing to export: for an install that has telemetry the values that switch it all off, for one that
// has none nothing. For settings with a problem there are no values, and the error says what is wrong.
func (t Telemetry) Values() ([]Value, error) {
	if !t.Active() {
		if t.HadTelemetry {
			return off(), nil
		}
		return nil, nil
	}
	if p := t.Problems(); len(p) > 0 {
		return nil, &ProblemsError{Problems: p}
	}
	var v []Value
	add := func(vals ...Value) { v = append(v, vals...) }
	// A list of names. An empty one is written as JSON (Strings): `--set flag='{}'` gives Helm a list holding one EMPTY STRING, which the
	// chart reads as "keep only the namespace named ''" (every record dropped) and as a processor with no name (a collector that will not
	// start).
	scope := func(base string, s ScopeOverride) {
		add(Strings(base+".namespaces", s.Namespaces), Strings(base+".exclude", s.Exclude), SetJSON(base+".workloads", jsonText(workloads(s.Workloads))))
	}
	sig := t.Signals

	// Sending each signal type to its own destination: the routes below say where everything goes, so the default is left alone (it is
	// only used by a signal without a route, and there is none here).
	if !t.Routes.Split {
		add(SetString("telemetry.export.otlp.endpoint", strings.TrimSpace(t.Destination.Endpoint)))
	}
	// Who this belongs to, for every destination (an operator's server-built fragment states the same two, and the intent id, after
	// this; helm takes the last). The scope and the tags are stated even when empty: clearing them has to actually clear them.
	if t.Owner.OrgID != "" {
		add(SetString("telemetry.resource.orgId", t.Owner.OrgID))
	}
	if t.Owner.ClusterID != "" {
		add(SetString("telemetry.resource.clusterId", t.Owner.ClusterID))
	}
	if !t.kept(KeptScopeShared) {
		add(SetString("telemetry.resource.scope", t.ScopeTag()))
	}
	// The single destination is stated IN FULL every time (protocol, TLS, mutual TLS, CA, credential): anything left unmentioned keeps
	// its earlier value, so moving from a destination with a client certificate, plain HTTP or a credential to one without would carry
	// those over. Only a destination seeded from an install whose connection details are not known, and not edited since, is stated by
	// its endpoint alone.
	if !t.Routes.Split && !t.kept(KeptDestination) {
		add(destinationValues("telemetry.export.otlp", t.Destination)...)
	}
	// The routes. Every route is stated on every command (an unmentioned one would keep sending): a route in use in full, so that what
	// an earlier command set cannot linger, and one not in use as an empty endpoint, which is how the chart reads "no route". A single
	// destination states them empty whenever the install has telemetry (it may have routes the agent did not report, or that an earlier
	// command here set): a fresh install has none to clear. One kept as installed is left as it is, routes included.
	lanes := t.ActiveLanes()
	if t.Routes.Split || (!t.kept(KeptDestination) && (t.Routes.Installed || t.HadTelemetry)) {
		for _, m := range Modalities {
			base := "telemetry.export.routes." + string(m)
			inUse := false
			for _, l := range lanes {
				inUse = inUse || l == m
			}
			if !inUse {
				add(SetString(base+".endpoint", ""))
				continue
			}
			if t.laneKept(m) {
				continue // as installed: unmentioned, so the install keeps it
			}
			lane := t.Routes.lane(m)
			add(SetString(base+".endpoint", strings.TrimSpace(lane.Endpoint)))
			add(destinationValues(base, lane)...)
		}
	}

	add(flag("telemetry.resourceUsage.metrics.enabled", sig.ResourceUsage))
	add(flag("telemetry.energy.metrics.enabled", sig.Energy))
	// The source is stated whenever the signal is on, the bundled one too: leaving it out when it is the default would keep an earlier
	// 'existing', and the cluster would go on scraping the old endpoint. Only a source the install did not report (an older agent) is
	// left as it is, until edited.
	if sig.Energy && !t.kept(KeptEnergySource) {
		add(Set("telemetry.energy.metrics.source", t.Energy.Kind))
		if t.Energy.Kind == "existing" {
			add(SetString("telemetry.energy.metrics.existing.prometheusEndpoint", strings.TrimSpace(t.Energy.Endpoint)))
		}
	}
	add(flag("telemetry.kubernetesState.metrics.enabled", sig.KubernetesState))
	add(flag("telemetry.nodeRuntime.metrics.enabled", sig.NodeRuntime))
	add(flag("telemetry.networkLatency.metrics.enabled", sig.NetworkLatency))
	add(flag("telemetry.clusterLinks.metrics.enabled", sig.ClusterLinks))
	if sig.ClusterLinks {
		// The link metrics are labelled from what the flow collectors see, so the chart refuses them without it.
		add(Set("flowObserver.enabled", "true"))
		if !t.kept(KeptClusterLinksOptions) {
			l := t.ClusterLinks
			add(flag("telemetry.clusterLinks.probe.enabled", l.Probe))
			add(flag("telemetry.clusterLinks.probe.capacity.enabled", l.Probe && l.Throughput))
			// Only for credentials of your own; empty means Ikhnos issues them (the chart's default), so no value.
			if own := strings.TrimSpace(l.AuthSecret); l.Probe && l.Throughput && own != "" {
				add(SetString("telemetry.clusterLinks.probe.capacity.auth.existingSecret", own))
			}
		}
	} else {
		// Stated off with the signal, whatever the draft says: the prober cannot stay on without it.
		add(Set("telemetry.clusterLinks.probe.enabled", "false"), Set("telemetry.clusterLinks.probe.capacity.enabled", "false"))
	}
	// Each application signal's scope is stated while the signal is on, even when both lists are empty: an empty list means "no
	// override, fall back to the install's scope", and stating it is what lets clearing an override actually take effect; leaving it
	// unstated whenever empty would let a stale earlier override survive. Omitted while the signal is off: the chart's own gating makes a
	// stale value harmless there.
	add(flag("telemetry.applicationMetrics.metrics.enabled", sig.ApplicationMetrics))
	if sig.ApplicationMetrics && !t.kept(KeptScopeMetrics) {
		scope("telemetry.applicationMetrics.metrics.scope", t.Scopes.ApplicationMetrics)
	}
	add(flag("telemetry.systemLogs.logs.enabled", sig.SystemLogs))
	add(flag("telemetry.kubernetesEvents.logs.enabled", sig.KubernetesEvents))
	add(flag("telemetry.applicationLogs.logs.enabled", sig.ApplicationLogs))
	if sig.ApplicationLogs && !t.kept(KeptScopeLogs) {
		scope("telemetry.applicationLogs.logs.scope", t.Scopes.ApplicationLogs)
	}
	add(flag("telemetry.traces.traces.enabled", sig.Traces))
	if sig.Traces && !t.kept(KeptScopeTraces) {
		scope("telemetry.traces.traces.scope", t.Scopes.Traces)
	}
	// What the infrastructure signals follow: the application signals' combined scope when that is switched on, otherwise nothing,
	// stated either way (lists replace on upgrade, so an old one is cleared).
	if !t.kept(KeptScopeShared) {
		infra := ScopeOverride{}
		if t.Scopes.Infrastructure {
			infra = t.CombinedScope()
		}
		scope("telemetry.scope.infra", infra)
	}
	add(flag("telemetry.accelerators.metrics.enabled", sig.Accelerators))
	if sig.Accelerators && !t.kept(KeptAcceleratorsSource) {
		add(Set("telemetry.accelerators.metrics.source", t.Accelerators.Kind))
		if t.Accelerators.Kind == "existing" {
			add(SetString("telemetry.accelerators.metrics.existing.prometheusEndpoint", strings.TrimSpace(t.Accelerators.Endpoint)))
		}
	}
	// Stated unconditionally, like the switches above (not gated on the signal): a previous applyScope=true left unmentioned would
	// survive a later edit that turns accelerators off and back on without re-checking this box.
	add(flag("telemetry.accelerators.metrics.applyScope", t.Accelerators.ApplyScope))
	// Processors, debug and tags: stated explicitly like the signals above (not conditionally, like the routes), for the same reason -
	// a sampling percentage or a redaction toggle left unmentioned because it was reset to its default would keep its old value. A group
	// seeded as unknown and not edited is the one exception: stating a default there would silently replace what the install really has.
	p := t.Processing
	if !t.kept(KeptProcessors) {
		add(flag("telemetry.processors.resourceDetection.enabled", p.ResourceDetection), flag("telemetry.processors.redaction.enabled", p.Redaction),
			Set("telemetry.processors.tracesSampling.percentage", strconv.FormatFloat(*p.TracesSamplingPercent, 'f', -1, 64)))
	}
	if !t.kept(KeptDebug) {
		add(SetString("telemetry.debug.verbosity", p.Debug))
	}
	// The tags are one JSON list (not a value per key) because a list REPLACES what an earlier command set, where a map would keep
	// every key since removed - the chart's field is a list for exactly this reason.
	if !t.kept(KeptTags) {
		add(SetJSON("telemetry.resource.attributes", jsonText(CleanTags(p.Tags))))
	}
	// Extra processors: the bodies all go in one JSON value (a map keyed by Key), and each key is then referenced in whichever pipeline
	// list it belongs in - tail sampling only ever the traces-only list, filter and transform the shared every-pipeline one. Both LISTS
	// are stated whole, empty when need be (a list replaces, where an indexed name would leave the tail of a longer one): a processor
	// taken out here would otherwise stay in the pipelines. Left out entirely while the processors are kept as installed (the agent does
	// not report them, and the draft cannot show them), and on a fresh install with none.
	if !t.kept(KeptExtraProcessors) && (len(p.Extra) > 0 || t.HadTelemetry) {
		if len(p.Extra) > 0 {
			add(SetJSON("telemetry.processors.extraProcessors", Bodies(p.Extra, true)))
		}
		byTarget := map[string][]string{}
		for _, e := range p.Extra {
			byTarget[e.Target()] = append(byTarget[e.Target()], e.Key())
		}
		// A fresh install states only the lists that have something in them; one with telemetry already states both, so that emptying a
		// list works.
		for _, target := range []string{"extraProcessorNames", "extraTracesProcessorNames"} {
			if keys := byTarget[target]; len(keys) > 0 || t.HadTelemetry {
				add(Strings("telemetry.processors."+target, keys))
			}
		}
	}
	return v, nil
}

// destinationValues are the values that state a destination in full under base, after its endpoint: the protocol, TLS, mutual TLS and the
// credential. A regional operator's receiver is mutual TLS, which only the server's own fragment (it holds the Secret and the name to
// verify) can state; everything else states it off, so a certificate an earlier command installed does not linger.
func destinationValues(base string, d Destination) []Value {
	v := []Value{
		Set(base+".protocol", d.Protocol),
		flag(base+".tls.insecure", d.Insecure),
	}
	if d.Operator == "" {
		v = append(v, flag(base+".tls.mtls.enabled", false), SetString(base+".tls.mtls.secretName", ""), SetString(base+".tls.serverName", ""), SetString(base+".tls.caFile", ""))
	}
	secret := strings.TrimSpace(d.AuthSecret)
	v = append(v, SetString(base+".auth.secretName", secret))
	if secret != "" {
		key, header := strings.TrimSpace(d.AuthKey), strings.TrimSpace(d.AuthHeader)
		if key == "" {
			key = "token"
		}
		if header == "" {
			header = "Authorization"
		}
		v = append(v, SetString(base+".auth.secretKey", key), SetString(base+".auth.headerName", header))
	}
	return v
}
