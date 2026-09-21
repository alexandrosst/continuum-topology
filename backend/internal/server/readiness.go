package server

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// Readiness says what /readyz checks besides the process being up. Every field is optional.
//
//   - The store is always checked (with one cheap read): without it nothing works, so the server is not ready.
//   - AgentsListening reports whether the agents' listener is accepting connections; without it no agent can connect.
//   - Graph is for the optional Neo4j history: when it is configured but not reachable the server is still ready (the
//     live topology, enrollment and the agents all work from the local database) and says it is degraded.
type Readiness struct {
	AgentsListening func() bool
	// Graph returns whether Neo4j is configured and, if so, whether it is reachable now.
	Graph func() (configured, ready bool)

	mu     sync.Mutex
	at     time.Time
	result string
	ok     bool
}

// readyTTL caches the answer so that a probe hitting the endpoint every second costs the database one read per interval.
const readyTTL = 2 * time.Second

func (a *Admin) readyz(w http.ResponseWriter, r *http.Request) {
	rd := a.Readiness
	if rd == nil {
		rd = &Readiness{}
	}
	ok, body := rd.check(r.Context(), a)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if !ok {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_, _ = w.Write([]byte(body + "\n"))
}

func (rd *Readiness) check(ctx context.Context, a *Admin) (bool, string) {
	rd.mu.Lock()
	defer rd.mu.Unlock()
	if !rd.at.IsZero() && time.Since(rd.at) < readyTTL {
		return rd.ok, rd.result
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ok, body := true, "ready"
	var notes []string
	if _, err := a.C.Store.CountUsers(cctx); err != nil {
		Metrics.storeErrors.Add(1)
		ok, body = false, "not ready: the database is not reachable"
	} else if rd.AgentsListening != nil && !rd.AgentsListening() {
		ok, body = false, "not ready: the agents' listener is not accepting connections"
	} else if rd.Graph != nil {
		if configured, up := rd.Graph(); configured && !up {
			notes = append(notes, "degraded: the Neo4j history is not reachable (live topology, agents and sign-in are unaffected)")
		}
	}
	if ok && len(notes) > 0 {
		body = "ready, " + notes[0]
	}
	rd.at, rd.ok, rd.result = time.Now(), ok, body
	return ok, body
}
