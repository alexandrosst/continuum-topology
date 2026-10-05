package server

import (
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
}

func toDestinationDoc(d store.Destination) destinationDoc {
	return destinationDoc{Kind: string(d.Kind), Endpoint: d.Endpoint, Insecure: d.Insecure, CAFile: d.CAFile, AuthHeaderName: d.AuthHeaderName, AuthSecretName: d.AuthSecretName, AuthSecretKey: d.AuthSecretKey, TargetOperatorID: d.TargetOperatorID}
}

func (d destinationDoc) toStore() store.Destination {
	return store.Destination{Kind: store.DestinationKind(d.Kind), Endpoint: d.Endpoint, Insecure: d.Insecure, CAFile: d.CAFile, AuthHeaderName: d.AuthHeaderName, AuthSecretName: d.AuthSecretName, AuthSecretKey: d.AuthSecretKey, TargetOperatorID: d.TargetOperatorID}
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

type operatorDoc struct {
	ID                 string         `json:"id"`
	Name               string         `json:"name"`
	SiteID             string         `json:"siteId,omitempty"`
	Status             string         `json:"status"`
	SourceClusterIDs   []string       `json:"sourceClusterIds"`
	Destination        destinationDoc `json:"destination"`
	AcceptedModalities []string       `json:"acceptedModalities,omitempty"`
	CreatedAt          string         `json:"createdAt"`
	CreatedBy          string         `json:"createdBy"`
	RevokedAt          string         `json:"revokedAt,omitempty"`
	Reason             string         `json:"reason,omitempty"`
	// Health is always present; for an operator that never opted in to a heartbeat it is
	// {state: "unknown", reporting: false}.
	Health operatorHealthDoc `json:"health"`
}

func toOperatorDoc(op store.Operator, now time.Time) operatorDoc {
	h := operatorHealthAt(op, now)
	d := operatorDoc{
		ID: op.ID, Name: op.Name, SiteID: op.SiteID, Status: string(op.Status),
		SourceClusterIDs: op.SourceClusterIDs, Destination: toDestinationDoc(op.Destination),
		CreatedAt: rfc(op.CreatedAt), CreatedBy: op.CreatedBy, Reason: op.Reason,
		Health: operatorHealthDoc{State: h.State, LastSeenAt: rfcp(h.LastSeenAt), Reporting: h.Reporting},
	}
	if op.SourceClusterIDs == nil {
		d.SourceClusterIDs = []string{}
	}
	for _, m := range op.AcceptedModalities {
		d.AcceptedModalities = append(d.AcceptedModalities, string(m))
	}
	if op.RevokedAt != nil {
		d.RevokedAt = rfc(*op.RevokedAt)
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
		out = append(out, toOperatorDoc(op, a.core(r).Now()))
	}
	writeJSON(w, 200, out)
}

func (a *Admin) getOperator(w http.ResponseWriter, r *http.Request) {
	op, err := a.core(r).GetOperator(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 200, toOperatorDoc(op, a.core(r).Now()))
}

func (a *Admin) createOperator(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name               string         `json:"name"`
		SourceClusterIDs   []string       `json:"sourceClusterIds"`
		Destination        destinationDoc `json:"destination"`
		AcceptedModalities []string       `json:"acceptedModalities,omitempty"`
		// Heartbeat opts the new operator in to reporting that it is alive (see EnableOperatorHeartbeat).
		// Absent means false: an older client that has never heard of the field must not quietly make an
		// operator start calling this server. The UI sends true by default and says what it does.
		Heartbeat bool `json:"heartbeat,omitempty"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	op, secret, tlsBundle, hbSecret, err := a.core(r).CreateOperatorWithHeartbeat(r.Context(), actor(r), req.Name, req.SourceClusterIDs, req.Destination.toStore(), modalitiesFromDoc(req.AcceptedModalities), req.Heartbeat)
	if err != nil {
		a.fail(w, err)
		return
	}
	img := a.images(a.core(r))
	hbURL := ""
	if hbSecret != "" {
		hbURL = a.heartbeatURL(r)
	}
	install, secretCmd := a.operatorInstallCommand(img, secret, op, tlsBundle, hbURL)
	resp := map[string]any{
		"operator":      toOperatorDoc(op, a.core(r).Now()),
		"token":         secret,
		"install":       install,
		"secretCommand": secretCmd,
		"reminders":     a.operatorSourceReminders(r, op, tlsBundle),
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
	writeJSON(w, 201, resp)
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
	var tlsBundle OperatorTLSBundle
	if clientCert, clientKey, tlsErr := a.core(r).CA.IssueOperatorClientTLS(op.ID, a.core(r).OrgID); tlsErr == nil {
		tlsBundle = OperatorTLSBundle{ClientCertPEM: clientCert, ClientKeyPEM: clientKey, CACertPEM: a.core(r).CA.CertPEM()}
	}
	writeJSON(w, 200, map[string]any{"operator": toOperatorDoc(op, a.core(r).Now()), "reminders": a.operatorSourceReminders(r, op, tlsBundle)})
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

// operatorInstallCommand is installCommand's own twin for the regional-operator chart: simpler, since
// this chart does not dial the Continuum server unless its opt-in heartbeat is on (see store.Operator's
// own comment) - there is no server.address or enrollment.key here, only where the operator exports to and
// the receiver token it checks incoming OTLP against, plus the heartbeat flags when asked for. Returns the `helm install` command and a companion `kubectl create
// secret` line for the receiver token, shown once - the same convention as an enrollment token.
// operatorReceiverTLSSecretName and operatorClientTLSSecretName are the fixed Secret names the operator
// chart's receiver.tls.secretName and the agent chart's telemetry.export.otlp.tls.mtls.secretName default
// install commands point at - fixed per operator so the reminders below and this function agree without
// threading a name through both.
func operatorReceiverTLSSecretName(op store.Operator) string { return op.ID + "-receiver-tls" }
func operatorClientTLSSecretName(op store.Operator) string   { return op.ID + "-export-mtls" }

// operatorTLSSecretCommand is the `kubectl create secret` for the operator's own receiver certificate -
// installed once, wherever the operator itself runs. Empty if CreateOperator could not mint the TLS
// material (a rare failure it already tolerates - see its own comment); the receiver bearer token alone
// still works in that case, this is additive.
func operatorTLSSecretCommand(op store.Operator, b OperatorTLSBundle) string {
	if len(b.ReceiverCertPEM) == 0 {
		return ""
	}
	return fmt.Sprintf(
		"kubectl create secret generic %s --namespace continuum-system \\\n  --from-literal=tls.crt=\"%s\" \\\n  --from-literal=tls.key=\"%s\" \\\n  --from-literal=ca.crt=\"%s\"",
		operatorReceiverTLSSecretName(op), b.ReceiverCertPEM, b.ReceiverKeyPEM, b.CACertPEM)
}

// operatorDestinationCommand builds the --set export.otlp.* flags (and, if a fresh client cert was minted,
// the kubectl create secret command for it) that point one agent's telemetry export at this operator -
// the same shape operatorSourceReminders already builds per source cluster at creation/scope-update time,
// factored out so TelemetryIntent's own /command endpoint (admin_telemetry_intents.go) can call it for a
// single agent on demand, reusing a freshly reissued client cert rather than requiring one from creation time.
func operatorDestinationCommand(op store.Operator, certPEM, keyPEM, caPEM []byte, namespace string) (setFlags string, secretCmd string) {
	endpoint := fmt.Sprintf("%s.continuum-system.svc:4317", op.ID)
	setFlags = fmt.Sprintf("--set telemetry.export.otlp.endpoint=%s", endpoint)
	if len(certPEM) == 0 {
		return setFlags, ""
	}
	// mTLS is additive: every source cluster of this operator presents a client certificate verified
	// against the CA bundle in the same Secret - see pki.IssueOperatorClientTLS.
	secretCmd = fmt.Sprintf(
		"kubectl create secret generic %s --namespace %s \\\n  --from-literal=tls.crt=\"%s\" \\\n  --from-literal=tls.key=\"%s\" \\\n  --from-literal=ca.crt=\"%s\"",
		operatorClientTLSSecretName(op), namespace, certPEM, keyPEM, caPEM)
	setFlags += fmt.Sprintf(" --set telemetry.export.otlp.tls.mtls.enabled=true --set telemetry.export.otlp.tls.mtls.secretName=%s", operatorClientTLSSecretName(op))
	return setFlags, secretCmd
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

// operatorInstallCommand's heartbeatURL is "" for an operator that did not opt in to a heartbeat (the
// command is then exactly what it was before heartbeats existed).
func (a *Admin) operatorInstallCommand(img ImageConfig, secret string, op store.Operator, tlsBundle OperatorTLSBundle, heartbeatURL string) (install, secretCmd string) {
	ref, version := a.operatorChartArgs(img)
	secretName := op.ID + "-receiver-auth"
	var b strings.Builder
	fmt.Fprintf(&b, "helm install %s %s%s \\\n  --namespace continuum-system --create-namespace \\\n  --set export.otlp.endpoint=%s",
		op.ID, ref, version, op.Destination.Endpoint)
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
	fmt.Fprintf(&b, " \\\n  --set receiver.auth.enabled=true \\\n  --set receiver.auth.secretName=%s", secretName)
	// mTLS on the receiver is additive to the bearer token above, not a replacement - see this chart's
	// own receiver.tls comment. Only set up when CreateOperator actually minted the certificates.
	if len(tlsBundle.ReceiverCertPEM) > 0 {
		fmt.Fprintf(&b, " \\\n  --set receiver.tls.enabled=true \\\n  --set receiver.tls.secretName=%s \\\n  --set receiver.tls.mtls=true", operatorReceiverTLSSecretName(op))
	}
	if heartbeatURL != "" {
		fmt.Fprintf(&b, " \\\n  %s", operatorHeartbeatSetFlags(op, heartbeatURL))
	}
	if img.Configured() {
		fmt.Fprintf(&b, " \\\n  --set image.repository=%s/continuum-regional-operator", img.Registry)
		if img.Tag != "" {
			fmt.Fprintf(&b, " \\\n  --set image.tag=%s", img.Tag)
		}
		if img.Digest != "" {
			fmt.Fprintf(&b, " \\\n  --set image.digest=%s", img.Digest)
		}
	}
	secretCmd = fmt.Sprintf("kubectl create secret generic %s --namespace continuum-system --from-literal=%s=%s", secretName, "token", secret)
	return b.String(), secretCmd
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
		setFlags, secretCmd := operatorDestinationCommand(op, tlsBundle.ClientCertPEM, tlsBundle.ClientKeyPEM, tlsBundle.CACertPEM, rns)
		if secretCmd != "" {
			out = append(out, fmt.Sprintf("%s  # cluster %s: create the client certificate Secret first", secretCmd, cl))
		}
		upgrade := fmt.Sprintf("helm upgrade %s %s%s --namespace %s --reuse-values %s", rname, ref, version, rns, setFlags)
		out = append(out, upgrade+fmt.Sprintf("  # cluster %s", cl))
	}
	return out
}
