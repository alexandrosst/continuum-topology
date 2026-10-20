package server

import (
	"continuum/internal/fusionhttp"
)

func fusionOps(a *Admin) []fusionhttp.Op {
	rng := []string{"from", "to"}
	join := func(groups ...[]string) []string {
		var out []string
		for _, g := range groups {
			out = append(out, g...)
		}
		return out
	}
	ops := []fusionhttp.Op{
		{Method: "GET", Path: "/status", Tag: "Service", Summary: "Is FUSION up, and what may I read?",
			Description: "Whether the stores are running and what the credential you present may read: its signals, namespaces and clusters, and when it expires.",
			Response:    "Status", Handler: a.fusionStatus},
		{Method: "GET", Path: "/applications", Tag: "Applications", Summary: "List the Ikhnos applications",
			Description: "The applications Ikhnos groups services into (made by a person or found by discovery), each with its services and which signals (metrics, logs, traces) FUSION has for them in the range. Pass an application's `id` or `name` as `application` to the reads below to read only that application. A token limited to certain namespaces or clusters sees only the services of an application in them, and no application with none there. The services as telemetry names them are listed by `/services`.",
			Params:      rng, Response: "ApplicationList", Handler: a.fusionApplications},
		{Method: "GET", Path: "/applications/{name}", Tag: "Applications", Summary: "One Ikhnos application at a glance",
			Description: "The signals the application has across all of its services, its most recent traces and failing traces, its latest error logs and the metric names it reports: the places to go on from. `name` is the application's id or name.",
			PathParams:  []string{"name"}, Params: rng, Response: "Overview", Handler: a.fusionApplication},
		{Method: "GET", Path: "/applications/{name}/topology", Tag: "Applications", Summary: "The topology of one Ikhnos application",
			Description: "The application's services with their health, the traffic between them and with everything else (the other services and addresses outside the clusters), and what Ikhnos recorded about all of that in the window before. Each service carries the labels its telemetry is found by (`telemetry.filter`), so the telemetry itself is read with `application` on the other routes: this answer is pointers, not data. Without `at` it is the estate now; with it, as the graph database remembered it at that moment (and `history is not enabled` without one). A token limited to certain namespaces or clusters sees only the services, neighbours and links it may see, and no addresses outside the clusters. `name` is the application's id or name.",
			PathParams:  []string{"name"}, Params: []string{"at", "window", "noise"}, Response: "ApplicationTopology", Handler: a.fusionAppTopology},
		{Method: "GET", Path: "/services", Tag: "Services", Summary: "List the services that have telemetry",
			Description: "Every service FUSION has data for in the range (its `service.name`), with which signals it has and the Ikhnos applications it is in. This includes infrastructure that reports under a name of its own, such as an exporter or an energy meter; `applications` is empty for those.",
			Params:      join(rng, []string{"application"}), Response: "ServiceList", Handler: a.fusionServices},
		{Method: "GET", Path: "/services/{name}", Tag: "Services", Summary: "One service at a glance",
			Description: "The signals the service has, its most recent traces and failing traces, its latest error logs and the metric names it reports.",
			PathParams:  []string{"name"}, Params: rng, Response: "Overview", Handler: a.fusionService},

		{Method: "GET", Path: "/links", Tag: "Links", Summary: "The latest figures of the links between clusters",
			Description: "For each pair of clusters the organisation watches (the `links` of its settings), the latest loss in each direction, round-trip time, delay variation, path MTU and throughput, and when they were measured, read from the `continuum_link_*` metrics the clusters send. A figure FUSION does not hold is absent, never zero. Each pair is listed once, with `a` before `b`, `aToB` and `bToA` naming its directions, and `measuredBy` saying whose series the figures are: both clusters measure towards each other, so do not count a pair under each of its clusters. The figures are fixed queries read by the server and shared for 15 seconds, so asking more often only costs your rate limit. Needs the `metrics` signal. A token limited to clusters sees only the pairs whose two clusters it may read; one limited to namespaces is refused, since the figures belong to no namespace. For arbitrary series, use `/metrics/range` with `metric=continuum_link_.*` (category `system`).",
			Params:      []string{"cluster", "peer"}, Notes: map[string]string{"cluster": "Only the pairs this cluster is in, on either side.", "peer": "With `cluster`: only the pair of the two. Alone it is refused."},
			Response: "LinkList", Handler: a.fusionLinks},
	}
	ops = append(ops, readOps(a)...)
	return append(ops, nativeOps(a)...)
}

// readOps are the metric, log and trace routes, which fusionhttp answers itself. A nil Admin (the table read only to describe the
// API) has no guard to bind them to: the handlers are methods of the guard, so a nil one still describes them.
func readOps(a *Admin) []fusionhttp.Op {
	var g *fusionhttp.Guard
	if a != nil {
		g = a.fd()
	}
	return g.ReadOps()
}
