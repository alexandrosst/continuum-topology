package operatorsapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"continuum/internal/agents"
	"continuum/internal/chart"
	"continuum/internal/httpkit"
	"continuum/internal/install"
	"continuum/internal/kernel"
	"continuum/internal/operators"
	"continuum/internal/store"
)

// RestartCommand restarts the operator's pods, which is how they pick up a Secret read into the environment.
func RestartCommand(op store.Operator) (string, error) {
	return install.RestartDeployment{Deployment: operators.OperatorServiceName(op.ID), Namespace: "continuum-system"}.Render()
}

// UninstallCommand removes the receiver. Revoking or deleting an operator in the server does not touch the
// running one - it keeps receiving, and trusting its own CA, until it is uninstalled - so the response says how.
func UninstallCommand(id string) (string, error) {
	return install.Uninstall{Release: id, Namespace: "continuum-system"}.Render()
}

// operatorReceiverTLSSecretName and ClientTLSSecretName are the fixed Secret names the operator
// chart's receiver.tls.secretName and the agent chart's telemetry.export.otlp.tls.mtls.secretName default
// install commands point at - fixed per operator so the reminders below and operatorInstallCommand agree
// without threading a name through both.
func operatorReceiverTLSSecretName(op store.Operator) string { return op.ID + "-receiver-tls" }

func ClientTLSSecretName(op store.Operator) string { return op.ID + "-export-mtls" }

// TLSSecretCommand is the Secret for the operator's own receiver certificate -
// installed once, wherever the operator itself runs. Empty if CreateOperator could not mint the TLS
// material (a rare failure it already tolerates - see its own comment): that operator is then a bearer
// one, and the bearer token is its only gate.
func TLSSecretCommand(op store.Operator, b operators.OperatorTLSBundle) (string, error) {
	if len(b.ReceiverCertPEM) == 0 {
		return "", nil
	}
	return install.Secret{Name: operatorReceiverTLSSecretName(op), Namespace: "continuum-system", EnsureNamespace: true, Entries: []install.Entry{
		{Key: "tls.crt", Value: string(b.ReceiverCertPEM)}, {Key: "tls.key", Value: string(b.ReceiverKeyPEM)}, {Key: "ca.crt", Value: string(b.CACertPEM)}}}.Render()
}

// ExportBlock states the whole destination block of one exporter (telemetry.export.otlp, or one route of
// telemetry.export.routes) that sends to the regional operator op at endpoint: endpoint, protocol, certificate
// checking, the client certificate and the name it is verified by, and no credential header. Every field is stated,
// the unused ones empty, because the command is applied with --reset-then-reuse-values: a field it leaves out keeps whatever an
// earlier destination set (an operator's mTLS Secret, protocol=http, insecure=true), and the exporter would carry on
// half pointed at the old one. withMTLS is whether the exporter presents a client certificate; a bearer operator
// reached without one leaves the credential header to the person, who holds the token.
func ExportBlock(prefix string, op store.Operator, endpoint string, withMTLS bool) []install.Value {
	values := []install.Value{
		install.Set(prefix+".endpoint", endpoint),
		install.Set(prefix+".protocol", "grpc"),
		install.Set(prefix+".tls.insecure", "false"),
		install.Set(prefix+".tls.caFile", ""),
	}
	serverName := ""
	if operators.OperatorNeedsServerName(op) {
		serverName = operators.OperatorServerName(op)
	}
	if withMTLS {
		values = append(values, install.Set(prefix+".tls.mtls.enabled", "true"), install.Set(prefix+".tls.mtls.secretName", ClientTLSSecretName(op)))
	} else {
		values = append(values, install.Set(prefix+".tls.mtls.enabled", "false"), install.Set(prefix+".tls.mtls.secretName", ""))
	}
	values = append(values, install.Set(prefix+".tls.serverName", serverName))
	if withMTLS {
		values = append(values, install.Set(prefix+".auth.secretName", ""))
	}
	return values
}

