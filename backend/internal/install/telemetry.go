package install

import (
	"strconv"
	"strings"
)

// Modality is the kind of signal a telemetry setting carries: metrics, logs or distributed traces.
type Modality string

const (
	Metrics Modality = "metrics"
	Logs    Modality = "logs"
	Traces  Modality = "traces"
)

// Modalities is every modality, in the fixed order the commands state them.
var Modalities = []Modality{Metrics, Logs, Traces}

// Signals are the telemetry signals an agent install can turn on. Each is one `telemetry.<signal>...enabled` value in the chart.
type Signals struct {
	ResourceUsage      bool `json:"resourceUsage"`
	Energy             bool `json:"energy"`
	KubernetesState    bool `json:"kubernetesState"`
	NodeRuntime        bool `json:"nodeRuntime"`
	NetworkLatency     bool `json:"networkLatency"`
	ClusterLinks       bool `json:"clusterLinks"`
	ApplicationMetrics bool `json:"applicationMetrics"`
	SystemLogs         bool `json:"systemLogs"`
	KubernetesEvents   bool `json:"kubernetesEvents"`
	ApplicationLogs    bool `json:"applicationLogs"`
	Traces             bool `json:"traces"`
	Accelerators       bool `json:"accelerators"`
}

// Any is whether a signal is on: the destination, and every setting about how telemetry is processed, matter only then.
func (s Signals) Any() bool {
	return s.ResourceUsage || s.Energy || s.KubernetesState || s.NodeRuntime || s.NetworkLatency || s.ClusterLinks ||
		s.ApplicationMetrics || s.SystemLogs || s.KubernetesEvents || s.ApplicationLogs || s.Traces || s.Accelerators
}

// Modalities is the kinds of signal that are on.
func (s Signals) Modalities() map[Modality]bool {
	on := map[Modality]bool{}
	for m, b := range map[Modality][]bool{
		Metrics: {s.ResourceUsage, s.Energy, s.KubernetesState, s.NodeRuntime, s.NetworkLatency, s.ClusterLinks, s.ApplicationMetrics, s.Accelerators},
		Logs:    {s.SystemLogs, s.KubernetesEvents, s.ApplicationLogs},
		Traces:  {s.Traces},
	} {
		for _, x := range b {
			if x {
				on[m] = true
			}
		}
	}
	return on
}

// Source is where a signal's data comes from: a component the chart deploys, or one that already exists and is scraped.
type Source struct {
	// Kind is "bundle-kepler" or "bundle-dcgm" (deployed with the agent) or "existing".
	Kind string `json:"kind"`
	// Endpoint is the host:port of the existing one, when Kind is "existing".
	Endpoint string `json:"endpoint"`
}

// Accelerators is how GPU metrics are collected, beyond the switch.
type Accelerators struct {
	Source
	// ApplyScope asks dcgm-exporter to attach the pod and namespace using the GPU, so the namespace scope narrows GPU metrics too.
	ApplyScope bool `json:"applyScope"`
}

// ClusterLinks is what the cluster-links signal does beyond the switch.
type ClusterLinks struct {
	// Probe runs the active probe: one-way delay, jitter and loss per direction, path size and route.
	Probe bool `json:"probe"`
	// Throughput also lets this cluster run and answer throughput tests (iperf3). It needs Probe.
	Throughput bool `json:"throughput"`
	// AuthSecret is a Secret of the person's own holding iperf3's credentials. Empty: Ikhnos issues them.
	AuthSecret string `json:"authSecret"`
}

// Workload names the workloads of one namespace that are kept.
type Workload struct {
	Namespace string   `json:"namespace"`
	Names     []string `json:"names"`
}

// ScopeOverride narrows one application signal. Each field that is empty falls back to the install's own scope.
type ScopeOverride struct {
	Namespaces []string `json:"namespaces"`
	Exclude    []string `json:"exclude"`
	// Workloads keep, inside a namespace, only the named workloads. A namespace listed with no names keeps nothing of it; one not
	// listed stays whole.
	Workloads []Workload `json:"workloads"`
}

