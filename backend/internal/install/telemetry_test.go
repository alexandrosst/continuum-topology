package install

import (
	"reflect"
	"strings"
	"testing"
)

func pct(f float64) *float64 { return &f }

// valid is telemetry that can be written: one signal, one destination.
func valid() Telemetry {
	return Telemetry{
		Signals:      Signals{ResourceUsage: true},
		Energy:       Source{Kind: "bundle-kepler"},
		Accelerators: Accelerators{Source: Source{Kind: "bundle-dcgm"}},
		ClusterLinks: ClusterLinks{Probe: true},
		Scopes:       Scopes{Infrastructure: true},
		Destination:  Destination{Endpoint: "otel.example.com:4317", Protocol: "grpc"},
		Processing:   Processing{Redaction: true, TracesSamplingPercent: pct(100)},
	}
}

func TestTelemetryProblems(t *testing.T) {
	mut := func(f func(*Telemetry)) Telemetry { v := valid(); f(&v); return v }
	for _, c := range []struct {
		name string
		t    Telemetry
		want string // a part of the one problem; empty: none
	}{
		{"fine", valid(), ""},
		{"no signal: nothing to check", Telemetry{}, ""},
		{"no endpoint", mut(func(t *Telemetry) { t.Destination.Endpoint = " " }), "An export endpoint is required"},
		{"routes need no single endpoint", mut(func(t *Telemetry) {
			t.Destination.Endpoint = ""
			t.Routes.Split = true
			t.Routes.Metrics = Destination{Endpoint: "m.example.com:4317", Protocol: "grpc"}
		}), ""},
		{"a route without a destination", mut(func(t *Telemetry) { t.Routes.Split = true }), "Metrics needs a destination"},
		{"a route not in use needs none", mut(func(t *Telemetry) {
			t.Routes.Split = true
			t.Routes.Metrics = Destination{Endpoint: "m.example.com:4317", Protocol: "grpc"}
			t.Routes.Logs = Destination{}
		}), ""},
		{"a hostile endpoint", mut(func(t *Telemetry) { t.Destination.Endpoint = "a.example.com;rm -rf /" }), "the endpoint is a host and port"},
		{"an endpoint with a comma", mut(func(t *Telemetry) { t.Destination.Endpoint = "a.example.com:1,b.example.com:2" }), "the endpoint is a host and port"},
		{"an endpoint with a backslash", mut(func(t *Telemetry) { t.Destination.Endpoint = `a\b:1` }), "the endpoint is a host and port"},
		{"an endpoint with a newline", mut(func(t *Telemetry) { t.Destination.Endpoint = "a.example.com\n:1" }), "the endpoint is a host and port"},
		{"an endpoint with a no-break space", mut(func(t *Telemetry) { t.Destination.Endpoint = "a.example.com\u00a0:1" }), "the endpoint is a host and port"},
		{"a URL with a query is fine", mut(func(t *Telemetry) { t.Destination.Endpoint = "https://a.example.com/v1?x=1&y=2" }), ""},
		{"an IPv6 host is fine", mut(func(t *Telemetry) { t.Destination.Endpoint = "[::1]:4317" }), ""},
		{"a placeholder left in", mut(func(t *Telemetry) { t.Destination.Endpoint = "<tenant>.example.com:4317" }), "replace <tenant> in the endpoint"},
		{"a bad secret name", mut(func(t *Telemetry) { t.Destination.AuthSecret = "Bad Name" }), "credential Secret name"},
		{"a bad secret key", mut(func(t *Telemetry) { t.Destination.AuthSecret = "ok"; t.Destination.AuthKey = "a b" }), "credential Secret key"},
		{"a bad header", mut(func(t *Telemetry) { t.Destination.AuthSecret = "ok"; t.Destination.AuthHeader = "X Key" }), "credential header name"},
		{"a bad secret name is not checked when the destination is as installed", mut(func(t *Telemetry) {
			t.Destination.AuthSecret = "Bad Name"
			t.Kept = []string{KeptDestination}
		}), ""},
		{"existing Kepler needs an endpoint", mut(func(t *Telemetry) { t.Signals.Energy = true; t.Energy = Source{Kind: "existing"} }), "The existing Prometheus endpoint is required when energy"},
		{"existing Kepler with a URL", mut(func(t *Telemetry) {
			t.Signals.Energy = true
			t.Energy = Source{Kind: "existing", Endpoint: "http://kepler:9102/metrics"}
		}), "Energy: the existing Prometheus endpoint is a host and port only"},
		{"existing Kepler is fine", mut(func(t *Telemetry) {
			t.Signals.Energy = true
			t.Energy = Source{Kind: "existing", Endpoint: "kepler.energy.svc:9102"}
		}), ""},
		{"existing DCGM needs an endpoint", mut(func(t *Telemetry) { t.Signals.Accelerators = true; t.Accelerators.Kind = "existing" }), "when accelerators points"},
		{"existing DCGM with a path", mut(func(t *Telemetry) {
			t.Signals.Accelerators = true
			t.Accelerators = Accelerators{Source: Source{Kind: "existing", Endpoint: "dcgm:9400/metrics"}}
		}), "Accelerators: the existing Prometheus endpoint is a host and port only"},
		{"an existing source nobody uses is not checked", mut(func(t *Telemetry) { t.Energy = Source{Kind: "existing", Endpoint: "nonsense"} }), ""},
		{"a bad iperf3 Secret", mut(func(t *Telemetry) {
			t.Signals.ClusterLinks = true
			t.ClusterLinks = ClusterLinks{Probe: true, Throughput: true, AuthSecret: "Bad_Name"}
		}), "Cluster links: the credential Secret name"},
		{"an iperf3 Secret nobody uses is not checked", mut(func(t *Telemetry) {
			t.Signals.ClusterLinks = true
			t.ClusterLinks = ClusterLinks{Probe: true, AuthSecret: "Bad_Name"}
		}), ""},
		{"network latency without the measurements", func() Telemetry {
			t := valid()
			t.Signals.NetworkLatency = true
			off := false
			t.MeasurementsOn = &off
			return t
		}(), "Network latency re-emits"},
		{"network latency with the measurements", func() Telemetry {
			t := valid()
			t.Signals.NetworkLatency = true
			on := true
			t.MeasurementsOn = &on
			return t
		}(), ""},
		{"sampling over 100", mut(func(t *Telemetry) { t.Processing.TracesSamplingPercent = pct(101) }), "Traces sampling must be between 0 and 100"},
		{"sampling under 0", mut(func(t *Telemetry) { t.Processing.TracesSamplingPercent = pct(-1) }), "Traces sampling must be between 0 and 100"},
		{"sampling not a number", mut(func(t *Telemetry) { t.Processing.TracesSamplingPercent = nil }), "Traces sampling must be between 0 and 100"},
		{"sampling 0 is a choice", mut(func(t *Telemetry) { t.Processing.TracesSamplingPercent = pct(0) }), ""},
		{"a reserved tag", mut(func(t *Telemetry) { t.Processing.Tags = []Tag{{"Ikhnos.org", "x"}} }), "is reserved for what Ikhnos adds itself"},
		{"a bad namespace in an application scope", mut(func(t *Telemetry) {
			t.Signals.ApplicationLogs = true
			t.Scopes.ApplicationLogs.Namespaces = []string{"Bad_Name"}
		}), `"Bad_Name" is not a valid namespace name`},
		{"a bad scope of a signal that is off is not checked", mut(func(t *Telemetry) { t.Scopes.ApplicationLogs.Namespaces = []string{"Bad_Name"} }), ""},
		{"a bad workload", mut(func(t *Telemetry) {
			t.Signals.Traces = true
			t.Scopes.Traces.Workloads = []Workload{{Namespace: "ok", Names: []string{"Bad"}}}
		}), `"Bad" is not a valid workload name`},
		{"workloads listed twice", mut(func(t *Telemetry) {
			t.Signals.ApplicationMetrics = true
			t.Scopes.ApplicationMetrics.Workloads = []Workload{{Namespace: "ok"}, {Namespace: "ok"}}
		}), `Workloads for "ok" are listed twice`},
		{"zipkin with metrics on", mut(func(t *Telemetry) { t.Destination.Protocol = "zipkin"; t.Signals.Traces = true }), "turn off metrics"},
		{"zipkin without traces", mut(func(t *Telemetry) {
			t.Destination.Protocol = "zipkin"
			t.Signals = Signals{}
			t.Signals.SystemLogs = true
		}), "turn off logs"},
		{"zipkin for traces", mut(func(t *Telemetry) { t.Destination.Protocol = "zipkin"; t.Signals = Signals{Traces: true} }), ""},
		{"zipkin for logs by route", mut(func(t *Telemetry) {
			t.Signals = Signals{SystemLogs: true}
			t.Routes.Split = true
			t.Routes.Logs = Destination{Endpoint: "z.example.com:9411", Protocol: "zipkin"}
		}), "Zipkin only carries traces, so it cannot be where logs go"},
		{"a route kept as installed is not checked", mut(func(t *Telemetry) {
			t.Routes.Split = true
			t.Routes.Metrics = Destination{Endpoint: "m example", Protocol: "grpc"}
			t.Routes.Kept = []Modality{Metrics}
		}), ""},
		{"a hostile route", mut(func(t *Telemetry) {
			t.Routes.Split = true
			t.Routes.Metrics = Destination{Endpoint: "m,x", Protocol: "grpc"}
		}), "Metrics: the endpoint"},
		{"a lane with no endpoint is only that", mut(func(t *Telemetry) {
			t.Routes.Split = true
			t.Routes.Metrics = Destination{Protocol: "zipkin"}
		}), "Metrics needs a destination"},
		{"a bad credential on a route", mut(func(t *Telemetry) {
			t.Routes.Split = true
			t.Routes.Metrics = Destination{Endpoint: "m.example.com:1", Protocol: "grpc", AuthSecret: "Bad Name"}
		}), "Metrics: the credential Secret name"},
		{"existing DCGM nobody uses is not checked", mut(func(t *Telemetry) {
			t.Accelerators = Accelerators{Source: Source{Kind: "existing", Endpoint: "nonsense"}}
		}), ""},
		{"a workload in a bad namespace", mut(func(t *Telemetry) {
			t.Signals.Traces = true
			t.Scopes.Traces.Workloads = []Workload{{Namespace: "Bad_NS", Names: []string{"ok"}}}
		}), `"Bad_NS" is not a valid namespace name`},
		{"a processor with no conditions", mut(func(t *Telemetry) { t.Processing.Extra = []Processor{{ID: "p", Kind: Filter, Name: "x"}} }), "has no conditions"},
	} {
		got := c.t.Problems()
		switch {
		case c.want == "" && len(got) != 0:
			t.Errorf("%s: unexpected %q", c.name, got)
		case c.want != "" && (len(got) != 1 || !strings.Contains(got[0], c.want)):
			t.Errorf("%s: got %q, want one containing %q", c.name, got, c.want)
		}
	}
}

