package server

import (
	"net/http"
	"strings"

	"continuum/internal/httpkit"
)

// model serves the effective model: GET /api/v1/orgs/{org}/model. It carries an entity tag that changes exactly when
// the model's content does (not with the clock), so a poller sends If-None-Match and gets 304 while nothing changed.
func (a *Admin) model(w http.ResponseWriter, r *http.Request) {
	m, etag, err := a.tn(r).Hub.Model(r.Context())
	if err != nil {
		a.fail(w, err)
		return
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	httpkit.WriteJSON(w, 200, m)
}

// etagMatches implements If-None-Match for a strong tag: "*" or any listed tag (a weak validator W/ counts).
func etagMatches(header, etag string) bool {
	for _, p := range strings.Split(header, ",") {
		p = strings.TrimSpace(p)
		if p == "*" || strings.TrimPrefix(p, "W/") == etag {
			return true
		}
	}
	return false
}
