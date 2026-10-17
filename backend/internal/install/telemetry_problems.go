package install

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

const (
	// TagLimit is how many tags a person can add: each is stamped on every record, so it costs storage and cardinality.
	TagLimit = 8
	// ReservedTagPrefix is the provenance the chart stamps itself; a tag under it would be refused by the chart.
	ReservedTagPrefix = "continuum."
)

var (
	// An endpoint is a host or URL as a person pastes it: it reaches a shell line and a `helm --set`, which splits a value on every
	// comma and reads a backslash as an escape, so those can never be part of one. A query string with & or a bracketed IPv6 host is
	// fine (the command quotes it), and so is a preset's <tenant> placeholder opening it; white space, quotes, backticks, commas,
	// backslashes and control characters are not.
	exportEndpoint   = regexp.MustCompile("^[A-Za-z0-9\\[<][^\\s\\p{Z}'\"`,\\\\\\x00-\\x1f\\x7f]{0,510}$")
	exportHeaderName = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	secretName       = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)
	secretKeyName    = regexp.MustCompile(`^[-._a-zA-Z0-9]{1,253}$`)
	// A preset's `<tenant>`-style placeholder left in an endpoint is text to the shell, so the command would print and run and send
	// everything to a host that does not exist.
	placeholder = regexp.MustCompile(`<[^<>]*>`)
	// scrapeTarget is a Prometheus endpoint that already exists: a host and a port, nothing else. The chart puts it verbatim in the
	// collector's scrape targets, where a scheme or a path stops the collector from starting.
	scrapeTarget = regexp.MustCompile(`^(?:[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?|\[[0-9A-Fa-f:.]+\]):[0-9]{1,5}$`)
)

func scrapeTargetProblem(value, what string) []string {
	v := strings.TrimSpace(value)
	if v == "" || scrapeTarget.MatchString(v) {
		return nil
	}
	return []string{what + " is a host and port only (kepler.monitoring:9102): no http://, no path - the collector scrapes /metrics itself"}
}

// destinationProblems says what is wrong with the typed parts of one destination, worded for the field they are in. credential is
// whether the credential's names are checked: a destination left as installed does not state them.
func destinationProblems(d Destination, label string, credential bool) []string {
	var out []string
	endpoint := strings.TrimSpace(d.Endpoint)
	if endpoint != "" && !exportEndpoint.MatchString(endpoint) {
		out = append(out, label+": the endpoint is a host and port (otlp.example.com:4317) or a URL, without spaces, quotes, commas or backslashes")
	} else if m := placeholder.FindString(endpoint); m != "" {
		out = append(out, fmt.Sprintf("%s: replace %s in the endpoint with the real value", label, m))
	}
	if secret := strings.TrimSpace(d.AuthSecret); credential && secret != "" {
		if !secretName.MatchString(secret) {
			out = append(out, label+": the credential Secret name uses lowercase letters, digits, - and . only")
		}
		if key := strings.TrimSpace(d.AuthKey); key != "" && !secretKeyName.MatchString(key) {
			out = append(out, label+": the credential Secret key uses letters, digits, . _ and - only")
		}
		if header := strings.TrimSpace(d.AuthHeader); header != "" && !exportHeaderName.MatchString(header) {
			out = append(out, label+": the credential header name uses letters, digits and - only")
		}
	}
	return out
}

// TagProblems says what is wrong with a tag list, in words a person can act on.
func TagProblems(tags []Tag) []string {
	var out []string
	// A row nobody has typed into yet is not a tag (CleanTags drops it from the command too).
	var filled []Tag
	for _, t := range tags {
		if strings.TrimSpace(t.Key) != "" || strings.TrimSpace(t.Value) != "" {
			filled = append(filled, t)
		}
	}
	if len(filled) > TagLimit {
		out = append(out, fmt.Sprintf("At most %d tags: each is stamped on every record", TagLimit))
	}
	seen := map[string]bool{}
	for _, t := range filled {
		key := strings.TrimSpace(t.Key)
		if key == "" {
			out = append(out, "Every tag needs a name")
			continue
		}
		if strings.HasPrefix(strings.ToLower(key), ReservedTagPrefix) {
			out = append(out, fmt.Sprintf("“%s” starts with %s, which is reserved for what Ikhnos adds itself", key, ReservedTagPrefix))
		}
		if strings.ContainsFunc(key, unicode.IsSpace) {
			out = append(out, fmt.Sprintf("“%s”: a tag name cannot contain spaces", key))
		}
		if seen[key] {
			out = append(out, fmt.Sprintf("“%s” is listed twice", key))
		}
		seen[key] = true
		if strings.TrimSpace(t.Value) == "" {
			out = append(out, fmt.Sprintf("“%s” needs a value", key))
		}
	}
	return out
}

// CleanTags is what the tags are stamped as: trimmed, and with nothing half-filled-in left over.
func CleanTags(tags []Tag) []Tag {
	out := []Tag{}
	for _, t := range tags {
		c := Tag{Key: strings.TrimSpace(t.Key), Value: strings.TrimSpace(t.Value)}
		if c.Key != "" || c.Value != "" {
			out = append(out, c)
		}
	}
	return out
}

