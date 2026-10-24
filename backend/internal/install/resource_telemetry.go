package install

import (
	"fmt"
	"strings"
)

// The telemetry an IkhnosAgent asks for. It is the Telemetry the Telemetry panel's `helm upgrade` is written from, in the words of a
// resource: a resource states the whole of what the agent is to be, so the page-only parts of Telemetry (the groups "left as installed",
// whether the install already had telemetry, whether a route is already there) have no place in it, and a field that is not stated is the
// chart's default, not what the install has now. Two things the panel can do are not on a resource yet: extra processors, and a regional
// operator as the destination (its mutual TLS is stated by a fragment only the server can write). ResourceOf refuses both, in words that
// send the person to the helm command.
//
// A credential is never in it, as in Telemetry: a destination names the Secret it is read from.

// ResourceSignals are the telemetry signals the agent is to collect.
type ResourceSignals struct {
	ResourceUsage      bool `json:"resourceUsage,omitempty"`
	Energy             bool `json:"energy,omitempty"`
	KubernetesState    bool `json:"kubernetesState,omitempty"`
	NodeRuntime        bool `json:"nodeRuntime,omitempty"`
	NetworkLatency     bool `json:"networkLatency,omitempty"`
	ClusterLinks       bool `json:"clusterLinks,omitempty"`
	ApplicationMetrics bool `json:"applicationMetrics,omitempty"`
	SystemLogs         bool `json:"systemLogs,omitempty"`
	KubernetesEvents   bool `json:"kubernetesEvents,omitempty"`
	ApplicationLogs    bool `json:"applicationLogs,omitempty"`
	Traces             bool `json:"traces,omitempty"`
	Accelerators       bool `json:"accelerators,omitempty"`
}