func TestTelemetryWithProblemsHasNoValues(t *testing.T) {
	bad := valid()
	bad.Destination.Endpoint = ""
	v, err := bad.Values()
	var pe *ProblemsError
	if v != nil || err == nil || !asProblems(err, &pe) || len(pe.Problems) != 1 {
		t.Fatalf("got %v, %v", v, err)
	}
}

func asProblems(err error, into **ProblemsError) bool {
	pe, ok := err.(*ProblemsError)
	*into = pe
	return ok
}

func TestTelemetryNothingOn(t *testing.T) {
	if v, err := (Telemetry{}).Values(); v != nil || err != nil {
		t.Fatalf("a fresh install with no signal states nothing, got %v %v", v, err)
	}
	// An install that has telemetry and now has no signal: every switch off, the routes and extra processors cleared.
	v, err := Telemetry{HadTelemetry: true}.Values()
	if err != nil {
		t.Fatal(err)
	}
	got := Flags(v)
	for _, want := range []string{"--set telemetry.resourceUsage.metrics.enabled=false", "--set telemetry.clusterLinks.probe.capacity.enabled=false", "--set telemetry.accelerators.metrics.enabled=false",
		"--set-string telemetry.export.routes.logs.endpoint=", "--set-json telemetry.processors.extraTracesProcessorNames='[]'"} {
		if !strings.Contains(got, want) {
			t.Errorf("the off command lacks %s:\n%s", want, got)
		}
	}
	if n := len(v); n != len(signalFlags)+3+2 {
		t.Errorf("%d values", n)
	}
}

