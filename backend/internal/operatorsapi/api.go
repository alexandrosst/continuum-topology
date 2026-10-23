// Package operatorsapi is the HTTP half of the regional operators: creating one and installing it, what it is told to accept and from whom,
// the address other clusters reach it at and a look at that address, its certificates, its heartbeat, and the commands a person pastes to put
// each of those to use. The operators themselves (their records, certificates and health) are the package operators; this package writes
// them down as documents and as commands.
//
// It depends on the shell for four things, handed in as edges: the operators of the organisation a request is for, the central operator
// (which lives with the server and may not exist), the install settings the commands are written from, and the few facts only the shell
// knows (whether the request came over HTTPS, which release an agent runs as).
package operatorsapi

import (
	"time"

	"context"
	"ikhnos/internal/agents"
	"ikhnos/internal/connectivity"
	"ikhnos/internal/telemetry"
	"net"
	"net/http"

	"ikhnos/internal/httpkit"
	"ikhnos/internal/installcfg"
	"ikhnos/internal/kernel"
	"ikhnos/internal/operators"
)

// HeartbeatPath is where an opted-in regional operator's collector POSTs its heartbeat (its otlphttp exporter's metrics_endpoint). It is
// outside /api/v1/orgs/{org}: the caller has no session and belongs to no browser, only a secret that finds the operator by itself. The
// server serves it (it is not one of the routes mounted here); the commands name it.
const HeartbeatPath = "/api/v1/operator-heartbeat"

// API is the operators' routes.
type API struct {
	// For is the operators of the organisation a request is for.
	For func(r *http.Request) *operators.Service
	// Central is the operator that runs next to the server (FUSION's gateway), or nil on an install without one. It is read for every
	// request: whether it exists changes while the server runs.
	Central func() operators.Central
	// Settings are the install settings the commands are written from, and CAPin the pin of the certificate authority the operators'
	// certificate renewal verifies the server against.
	Settings *installcfg.Settings
	CAPin    func() string
	// Secure says whether a request came over HTTPS (the connection itself or the proxy in front of it): the address the heartbeat is told to
	// report to takes its scheme from it.
	Secure func(r *http.Request) bool
	// AgentRelease is the namespace and the Helm release the agent with this id was installed as ("" when it never said).
	AgentRelease func(r *http.Request, agentID string) (namespace, release string)
	// Hub is what the hub of the organisation a request is for knows of the agents that send to an operator (the senders route reads
	// it); Intents is the telemetry intents that name the operators.
	Hub     func(r *http.Request) SenderHub
	Intents func(r *http.Request) *telemetry.Service
	// ProbeDial replaces the network the address check dials with (a test); nil dials as the decider allows.
	ProbeDial func(ctx context.Context, network, addr string) (net.Conn, error)

	probes addressProbes
}

// SenderHub is what the operators' routes ask the hub about a sending agent: how its address was last tested from its own cluster, and
// how its exporters fare. *estate.Hub is one.
type SenderHub interface {
	ReachOf(agentID, address string, now time.Time) *connectivity.AddressReachDoc
	ExportHealthOf(agentID string) (connected bool, export *agents.ExportHealthDoc)
}

func (h *API) fail(w http.ResponseWriter, r *http.Request, err error) {
	httpkit.Fail(w, h.For(r).Log, err)
}

// cfgErr logs a command that could not be written and returns the error the caller answers with.
func (h *API) cfgErr(r *http.Request, err error) error {
	return installcfg.ConfigError(h.For(r).Log, err)
}

func (h *API) central() operators.Central {
	if h.Central == nil {
		return nil
	}
	return h.Central()
}

// images are the image defaults of the organisation a request is for, overlaid on the server's.
func (h *API) images(r *http.Request) kernel.ImageConfig {
	return h.Settings.Images(h.For(r).Core)
}

// Mount puts the routes on r.
func (h *API) Mount(r httpkit.Router) {
	const o = httpkit.OrgPath
	r.Handle("GET "+o+"/operators", httpkit.AdminRole, h.listOperators)
	r.Handle("POST "+o+"/operators", httpkit.AdminRole, h.createOperator)
	r.Handle("GET "+o+"/operators/{id}", httpkit.AdminRole, h.getOperator)
	// What a person choosing where telemetry goes may pick: active operators and their state, no secrets - so an editor's (the wizard's),
	// where the list above stays an administrator's.
	r.Handle("GET "+o+"/operator-destinations", httpkit.EditorRole, h.listOperatorDestinations)
	r.Handle("POST "+o+"/operators/{id}/scope", httpkit.AdminRole, h.updateOperatorScope)
	// Installs the operator again: fresh receiver and client certificates from its stored CA, and a new receiver token / heartbeat secret
	// where it has one. Credential material, so an administrator's and audited.
	r.Handle("POST "+o+"/operators/{id}/install", httpkit.AdminRole, h.reissueOperatorInstall)
	// What was issued for an operator (not the certificates: those are shown once): which sender holds which, and until when.
	r.Handle("GET "+o+"/operators/{id}/certificates", httpkit.AdminRole, h.listOperatorCertificates)
	r.Handle("POST "+o+"/operators/{id}/address", httpkit.AdminRole, h.setOperatorAddress)
	// One look at that address from this server: does anything answer there, and is it this operator.
	r.Handle("POST "+o+"/operators/{id}/address/check", httpkit.AdminRole, h.checkOperatorAddress)
	// Mints (or rotates) the operator's heartbeat secret: credential material, so an administrator's like the rest.
	r.Handle("POST "+o+"/operators/{id}/heartbeat", httpkit.AdminRole, h.enableOperatorHeartbeat)
	// The clusters that send to the operator, how each reaches it, and which end a fault is on: read from the telemetry intents and what the
	// hub holds, nothing dialled.
	r.Handle("GET "+o+"/operators/{id}/senders", httpkit.AdminRole, h.listOperatorSenders)
	// The command that points one agent's bundled local operator at what its telemetry intent grants. It mints and returns operator
	// credential material when the destination is an operator (a client certificate) - the same bar as operator CRUD itself, higher than
	// the editor's the rest of the intent's own routes (telemetry.API) ask.
	r.Handle("POST "+o+"/telemetry-intents/{id}/command", httpkit.AdminRole, h.telemetryIntentCommand)
	r.Handle("POST "+o+"/operators/{id}/revoke", httpkit.AdminRole, h.revokeOperator)
	r.Handle("DELETE "+o+"/operators/{id}", httpkit.AdminRole, h.deleteOperator)
}
