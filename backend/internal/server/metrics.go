package server

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"time"

	"continuum/internal/kernel"
	"continuum/internal/store"
)

// MetricsSource is what the metrics endpoint asks for the numbers that are not counters: it is the platform.
type MetricsSource interface {
	// AgentCounts is the number of agents in each status, across every organisation, and how many have a live stream.
	AgentCounts(ctx context.Context) (byStatus map[string]int, connected int, err error)
}

// AgentCounts implements MetricsSource.
func (p *Platform) AgentCounts(ctx context.Context) (map[string]int, int, error) {
	out := map[string]int{}
	orgs, err := p.Base.Store.ListOrgs(ctx)
	if err != nil {
		return nil, 0, err
	}
	for _, o := range orgs {
		agents, err := p.Base.Store.ListAgents(ctx, o.ID)
		if err != nil {
			return nil, 0, err
		}
		for _, a := range agents {
			out[string(a.Status)]++
		}
	}
	connected := 0
	p.mu.Lock()
	for _, t := range p.tenants {
		t.Hub.mu.Lock()
		connected += len(t.Hub.sessions)
		t.Hub.mu.Unlock()
	}
	p.mu.Unlock()
	return out, connected, nil
}

// MetricsHandler serves /metrics. token, when not empty, must be presented as a bearer token: the endpoint counts agents across every
// organisation, so it is not something to leave open on a network. Without one it is meant for a loopback or otherwise
// protected address (see --metrics-listen).
func MetricsHandler(src MetricsSource, version, token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		if token != "" {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="continuum metrics"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		writeMetrics(ctx, w, src, version)
	})
	return mux
}

func writeMetrics(ctx context.Context, w http.ResponseWriter, src MetricsSource, version string) {
	m := kernel.Metrics
	var b strings.Builder
	line := func(name, help, typ string, v any, labels ...string) {
		if help != "" {
			fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
		}
		l := ""
		if len(labels) > 0 {
			l = "{" + strings.Join(labels, ",") + "}"
		}
		fmt.Fprintf(&b, "%s%s %v\n", name, l, v)
	}
	line("continuum_build_info", "The server's version (always 1).", "gauge", 1, fmt.Sprintf("version=%q", version), fmt.Sprintf("go=%q", runtime.Version()))
	line("continuum_uptime_seconds", "Seconds since the process started.", "gauge", int64(time.Since(m.Started).Seconds()))
	if src != nil {
		by, connected, err := src.AgentCounts(ctx)
		if err != nil {
			m.StoreErrors.Add(1)
			line("continuum_agents_count_error", "1 when the agent counts could not be read from the store.", "gauge", 1)
		} else {
			keys := make([]string, 0, len(by))
			for k := range by {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			fmt.Fprintf(&b, "# HELP continuum_agents Agents by status, across every organisation.\n# TYPE continuum_agents gauge\n")
			for _, st := range []string{string(store.StatusPending), string(store.StatusApproved), string(store.StatusRevoked), string(store.StatusRejected), string(store.StatusExpired)} {
				fmt.Fprintf(&b, "continuum_agents{status=%q} %d\n", st, by[st])
			}
			line("continuum_agents_connected", "Agents with a live stream to this server now.", "gauge", connected)
		}
	}
	line("continuum_syncs_applied_total", "Pictures of a cluster (full or changes) received and applied.", "counter", m.SyncsApplied.Load())
	line("continuum_syncs_chunked_total", "Of those, how many arrived as several messages.", "counter", m.SyncsChunked.Load())
	line("continuum_syncs_refused_total", "Messages from agents refused (over a limit, malformed, out of order).", "counter", m.SyncsRefused.Load())
	line("continuum_auth_failures_total", "Failed sign-ins, failed session checks and agent connections without a valid certificate.", "counter", m.AuthFailures.Load())
	line("continuum_rate_limited_total", "Requests and messages refused because a rate limit was reached.", "counter", m.RateLimited.Load())
	line("continuum_stream_panics_total", "Panics recovered in a request handler or an agent stream.", "counter", m.StreamPanics.Load())
	line("continuum_store_errors_total", "Failed writes to the database (audit, snapshots, agent records) and failed readiness checks.", "counter", m.StoreErrors.Load())
	line("continuum_agent_tier_changes_total", "Times an administrator changed an agent's approved access tier.", "counter", m.TierChanges.Load())
	line("continuum_agent_consent_changes_total", "Times an administrator changed the collectors or namespaces an agent is asked to leave out.", "counter", m.ConsentChanges.Load())
	line("continuum_flow_items_dropped_total", "Flows, pod flows and collector reports dropped from an accepted flow batch because they were malformed or cannot become a dependency (node-level traffic); the rest of the batch was kept.", "counter", m.FlowItemsDropped.Load())
	line("continuum_implausible_facts_total", "Reported field values omitted for being physically impossible (a negative count, an out-of-range percentage), never clamped to a plausible-looking number.", "counter", m.ImplausibleFacts.Load())
	_, _ = w.Write([]byte(b.String()))
}