// Narrows is whether the override narrows anything.
func (s ScopeOverride) Narrows() bool {
	return len(s.Namespaces) > 0 || len(s.Exclude) > 0 || len(s.Workloads) > 0
}

// Scopes are where the application signals collect from, and whether the infrastructure signals follow.
type Scopes struct {
	ApplicationMetrics ScopeOverride `json:"applicationMetrics"`
	ApplicationLogs    ScopeOverride `json:"applicationLogs"`
	Traces             ScopeOverride `json:"traces"`
	// Infrastructure is whether the infrastructure signals that can follow a scope (the pod and container parts of resource usage and
	// node runtime, cluster state, Kubernetes events) follow the one the application signals combine to.
	Infrastructure bool `json:"infrastructure"`
}

// Destination is where one stream of telemetry goes and how it is spoken to.
type Destination struct {
	Endpoint string `json:"endpoint"`
	// Protocol is "grpc" or "http" (OTLP) or "zipkin" (spans only, over HTTP).
	Protocol string `json:"protocol"`
	// Insecure turns TLS verification off.
	Insecure bool `json:"insecure"`
	// AuthHeader is the header the destination expects its credential in; the credential is read from the Secret AuthSecret/AuthKey,
	// never carried here.
	AuthHeader string `json:"authHeader"`
	AuthSecret string `json:"authSecret"`
	AuthKey    string `json:"authKey"`
	// Operator is the id of the regional operator the destination is, or empty. Its receiver needs mutual TLS, which only the
	// server's own fragment can state (it holds the Secret), so for one the command does not state it off.
	Operator string `json:"operator"`
}

// Routes send each kind of signal to a destination of its own. Without Split everything goes to Telemetry.Destination.
type Routes struct {
	Split   bool        `json:"split"`
	Metrics Destination `json:"metrics"`
	Logs    Destination `json:"logs"`
	Traces  Destination `json:"traces"`
	// Installed is whether the install already has routes: only then does a single destination have routes to clear.
	Installed bool `json:"installed"`
	// Kept are the kinds whose route the install has and nothing here has edited. They are left out of the command.
	Kept []Modality `json:"kept"`
}

func (r Routes) lane(m Modality) Destination {
	switch m {
	case Metrics:
		return r.Metrics
	case Logs:
		return r.Logs
	}
	return r.Traces
}

// Tag is a label put on everything the install emits.
type Tag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Processing is what happens to telemetry before it leaves the cluster.
type Processing struct {
	// ResourceDetection enriches every signal with facts about the collector's own environment.
	ResourceDetection bool `json:"resourceDetection"`
	// Redaction masks values of keys that look like secrets before anything leaves the cluster.
	Redaction bool `json:"redaction"`
	// TracesSamplingPercent is the share of traces kept, 0 to 100; nil is not a number, which is a problem.
	TracesSamplingPercent *float64 `json:"tracesSamplingPercent"`
	// Debug writes what passed to the collector's own log: "" off, "basic" counts, "detailed" every record.
	Debug string `json:"debug"`
	Tags  []Tag  `json:"tags"`
	// Extra are processors beyond the fixed ones above: a filter, tail sampling, a transform.
	Extra []Processor `json:"extra"`
}

// Owner is who the telemetry belongs to, stamped on everything as continuum.org.id and continuum.cluster.id. Empty: not stamped.
type Owner struct {
	OrgID     string `json:"orgId"`
	ClusterID string `json:"clusterId"`
}

// Kept groups. A group the draft shows but does not know the installed value of is left out of the command, so that
// --reset-then-reuse-values keeps what is installed instead of stating a default that may widen collection or drop a credential.
const (
	KeptDestination         = "destination"
	KeptScopeShared         = "scopeShared"
	KeptScopeMetrics        = "scope:applicationMetrics"
	KeptScopeLogs           = "scope:applicationLogs"
	KeptScopeTraces         = "scope:traces"
	KeptTags                = "tags"
	KeptDebug               = "debug"
	KeptProcessors          = "processors"
	KeptExtraProcessors     = "extraProcessors"
	KeptEnergySource        = "energySource"
	KeptAcceleratorsSource  = "acceleratorsSource"
	KeptClusterLinksOptions = "clusterLinksOptions"
)

