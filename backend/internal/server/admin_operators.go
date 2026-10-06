package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"continuum/internal/chart"
	"continuum/internal/store"
)

type destinationDoc struct {
	Kind           string `json:"kind"`
	Endpoint       string `json:"endpoint"`
	Insecure       bool   `json:"insecure,omitempty"`
	CAFile         string `json:"caFile,omitempty"`
	AuthHeaderName string `json:"authHeaderName,omitempty"`
	AuthSecretName string `json:"authSecretName,omitempty"`
	AuthSecretKey  string `json:"authSecretKey,omitempty"`
	// TargetOperatorID is only meaningful for destination kind "operator" - see store.Destination's own
	// field.
	TargetOperatorID string `json:"targetOperatorId,omitempty"`
	// FusionRelease and FusionNamespace are only meaningful for destination kind "fusion" - see
	// store.Destination's own fields. Both default (to "fusion" and "continuum-system") when left empty.
	FusionRelease   string `json:"fusionRelease,omitempty"`
	FusionNamespace string `json:"fusionNamespace,omitempty"`
}

func toDestinationDoc(d store.Destination) destinationDoc {
	return destinationDoc{Kind: string(d.Kind), Endpoint: d.Endpoint, Insecure: d.Insecure, CAFile: d.CAFile, AuthHeaderName: d.AuthHeaderName, AuthSecretName: d.AuthSecretName, AuthSecretKey: d.AuthSecretKey, TargetOperatorID: d.TargetOperatorID, FusionRelease: d.FusionRelease, FusionNamespace: d.FusionNamespace}
}

func (d destinationDoc) toStore() store.Destination {
	return store.Destination{Kind: store.DestinationKind(d.Kind), Endpoint: d.Endpoint, Insecure: d.Insecure, CAFile: d.CAFile, AuthHeaderName: d.AuthHeaderName, AuthSecretName: d.AuthSecretName, AuthSecretKey: d.AuthSecretKey, TargetOperatorID: d.TargetOperatorID, FusionRelease: d.FusionRelease, FusionNamespace: d.FusionNamespace}
}

// operatorHealthDoc is an operator's liveness as the UI reads it: computed at read time from the stored
// last-seen time and the server clock, never stored. state is "unknown" (no heartbeat credential, or none
// has ever arrived), "online" (a heartbeat within the last three intervals) or "offline". lastSeenAt is
// absent until one has arrived. reporting is true once the operator has a heartbeat credential and has
// sent at least one heartbeat. See operatorHealthAt.
type operatorHealthDoc struct {
	State      string `json:"state"`
	LastSeenAt string `json:"lastSeenAt,omitempty"`
	Reporting  bool   `json:"reporting"`
}

// labelDoc is one operator label on the wire.
type labelDoc struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type operatorDoc struct {
	ID                 string         `json:"id"`
	Name               string         `json:"name"`
	SiteID             string         `json:"siteId,omitempty"`
	Status             string         `json:"status"`
	SourceClusterIDs   []string       `json:"sourceClusterIds"`
	Destination        destinationDoc `json:"destination"`
	AcceptedModalities []string       `json:"acceptedModalities,omitempty"`
	// Labels are the tags this operator adds to everything it forwards, next to the continuum.operator.id and
	// .name it always adds. Absent when there are none.
	Labels    []labelDoc `json:"labels,omitempty"`
	CreatedAt string     `json:"createdAt"`
	CreatedBy string     `json:"createdBy"`
	RevokedAt string     `json:"revokedAt,omitempty"`
	Reason    string     `json:"reason,omitempty"`
	// ReceiverAuth is how the operator's receiver authenticates what exports into it: "mtls" (only the
	// client certificate every source cluster presents, signed by the operator's own CA - no bearer token exists) or "bearer"
	// (a bearer token, as every operator created before this field existed - read back as "bearer").
	ReceiverAuth string `json:"receiverAuth"`
	// ClientCaScope says which CA the receiver trusts for client certificates: "operator" (the operator's
	// own private CA: no other operator's certificate, and not the org CA's, is accepted), "org" (an mTLS
	// operator created before per-operator CAs: its receiver trusts the server-wide org CA, so any
	// certificate that CA signed is accepted - weaker, fixed by recreating the operator) or "" (a bearer
	// operator: the token is the gate).
	ClientCaScope string `json:"clientCaScope"`
	// Health is always present; for an operator that never opted in to a heartbeat it is
	// {state: "unknown", reporting: false}.
	Health operatorHealthDoc `json:"health"`
	// Address is the host:port other clusters reach this operator's receiver at; absent until it is set.
	// ReachableFromOtherClusters says whether the server can hand that out: false means the only address it has
	// is the in-cluster name, which resolves in the operator's own cluster alone. The central operator's comes
	// from FUSION (see FusionControl.Exposed), so it is filled in by the caller that knows.
	Address                    string `json:"address,omitempty"`
	ReachableFromOtherClusters bool   `json:"reachableFromOtherClusters"`
}