func TestEverySignalIsStatedWhateverItsValue(t *testing.T) {
	v, err := valid().Values()
	if err != nil {
		t.Fatal(err)
	}
	got := Flags(v)
	for _, f := range signalFlags[:6] {
		if f == "telemetry.clusterLinks.probe.enabled" {
			continue
		}
		if !strings.Contains(got, f+"=") {
			t.Errorf("%s is not stated", f)
		}
	}
	if !strings.Contains(got, "--set telemetry.resourceUsage.metrics.enabled=true") || !strings.Contains(got, "--set telemetry.energy.metrics.enabled=false") {
		t.Errorf("a signal is stated true when on and false when not:\n%s", got)
	}
}

func TestTheScopeTheInfrastructureFollows(t *testing.T) {
	scope := func(ns ...string) ScopeOverride { return ScopeOverride{Namespaces: ns} }
	for _, c := range []struct {
		name string
		t    Telemetry
		want ScopeOverride
		tag  string
	}{
		{"no application signal", Telemetry{}, ScopeOverride{}, ""},
		{"one narrowed", Telemetry{Signals: Signals{Traces: true}, Scopes: Scopes{Traces: scope("shop")}}, scope("shop"), "shop"},
		{"a signal that is not narrowed makes it everything", Telemetry{
			Signals: Signals{Traces: true, ApplicationLogs: true}, Scopes: Scopes{Traces: scope("shop")}}, ScopeOverride{}, ""},
		{"the union of namespaces", Telemetry{
			Signals: Signals{Traces: true, ApplicationLogs: true}, Scopes: Scopes{Traces: scope("shop", "b"), ApplicationLogs: scope("a", "shop")}}, scope("a", "b", "shop"), "a; b; shop"},
		{"a signal that is off does not count", Telemetry{
			Signals: Signals{Traces: true}, Scopes: Scopes{Traces: scope("shop"), ApplicationLogs: ScopeOverride{}}}, scope("shop"), "shop"},
		{"excluded by every signal", Telemetry{
			Signals: Signals{Traces: true, ApplicationLogs: true},
			Scopes:  Scopes{Traces: ScopeOverride{Exclude: []string{"x", "tmp"}}, ApplicationLogs: ScopeOverride{Exclude: []string{"tmp", "y"}}}},
			ScopeOverride{Exclude: []string{"tmp"}}, "all namespaces - excluding tmp"},
		{"excluded by one only is not excluded", Telemetry{
			Signals: Signals{Traces: true, ApplicationLogs: true},
			Scopes:  Scopes{Traces: ScopeOverride{Exclude: []string{"x"}}, ApplicationLogs: ScopeOverride{Exclude: []string{"y"}}}},
			ScopeOverride{}, ""},
		{"workloads of one signal", Telemetry{
			Signals: Signals{Traces: true},
			Scopes:  Scopes{Traces: ScopeOverride{Namespaces: []string{"shop"}, Workloads: []Workload{{"shop", []string{"worker", "api"}}}}}},
			ScopeOverride{Namespaces: []string{"shop"}, Workloads: []Workload{{"shop", []string{"api", "worker"}}}}, "shop: api+worker"},
		{"workloads of two signals are joined", Telemetry{
			Signals: Signals{Traces: true, ApplicationLogs: true},
			Scopes: Scopes{
				Traces:          ScopeOverride{Namespaces: []string{"shop"}, Workloads: []Workload{{"shop", []string{"api"}}}},
				ApplicationLogs: ScopeOverride{Namespaces: []string{"shop"}, Workloads: []Workload{{"shop", []string{"worker", "api"}}}}}},
			ScopeOverride{Namespaces: []string{"shop"}, Workloads: []Workload{{"shop", []string{"api", "worker"}}}}, "shop: api+worker"},
		{"a signal that keeps the namespace whole keeps it whole", Telemetry{
			Signals: Signals{Traces: true, ApplicationLogs: true},
			Scopes: Scopes{
				Traces:          ScopeOverride{Namespaces: []string{"shop"}, Workloads: []Workload{{"shop", []string{"api"}}}},
				ApplicationLogs: scope("shop")}},
			scope("shop"), "shop"},
		{"a namespace narrowed to nothing", Telemetry{
			Signals: Signals{Traces: true},
			Scopes:  Scopes{Traces: ScopeOverride{Namespaces: []string{"shop"}, Workloads: []Workload{{"shop", nil}}}}},
			ScopeOverride{Namespaces: []string{"shop"}, Workloads: []Workload{{"shop", nil}}}, "shop: nothing"},
		{"one signal everywhere, another in a namespace", Telemetry{
			Signals: Signals{Traces: true, ApplicationLogs: true},
			Scopes:  Scopes{Traces: ScopeOverride{Exclude: []string{"tmp"}}, ApplicationLogs: scope("a")}},
			ScopeOverride{}, ""},
		{"a signal that does not collect the namespace does not widen it", Telemetry{
			Signals: Signals{Traces: true, ApplicationLogs: true},
			Scopes: Scopes{
				Traces:          ScopeOverride{Namespaces: []string{"shop"}, Workloads: []Workload{{"shop", []string{"api"}}}},
				ApplicationLogs: scope("other")}},
			ScopeOverride{Namespaces: []string{"other", "shop"}, Workloads: []Workload{{"shop", []string{"api"}}}}, "other; shop: api"},
		{"workloads of one namespace everywhere", Telemetry{
			Signals: Signals{Traces: true},
			Scopes:  Scopes{Traces: ScopeOverride{Workloads: []Workload{{"a", []string{"y"}}}}}},
			ScopeOverride{Workloads: []Workload{{"a", []string{"y"}}}}, "all namespaces (a: y only)"},
		{"workloads everywhere", Telemetry{
			Signals: Signals{Traces: true},
			Scopes:  Scopes{Traces: ScopeOverride{Workloads: []Workload{{"b", []string{"x"}}, {"a", []string{"y"}}}}}},
			ScopeOverride{Workloads: []Workload{{"a", []string{"y"}}, {"b", []string{"x"}}}}, "all namespaces (a: y only; b: x only)"},
	} {
		got := c.t.CombinedScope()
		norm := func(s ScopeOverride) ScopeOverride {
			s.Namespaces, s.Exclude = names(s.Namespaces), names(s.Exclude)
			s.Workloads = workloads(s.Workloads)
			return s
		}
		if !reflect.DeepEqual(norm(got), norm(c.want)) {
			t.Errorf("%s: combined scope %+v, want %+v", c.name, got, c.want)
		}
		if tag := c.t.ScopeTag(); tag != c.tag {
			t.Errorf("%s: scope tag %q, want %q", c.name, tag, c.tag)
		}
	}
}

