package server

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"continuum/internal/store"
)

// OperatorHeartbeatPath is where an opted-in regional operator's collector POSTs its heartbeat (its
// otlphttp exporter's metrics_endpoint). It is outside /api/v1/orgs/{org}: the caller has no session and
// belongs to no browser, only a secret that finds the operator by itself.
const OperatorHeartbeatPath = "/api/v1/operator-heartbeat"

// operatorHeartbeat receives one heartbeat. Authentication is the heartbeat secret in `Authorization:
// Bearer ...` and nothing else: no cookie, no organisation in the path, and it is deliberately NOT behind
// the CORS or CSRF wrappers (no browser ever calls it, so no origin is ever allowed to).
//
// It answers 200 with an empty body on success - which is what the OpenTelemetry Collector's otlphttp
// exporter treats as delivered - and 401 with one fixed body for every kind of failure, so a caller learns
// nothing about which operators or secrets exist. The request body is the exporter's OTLP metrics message
// carrying only the collector's own health probe result; it is read, bounded, and thrown away.
func (a *Admin) operatorHeartbeat(w http.ResponseWriter, r *http.Request) {
	secret, _ := bearerToken(r)
	err := a.C.RecordOperatorHeartbeat(r.Context(), secret)
	if errors.Is(err, errHeartbeatRejected) {
		Metrics.authFailures.Add(1)
		// Failed attempts are throttled per address (never a successful one, as in guard), so guessing
		// secrets from one place is slow and a healthy operator can never lock itself out.
		if !a.hbFailRL.Allow(LimitKey(a.clientIP(r))) {
			w.Header().Set("Retry-After", "60")
			writeErr(w, http.StatusTooManyRequests, "too many failed attempts, wait a minute")
			return
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="continuum operator heartbeat"`)
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if errors.Is(err, errHeartbeatRateLimited) {
		w.Header().Set("Retry-After", "60")
		writeErr(w, http.StatusTooManyRequests, "heartbeats are arriving too often")
		return
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	// Read and discard, bounded. Authentication came first so an unauthenticated caller never gets this
	// server to read anything.
	if r.ContentLength > maxHeartbeatBody {
		writeErr(w, http.StatusRequestEntityTooLarge, "heartbeat too large")
		return
	}
	if _, err := io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, maxHeartbeatBody)); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeErr(w, http.StatusRequestEntityTooLarge, "heartbeat too large")
			return
		}
		// A client that hung up mid-body gets nothing useful from any answer.
		writeErr(w, http.StatusBadRequest, "could not read the request")
		return
	}
	w.WriteHeader(http.StatusOK)
}

// enableOperatorHeartbeat mints (or rotates) an existing operator's heartbeat secret and returns the
// one-time commands that put it to use, in the same style createOperator's reminders use.
func (a *Admin) enableOperatorHeartbeat(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	core := a.core(r)
	secret, rotated, err := core.EnableOperatorHeartbeat(r.Context(), actor(r), id)
	if err != nil {
		a.fail(w, err)
		return
	}
	op, err := core.GetOperator(r.Context(), id)
	if err != nil {
		a.fail(w, err)
		return
	}
	img := a.images(core)
	ref, version := a.operatorChartArgs(img)
	url := a.heartbeatURL(r)
	resp := map[string]any{
		"operator":                 a.opDoc(r, op),
		"rotated":                  rotated,
		"heartbeatToken":           secret,
		"heartbeatSecretCommand":   operatorHeartbeatSecretCommand(op, secret),
		"heartbeatUpgradeCommand":  fmt.Sprintf("helm upgrade %s %s%s --namespace continuum-system --reuse-values %s", op.ID, ref, version, a.operatorHeartbeatSetFlags(op, url)),
		"heartbeatUrl":             url,
		"heartbeatIntervalSeconds": int(OperatorHeartbeatInterval / time.Second),
	}
	if rotated {
		// A Secret's new value is not seen by a running pod's environment: the collector has to restart
		// to present it.
		resp["heartbeatRestartCommand"] = operatorRestartCommand(op)
	}
	if warn := heartbeatURLWarning(url); warn != "" {
		resp["heartbeatWarning"] = warn
	}
	if caCmd := a.heartbeatCASecretCommand(op); caCmd != "" {
		resp["heartbeatCaSecretCommand"] = caCmd
	}
	writeJSON(w, 200, resp)
}

// heartbeatURL is the address an operator is told to report to: the server's configured public URL (PublicURL)
// when there is one, and otherwise this server's admin address as the administrator reached it just now (scheme
// from the connection or the trusted proxy, host from the request) plus OperatorHeartbeatPath - the same source of
// truth relyingParty uses. The request's own host is a guess that is wrong exactly when it matters (an administrator
// on a port-forward, or through an internal name, is not how an operator in another cluster reaches the server), so
// a configured URL wins, and heartbeatURLWarning says when the address it is left with cannot be right. Whichever it
// is, the person can still edit heartbeat.url; it is only a --set flag.
func (a *Admin) heartbeatURL(r *http.Request) string {
	if base := a.publicBase(); base != "" {
		return base + OperatorHeartbeatPath
	}
	scheme := "http"
	if a.secure(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host + OperatorHeartbeatPath
}

// publicBase is PublicURL cleaned up for use as a prefix ("" when it is not set, or is not an http(s) URL with a host).
func (a *Admin) publicBase() string {
	u, err := url.Parse(strings.TrimSpace(a.PublicURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host + strings.TrimRight(u.Path, "/")
}

// heartbeatURLWarning explains an address that cannot work as it stands: plain HTTP is refused by the chart by
// default (the heartbeat secret would cross the network in clear text), and a host that only exists inside one
// machine, cluster or network is not one an operator elsewhere can report to.
func heartbeatURLWarning(heartbeatURL string) string {
	var warn []string
	if !strings.HasPrefix(heartbeatURL, "https://") {
		warn = append(warn, "this address is plain HTTP, and the operator chart refuses a plain-HTTP heartbeat.url unless heartbeat.allowPlainHTTP=true, since the heartbeat secret would travel unencrypted. Open the server over HTTPS (or set heartbeat.url to its HTTPS address) before applying this")
	}
	if u, err := url.Parse(heartbeatURL); err == nil {
		if why := unreachableHostWhy(u.Hostname()); why != "" {
			warn = append(warn, fmt.Sprintf("%s is %s, which an operator in another cluster or network cannot reach. Give the server a public URL (admin.publicURL in the chart, or CONTINUUM_PUBLIC_URL), or set heartbeat.url to the address the operator can use", u.Hostname(), why))
		}
	}
	return strings.Join(warn, ". ")
}

// unreachableHostWhy says why host cannot be reached from another network ("" when it may be): this machine, a Service
// name that exists inside one cluster, or a private address.
func unreachableHostWhy(host string) string {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if ip := net.ParseIP(host); ip != nil {
		switch {
		case ip.IsLoopback() || ip.IsUnspecified():
			return "this machine"
		case ip.IsPrivate() || ip.IsLinkLocalUnicast():
			return "a private address"
		}
		return ""
	}
	switch {
	case host == "localhost" || strings.HasSuffix(host, ".localhost"):
		return "this machine"
	case isClusterLocalName(host):
		return "a name that only exists inside one Kubernetes cluster"
	}
	return ""
}

func operatorHeartbeatSecretName(op store.Operator) string   { return op.ID + "-heartbeat-auth" }
func operatorHeartbeatCASecretName(op store.Operator) string { return op.ID + "-heartbeat-ca" }

// operatorHeartbeatSetFlags is the Helm values that turn the heartbeat on. intervalSeconds is left at the
// chart's default, which equals OperatorHeartbeatInterval. When the server's certificate is signed by a private CA
// (HeartbeatCAPEM) the chart is told which Secret holds that CA, or the operator could never verify the server.
func (a *Admin) operatorHeartbeatSetFlags(op store.Operator, heartbeatURL string) string {
	flags := "--set heartbeat.enabled=true " + setFlag("heartbeat.url", heartbeatURL) + " " + setFlag("heartbeat.auth.secretName", operatorHeartbeatSecretName(op))
	if len(a.HeartbeatCAPEM) > 0 {
		flags += " " + setFlag("heartbeat.tls.caSecretName", operatorHeartbeatCASecretName(op))
	}
	return flags
}

// heartbeatCASecretCommand creates the Secret holding the private CA the server's certificate is signed by, in the
// operator's namespace; "" when the server's certificate chains to a public root and there is nothing to add.
func (a *Admin) heartbeatCASecretCommand(op store.Operator) string {
	if len(a.HeartbeatCAPEM) == 0 {
		return ""
	}
	return withNamespace("continuum-system", applySecretCommand(operatorHeartbeatCASecretName(op), "continuum-system", secretKV("ca.crt", string(a.HeartbeatCAPEM))))
}

// operatorHeartbeatSecretCommand creates the Secret the chart's heartbeat.auth.secretName points at. A first install and
// a rotation are the same create-or-update command, and it makes the namespace first, so it can be the first thing run.
func operatorHeartbeatSecretCommand(op store.Operator, secret string) string {
	return withNamespace("continuum-system", applySecretCommand(operatorHeartbeatSecretName(op), "continuum-system", secretKV("token", secret)))
}