func toOperatorDoc(op store.Operator, now time.Time) operatorDoc {
	h := operatorHealthAt(op, now)
	d := operatorDoc{
		ID: op.ID, Name: op.Name, SiteID: op.SiteID, Status: string(op.Status),
		SourceClusterIDs: op.SourceClusterIDs, Destination: toDestinationDoc(op.Destination),
		CreatedAt: rfc(op.CreatedAt), CreatedBy: op.CreatedBy, Reason: op.Reason, ReceiverAuth: string(op.ReceiverAuth), ClientCaScope: op.ClientCAScope(),
		Health: operatorHealthDoc{State: h.State, LastSeenAt: rfcp(h.LastSeenAt), Reporting: h.Reporting},
	}
	if op.SourceClusterIDs == nil {
		d.SourceClusterIDs = []string{}
	}
	for _, m := range op.AcceptedModalities {
		d.AcceptedModalities = append(d.AcceptedModalities, string(m))
	}
	for _, l := range op.Labels {
		d.Labels = append(d.Labels, labelDoc{Key: l.Key, Value: l.Value})
	}
	if op.RevokedAt != nil {
		d.RevokedAt = rfc(*op.RevokedAt)
	}
	d.Address, d.ReachableFromOtherClusters = op.Address, op.Address != ""
	return d
}

// opDoc is toOperatorDoc plus what only the server's own wiring knows: the central operator is reachable from
// other clusters exactly when FUSION says it is exposed, at the address FUSION gives.
func (a *Admin) opDoc(r *http.Request, op store.Operator) operatorDoc {
	d := toOperatorDoc(op, a.core(r).Now())
	if op.ID == CentralOperatorID && a.Fusion != nil {
		if d.ReachableFromOtherClusters = a.Fusion.Exposed(); d.ReachableFromOtherClusters {
			d.Address = a.Fusion.CentralEndpoint()
		}
	}
	return d
}

// modalitiesFromDoc converts a request's plain-string modality list to store.Modality - validated later
// by Core (validModalities), the same deferral destinationDoc.toStore's own untyped Kind follows.
func modalitiesFromDoc(in []string) []store.Modality {
	if len(in) == 0 {
		return nil
	}
	out := make([]store.Modality, len(in))
	for i, m := range in {
		out[i] = store.Modality(m)
	}
	return out
}

func (a *Admin) listOperators(w http.ResponseWriter, r *http.Request) {
	ops, err := a.core(r).ListOperators(r.Context())
	if err != nil {
		a.fail(w, err)
		return
	}
	out := []operatorDoc{}
	for _, op := range ops {
		out = append(out, a.opDoc(r, op))
	}
	writeJSON(w, 200, out)
}

func (a *Admin) getOperator(w http.ResponseWriter, r *http.Request) {
	op, err := a.core(r).GetOperator(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 200, a.opDoc(r, op))
}

