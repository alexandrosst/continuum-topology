package server

import (
	"net/http"
	"strings"
)

// rotateLinkCredentials is POST /api/v1/orgs/{org}/links/rotate-credentials: it replaces the iperf3 credentials the server issued for the
// throughput tests, with no work on the clusters. The body names either one cluster that serves tests ({"cluster": "cl-us"}: its key pair is
// made again) or one pair ({"a": "cl-eu", "b": "cl-us"}: its user name and password are). The new credentials reach the clusters in their next
// Config, within a minute, and the pair's tests fail with `auth` until both ends have them. Nothing secret is in the request or the answer,
// and the audit row names clusters only. Disabling a pair's tests and enabling them again also makes new ones.
func (a *Admin) rotateLinkCredentials(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster string `json:"cluster"`
		A       string `json:"a"`
		B       string `json:"b"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	req.Cluster, req.A, req.B = strings.TrimSpace(req.Cluster), strings.TrimSpace(req.A), strings.TrimSpace(req.B)
	byCluster, byPair := req.Cluster != "" && req.A == "" && req.B == "", req.Cluster == "" && req.A != "" && req.B != ""
	if !byCluster && !byPair {
		a.fail(w, errf(KindInvalid, "invalid request body: expected {\"cluster\": \"id\"} or {\"a\": \"id\", \"b\": \"id\"}"))
		return
	}
	c := a.core(r)
	target := req.Cluster
	if target == "" {
		target = min(req.A, req.B) + " " + max(req.A, req.B)
	}
	links := c.Settings().Links
	// A request that cannot rotate anything (an id that is no cluster id, a cluster that serves no pair with throughput tests, a pair
	// that does not ask for them) is refused before the audit row: audited() writes the row first, and a refused request would
	// leave two rows with text of the caller's choosing.
	if why := rotateRefusal(links, req.Cluster, req.A, req.B); why != "" {
		writeErr(w, http.StatusUnprocessableEntity, why)
		return
	}
	err := c.audited(r.Context(), actor(r), "link-credentials-rotate", "links", target, "iperf3 credentials for the throughput tests", func() error {
		if err := c.RotateLinkCredentials(r.Context(), links, req.Cluster, req.A, req.B); err != nil {
			return errf(KindInvalid, "%s", err.Error())
		}
		return nil
	})
	if err != nil {
		a.fail(w, err)
		return
	}
	go a.tn(r).Hub.pushConfigs()
	writeJSON(w, 200, map[string]any{"rotated": true})
}

// rotateRefusal says why a rotation request cannot do anything, or "" when it can: against the pairs that ask for throughput tests,
// which are the only ones that have credentials.
func rotateRefusal(links []LinkSetting, cluster, a, b string) string {
	for _, id := range []string{cluster, a, b} {
		if len(id) > 64 {
			return "a cluster id is at most 64 characters"
		}
	}
	pairs := throughputPairs(links)
	if cluster != "" {
		for _, p := range pairs {
			if linkServer(p[0], p[1]) == cluster {
				return ""
			}
		}
		return "that cluster serves no pair with throughput tests"
	}
	if !containsPair(pairs, [2]string{min(a, b), max(a, b)}) {
		return "that pair does not ask for throughput tests"
	}
	return ""
}
