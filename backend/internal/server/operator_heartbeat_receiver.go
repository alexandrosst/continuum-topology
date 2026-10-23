package server

import (
	"errors"
	"io"
	"net/http"

	"ikhnos/internal/httpkit"
	"ikhnos/internal/kernel"
	"ikhnos/internal/operators"
	"ikhnos/internal/operatorsapi"
)

// OperatorHeartbeatPath is where an opted-in regional operator's collector POSTs its heartbeat. The route is the shell's
// (it is open: no session, no organisation in the path), the address the operators' commands name is the operators' (operatorsapi).
const OperatorHeartbeatPath = operatorsapi.HeartbeatPath

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
	err := a.C.Operators.RecordOperatorHeartbeat(r.Context(), secret)
	if errors.Is(err, operators.ErrHeartbeatRejected) {
		kernel.Metrics.AuthFailures.Add(1)
		// Failed attempts are throttled per address (never a successful one, as in guard), so guessing
		// secrets from one place is slow and a healthy operator can never lock itself out.
		if !a.hbFailRL.Allow(kernel.LimitKey(a.clientIP(r))) {
			w.Header().Set("Retry-After", "60")
			httpkit.WriteErr(w, http.StatusTooManyRequests, "too many failed attempts, wait a minute")
			return
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="ikhnos operator heartbeat"`)
		httpkit.WriteErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if errors.Is(err, operators.ErrHeartbeatRateLimited) {
		w.Header().Set("Retry-After", "60")
		httpkit.WriteErr(w, http.StatusTooManyRequests, "heartbeats are arriving too often")
		return
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	// Read and discard, bounded. Authentication came first so an unauthenticated caller never gets this
	// server to read anything.
	if r.ContentLength > operators.MaxHeartbeatBody {
		httpkit.WriteErr(w, http.StatusRequestEntityTooLarge, "heartbeat too large")
		return
	}
	if _, err := io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, operators.MaxHeartbeatBody)); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			httpkit.WriteErr(w, http.StatusRequestEntityTooLarge, "heartbeat too large")
			return
		}
		// A client that hung up mid-body gets nothing useful from any answer.
		httpkit.WriteErr(w, http.StatusBadRequest, "could not read the request")
		return
	}
	w.WriteHeader(http.StatusOK)
}