func (a *Admin) createOperator(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name               string         `json:"name"`
		SourceClusterIDs   []string       `json:"sourceClusterIds"`
		Destination        destinationDoc `json:"destination"`
		AcceptedModalities []string       `json:"acceptedModalities,omitempty"`
		// Labels are fixed at creation: they live in the operator's own install (see store.OperatorLabel).
		Labels []labelDoc `json:"labels,omitempty"`
		// Heartbeat opts the new operator in to reporting that it is alive (see EnableOperatorHeartbeat).
		// Absent means false: an older client that has never heard of the field must not quietly make an
		// operator start calling this server. The UI sends true by default and says what it does.
		Heartbeat bool `json:"heartbeat,omitempty"`
		// Exposure is how the operator's Service is made reachable from other clusters: "" or "cluster" (it is
		// not: in-cluster only), "loadbalancer" or "nodeport". It only sets the install command's service.type;
		// the address that results is learned afterwards (POST .../address). Not stored.
		Exposure string `json:"exposure,omitempty"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	svcType, err := serviceTypeFor(req.Exposure)
	if err != nil {
		a.fail(w, err)
		return
	}
	labels := make([]store.OperatorLabel, 0, len(req.Labels))
	for _, l := range req.Labels {
		labels = append(labels, store.OperatorLabel{Key: l.Key, Value: l.Value})
	}
	op, secret, tlsBundle, hbSecret, err := a.core(r).CreateOperatorWithOptions(r.Context(), actor(r), req.Name, req.SourceClusterIDs, req.Destination.toStore(), modalitiesFromDoc(req.AcceptedModalities), OperatorOptions{Heartbeat: req.Heartbeat, Labels: labels})
	if err != nil {
		a.fail(w, err)
		return
	}
	img := a.images(a.core(r))
	hbURL := ""
	if hbSecret != "" {
		hbURL = a.heartbeatURL(r)
	}
	var target *store.Operator
	if op.Destination.Kind == store.DestinationOperator {
		if t, err := a.core(r).GetOperator(r.Context(), op.Destination.TargetOperatorID); err == nil {
			target = &t
		}
	}
	install, secretCmd := a.operatorInstallCommandTo(img, secret, op, tlsBundle, hbURL, target)
	if svcType != "" {
		install += fmt.Sprintf(" \\\n  --set service.type=%s", svcType)
	}
	resp := map[string]any{
		"operator":  a.opDoc(r, op),
		"install":   install,
		"reminders": a.operatorSourceReminders(r, op, tlsBundle),
	}
	// A receiver bearer token exists only for a bearer operator (see operatorInstallCommand): for an mTLS
	// one, "token" and "secretCommand" are absent - there is nothing to show, and operator.receiverAuth
	// says why.
	if op.ReceiverAuth != store.ReceiverAuthMTLS {
		resp["token"] = secret
		resp["secretCommand"] = secretCmd
	}
	if hbSecret != "" {
		// The install command above already carries the heartbeat --set flags; this is the one extra
		// Secret it points at, plus the same facts a UI wants to show next to it.
		resp["heartbeatToken"] = hbSecret
		resp["heartbeatSecretCommand"] = operatorHeartbeatSecretCommand(op, hbSecret, false)
		resp["heartbeatUrl"] = hbURL
		resp["heartbeatIntervalSeconds"] = int(OperatorHeartbeatInterval / time.Second)
		if warn := heartbeatURLWarning(hbURL); warn != "" {
			resp["heartbeatWarning"] = warn
		}
	}
	if tlsCmd := operatorTLSSecretCommand(op, tlsBundle); tlsCmd != "" {
		resp["tlsSecretCommand"] = tlsCmd
	}
	a.addOperatorTargetExport(r, resp, op)
	writeJSON(w, 201, resp)
}

// serviceTypeFor maps the create request's exposure to the chart's service.type ("" keeps the chart's own
// default, ClusterIP).
func serviceTypeFor(exposure string) (string, error) {
	switch exposure {
	case "", "cluster":
		return "", nil
	case "loadbalancer":
		return "LoadBalancer", nil
	case "nodeport":
		return "NodePort", nil
	}
	return "", errf(KindInvalid, "exposure must be cluster, loadbalancer or nodeport")
}

// setOperatorAddress records the host:port other clusters reach the operator at ({"address": ""} clears it).
func (a *Admin) setOperatorAddress(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Address string `json:"address"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	id := r.PathValue("id")
	if _, err := a.core(r).SetOperatorAddress(r.Context(), actor(r), id, req.Address); err != nil {
		a.fail(w, err)
		return
	}
	op, err := a.core(r).GetOperator(r.Context(), id)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 200, a.opDoc(r, op))
}