func TestTagsAreCleaned(t *testing.T) {
	got := CleanTags([]Tag{{" team ", " core "}, {"", ""}, {" ", " "}, {"env", "prod; test"}})
	if want := []Tag{{"team", "core"}, {"env", "prod; test"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	if got := CleanTags([]Tag{{"k", ""}}); !reflect.DeepEqual(got, []Tag{{"k", ""}}) {
		t.Fatalf("a half-filled row is kept, so that it is reported: %v", got)
	}
	if jsonText(CleanTags(nil)) != "[]" {
		t.Fatal("no tags are the empty list, not null")
	}
	for name, c := range map[string]struct {
		tags []Tag
		want string
	}{
		"a half-filled row is ignored": {[]Tag{{"", ""}}, ""},
		"no name":                      {[]Tag{{"", "v"}}, "Every tag needs a name"},
		"two with no name":             {[]Tag{{"", "v"}, {"", "w"}}, "Every tag needs a name"},
		"no value":                     {[]Tag{{"k", ""}}, "“k” needs a value"},
		"space in the name":            {[]Tag{{"a b", "v"}}, "a tag name cannot contain spaces"},
		"twice":                        {[]Tag{{"k", "1"}, {"k", "2"}}, "“k” is listed twice"},
		"too many": {func() []Tag {
			var ts []Tag
			for i := 0; i < TagLimit+1; i++ {
				ts = append(ts, Tag{string(rune('a' + i)), "v"})
			}
			return ts
		}(), "At most 8 tags"},
	} {
		p := TagProblems(c.tags)
		if (c.want == "") != (len(p) == 0) || (c.want != "" && !strings.Contains(p[0], c.want)) {
			t.Errorf("%s: %q", name, p)
		}
	}
}

func TestJSONIsWrittenAsTheCommandsAlwaysWroteIt(t *testing.T) {
	// <, > and & are left as they are (a filter's regular expression and a URL hold them), and a list that is empty is [] and not null.
	if got := jsonText([]Tag{{"k", "a<b>&c"}}); got != `[{"key":"k","value":"a<b>&c"}]` {
		t.Fatal(got)
	}
	if got := jsonText(workloads(nil)); got != "[]" {
		t.Fatal(got)
	}
}

// Sending each kind of signal separately: a route is stated in full for a kind that has a signal on, and empty for one that has none,
// which is how the chart reads "no route" (an unmentioned one would keep sending).
func TestRoutesAreStatedInFullOrEmpty(t *testing.T) {
	tel := valid()
	tel.Routes.Split = true
	tel.Routes.Metrics = Destination{Endpoint: "m.example.com:4317", Protocol: "grpc"}
	tel.Routes.Logs = Destination{Endpoint: "stale.example.com:4317", Protocol: "grpc"} // no log signal is on: never stated
	v, err := tel.Values()
	if err != nil {
		t.Fatal(err)
	}
	got := Flags(v)
	for _, want := range []string{"--set-string telemetry.export.routes.metrics.endpoint=m.example.com:4317", "--set telemetry.export.routes.metrics.protocol=grpc",
		"--set-string telemetry.export.routes.logs.endpoint= ", "--set-string telemetry.export.routes.traces.endpoint= "} {
		if !strings.Contains(got+" ", want) {
			t.Errorf("lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "stale") || strings.Contains(got, "routes.logs.protocol") || strings.Contains(got, "telemetry.export.otlp.endpoint") {
		t.Errorf("a route with no signal is not stated, nor is the single destination while routes are used:\n%s", got)
	}
}

func TestTagsWithNoNameAreEachReportedOnce(t *testing.T) {
	if got := TagProblems([]Tag{{"", "v"}, {"", "w"}}); len(got) != 2 || got[0] != "Every tag needs a name" || got[1] != got[0] {
		t.Fatalf("got %q", got)
	}
}