// workloadName is a workload (deployment, statefulset, ...) name as the chart's filter accepts it.
var workloadName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func workloadProblems(ws []Workload) []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range ws {
		if !workloadName.MatchString(w.Namespace) {
			out = append(out, fmt.Sprintf("%q is not a valid namespace name", w.Namespace))
		}
		if seen[w.Namespace] {
			out = append(out, fmt.Sprintf("Workloads for %q are listed twice", w.Namespace))
		}
		seen[w.Namespace] = true
		for _, n := range w.Names {
			if !workloadName.MatchString(n) {
				out = append(out, fmt.Sprintf("%q is not a valid workload name (lowercase letters, digits and hyphens)", n))
			}
		}
	}
	return out
}

func (t Telemetry) scopeProblems(on bool, s ScopeOverride) []string {
	if !on {
		return nil
	}
	return append(NamespaceProblems(append(append([]string{}, s.Namespaces...), s.Exclude...)), workloadProblems(s.Workloads)...)
}

// Problems says what is wrong with the telemetry settings, in words a person can act on; none when they are fine. It holds what would
// make the command broken or unsafe: a missing endpoint, typed text that does not belong in a command, a value the chart would refuse.
// Advice about a particular backend (that Jaeger only takes traces) belongs to whoever knows the backend, not here.
func (t Telemetry) Problems() []string {
	if !t.Active() {
		return nil
	}
	var out []string
	s := t.Signals
	if !t.Routes.Split && strings.TrimSpace(t.Destination.Endpoint) == "" {
		out = append(out, "An export endpoint is required once any telemetry signal is on")
	}
	if s.Energy && t.Energy.Kind == "existing" && strings.TrimSpace(t.Energy.Endpoint) == "" {
		out = append(out, "The existing Prometheus endpoint is required when energy points at an existing source")
	}
	if s.Accelerators && t.Accelerators.Kind == "existing" && strings.TrimSpace(t.Accelerators.Endpoint) == "" {
		out = append(out, "The existing Prometheus endpoint is required when accelerators points at an existing source")
	}
	// Typed text that ends up in the printed command. A destination as installed (kept) still has its endpoint stated.
	if !t.Routes.Split {
		out = append(out, destinationProblems(t.Destination, "Destination", !t.kept(KeptDestination))...)
	}
	if s.Energy && t.Energy.Kind == "existing" {
		out = append(out, scrapeTargetProblem(t.Energy.Endpoint, "Energy: the existing Prometheus endpoint")...)
	}
	if s.Accelerators && t.Accelerators.Kind == "existing" {
		out = append(out, scrapeTargetProblem(t.Accelerators.Endpoint, "Accelerators: the existing Prometheus endpoint")...)
	}
	if l := t.ClusterLinks; s.ClusterLinks && !t.kept(KeptClusterLinksOptions) && l.Probe && l.Throughput {
		// Optional: left empty, Ikhnos issues the iperf3 credentials. Only a name that was typed has to be a valid one.
		if secret := strings.TrimSpace(l.AuthSecret); secret != "" && !secretName.MatchString(secret) {
			out = append(out, "Cluster links: the credential Secret name uses lowercase letters, digits, - and . only")
		}
	}
	if s.NetworkLatency && t.MeasurementsOn != nil && !*t.MeasurementsOn {
		out = append(out, "Network latency re-emits the path measurements extra, so turn that on too, or it will report nothing")
	}
	// An emptied field is not a number (it is not turned into 0: that would silently mean "drop every trace").
	if p := t.Processing.TracesSamplingPercent; p == nil || !(*p >= 0 && *p <= 100) {
		out = append(out, "Traces sampling must be between 0 and 100")
	}
	out = append(out, ProcessorProblems(t.Processing.Extra)...)
	out = append(out, TagProblems(t.Processing.Tags)...)
	out = append(out, t.scopeProblems(s.ApplicationMetrics, t.Scopes.ApplicationMetrics)...)
	out = append(out, t.scopeProblems(s.ApplicationLogs, t.Scopes.ApplicationLogs)...)
	out = append(out, t.scopeProblems(s.Traces, t.Scopes.Traces)...)
	if t.Routes.Split {
		for _, m := range t.ActiveLanes() {
			lane := t.Routes.lane(m)
			title := strings.ToUpper(string(m)[:1]) + string(m)[1:]
			endpoint := strings.TrimSpace(lane.Endpoint)
			if endpoint == "" {
				out = append(out, title+" needs a destination")
				continue
			}
			// A route kept as installed is left out of the command, so what it holds is not typed text going into one.
			if !t.laneKept(m) {
				out = append(out, destinationProblems(lane, title, true)...)
			}
			if lane.Protocol == "zipkin" && m != Traces {
				out = append(out, fmt.Sprintf("Zipkin only carries traces, so it cannot be where %s go", m))
			}
		}
		return out
	}
	if t.Destination.Protocol == "zipkin" {
		var others []string
		for _, m := range Modalities {
			if m != Traces && s.Modalities()[m] {
				others = append(others, string(m))
			}
		}
		// (With a signal on, and only traces carried, there is always another kind to turn off, or the destination carries what is on.)
		if len(others) > 0 {
			out = append(out, fmt.Sprintf("Zipkin only carries traces - turn off %s, or send everything somewhere that speaks OTLP", strings.Join(others, " and ")))
		}
	}
	return out
}