func (a *Admin) updateOperatorScope(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SourceClusterIDs   []string       `json:"sourceClusterIds"`
		Destination        destinationDoc `json:"destination"`
		AcceptedModalities []string       `json:"acceptedModalities,omitempty"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	id := r.PathValue("id")
	if err := a.core(r).UpdateOperatorScope(r.Context(), actor(r), id, req.SourceClusterIDs, req.Destination.toStore(), modalitiesFromDoc(req.AcceptedModalities)); err != nil {
		a.fail(w, err)
		return
	}
	op, err := a.core(r).GetOperator(r.Context(), id)
	if err != nil {
		a.fail(w, err)
		return
	}
	// A freshly minted client certificate, not the one from CreateOperator (never stored, see its own
	// comment) - cheap to redo and needed so a newly added source cluster has something to install.
	// Nothing about the previously issued one stops working: there is no per-certificate revocation
	// here, only the operator-wide bearer token and (if the operator itself is later revoked) the CA
	// relationship as a whole, so reissuing here does not disturb clusters already configured.
	// IssueOperatorClientCert signs with the operator's own CA when it has one and hands back that CA's
	// certificate, so the Secret in the reminders trusts what the receiver trusts (and audits the reissue).
	var tlsBundle OperatorTLSBundle
	if clientCert, clientKey, caPEM, tlsErr := a.core(r).IssueOperatorClientCert(r.Context(), actor(r), op.ID); tlsErr == nil {
		tlsBundle = OperatorTLSBundle{ClientCertPEM: clientCert, ClientKeyPEM: clientKey, CACertPEM: caPEM}
	}
	resp := map[string]any{"operator": a.opDoc(r, op), "reminders": a.operatorSourceReminders(r, op, tlsBundle)}
	a.addOperatorTargetExport(r, resp, op)
	writeJSON(w, 200, resp)
}

func (a *Admin) revokeOperator(w http.ResponseWriter, r *http.Request) {
	a.reasoned(w, r, func(reason string) error {
		return a.core(r).RevokeOperator(r.Context(), actor(r), r.PathValue("id"), reason)
	})
}

func (a *Admin) deleteOperator(w http.ResponseWriter, r *http.Request) {
	if err := a.core(r).DeleteOperator(r.Context(), actor(r), r.PathValue("id")); err != nil {
		a.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// operatorReceiverTLSSecretName and operatorClientTLSSecretName are the fixed Secret names the operator
// chart's receiver.tls.secretName and the agent chart's telemetry.export.otlp.tls.mtls.secretName default
// install commands point at - fixed per operator so the reminders below and operatorInstallCommand agree
// without threading a name through both.
func operatorReceiverTLSSecretName(op store.Operator) string { return op.ID + "-receiver-tls" }
func operatorClientTLSSecretName(op store.Operator) string   { return op.ID + "-export-mtls" }

// operatorTLSSecretCommand is the `kubectl create secret` for the operator's own receiver certificate -
// installed once, wherever the operator itself runs. Empty if CreateOperator could not mint the TLS
// material (a rare failure it already tolerates - see its own comment): that operator is then a bearer
// one, and the bearer token is its only gate.
func operatorTLSSecretCommand(op store.Operator, b OperatorTLSBundle) string {
	if len(b.ReceiverCertPEM) == 0 {
		return ""
	}
	return applySecretCommand(operatorReceiverTLSSecretName(op), "continuum-system",
		fmt.Sprintf("tls.crt=\"%s\"", b.ReceiverCertPEM), fmt.Sprintf("tls.key=\"%s\"", b.ReceiverKeyPEM), fmt.Sprintf("ca.crt=\"%s\"", b.CACertPEM))
}

// applySecretCommand is every Secret line this file hands out: a create-or-update, so running a generated
// command a second time (the person regenerates it, a first attempt failed half way, a certificate is
// reissued) replaces the Secret rather than failing on "already exists". Each literal is a complete
// `key=value` (already quoted where it needs to be).
func applySecretCommand(name, namespace string, literals ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "kubectl create secret generic %s --namespace %s", name, namespace)
	for _, l := range literals {
		fmt.Fprintf(&b, " \\\n  --from-literal=%s", l)
	}
	b.WriteString(" \\\n  --dry-run=client -o yaml | kubectl apply -f -")
	return b.String()
}

// operatorDestinationCommand builds the --set export.otlp.* flags (and, if a fresh client cert was minted,
// the kubectl create secret command for it) that point one agent's telemetry export at this operator -
// the same shape operatorSourceReminders already builds per source cluster at creation/scope-update time,
// factored out so TelemetryIntent's own /command endpoint (admin_telemetry_intents.go) can call it for a
// single agent on demand, reusing a freshly reissued client cert rather than requiring one from creation time.
func operatorDestinationCommand(op store.Operator, endpoint string, certPEM, keyPEM, caPEM []byte, namespace string) (setFlags string, secretCmd string) {
	setFlags = fmt.Sprintf("--set telemetry.export.otlp.endpoint=%s", endpoint)
	if len(certPEM) == 0 {
		return setFlags, ""
	}
	// mTLS is additive: every source cluster of this operator presents a client certificate verified
	// against the CA bundle in the same Secret - see pki.IssueOperatorClientTLS.
	secretCmd = operatorClientSecretCommand(op, certPEM, keyPEM, caPEM, namespace)
	setFlags += fmt.Sprintf(" --set telemetry.export.otlp.tls.mtls.enabled=true --set telemetry.export.otlp.tls.mtls.secretName=%s", operatorClientTLSSecretName(op))
	if op.Address != "" {
		setFlags += fmt.Sprintf(" --set telemetry.export.otlp.tls.serverName=%s", operatorServerName(op))
	}
	return setFlags, secretCmd
}

// operatorClientSecretCommand is the `kubectl create secret` holding the client certificate an agent
// presents to this operator. One Secret per operator, whatever number of the agent's signal types export to
// it: every route to the same operator names this same Secret.
func operatorClientSecretCommand(op store.Operator, certPEM, keyPEM, caPEM []byte, namespace string) string {
	return applySecretCommand(operatorClientTLSSecretName(op), namespace,
		fmt.Sprintf("tls.crt=\"%s\"", certPEM), fmt.Sprintf("tls.key=\"%s\"", keyPEM), fmt.Sprintf("ca.crt=\"%s\"", caPEM))
}

// operatorRouteFlags are the --set flags that make one signal type's own route (telemetry.export.routes.<m>)
// export to this operator over mutual TLS: its receiver, gRPC, certificate verified, and the Secret from
// operatorClientSecretCommand. Stated in full so the route does not depend on anything an earlier command set.
func operatorRouteFlags(op store.Operator, endpoint string, m store.Modality) string {
	base := fmt.Sprintf("telemetry.export.routes.%s", m)
	flags := fmt.Sprintf("--set %s.endpoint=%s --set %s.protocol=grpc --set %s.tls.insecure=false --set %s.tls.mtls.enabled=true --set %s.tls.mtls.secretName=%s",
		base, endpoint, base, base, base, base, operatorClientTLSSecretName(op))
	if op.Address != "" {
		flags += fmt.Sprintf(" --set %s.tls.serverName=%s", base, operatorServerName(op))
	}
	return flags
}

// operatorEndpoint is where an exporter is pointed to reach op's receiver: the operator's own Service in the
// namespace the install commands use, or - for the central operator, which lives with the server and may be
// exposed - the address FusionControl says (see CentralEndpoint). Any other operator with an advertised address
// (see store.Operator.Address) is reached there, so a cluster elsewhere can find it.
func (a *Admin) operatorEndpoint(op store.Operator) string {
	if op.ID == CentralOperatorID && a.Fusion != nil {
		return a.Fusion.CentralEndpoint()
	}
	if op.Address != "" {
		return op.Address
	}
	return fmt.Sprintf("%s.continuum-system.svc:4317", op.ID)
}

// addOperatorTargetExport adds, for a regional operator that exports to ANOTHER operator (the central one, usually),
// the one Secret its install command depends on: the client certificate the target's receiver requires, issued now
// from the target's own CA. The install command itself already points the exporter at it (operatorInstallCommand).
func (a *Admin) addOperatorTargetExport(r *http.Request, resp map[string]any, op store.Operator) {
	if op.Destination.Kind != store.DestinationOperator {
		return
	}
	core := a.core(r)
	target, err := core.GetOperator(r.Context(), op.Destination.TargetOperatorID)
	if err != nil {
		return
	}
	certPEM, keyPEM, caPEM, err := core.IssueOperatorClientCert(r.Context(), actor(r), target.ID)
	if err != nil || len(certPEM) == 0 {
		return
	}
	resp["exportSecretCommand"] = operatorClientSecretCommand(target, certPEM, keyPEM, caPEM, "continuum-system")
	resp["exportTarget"] = map[string]any{"operatorId": target.ID, "name": target.Name, "endpoint": a.operatorEndpoint(target), "reachableFromOtherClusters": target.Address != "" || (target.ID == CentralOperatorID && a.Fusion != nil && a.Fusion.Exposed())}
}

// operatorChartArgs is the chart reference an operator `helm` command names, and the " --version ..." that
// goes with it when that reference is a registry one (empty for a local .tgz).
func (a *Admin) operatorChartArgs(img ImageConfig) (ref, version string) {
	ref = a.operatorChartRef(img)
	if ref == "" {
		ref = "./" + chart.RegionalOperator.Filename()
	} else if !strings.HasSuffix(ref, ".tgz") {
		version = " --version " + a.operatorChartVersion()
	}
	return ref, version
}

// operatorInstallCommand is installCommand's own twin for the regional-operator chart: simpler, since this
// chart does not dial the Ikhnos server unless its opt-in heartbeat is on (see store.Operator's own
// comment) - there is no server.address or enrollment.key here, only where the operator exports to and how
// its receiver authenticates what exports into it. Returns the `helm install` command and, for a bearer
// operator only, a companion `kubectl create secret` line for the receiver token, shown once.
//
// How the receiver authenticates follows op.ReceiverAuth:
//   - ReceiverAuthMTLS: receiver.auth.enabled=false and receiver.tls.enabled/mtls=true, so the ONLY gate is
//     the required client certificate (signed by the operator's own CA, see ClientCaScope). receiver.requireAuth=true makes the chart refuse to
//     render at all if either half is missing, so this operator can never be installed open. There is no
//     receiver token: secret is unused and secretCmd is "".
//   - ReceiverAuthBearer (every operator from before ReceiverAuth existed, and a new one whose TLS mint
//     failed): unchanged - the bearer token, plus mTLS on top when the certificates were minted.
//
// heartbeatURL is "" for an operator that did not opt in to a heartbeat (the command is then exactly what
// it was before heartbeats existed).
func (a *Admin) operatorInstallCommand(img ImageConfig, secret string, op store.Operator, tlsBundle OperatorTLSBundle, heartbeatURL string) (install, secretCmd string) {
	return a.operatorInstallCommandTo(img, secret, op, tlsBundle, heartbeatURL, nil)
}

// operatorInstallCommandTo is operatorInstallCommand when the operator's destination is another operator and the
// caller has that operator's record: its advertised address (if any) is then what the exporter dials, and the
// certificate is verified against the stable name on it. With target nil only the id is known, which fixes the
// in-cluster name and nothing more.
func (a *Admin) operatorInstallCommandTo(img ImageConfig, secret string, op store.Operator, tlsBundle OperatorTLSBundle, heartbeatURL string, target *store.Operator) (install, secretCmd string) {
	ref, version := a.operatorChartArgs(img)
	secretName := op.ID + "-receiver-auth"
	var b strings.Builder
	fmt.Fprintf(&b, "helm install %s %s%s \\\n  --namespace continuum-system --create-namespace", op.ID, ref, version)
	if op.Destination.Kind == store.DestinationOperator {
		// Another regional operator (the central one in front of FUSION, usually): its receiver, over mutual TLS with
		// a client certificate from the target's own CA (the Secret addOperatorTargetExport hands out).
		tgt := store.Operator{ID: op.Destination.TargetOperatorID} // the id alone fixes the in-cluster endpoint and the Secret name
		if target != nil {
			tgt = *target
		}
		fmt.Fprintf(&b, " \\\n  --set export.otlp.endpoint=%s \\\n  --set export.otlp.tls.mtls.enabled=true \\\n  --set export.otlp.tls.mtls.secretName=%s",
			a.operatorEndpoint(tgt), operatorClientTLSSecretName(tgt))
		if tgt.Address != "" {
			fmt.Fprintf(&b, " \\\n  --set export.otlp.tls.serverName=%s", operatorServerName(tgt))
		}
	} else {
		fmt.Fprintf(&b, " \\\n  --set export.otlp.endpoint=%s", op.Destination.Endpoint)
		if op.Destination.Insecure {
			fmt.Fprintf(&b, " \\\n  --set export.otlp.tls.insecure=true")
		}
		if op.Destination.CAFile != "" {
			fmt.Fprintf(&b, " \\\n  --set export.otlp.tls.caFile=%s", op.Destination.CAFile)
		}
		if op.Destination.AuthSecretName != "" {
			fmt.Fprintf(&b, " \\\n  --set export.otlp.auth.headerName=%s \\\n  --set export.otlp.auth.secretName=%s \\\n  --set export.otlp.auth.secretKey=%s",
				op.Destination.AuthHeaderName, op.Destination.AuthSecretName, op.Destination.AuthSecretKey)
		}
	}
	mtlsOnly := op.ReceiverAuth == store.ReceiverAuthMTLS
	if mtlsOnly {
		fmt.Fprintf(&b, " \\\n  --set receiver.auth.enabled=false \\\n  --set receiver.requireAuth=true \\\n  --set receiver.tls.enabled=true \\\n  --set receiver.tls.secretName=%s \\\n  --set receiver.tls.mtls=true", operatorReceiverTLSSecretName(op))
	} else {
		fmt.Fprintf(&b, " \\\n  --set receiver.auth.enabled=true \\\n  --set receiver.auth.secretName=%s", secretName)
	}
	// For a bearer operator, mTLS on the receiver is additive to the bearer token above, not a
	// replacement - see this chart's own receiver.tls comment. Only set up when CreateOperator actually
	// minted the certificates.
	if !mtlsOnly && len(tlsBundle.ReceiverCertPEM) > 0 {
		fmt.Fprintf(&b, " \\\n  --set receiver.tls.enabled=true \\\n  --set receiver.tls.secretName=%s \\\n  --set receiver.tls.mtls=true", operatorReceiverTLSSecretName(op))
	}
	if heartbeatURL != "" {
		fmt.Fprintf(&b, " \\\n  %s", operatorHeartbeatSetFlags(op, heartbeatURL))
	}
	fmt.Fprintf(&b, " \\\n  --set-json operator=%s", shellQuote(operatorProvenanceJSON(op)))
	// No image flags, on purpose. The operator runs the upstream OpenTelemetry Collector image the chart
	// already names - not the `continuum` image the configured registry holds - so pointing it at
	// <registry>/continuum-regional-operator (an image nothing ever published) made every install of it
	// fail with ImagePullBackOff. A cluster that must pull from its own registry sets image.repository
	// and image.tag itself (see the chart's values.yaml).
	if !mtlsOnly {
		secretCmd = applySecretCommand(secretName, "continuum-system", "token="+secret)
	}
	return b.String(), secretCmd
}

// shellQuote wraps s in single quotes for a POSIX shell, closing and reopening the quotes around any
// single quote inside it, so a name or a label value can never break out of the command it is pasted into.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// operatorProvenanceJSON is the chart's `operator` value: who this operator is (stamped as
// continuum.operator.id and .name on everything it forwards) and the labels set when it was created. One
// --set-json object rather than separate --set flags, so a comma or a space in a name or a value needs no
// escaping beyond the shell quoting around it.
func operatorProvenanceJSON(op store.Operator) string {
	type label struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	labels := make([]label, 0, len(op.Labels))
	for _, l := range op.Labels {
		labels = append(labels, label{Key: l.Key, Value: l.Value})
	}
	b, _ := json.Marshal(struct {
		ID     string  `json:"id"`
		Name   string  `json:"name"`
		Labels []label `json:"labels"`
	}{op.ID, op.Name, labels})
	return string(b)
}

// operatorSourceReminders is informational only, not applied: for each source cluster, the exact `helm
// upgrade` a person would run against that cluster's own continuum-agent release to actually point its
// telemetry.export.otlp.endpoint at this operator - there is no live reparenting in this release (see the
// plan), so nothing here is executed on the caller's behalf. Uses the same release-name/namespace guess
// upgradeCommand/teardownCommands already fall back to when an agent has never reported its own.
func (a *Admin) operatorSourceReminders(r *http.Request, op store.Operator, tlsBundle OperatorTLSBundle) []string {
	hub := a.tn(r).Hub
	img := a.images(a.core(r))
	ref, version := a.chartRef(img), ""
	if ref == "" {
		ref = "./" + chart.Agent.Filename()
	} else if !strings.HasSuffix(ref, ".tgz") {
		version = " --version " + a.agentChartVersion()
	}
	agents, err := a.core(r).Store.ListAgents(r.Context(), a.core(r).OrgID)
	if err != nil {
		return nil
	}
	byCluster := make(map[string]store.Agent, len(agents))
	for _, ag := range agents {
		if ag.Status == store.StatusApproved && ag.ClusterID != "" {
			byCluster[ag.ClusterID] = ag
		}
	}
	out := make([]string, 0, len(op.SourceClusterIDs))
	for _, cl := range op.SourceClusterIDs {
		ag, ok := byCluster[cl]
		ns, name := "", ""
		if ok {
			ns, name = hub.NamespaceOf(ag.ID), hub.ReleaseNameOf(ag.ID)
		}
		rns, rname, _ := releaseTarget(ns, name)
		// Only wired in here when CreateOperator actually minted the client certificate (see its own
		// comment on why that mint can fail without failing operator creation itself) - this is additive,
		// the bearer token alone still works without it.
		setFlags, secretCmd := operatorDestinationCommand(op, a.operatorEndpoint(op), tlsBundle.ClientCertPEM, tlsBundle.ClientKeyPEM, tlsBundle.CACertPEM, rns)
		if secretCmd != "" {
			out = append(out, fmt.Sprintf("%s  # cluster %s: create the client certificate Secret first", secretCmd, cl))
		}
		upgrade := fmt.Sprintf("helm upgrade %s %s%s --namespace %s --reuse-values %s", rname, ref, version, rns, setFlags)
		out = append(out, upgrade+fmt.Sprintf("  # cluster %s", cl))
	}
	return out
}