// ResourceSource is where a signal's data comes from: "bundle-kepler" or "bundle-dcgm" (deployed with the agent, the default of the signal)
// or "existing" at Endpoint (host:port).
type ResourceSource struct {
	Kind     string `json:"kind,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
}

// ResourceAccelerators is the GPU signal's source and whether the namespace scope narrows it too.
type ResourceAccelerators struct {
	Kind       string `json:"kind,omitempty"`
	Endpoint   string `json:"endpoint,omitempty"`
	ApplyScope bool   `json:"applyScope,omitempty"`
}

// ResourceClusterLinks is what the cluster-links signal does beyond the switch.
type ResourceClusterLinks struct {
	Probe      bool   `json:"probe,omitempty"`
	Throughput bool   `json:"throughput,omitempty"`
	AuthSecret string `json:"authSecret,omitempty"`
}

// ResourceWorkload names the workloads of one namespace that are kept.
type ResourceWorkload struct {
	Namespace string   `json:"namespace"`
	Names     []string `json:"names"`
}

// ResourceScope narrows one application signal; what it does not say, the install's own scope decides.
type ResourceScope struct {
	Namespaces []string           `json:"namespaces,omitempty"`
	Exclude    []string           `json:"exclude,omitempty"`
	Workloads  []ResourceWorkload `json:"workloads,omitempty"`
}

// ResourceScopes are where the application signals collect from, and whether the infrastructure signals follow.
type ResourceScopes struct {
	ApplicationMetrics *ResourceScope `json:"applicationMetrics,omitempty"`
	ApplicationLogs    *ResourceScope `json:"applicationLogs,omitempty"`
	Traces             *ResourceScope `json:"traces,omitempty"`
	Infrastructure     bool           `json:"infrastructure,omitempty"`
}

// ResourceDestination is where telemetry goes. Protocol is "grpc" (the default), "http" or "zipkin" (spans only). The credential is read
// from the Secret AuthSecret, key AuthKey (default token), and sent in the header AuthHeader (default Authorization).
type ResourceDestination struct {
	Endpoint   string `json:"endpoint,omitempty"`
	Protocol   string `json:"protocol,omitempty"`
	Insecure   bool   `json:"insecure,omitempty"`
	AuthHeader string `json:"authHeader,omitempty"`
	AuthSecret string `json:"authSecret,omitempty"`
	AuthKey    string `json:"authKey,omitempty"`
}

// ResourceRoutes send each kind of signal to a destination of its own, instead of everything to Destination.
type ResourceRoutes struct {
	Split   bool                 `json:"split,omitempty"`
	Metrics *ResourceDestination `json:"metrics,omitempty"`
	Logs    *ResourceDestination `json:"logs,omitempty"`
	Traces  *ResourceDestination `json:"traces,omitempty"`
}

// ResourceProcessing is what happens to telemetry before it leaves the cluster. TracesSamplingPercent defaults to 100.
type ResourceProcessing struct {
	ResourceDetection     bool     `json:"resourceDetection,omitempty"`
	Redaction             bool     `json:"redaction,omitempty"`
	TracesSamplingPercent *float64 `json:"tracesSamplingPercent,omitempty"`
	Debug                 string   `json:"debug,omitempty"`
	Tags                  []Tag    `json:"tags,omitempty"`
}

// ResourceTelemetry is the telemetry of an IkhnosAgent.
type ResourceTelemetry struct {
	Signals      ResourceSignals       `json:"signals"`
	Energy       *ResourceSource       `json:"energy,omitempty"`
	Accelerators *ResourceAccelerators `json:"accelerators,omitempty"`
	ClusterLinks *ResourceClusterLinks `json:"clusterLinks,omitempty"`
	Scopes       *ResourceScopes       `json:"scopes,omitempty"`
	Destination  *ResourceDestination  `json:"destination,omitempty"`
	Routes       *ResourceRoutes       `json:"routes,omitempty"`
	Processing   *ResourceProcessing   `json:"processing,omitempty"`
	Owner        *Owner                `json:"owner,omitempty"`
}

func (s ResourceSignals) signals() Signals { return Signals(s) }

// Telemetry is the draft the page's words make of the resource, with the chart's defaults where it says nothing. measurements is
// whether the path-measurements collector is on (network latency re-emits those and reports nothing without them).
func (r ResourceTelemetry) Telemetry(measurements bool) Telemetry {
	t := Telemetry{
		Signals:        r.Signals.signals(),
		Energy:         Source{Kind: "bundle-kepler"},
		Accelerators:   Accelerators{Source: Source{Kind: "bundle-dcgm"}},
		Destination:    Destination{Protocol: "grpc"},
		Processing:     Processing{TracesSamplingPercent: pctOf(100)},
		MeasurementsOn: &measurements,
	}
	if e := r.Energy; e != nil {
		if e.Kind != "" {
			t.Energy.Kind = e.Kind
		}
		t.Energy.Endpoint = e.Endpoint
	}
	if a := r.Accelerators; a != nil {
		if a.Kind != "" {
			t.Accelerators.Kind = a.Kind
		}
		t.Accelerators.Endpoint, t.Accelerators.ApplyScope = a.Endpoint, a.ApplyScope
	}
	if l := r.ClusterLinks; l != nil {
		t.ClusterLinks = ClusterLinks{Probe: l.Probe, Throughput: l.Throughput, AuthSecret: l.AuthSecret}
	}
	if s := r.Scopes; s != nil {
		t.Scopes = Scopes{ApplicationMetrics: scopeOf(s.ApplicationMetrics), ApplicationLogs: scopeOf(s.ApplicationLogs), Traces: scopeOf(s.Traces), Infrastructure: s.Infrastructure}
	}
	if d := r.Destination; d != nil {
		t.Destination = destinationOf(*d)
	}
	if rt := r.Routes; rt != nil {
		t.Routes.Split = rt.Split
		for m, d := range map[Modality]*ResourceDestination{Metrics: rt.Metrics, Logs: rt.Logs, Traces: rt.Traces} {
			if d == nil {
				continue
			}
			switch m {
			case Metrics:
				t.Routes.Metrics = destinationOf(*d)
			case Logs:
				t.Routes.Logs = destinationOf(*d)
			default:
				t.Routes.Traces = destinationOf(*d)
			}
		}
	}
	if p := r.Processing; p != nil {
		t.Processing.ResourceDetection, t.Processing.Redaction, t.Processing.Debug, t.Processing.Tags = p.ResourceDetection, p.Redaction, p.Debug, p.Tags
		if p.TracesSamplingPercent != nil {
			t.Processing.TracesSamplingPercent = p.TracesSamplingPercent
		}
	}
	if o := r.Owner; o != nil {
		t.Owner = *o
	}
	return t
}

func pctOf(f float64) *float64 { return &f }

func scopeOf(s *ResourceScope) ScopeOverride {
	if s == nil {
		return ScopeOverride{}
	}
	out := ScopeOverride{Namespaces: s.Namespaces, Exclude: s.Exclude}
	for _, w := range s.Workloads {
		out.Workloads = append(out.Workloads, Workload{Namespace: w.Namespace, Names: w.Names})
	}
	return out
}

func destinationOf(d ResourceDestination) Destination {
	if d.Protocol == "" {
		d.Protocol = "grpc"
	}
	return Destination{Endpoint: d.Endpoint, Protocol: d.Protocol, Insecure: d.Insecure, AuthHeader: d.AuthHeader, AuthSecret: d.AuthSecret, AuthKey: d.AuthKey}
}

// Values are the Helm values of the telemetry, the ones the panel's command states, written for a release that is installed whole.
// The settings are checked first; the access tier says whether the cluster-links signal can run (it reads the flows).
func (r ResourceTelemetry) Values(tier int, measurements bool) ([]Value, error) {
	t := r.Telemetry(measurements)
	if t.Signals.ClusterLinks && tier < 2 {
		return nil, &ProblemsError{Problems: []string{"cluster links need access tier 2: they are labelled from what the traffic observer sees"}}
	}
	return t.Values()
}

// Problems say what makes the telemetry unfit to install from, in the words of the panel.
func (r ResourceTelemetry) Problems(tier int, measurements bool) []string {
	if _, err := r.Values(tier, measurements); err != nil {
		if pe, ok := err.(*ProblemsError); ok {
			return pe.Problems
		}
		return []string{err.Error()}
	}
	t := r.Telemetry(measurements)
	var out []string
	for _, k := range []struct {
		on         bool
		what, kind string
	}{
		{t.Signals.Energy, "energy.kind", t.Energy.Kind}, {t.Signals.Accelerators, "accelerators.kind", t.Accelerators.Kind},
	} {
		if !k.on {
			continue
		}
		ok := k.kind == "existing" || (k.what == "energy.kind" && k.kind == "bundle-kepler") || (k.what == "accelerators.kind" && k.kind == "bundle-dcgm")
		if !ok {
			out = append(out, fmt.Sprintf("telemetry.%s %q is not one the agent knows (bundled, or existing with an endpoint)", k.what, k.kind))
		}
	}
	for _, d := range []struct{ name, protocol string }{{"destination", t.Destination.Protocol}, {"routes.metrics", t.Routes.Metrics.Protocol}, {"routes.logs", t.Routes.Logs.Protocol}, {"routes.traces", t.Routes.Traces.Protocol}} {
		switch d.protocol {
		case "", "grpc", "http", "zipkin":
		default:
			out = append(out, fmt.Sprintf("telemetry.%s.protocol %q is not grpc, http or zipkin", d.name, d.protocol))
		}
	}
	switch t.Processing.Debug {
	case "", "basic", "detailed":
	default:
		out = append(out, fmt.Sprintf("telemetry.processing.debug %q is not basic or detailed", t.Processing.Debug))
	}
	return out
}

// ResourceOf is the resource's telemetry for a draft of the panel, or nil when the draft turns telemetry off (a resource with none is
// an agent with none). A draft the resource cannot state is refused: one that leaves a group "as installed" (a resource states
// everything), one with extra processors, one that sends to a regional operator.
func ResourceOf(t Telemetry) (*ResourceTelemetry, error) {
	if !t.Active() {
		return nil, nil
	}
	var no []string
	if len(t.Kept) > 0 || len(t.Routes.Kept) > 0 {
		no = append(no, "part of this draft is left as installed, and a resource states all of its telemetry: change the settings so each is stated")
	}
	if len(t.Processing.Extra) > 0 {
		no = append(no, "extra processors are not on a resource yet: use the helm command")
	}
	for _, d := range []Destination{t.Destination, t.Routes.Metrics, t.Routes.Logs, t.Routes.Traces} {
		if strings.TrimSpace(d.Operator) != "" {
			no = append(no, "a regional operator as the destination is not on a resource yet (its mutual TLS is set by the server's own command): use the helm command")
			break
		}
	}
	if len(no) > 0 {
		return nil, &ProblemsError{Problems: no}
	}
	if p := t.Problems(); len(p) > 0 {
		return nil, &ProblemsError{Problems: p}
	}
	r := &ResourceTelemetry{Signals: ResourceSignals(t.Signals)}
	if t.Signals.Energy {
		r.Energy = &ResourceSource{Kind: t.Energy.Kind, Endpoint: strings.TrimSpace(t.Energy.Endpoint)}
	}
	if t.Signals.Accelerators || t.Accelerators.ApplyScope {
		r.Accelerators = &ResourceAccelerators{Kind: t.Accelerators.Kind, Endpoint: strings.TrimSpace(t.Accelerators.Endpoint), ApplyScope: t.Accelerators.ApplyScope}
	}
	if l := t.ClusterLinks; t.Signals.ClusterLinks && (l.Probe || l.Throughput) {
		r.ClusterLinks = &ResourceClusterLinks{Probe: l.Probe, Throughput: l.Throughput, AuthSecret: strings.TrimSpace(l.AuthSecret)}
	}
	sc := &ResourceScopes{Infrastructure: t.Scopes.Infrastructure}
	for _, s := range []struct {
		on  bool
		in  ScopeOverride
		out **ResourceScope
	}{{t.Signals.ApplicationMetrics, t.Scopes.ApplicationMetrics, &sc.ApplicationMetrics}, {t.Signals.ApplicationLogs, t.Scopes.ApplicationLogs, &sc.ApplicationLogs}, {t.Signals.Traces, t.Scopes.Traces, &sc.Traces}} {
		if s.on && s.in.Narrows() {
			rs := &ResourceScope{Namespaces: s.in.Namespaces, Exclude: s.in.Exclude}
			for _, w := range s.in.Workloads {
				rs.Workloads = append(rs.Workloads, ResourceWorkload{Namespace: w.Namespace, Names: names(w.Names)})
			}
			*s.out = rs
		}
	}
	if sc.ApplicationMetrics != nil || sc.ApplicationLogs != nil || sc.Traces != nil || sc.Infrastructure {
		r.Scopes = sc
	}
	if t.Routes.Split {
		rt := &ResourceRoutes{Split: true}
		for _, m := range t.ActiveLanes() {
			d := destinationTo(t.Routes.lane(m))
			switch m {
			case Metrics:
				rt.Metrics = &d
			case Logs:
				rt.Logs = &d
			default:
				rt.Traces = &d
			}
		}
		r.Routes = rt
	} else {
		d := destinationTo(t.Destination)
		r.Destination = &d
	}
	p := t.Processing
	pr := &ResourceProcessing{ResourceDetection: p.ResourceDetection, Redaction: p.Redaction, Debug: p.Debug, Tags: nilIfEmpty(CleanTags(p.Tags))}
	if p.TracesSamplingPercent != nil && *p.TracesSamplingPercent != 100 {
		pr.TracesSamplingPercent = p.TracesSamplingPercent
	}
	if pr.ResourceDetection || pr.Redaction || pr.Debug != "" || pr.Tags != nil || pr.TracesSamplingPercent != nil {
		r.Processing = pr
	}
	if t.Owner != (Owner{}) {
		o := t.Owner
		r.Owner = &o
	}
	return r, nil
}

func nilIfEmpty(t []Tag) []Tag {
	if len(t) == 0 {
		return nil
	}
	return t
}

func destinationTo(d Destination) ResourceDestination {
	out := ResourceDestination{Endpoint: strings.TrimSpace(d.Endpoint), Insecure: d.Insecure, AuthSecret: strings.TrimSpace(d.AuthSecret)}
	if d.Protocol != "grpc" && d.Protocol != "" {
		out.Protocol = d.Protocol
	}
	if out.AuthSecret != "" {
		if k := strings.TrimSpace(d.AuthKey); k != "token" {
			out.AuthKey = k
		}
		if h := strings.TrimSpace(d.AuthHeader); h != "Authorization" {
			out.AuthHeader = h
		}
	}
	return out
}