// DestinationCommand builds the export.otlp.* values (and, if a fresh client cert was minted,
// the Secret command for it) that point one agent's telemetry export at this operator -
// the same shape SourceReminders already builds per source cluster at creation/scope-update time,
// factored out so TelemetryIntent's own /command endpoint (admin_telemetry_intents.go) can call it for a
// single agent on demand, reusing a freshly reissued client cert rather than requiring one from creation time.
// The flags are the whole destination block (see ExportBlock) whether or not a certificate is passed: an mTLS
// operator is always reached with a client certificate, and one that is not minted this time is the Secret already in
// the cluster.
func DestinationCommand(op store.Operator, endpoint string, certPEM, keyPEM, caPEM []byte, namespace string) (values []install.Value, secretCmd string, err error) {
	withMTLS := len(certPEM) > 0 || op.ReceiverAuth == store.ReceiverAuthMTLS
	values = ExportBlock("telemetry.export.otlp", op, endpoint, withMTLS)
	if len(certPEM) > 0 {
		// mTLS is additive: every source cluster of this operator presents a client certificate verified
		// against the CA bundle in the same Secret - see pki.IssueOperatorClientTLS.
		if secretCmd, err = ClientSecretCommand(op, certPEM, keyPEM, caPEM, namespace, false); err != nil {
			return nil, "", err
		}
	}
	return values, secretCmd, nil
}

// ClientSecretCommand is the Secret holding the client certificate an agent
// presents to this operator. One Secret per operator, whatever number of the agent's signal types export to
// it: every route to the same operator names this same Secret. ensureNamespace says the Secret is applied before the install that
// would create its namespace.
func ClientSecretCommand(op store.Operator, certPEM, keyPEM, caPEM []byte, namespace string, ensureNamespace bool) (string, error) {
	return install.Secret{Name: ClientTLSSecretName(op), Namespace: namespace, EnsureNamespace: ensureNamespace, Entries: []install.Entry{
		{Key: "tls.crt", Value: string(certPEM)}, {Key: "tls.key", Value: string(keyPEM)}, {Key: "ca.crt", Value: string(caPEM)}}}.Render()
}

// RouteModalities are the signal types that can have a destination of their own (telemetry.export.routes.<type>).
var RouteModalities = []store.Modality{store.ModalityMetrics, store.ModalityLogs, store.ModalityTraces}

// ClearRoutes states that the given signal types have no destination of their own: an empty route endpoint is how
// the chart reads "no route", so they go to the default destination again.
func ClearRoutes(ms ...store.Modality) []install.Value {
	values := make([]install.Value, len(ms))
	for i, m := range ms {
		values[i] = install.Set(fmt.Sprintf("telemetry.export.routes.%s.endpoint", m), "")
	}
	return values
}

// RouteValues are the values that make one signal type's own route (telemetry.export.routes.<m>)
// export to this operator over mutual TLS: its receiver, gRPC, certificate verified, and the Secret from
// ClientSecretCommand. Stated in full (see ExportBlock) so the route does not depend on anything an
// earlier command set.
func RouteValues(op store.Operator, endpoint string, m store.Modality) []install.Value {
	return ExportBlock(fmt.Sprintf("telemetry.export.routes.%s", m), op, endpoint, true)
}

