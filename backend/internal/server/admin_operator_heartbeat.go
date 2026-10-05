package server

import (
	"errors"
	"fmt"
	"io"
	"net/http"
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
		"operator":                 toOperatorDoc(op, core.Now()),
		"rotated":                  rotated,
		"heartbeatToken":           secret,
		"heartbeatSecretCommand":   operatorHeartbeatSecretCommand(op, secret, rotated),
		"heartbeatUpgradeCommand":  fmt.Sprintf("helm upgrade %s %s%s --namespace continuum-system --reuse-values %s", op.ID, ref, version, operatorHeartbeatSetFlags(op, url)),
		"heartbeatUrl":             url,
		"heartbeatIntervalSeconds": int(OperatorHeartbeatInterval / time.Second),
	}
	if rotated {
		// A Secret's new value is not seen by a running pod's environment: the collector has to restart
		// to present it.
		resp["heartbeatRestartCommand"] = fmt.Sprintf("kubectl rollout restart deployment/%s-regional-operator --namespace continuum-system", op.ID)
	}
	if warn := heartbeatURLWarning(url); warn != "" {
		resp["heartbeatWarning"] = warn
	}
	writeJSON(w, 200, resp)
}

// heartbeatURL is the address an operator is told to report to: this server's admin address as the
// administrator reached it just now (scheme from the connection or the trusted proxy, host from the
// request) plus OperatorHeartbeatPath - the same source of truth relyingParty uses. If operators reach
// the server some other way, the person edits heartbeat.url; it is only a --set flag.
func (a *Admin) heartbeatURL(r *http.Request) string {
	scheme := "http"
	if a.secure(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host + OperatorHeartbeatPath
}

// heartbeatURLWarning explains a URL the chart will refuse by default: the heartbeat secret would cross
// the network in clear text.
func heartbeatURLWarning(url string) string {
	if strings.HasPrefix(url, "https://") {
		return ""
	}
	return "this address is plain HTTP, and the operator chart refuses a plain-HTTP heartbeat.url unless heartbeat.allowPlainHTTP=true, since the heartbeat secret would travel unencrypted. Open the server over HTTPS (or set heartbeat.url to its HTTPS address) before applying this"
}

func operatorHeartbeatSecretName(op store.Operator) string { return op.ID + "-heartbeat-auth" }

// operatorHeartbeatSetFlags is the Helm values that turn the heartbeat on. intervalSeconds is left at the
// chart's default, which equals OperatorHeartbeatInterval.
func operatorHeartbeatSetFlags(op store.Operator, url string) string {
	return fmt.Sprintf("--set heartbeat.enabled=true --set heartbeat.url=%s --set heartbeat.auth.secretName=%s", url, operatorHeartbeatSecretName(op))
}

// operatorHeartbeatSecretCommand creates the Secret the chart's heartbeat.auth.secretName points at. For a
// rotation the Secret already exists, so it is replaced in place instead.
func operatorHeartbeatSecretCommand(op store.Operator, secret string, replace bool) string {
	// Always create-or-update: a first install and a rotation are the same command, and a regenerated first
	// install no longer fails on "already exists". replace is kept so callers need not change.
	_ = replace
	return applySecretCommand(operatorHeartbeatSecretName(op), "continuum-system", "token="+secret)
}