// Telemetry is what an agent install is asked to collect, where it sends it, and how it processes it.
type Telemetry struct {
	Signals      Signals      `json:"signals"`
	Energy       Source       `json:"energy"`
	Accelerators Accelerators `json:"accelerators"`
	ClusterLinks ClusterLinks `json:"clusterLinks"`
	Scopes       Scopes       `json:"scopes"`
	Destination  Destination  `json:"destination"`
	Routes       Routes       `json:"routes"`
	Processing   Processing   `json:"processing"`
	Owner        Owner        `json:"owner"`
	// Kept are the groups left as installed.
	Kept []string `json:"kept"`
	// HadTelemetry is whether the install already has telemetry configured. Only then does the command have something to clear: a
	// setting it does not name keeps its old value, so every signal is stated, and an unset route is stated empty.
	HadTelemetry bool `json:"hadTelemetry"`
	// MeasurementsOn is whether the path-measurements collector is, or will be, on, when the caller controls that; nil when it does
	// not. Network latency re-emits those measurements and reports nothing without them.
	MeasurementsOn *bool `json:"measurementsOn"`
}

func (t Telemetry) kept(group string) bool {
	for _, g := range t.Kept {
		if g == group {
			return true
		}
	}
	return false
}

func (t Telemetry) laneKept(m Modality) bool {
	for _, k := range t.Routes.Kept {
		if k == m {
			return true
		}
	}
	return false
}

// Active is whether any signal is on.
func (t Telemetry) Active() bool { return t.Signals.Any() }

// ActiveLanes are the kinds of signal that need a destination of their own: the ones with a signal on, when each kind is sent
// separately.
func (t Telemetry) ActiveLanes() []Modality {
	if !t.Routes.Split {
		return nil
	}
	on := t.Signals.Modalities()
	var out []Modality
	for _, m := range Modalities {
		if on[m] {
			out = append(out, m)
		}
	}
	return out
}

// ProblemsError is why a command cannot be written for the settings, each worded for the person to act on.
type ProblemsError struct{ Problems []string }

func (e *ProblemsError) Error() string { return strings.Join(e.Problems, "; ") }

func flag(key string, on bool) Value { return Set(key, strconv.FormatBool(on)) }

// names is a list that is never nil, so it is written as [] and not null.
func names(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

func workloads(ws []Workload) []Workload {
	out := make([]Workload, len(ws))
	for i, w := range ws {
		out[i] = Workload{Namespace: w.Namespace, Names: names(w.Names)}
	}
	return out
}

// signalFlags is every signal's switch, by its chart value: what the command that turns telemetry off states off.
var signalFlags = []string{
	"telemetry.resourceUsage.metrics.enabled",
	"telemetry.energy.metrics.enabled",
	"telemetry.kubernetesState.metrics.enabled",
	"telemetry.nodeRuntime.metrics.enabled",
	"telemetry.networkLatency.metrics.enabled",
	"telemetry.clusterLinks.metrics.enabled",
	// The prober needs the metrics switch above: left on while that goes off, the chart would refuse to render.
	"telemetry.clusterLinks.probe.enabled",
	"telemetry.clusterLinks.probe.capacity.enabled",
	"telemetry.applicationMetrics.metrics.enabled",
	"telemetry.systemLogs.logs.enabled",
	"telemetry.kubernetesEvents.logs.enabled",
	"telemetry.applicationLogs.logs.enabled",
	"telemetry.traces.traces.enabled",
	"telemetry.accelerators.metrics.enabled",
}

// off is the values that turn all telemetry off: every signal stated false, and the routes and extra-processor lists stated empty so
// that a destination or processor chain an earlier command set does not come back, unseen, the next time telemetry is turned on.
func off() []Value {
	var v []Value
	for _, f := range signalFlags {
		v = append(v, Set(f, "false"))
	}
	for _, m := range Modalities {
		v = append(v, SetString("telemetry.export.routes."+string(m)+".endpoint", ""))
	}
	return append(v, SetJSON("telemetry.processors.extraProcessorNames", "[]"), SetJSON("telemetry.processors.extraTracesProcessorNames", "[]"))
}