// InstallCommand is installCommand's own twin for the regional-operator chart: simpler, since this
// chart does not dial the Ikhnos server unless its opt-in heartbeat is on (see store.Operator's own
// comment) - there is no server.address or enrollment.key here, only where the operator exports to and how
// its receiver authenticates what exports into it. Returns the `helm install` command and, for a bearer
// operator only, a companion Secret command for the receiver token, shown once.
//
// How the receiver authenticates follows op.ReceiverAuth:
//   - ReceiverAuthMTLS: receiver.auth.enabled=false and receiver.tls.enabled/mtls=true, so the ONLY gate is
//     the required client certificate (signed by the operator's own CA, see ClientCaScope). receiver.requireAuth=true makes the chart refuse to
//     render at all if either half is missing, so this operator can never be installed open. There is no
//     receiver token: secret is unused and secretCmd is "".
//   - ReceiverAuthBearer (only operators stored before ReceiverAuth existed; new ones are never created
//     this way): unchanged - the bearer token, plus mTLS on top when the certificates were minted.
//
// heartbeatURL is "" for an operator that did not opt in to a heartbeat (the command is then exactly what
// it was before heartbeats existed).
//
// When the operator's destination is another operator, target is that operator's record: its Advertised address (if any) is then what
// the exporter dials, and the certificate is verified against the stable name on it. With target nil only the id is known, which fixes
// the in-cluster name and nothing more.
//
// With reissue the command upgrades a release that already runs: `helm upgrade --install --reset-then-reuse-values` (Helm 3.14 or
// newer; see upgradeCommand for why not --reuse-values) keeps what the person tuned (resources, image), so every value this command is
// about is stated, the unused ones empty - an export block left out would keep an old destination's settings.
func (h *API) InstallCommand(img kernel.ImageConfig, secret string, op store.Operator, tlsBundle operators.OperatorTLSBundle, heartbeatURL string, target *store.Operator, reissue bool) (cmd, secretCmd string, err error) {
	var v []install.Value
	set := func(key, value string) { v = append(v, install.Set(key, value)) }
	if op.Destination.Kind == store.DestinationOperator {
		// Another regional operator (the central one in front of FUSION, usually): its receiver, over mutual TLS with
		// a client certificate from the target's own CA (the Secret addOperatorTargetExport hands out).
		tgt := store.Operator{ID: op.Destination.TargetOperatorID} // the id alone fixes the in-cluster endpoint and the Secret name
		if target != nil {
			tgt = *target
		}
		set("export.otlp.endpoint", h.DestEndpoint(tgt, op.Destination))
		if reissue {
			set("export.otlp.protocol", "grpc")
			set("export.otlp.tls.insecure", "false")
			set("export.otlp.tls.caFile", "")
		}
		set("export.otlp.tls.mtls.enabled", "true")
		set("export.otlp.tls.mtls.secretName", ClientTLSSecretName(tgt))
		if operators.OperatorNeedsServerName(tgt) {
			set("export.otlp.tls.serverName", operators.OperatorServerName(tgt))
		} else if reissue {
			set("export.otlp.tls.serverName", "")
		}
		if reissue {
			set("export.otlp.auth.secretName", "")
		}
	} else {
		d := op.Destination
		set("export.otlp.endpoint", d.Endpoint)
		if reissue {
			set("export.otlp.protocol", "grpc")
			set("export.otlp.tls.insecure", strconv.FormatBool(d.Insecure))
			set("export.otlp.tls.caFile", d.CAFile)
			set("export.otlp.tls.mtls.enabled", "false")
			set("export.otlp.tls.mtls.secretName", "")
			set("export.otlp.tls.serverName", "")
		} else {
			if d.Insecure {
				set("export.otlp.tls.insecure", "true")
			}
			if d.CAFile != "" {
				set("export.otlp.tls.caFile", d.CAFile)
			}
		}
		if d.AuthSecretName != "" {
			set("export.otlp.auth.headerName", d.AuthHeaderName)
			set("export.otlp.auth.secretName", d.AuthSecretName)
			set("export.otlp.auth.secretKey", d.AuthSecretKey)
		} else if reissue {
			set("export.otlp.auth.secretName", "")
		}
	}
	mtlsOnly := op.ReceiverAuth == store.ReceiverAuthMTLS
	if mtlsOnly {
		set("receiver.auth.enabled", "false")
		set("receiver.requireAuth", "true")
		set("receiver.tls.enabled", "true")
		set("receiver.tls.secretName", operatorReceiverTLSSecretName(op))
		set("receiver.tls.mtls", "true")
	} else {
		set("receiver.auth.enabled", "true")
		set("receiver.auth.secretName", op.ID+"-receiver-auth")
	}
	// For a bearer operator, mTLS on the receiver is additive to the bearer token above, not a
	// replacement - see this chart's own receiver.tls comment. Only set up when CreateOperator actually
	// minted the certificates.
	if !mtlsOnly && len(tlsBundle.ReceiverCertPEM) > 0 {
		set("receiver.tls.enabled", "true")
		set("receiver.tls.secretName", operatorReceiverTLSSecretName(op))
		set("receiver.tls.mtls", "true")
	}
	if heartbeatURL != "" {
		v = append(v, h.HeartbeatValues(op, heartbeatURL)...)
	}
	// Keeps this operator's certificates from running out: the chart's cert-renew sidecar (the discovery agent's image, a
	// different role) asks this server for a new one before the old one ends. It only needs to know where the server is
	// and which CA to trust; there is no token or key to hand out. Only when the operator holds a certificate at all.
	if h.Settings.AgentAddr != "" && (mtlsOnly || len(tlsBundle.ReceiverCertPEM) > 0 || op.Destination.Kind == store.DestinationOperator) {
		set("renew.enabled", "true")
		set("renew.server", h.Settings.AgentAddr)
		set("renew.caPin", h.CAPin())
		if img.Configured() {
			set("renew.image.repository", img.Registry+"/continuum")
			if img.Tag != "" {
				set("renew.image.tag", img.Tag)
			}
			if img.Digest != "" {
				set("renew.image.digest", img.Digest)
			}
		} else if av := chart.Agent.AppVersion(); av != "" {
			// The operator chart's own appVersion is the collector's; the sidecar runs the agent image.
			set("renew.image.tag", av)
		}
	}
	v = append(v, install.SetJSON("operator", operatorProvenanceJSON(op)))
	// No image flags, on purpose. The operator runs the upstream OpenTelemetry Collector image the chart
	// already names - not the `continuum` image the configured registry holds - so pointing it at
	// <registry>/continuum-regional-operator (an image nothing ever published) made every install of it
	// fail with ImagePullBackOff. A cluster that must pull from its own registry sets image.repository
	// and image.tag itself (see the chart's values.yaml).
	//
	// How its Service is made reachable from other clusters (see Operator.Exposure).
	if svcType, _ := operators.ServiceTypeFor(op.Exposure); svcType != "" {
		set("service.type", svcType)
	}
	ref, version := h.Settings.OperatorChart(img)
	helm := install.Helm{Verb: install.Install, Release: op.ID, Chart: ref, Version: version, Namespace: "continuum-system", CreateNamespace: true, Values: v, Layout: install.Wrapped}
	if reissue {
		helm.Verb, helm.ResetThenReuse = install.UpgradeInstall, true
	}
	if cmd, err = helm.Render(); err != nil {
		return "", "", err
	}
	if !mtlsOnly {
		if secretCmd, err = (install.Secret{Name: op.ID + "-receiver-auth", Namespace: "continuum-system", EnsureNamespace: true, Entries: []install.Entry{{Key: "token", Value: secret}}}).Render(); err != nil {
			return "", "", err
		}
	}
	return cmd, secretCmd, nil
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

// SourceReminders is informational only, not applied: for each source cluster, the exact `helm
// upgrade` a person would run against that cluster's own continuum-agent release to actually point its
// telemetry.export.otlp.endpoint at this operator - there is no live reparenting in this release (see the
// plan), so nothing here is executed on the caller's behalf. Uses the same release-name/namespace guess
// upgradeCommand/agents.TeardownCommands already fall back to when an agent has never reported its own.
//
// Each upgrade states the whole destination block (see ExportBlock), and the client certificate Secret goes once per
// distinct namespace rather than once per cluster, so the private key is not repeated for every cluster that
// shares a namespace name; it is applied in each of the clusters listed above it. A Secret is only listed when a
// certificate was just minted (tlsBundle): without one the clusters keep the Secret they have.
func (h *API) SourceReminders(r *http.Request, op store.Operator, tlsBundle operators.OperatorTLSBundle) []string {
	img := h.images(r)
	agentList, err := h.For(r).Store.ListAgents(r.Context(), h.For(r).OrgID)
	if err != nil {
		return nil
	}
	byCluster := make(map[string]store.Agent, len(agentList))
	for _, ag := range agentList {
		if ag.Status == store.StatusApproved && ag.ClusterID != "" {
			byCluster[ag.ClusterID] = ag
		}
	}
	targets := make([]SourceTarget, 0, len(op.SourceClusterIDs))
	for _, cl := range op.SourceClusterIDs {
		ag, ok := byCluster[cl]
		ns, name := "", ""
		if ok {
			ns, name = h.AgentRelease(r, ag.ID)
		}
		rns, rname, _ := agents.ReleaseTarget(ns, name)
		targets = append(targets, SourceTarget{Cluster: cl, NS: rns, Name: rname})
	}
	out, err := h.Reminders(img, op, tlsBundle, targets)
	if err != nil {
		h.cfgErr(r, err) // logged; the operator is shown without the commands for its sources
		return nil
	}
	return out
}

// SourceTarget is one source cluster of an operator and the release that is told to send to it: where it runs and what it is
// called, as agents.ReleaseTarget settles them.
type SourceTarget struct{ Cluster, NS, Name string }

// Reminders is SourceReminders once the clusters are known: it reads nothing, so the text it builds is pinned
// by a test (install_commands_test.go) like the other commands.
func (h *API) Reminders(img kernel.ImageConfig, op store.Operator, tlsBundle operators.OperatorTLSBundle, targets []SourceTarget) ([]string, error) {
	ref, version := h.Settings.AgentChart(img)
	out := make([]string, 0, 2*len(targets))
	// Each cluster holds its own client certificate (see OperatorTLSBundle.Senders), so each gets its own Secret
	// command, in its own namespace. Only wired in when that certificate was minted (see CreateOperator on why
	// that can fail without failing the operator): this is additive, the bearer token alone still works without it.
	for _, t := range targets {
		sc, ok := tlsBundle.ForSender(t.Cluster)
		if !ok {
			continue
		}
		_, secretCmd, err := DestinationCommand(op, h.Endpoint(op), sc.CertPEM, sc.KeyPEM, tlsBundle.CACertPEM, t.NS)
		if err != nil {
			return nil, err
		}
		if secretCmd != "" {
			out = append(out, fmt.Sprintf("# in cluster %s (namespace %s): create its client certificate Secret first\n%s", t.Cluster, t.NS, secretCmd))
		}
	}
	for _, t := range targets {
		sc, _ := tlsBundle.ForSender(t.Cluster)
		values, _, err := DestinationCommand(op, h.Endpoint(op), sc.CertPEM, sc.KeyPEM, tlsBundle.CACertPEM, t.NS)
		if err != nil {
			return nil, err
		}
		// Everything goes to this operator: no per-signal route may stay (a route outranks the default, and one an
		// earlier command set is kept by --reset-then-reuse-values).
		values = append(values, ClearRoutes(RouteModalities...)...)
		upgrade, err := install.Helm{Verb: install.Upgrade, Release: t.Name, Chart: ref, Version: version, Namespace: t.NS, ResetThenReuse: true, Values: values}.Render()
		if err != nil {
			return nil, err
		}
		out = append(out, upgrade+fmt.Sprintf("  # cluster %s", t.Cluster))
	}
	return out, nil
}

// CheckInstall says whether the install command of a regional operator can be written with the chart this server names, before
// anything for it is minted. Only the chart and the versions can stop it: the operator's own words are names this server made.
func (h *API) CheckInstall(img kernel.ImageConfig) error {
	probe := store.Operator{ID: "op-check", Name: "check", Status: store.OperatorActive, ReceiverAuth: store.ReceiverAuthMTLS,
		Destination: store.Destination{Kind: store.DestinationExternal, Endpoint: "check:4317"}}
	_, _, err := h.InstallCommand(img, "check", probe, operators.OperatorTLSBundle{}, "", nil, true)
	return err
}

// installMaterial is what was minted for one install and is shown once: a bearer operator's receiver token, the
// certificates of an mTLS one, the heartbeat secret of one that reports. Reissue says the operator already runs, so
// the commands upgrade it in place instead of installing it.
type installMaterial struct {
	Secret          string
	TLS             operators.OperatorTLSBundle
	HeartbeatSecret string
	Reissue         bool
	// Processors are the chart values of the extra processors a person asked for, which follow the rest of the command.
	Processors []install.Value
}

// operatorInstallDoc is the response of creating an operator and of installing it again: the operator, the install
// command, what to apply in the source clusters, and the Secrets the install points at.
func (h *API) operatorInstallDoc(r *http.Request, op store.Operator, m installMaterial) (map[string]any, error) {
	core := h.For(r)
	img := h.Settings.Images(core.Core)
	hbURL := ""
	if m.HeartbeatSecret != "" {
		hbURL = h.HeartbeatURL(r)
	}
	var target *store.Operator
	if op.Destination.Kind == store.DestinationOperator {
		if t, err := core.GetOperator(r.Context(), op.Destination.TargetOperatorID); err == nil {
			t = h.Advertised(t)
			target = &t
		}
	}
	cmd, secretCmd, err := h.InstallCommand(img, m.Secret, op, m.TLS, hbURL, target, m.Reissue)
	if err != nil {
		return nil, h.cfgErr(r, err)
	}
	resp := map[string]any{
		"operator":  h.opDoc(r, op),
		"install":   strings.TrimRight(cmd, "\n ") + install.Continued(m.Processors),
		"reminders": h.SourceReminders(r, op, m.TLS),
	}
	// A receiver bearer token exists only for a bearer operator (see operatorInstallCommand): for an mTLS
	// one, "token" and "secretCommand" are absent - there is nothing to show, and operator.receiverAuth
	// says why.
	if op.ReceiverAuth != store.ReceiverAuthMTLS {
		resp["token"] = m.Secret
		resp["secretCommand"] = secretCmd
	}
	if m.HeartbeatSecret != "" {
		// The install command above already carries the heartbeat --set flags; this is the one extra
		// Secret it points at, plus the same facts a UI wants to show next to it.
		resp["heartbeatToken"] = m.HeartbeatSecret
		hbCmd, err := HeartbeatSecretCommand(op, m.HeartbeatSecret)
		if err != nil {
			return nil, h.cfgErr(r, err)
		}
		resp["heartbeatSecretCommand"] = hbCmd
		resp["heartbeatUrl"] = hbURL
		resp["heartbeatIntervalSeconds"] = int(operators.OperatorHeartbeatInterval / time.Second)
		if warn := HeartbeatURLWarning(hbURL); warn != "" {
			resp["heartbeatWarning"] = warn
		}
		caCmd, err := h.HeartbeatCASecretCommand(op)
		if err != nil {
			return nil, h.cfgErr(r, err)
		}
		if caCmd != "" {
			resp["heartbeatCaSecretCommand"] = caCmd
		}
	}
	tlsCmd, err := TLSSecretCommand(op, m.TLS)
	if err != nil {
		return nil, h.cfgErr(r, err)
	}
	if tlsCmd != "" {
		resp["tlsSecretCommand"] = tlsCmd
	}
	if m.Reissue {
		// A Secret's new value is not seen by a running pod until it is restarted (its certificates reload on their
		// own, a changed token or heartbeat secret does not).
		restart, err := RestartCommand(op)
		if err != nil {
			return nil, h.cfgErr(r, err)
		}
		resp["restartCommand"] = restart
	}
	h.addOperatorTargetExport(r, resp, op)
	return resp, nil
}

// addOperatorTargetExport adds, for a regional operator that exports to ANOTHER operator (the central one, usually),
// the one Secret its install command depends on: the client certificate the target's receiver requires, issued now
// from the target's own CA. The install command itself already points the exporter at it (operatorInstallCommand).
// The Secret lands in the operator's own namespace, which on a first install does not exist yet, so the command makes it.
func (h *API) addOperatorTargetExport(r *http.Request, resp map[string]any, op store.Operator) {
	if op.Destination.Kind != store.DestinationOperator {
		return
	}
	core := h.For(r)
	target, err := core.GetOperator(r.Context(), op.Destination.TargetOperatorID)
	if err != nil {
		return
	}
	target = h.Advertised(target)
	certPEM, keyPEM, caPEM, err := core.IssueOperatorClientCertFor(r.Context(), httpkit.Actor(r), target.ID, op.ID, "for="+op.ID)
	if err != nil || len(certPEM) == 0 {
		return
	}
	secretCmd, err := ClientSecretCommand(target, certPEM, keyPEM, caPEM, "continuum-system", true)
	if err != nil {
		h.cfgErr(r, err) // logged; the response is without the Secret
		return
	}
	resp["exportSecretCommand"] = secretCmd
	endpoint := h.DestEndpoint(target, op.Destination)
	exportTarget := map[string]any{"operatorId": target.ID, "name": target.Name, "endpoint": endpoint, "reachableFromOtherClusters": target.Address != "" || endpoint != h.Endpoint(target)}
	if w := h.DestWarnings(r.Context(), target, op.Destination); len(w) > 0 {
		exportTarget["warnings"] = w
	}
	resp["exportTarget"] = exportTarget
}
