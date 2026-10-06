package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
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
// last-seen time and the server clock, never stored. state is "unknown" (no heartbeat credential), "waiting" (a
// credential exists and no heartbeat has arrived yet), "online" (a heartbeat within the last three intervals) or
// "offline"; the central operator's is FUSION's own state ("online" when it runs, "starting", "off" or
// "attention"). lastSeenAt is absent until one has arrived (for the central operator: until FUSION held data).
// reporting is true once the operator has a heartbeat credential and has sent at least one heartbeat.
// heartbeatEnabledAt is when its heartbeat credential was last minted. See operatorHealthAt and centralHealth.
type operatorHealthDoc struct {
	State              string `json:"state"`
	LastSeenAt         string `json:"lastSeenAt,omitempty"`
	Reporting          bool   `json:"reporting"`
	HeartbeatEnabledAt string `json:"heartbeatEnabledAt,omitempty"`
}

func toHealthDoc(h OperatorHealth) operatorHealthDoc {
	return operatorHealthDoc{State: h.State, LastSeenAt: rfcp(h.LastSeenAt), Reporting: h.Reporting, HeartbeatEnabledAt: rfcp(h.HeartbeatEnabledAt)}
}

// labelDoc is one operator label on the wire.
type labelDoc struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// certsDoc is when an operator's certificates stop being valid (RFC 3339); a date the server does not know is absent.
type certsDoc struct {
	ReceiverNotAfter string `json:"receiverNotAfter,omitempty"`
	ClientNotAfter   string `json:"clientNotAfter,omitempty"`
	CANotAfter       string `json:"caNotAfter,omitempty"`
}

// usedByDoc is what is configured to send to an operator: other operators exporting into it, active telemetry
// intents granting a cluster export to it, and the distinct clusters those intents belong to.
type usedByDoc struct {
	Operators []operatorRefDoc `json:"operators"`
	Intents   int              `json:"intents"`
	Clusters  int              `json:"clusters"`
}

type operatorRefDoc struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func toUsedByDoc(u OperatorUsage) *usedByDoc {
	if u.Empty() {
		return nil
	}
	d := &usedByDoc{Operators: []operatorRefDoc{}, Intents: u.Intents, Clusters: u.Clusters}
	for _, o := range u.Operators {
		d.Operators = append(d.Operators, operatorRefDoc{ID: o.ID, Name: o.Name})
	}
	return d
}

// The three states of an operator's reachability record: nothing to record (it is exposed to its own cluster only),
// waiting for an address (it was installed as a LoadBalancer or NodePort and none is recorded yet), recorded.
const (
	addressNone    = "none"
	addressPending = "pending"
	addressSet     = "set"
)

func addressStateOf(op store.Operator) string {
	switch {
	case op.Address != "":
		return addressSet
	case op.Exposure == "loadbalancer" || op.Exposure == "nodeport":
		return addressPending
	}
	return addressNone
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
	// Certs and CertState say when the operator's certificates stop working and whether that is near ("ok",
	// "expiring": within 60 days, "expired"). Absent for an operator with no certificate the server can vouch for
	// (a bearer one) and for a revoked one.
	Certs     *certsDoc `json:"certs,omitempty"`
	CertState string    `json:"certState,omitempty"`
	// Health is always present; for an operator that never opted in to a heartbeat it is
	// {state: "unknown", reporting: false}.
	Health operatorHealthDoc `json:"health"`
	// Address is the host:port other clusters reach this operator's receiver at; absent until it is set.
	// ReachableFromOtherClusters says whether the server can hand that out: false means the only address it has
	// is the in-cluster name, which resolves in the operator's own cluster alone. The central operator's comes
	// from FUSION (see FusionControl.Exposed), so it is filled in by the caller that knows.
	Address                    string `json:"address,omitempty"`
	ReachableFromOtherClusters bool   `json:"reachableFromOtherClusters"`
	// AddressState is "none" (nothing to record: exposed to its own cluster only), "pending" (exposed as a
	// LoadBalancer or NodePort, no address recorded yet) or "set".
	AddressState string `json:"addressState"`
	// Exposure is how its Service was exposed when it was created (cluster | loadbalancer | nodeport); absent for an
	// operator from before it was asked.
	Exposure string `json:"exposure,omitempty"`
	// Endpoint is what a command that points something at this operator dials: its address once one is recorded, and
	// otherwise the in-cluster name, which only resolves in the operator's own cluster.
	Endpoint string `json:"endpoint,omitempty"`
	// UsedBy is what is configured to send to this operator; absent when nothing is.
	UsedBy *usedByDoc `json:"usedBy,omitempty"`
}

func toOperatorDoc(op store.Operator, now time.Time) operatorDoc {
	d := operatorDoc{
		ID: op.ID, Name: op.Name, SiteID: op.SiteID, Status: string(op.Status),
		SourceClusterIDs: op.SourceClusterIDs, Destination: toDestinationDoc(op.Destination),
		CreatedAt: rfc(op.CreatedAt), CreatedBy: op.CreatedBy, Reason: op.Reason, ReceiverAuth: string(op.ReceiverAuth), ClientCaScope: op.ClientCAScope(),
		Health: toHealthDoc(operatorHealthAt(op, now)),
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
	if op.Status == store.OperatorActive {
		if c := operatorCertDates(op); c.known() {
			d.Certs = &certsDoc{ReceiverNotAfter: rfcp(c.Receiver), ClientNotAfter: rfcp(c.Client), CANotAfter: rfcp(c.CA)}
			d.CertState = c.certState(now)
		}
	}
	d.Address, d.ReachableFromOtherClusters, d.Exposure, d.AddressState = op.Address, op.Address != "", op.Exposure, addressStateOf(op)
	return d
}

// opDoc is toOperatorDoc plus what only the server's own wiring knows: the central operator is reachable from
// other clusters exactly when FUSION says it is exposed, at the address FUSION gives, and its health is FUSION's.
// What depends on the operator is worked out here, so a caller with many operators passes the usages it already
// computed (opDocWith) instead of scanning once per operator.
func (a *Admin) opDoc(r *http.Request, op store.Operator) operatorDoc {
	usages, _ := a.core(r).OperatorUsages(r.Context()) // best effort: without it the document simply has no usedBy
	return a.opDocWith(r, op, usages, a.fusionState(r))
}

// fusionState is FUSION's status for the documents of the central operator; read once per request by the callers
// that build several. FUSION is managed from the server's main organisation only, so for any other it is not
// available (there is no central operator to speak of).
func (a *Admin) fusionState(r *http.Request) FusionStatus {
	if f := a.Fusion; f != nil && f.Org != "" && a.core(r).OrgID != f.Org {
		return FusionStatus{State: "off", Reason: "other-org"}
	}
	return a.Fusion.Status(r.Context())
}

func (a *Admin) opDocWith(r *http.Request, op store.Operator, usages map[string]OperatorUsage, fusion FusionStatus) operatorDoc {
	d := toOperatorDoc(op, a.core(r).Now())
	d.Endpoint = a.operatorEndpoint(op)
	d.UsedBy = toUsedByDoc(usages[op.ID])
	if op.ID == CentralOperatorID {
		h := centralHealth(fusion)
		h.LastSeenAt = fusion.LastDataAt
		d.Health = toHealthDoc(h)
		if a.Fusion != nil {
			if d.ReachableFromOtherClusters = a.Fusion.Exposed(); d.ReachableFromOtherClusters {
				d.Address = a.Fusion.CentralEndpoint()
				d.AddressState = addressSet
			} else {
				d.AddressState = addressNone
			}
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
	usages, _ := a.core(r).OperatorUsages(r.Context())
	fusion := a.fusionState(r)
	out := []operatorDoc{}
	for _, op := range ops {
		out = append(out, a.opDocWith(r, op, usages, fusion))
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
		// operator start calling this server (and an install whose heartbeat.url is plain HTTP is refused by the
		// chart). The UI sends true by default and says what it does.
		Heartbeat bool `json:"heartbeat,omitempty"`
		// Exposure is how the operator's Service is made reachable from other clusters: "" or "cluster" (it is
		// not: in-cluster only), "loadbalancer" or "nodeport". It sets the install command's service.type and is
		// kept (store.Operator.Exposure) so the page can say an address is still to be recorded; the address that
		// results is learned afterwards (POST .../address).
		Exposure string `json:"exposure,omitempty"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	labels := make([]store.OperatorLabel, 0, len(req.Labels))
	for _, l := range req.Labels {
		labels = append(labels, store.OperatorLabel{Key: l.Key, Value: l.Value})
	}
	// req.Exposure "" stays "": a client that was not asked has not said "this cluster only". The core validates it.
	op, secret, tlsBundle, hbSecret, err := a.core(r).CreateOperatorWithOptions(r.Context(), actor(r), req.Name, req.SourceClusterIDs, req.Destination.toStore(), modalitiesFromDoc(req.AcceptedModalities), OperatorOptions{Heartbeat: req.Heartbeat, Labels: labels, Exposure: req.Exposure})
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 201, a.operatorInstallDoc(r, op, installMaterial{Secret: secret, TLS: tlsBundle, HeartbeatSecret: hbSecret}))
}

// installMaterial is what was minted for one install and is shown once: a bearer operator's receiver token, the
// certificates of an mTLS one, the heartbeat secret of one that reports. Reissue says the operator already runs, so
// the commands upgrade it in place instead of installing it.
type installMaterial struct {
	Secret          string
	TLS             OperatorTLSBundle
	HeartbeatSecret string
	Reissue         bool
}

// operatorInstallDoc is the response of creating an operator and of installing it again: the operator, the install
// command, what to apply in the source clusters, and the Secrets the install points at.
func (a *Admin) operatorInstallDoc(r *http.Request, op store.Operator, m installMaterial) map[string]any {
	core := a.core(r)
	img := a.images(core)
	hbURL := ""
	if m.HeartbeatSecret != "" {
		hbURL = a.heartbeatURL(r)
	}
	var target *store.Operator
	if op.Destination.Kind == store.DestinationOperator {
		if t, err := core.GetOperator(r.Context(), op.Destination.TargetOperatorID); err == nil {
			t = a.advertised(t)
			target = &t
		}
	}
	install, secretCmd := a.operatorInstallCommandWith(img, m.Secret, op, m.TLS, hbURL, target, m.Reissue)
	if svcType, _ := serviceTypeFor(op.Exposure); svcType != "" {
		install += " \\\n  " + setFlag("service.type", svcType)
	}
	resp := map[string]any{
		"operator":  a.opDoc(r, op),
		"install":   install,
		"reminders": a.operatorSourceReminders(r, op, m.TLS),
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
		resp["heartbeatSecretCommand"] = operatorHeartbeatSecretCommand(op, m.HeartbeatSecret)
		resp["heartbeatUrl"] = hbURL
		resp["heartbeatIntervalSeconds"] = int(OperatorHeartbeatInterval / time.Second)
		if warn := heartbeatURLWarning(hbURL); warn != "" {
			resp["heartbeatWarning"] = warn
		}
		if caCmd := a.heartbeatCASecretCommand(op); caCmd != "" {
			resp["heartbeatCaSecretCommand"] = caCmd
		}
	}
	if tlsCmd := operatorTLSSecretCommand(op, m.TLS); tlsCmd != "" {
		resp["tlsSecretCommand"] = tlsCmd
	}
	if m.Reissue {
		// A Secret's new value is not seen by a running pod until it is restarted (its certificates reload on their
		// own, a changed token or heartbeat secret does not).
		resp["restartCommand"] = operatorRestartCommand(op)
	}
	a.addOperatorTargetExport(r, resp, op)
	return resp
}

// reissueOperatorInstall is "install this operator again / renew its certificates", available at any time: a fresh
// receiver and client certificate from the operator's stored CA, with the commands to apply them, and a new
// receiver token or heartbeat secret where the operator has one (they are hashed and cannot be shown again).
func (a *Admin) reissueOperatorInstall(w http.ResponseWriter, r *http.Request) {
	op, mat, err := a.core(r).ReissueOperatorInstall(r.Context(), actor(r), r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 200, a.operatorInstallDoc(r, op, installMaterial{Secret: mat.ReceiverToken, TLS: mat.TLS, HeartbeatSecret: mat.HeartbeatSecret, Reissue: true}))
}

// issuedCertDoc is one ledger entry. state is "ok", "expiring" (inside the first warning threshold) or "expired",
// judged by the same thresholds as the operator's own certState.
type issuedCertDoc struct {
	Serial    string `json:"serial"`
	Kind      string `json:"kind"`
	Subject   string `json:"subject"`
	Sender    string `json:"sender,omitempty"`
	IssuedBy  string `json:"issuedBy"`
	IssuedAt  string `json:"issuedAt"`
	NotBefore string `json:"notBefore"`
	NotAfter  string `json:"notAfter"`
	State     string `json:"state"`
}

// listOperatorCertificates answers "which certificates exist for this operator, who holds them, and when do they
// stop": the ledger newest first. It never returns a certificate or key.
func (a *Admin) listOperatorCertificates(w http.ResponseWriter, r *http.Request) {
	core := a.core(r)
	certs, err := core.OperatorCertificates(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	now := core.Now()
	out := make([]issuedCertDoc, 0, len(certs))
	for _, c := range certs {
		end := c.NotAfter
		state := OperatorCerts{Receiver: &end}.certState(now)
		out = append(out, issuedCertDoc{
			Serial: c.Serial, Kind: string(c.Kind), Subject: c.Subject, Sender: c.Sender, IssuedBy: c.IssuedBy,
			IssuedAt: rfc(c.IssuedAt), NotBefore: rfc(c.NotBefore), NotAfter: rfc(c.NotAfter), State: state,
		})
	}
	writeJSON(w, 200, map[string]any{"certificates": out})
}

// operatorRestartCommand restarts the operator's pods, which is how they pick up a Secret read into the environment.
func operatorRestartCommand(op store.Operator) string {
	return fmt.Sprintf("kubectl rollout restart deployment/%s --namespace continuum-system", operatorServiceName(op.ID))
}

// serviceTypeFor maps an exposure to the chart's service.type ("" keeps the chart's own default, ClusterIP) and says
// whether it is one of the three that exist.
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
	addr, err := a.core(r).SetOperatorAddress(r.Context(), actor(r), id, req.Address)
	if err != nil {
		a.fail(w, err)
		return
	}
	if id == CentralOperatorID && a.Fusion != nil {
		a.Fusion.SetPublicAddress(addr) // from now on every command and the screens use it; no certificate changes
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
	before, err := a.core(r).GetOperator(r.Context(), id)
	if err != nil {
		a.fail(w, err)
		return
	}
	if err := a.core(r).UpdateOperatorScope(r.Context(), actor(r), id, req.SourceClusterIDs, req.Destination.toStore(), modalitiesFromDoc(req.AcceptedModalities)); err != nil {
		a.fail(w, err)
		return
	}
	op, err := a.core(r).GetOperator(r.Context(), id)
	if err != nil {
		a.fail(w, err)
		return
	}
	// A client certificate is minted only for a cluster the scope gained: it has none yet. The ones already
	// configured keep the certificate they have (it stays valid until it expires; renewing is the install route's
	// job), so a scope change that adds nobody costs no certificate and no key derivation. Nothing about the
	// previously issued ones stops working either: there is no per-certificate revocation here. Each gained
	// cluster gets its own, naming it, signed by the operator's own CA, whose certificate the receiver trusts.
	var tlsBundle OperatorTLSBundle
	if gained := clustersGained(before.SourceClusterIDs, op.SourceClusterIDs); len(gained) > 0 {
		tlsBundle.Senders, tlsBundle.CACertPEM = a.core(r).mintSenderCerts(r.Context(), actor(r), op, gained)
	}
	resp := map[string]any{"operator": a.opDoc(r, op), "reminders": a.operatorSourceReminders(r, op, tlsBundle)}
	a.addOperatorTargetExport(r, resp, op)
	writeJSON(w, 200, resp)
}

// clustersGained are the clusters in now that were not in before.
func clustersGained(before, now []string) []string {
	had := make(map[string]bool, len(before))
	for _, c := range before {
		had[c] = true
	}
	var out []string
	for _, c := range now {
		if !had[c] {
			out = append(out, c)
		}
	}
	return out
}

// operatorUninstallCommand removes the receiver. Revoking or deleting an operator in the server does not touch the
// running one - it keeps receiving, and trusting its own CA, until it is uninstalled - so the response says how.
func operatorUninstallCommand(id string) string {
	return fmt.Sprintf("helm uninstall %s --namespace continuum-system", id)
}

func (a *Admin) revokeOperator(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Reason string `json:"reason"`
		// Force confirms revoking an operator that others still send to (see Core.RevokeOperatorForce).
		Force bool `json:"force,omitempty"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	id := r.PathValue("id")
	if err := a.core(r).RevokeOperatorForce(r.Context(), actor(r), id, req.Reason, req.Force); err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"uninstall": operatorUninstallCommand(id)})
}

func (a *Admin) deleteOperator(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := a.core(r).DeleteOperatorForce(r.Context(), actor(r), id, r.URL.Query().Get("force") == "true"); err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"uninstall": operatorUninstallCommand(id)})
}

// operatorReceiverTLSSecretName and operatorClientTLSSecretName are the fixed Secret names the operator
// chart's receiver.tls.secretName and the agent chart's telemetry.export.otlp.tls.mtls.secretName default
// install commands point at - fixed per operator so the reminders below and operatorInstallCommand agree
// without threading a name through both.
func operatorReceiverTLSSecretName(op store.Operator) string { return op.ID + "-receiver-tls" }
func operatorClientTLSSecretName(op store.Operator) string   { return op.ID + "-export-mtls" }

// operatorTLSSecretCommand is the Secret for the operator's own receiver certificate -
// installed once, wherever the operator itself runs. Empty if CreateOperator could not mint the TLS
// material (a rare failure it already tolerates - see its own comment): that operator is then a bearer
// one, and the bearer token is its only gate.
func operatorTLSSecretCommand(op store.Operator, b OperatorTLSBundle) string {
	if len(b.ReceiverCertPEM) == 0 {
		return ""
	}
	return withNamespace("continuum-system", applySecretCommand(operatorReceiverTLSSecretName(op), "continuum-system",
		secretKV("tls.crt", string(b.ReceiverCertPEM)), secretKV("tls.key", string(b.ReceiverKeyPEM)), secretKV("ca.crt", string(b.CACertPEM))))
}

// exportBlockFlags states the whole destination block of one exporter (telemetry.export.otlp, or one route of
// telemetry.export.routes) that sends to the regional operator op at endpoint: endpoint, protocol, certificate
// checking, the client certificate and the name it is verified by, and no credential header. Every field is stated,
// the unused ones empty, because the command is applied with --reuse-values: a field it leaves out keeps whatever an
// earlier destination set (an operator's mTLS Secret, protocol=http, insecure=true), and the exporter would carry on
// half pointed at the old one. withMTLS is whether the exporter presents a client certificate; a bearer operator
// reached without one leaves the credential header to the person, who holds the token.
func exportBlockFlags(prefix string, op store.Operator, endpoint string, withMTLS bool) string {
	flags := []string{
		setFlag(prefix+".endpoint", endpoint),
		setFlag(prefix+".protocol", "grpc"),
		setFlag(prefix+".tls.insecure", "false"),
		setFlag(prefix+".tls.caFile", ""),
	}
	serverName := ""
	if operatorNeedsServerName(op) {
		serverName = operatorServerName(op)
	}
	if withMTLS {
		flags = append(flags, setFlag(prefix+".tls.mtls.enabled", "true"), setFlag(prefix+".tls.mtls.secretName", operatorClientTLSSecretName(op)))
	} else {
		flags = append(flags, setFlag(prefix+".tls.mtls.enabled", "false"), setFlag(prefix+".tls.mtls.secretName", ""))
	}
	flags = append(flags, setFlag(prefix+".tls.serverName", serverName))
	if withMTLS {
		flags = append(flags, setFlag(prefix+".auth.secretName", ""))
	}
	return strings.Join(flags, " ")
}

// operatorDestinationCommand builds the --set export.otlp.* flags (and, if a fresh client cert was minted,
// the Secret command for it) that point one agent's telemetry export at this operator -
// the same shape operatorSourceReminders already builds per source cluster at creation/scope-update time,
// factored out so TelemetryIntent's own /command endpoint (admin_telemetry_intents.go) can call it for a
// single agent on demand, reusing a freshly reissued client cert rather than requiring one from creation time.
// The flags are the whole destination block (see exportBlockFlags) whether or not a certificate is passed: an mTLS
// operator is always reached with a client certificate, and one that is not minted this time is the Secret already in
// the cluster.
func operatorDestinationCommand(op store.Operator, endpoint string, certPEM, keyPEM, caPEM []byte, namespace string) (setFlags string, secretCmd string) {
	withMTLS := len(certPEM) > 0 || op.ReceiverAuth == store.ReceiverAuthMTLS
	setFlags = exportBlockFlags("telemetry.export.otlp", op, endpoint, withMTLS)
	if len(certPEM) > 0 {
		// mTLS is additive: every source cluster of this operator presents a client certificate verified
		// against the CA bundle in the same Secret - see pki.IssueOperatorClientTLS.
		secretCmd = operatorClientSecretCommand(op, certPEM, keyPEM, caPEM, namespace)
	}
	return setFlags, secretCmd
}

// operatorClientSecretCommand is the Secret holding the client certificate an agent
// presents to this operator. One Secret per operator, whatever number of the agent's signal types export to
// it: every route to the same operator names this same Secret.
func operatorClientSecretCommand(op store.Operator, certPEM, keyPEM, caPEM []byte, namespace string) string {
	return applySecretCommand(operatorClientTLSSecretName(op), namespace,
		secretKV("tls.crt", string(certPEM)), secretKV("tls.key", string(keyPEM)), secretKV("ca.crt", string(caPEM)))
}

// operatorRouteFlags are the --set flags that make one signal type's own route (telemetry.export.routes.<m>)
// export to this operator over mutual TLS: its receiver, gRPC, certificate verified, and the Secret from
// operatorClientSecretCommand. Stated in full (see exportBlockFlags) so the route does not depend on anything an
// earlier command set.
func operatorRouteFlags(op store.Operator, endpoint string, m store.Modality) string {
	return exportBlockFlags(fmt.Sprintf("telemetry.export.routes.%s", m), op, endpoint, true)
}

// advertised is op as a caller elsewhere sees it: the central operator is reached at the address FUSION is exposed at
// (empty when it is not, which leaves its in-cluster name), so from here on it is handled like any operator that has an
// advertised address - dialled there, and verified by its stable name.
func (a *Admin) advertised(op store.Operator) store.Operator {
	if op.ID == CentralOperatorID && a.Fusion != nil && a.Fusion.Exposed() {
		op.Address = a.Fusion.CentralEndpoint()
	}
	return op
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
	return operatorInClusterEndpoint(op)
}

// addOperatorTargetExport adds, for a regional operator that exports to ANOTHER operator (the central one, usually),
// the one Secret its install command depends on: the client certificate the target's receiver requires, issued now
// from the target's own CA. The install command itself already points the exporter at it (operatorInstallCommand).
// The Secret lands in the operator's own namespace, which on a first install does not exist yet, so the command makes it.
func (a *Admin) addOperatorTargetExport(r *http.Request, resp map[string]any, op store.Operator) {
	if op.Destination.Kind != store.DestinationOperator {
		return
	}
	core := a.core(r)
	target, err := core.GetOperator(r.Context(), op.Destination.TargetOperatorID)
	if err != nil {
		return
	}
	target = a.advertised(target)
	certPEM, keyPEM, caPEM, err := core.IssueOperatorClientCertFor(r.Context(), actor(r), target.ID, op.ID, "for="+op.ID)
	if err != nil || len(certPEM) == 0 {
		return
	}
	resp["exportSecretCommand"] = withNamespace("continuum-system", operatorClientSecretCommand(target, certPEM, keyPEM, caPEM, "continuum-system"))
	resp["exportTarget"] = map[string]any{"operatorId": target.ID, "name": target.Name, "endpoint": a.operatorEndpoint(target), "reachableFromOtherClusters": target.Address != ""}
}

// operatorChartArgs is the chart reference an operator `helm` command names, and the " --version ..." that
// goes with it when that reference is a registry one (empty for a local .tgz). Both are quoted: they come from
// settings an organisation's administrator can change.
func (a *Admin) operatorChartArgs(img ImageConfig) (ref, version string) {
	ref = a.operatorChartRef(img)
	if ref == "" {
		ref = "./" + chart.RegionalOperator.Filename()
	} else if !strings.HasSuffix(ref, ".tgz") {
		version = " --version " + shellArg(a.operatorChartVersion())
	}
	return shellArg(ref), version
}

// operatorInstallCommand is installCommand's own twin for the regional-operator chart: simpler, since this
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
//   - ReceiverAuthBearer (every operator from before ReceiverAuth existed, and a new one whose TLS mint
//     failed): unchanged - the bearer token, plus mTLS on top when the certificates were minted.
//
// heartbeatURL is "" for an operator that did not opt in to a heartbeat (the command is then exactly what
// it was before heartbeats existed).
func (a *Admin) operatorInstallCommand(img ImageConfig, secret string, op store.Operator, tlsBundle OperatorTLSBundle, heartbeatURL string) (install, secretCmd string) {
	return a.operatorInstallCommandWith(img, secret, op, tlsBundle, heartbeatURL, nil, false)
}

// operatorInstallCommandTo is operatorInstallCommand when the operator's destination is another operator and the
// caller has that operator's record: its advertised address (if any) is then what the exporter dials, and the
// certificate is verified against the stable name on it. With target nil only the id is known, which fixes the
// in-cluster name and nothing more.
func (a *Admin) operatorInstallCommandTo(img ImageConfig, secret string, op store.Operator, tlsBundle OperatorTLSBundle, heartbeatURL string, target *store.Operator) (install, secretCmd string) {
	return a.operatorInstallCommandWith(img, secret, op, tlsBundle, heartbeatURL, target, false)
}

// operatorInstallCommandWith is operatorInstallCommandTo that can also upgrade a release that already runs
// (reissue): `helm upgrade --install --reuse-values` keeps what the person tuned (resources, image), so every value
// this command is about is stated, the unused ones empty - an export block left out would keep an old destination's
// settings.
func (a *Admin) operatorInstallCommandWith(img ImageConfig, secret string, op store.Operator, tlsBundle OperatorTLSBundle, heartbeatURL string, target *store.Operator, reissue bool) (install, secretCmd string) {
	ref, version := a.operatorChartArgs(img)
	secretName := op.ID + "-receiver-auth"
	var b strings.Builder
	if reissue {
		fmt.Fprintf(&b, "helm upgrade --install %s %s%s \\\n  --namespace continuum-system --create-namespace --reuse-values", op.ID, ref, version)
	} else {
		fmt.Fprintf(&b, "helm install %s %s%s \\\n  --namespace continuum-system --create-namespace", op.ID, ref, version)
	}
	flag := func(f string) { fmt.Fprintf(&b, " \\\n  %s", f) }
	if op.Destination.Kind == store.DestinationOperator {
		// Another regional operator (the central one in front of FUSION, usually): its receiver, over mutual TLS with
		// a client certificate from the target's own CA (the Secret addOperatorTargetExport hands out).
		tgt := store.Operator{ID: op.Destination.TargetOperatorID} // the id alone fixes the in-cluster endpoint and the Secret name
		if target != nil {
			tgt = *target
		}
		flag(setFlag("export.otlp.endpoint", a.operatorEndpoint(tgt)))
		if reissue {
			flag(setFlag("export.otlp.protocol", "grpc"))
			flag(setFlag("export.otlp.tls.insecure", "false"))
			flag(setFlag("export.otlp.tls.caFile", ""))
		}
		flag(setFlag("export.otlp.tls.mtls.enabled", "true"))
		flag(setFlag("export.otlp.tls.mtls.secretName", operatorClientTLSSecretName(tgt)))
		if operatorNeedsServerName(tgt) {
			flag(setFlag("export.otlp.tls.serverName", operatorServerName(tgt)))
		} else if reissue {
			flag(setFlag("export.otlp.tls.serverName", ""))
		}
		if reissue {
			flag(setFlag("export.otlp.auth.secretName", ""))
		}
	} else {
		d := op.Destination
		flag(setFlag("export.otlp.endpoint", d.Endpoint))
		if reissue {
			flag(setFlag("export.otlp.protocol", "grpc"))
			flag(setFlag("export.otlp.tls.insecure", strconv.FormatBool(d.Insecure)))
			flag(setFlag("export.otlp.tls.caFile", d.CAFile))
			flag(setFlag("export.otlp.tls.mtls.enabled", "false"))
			flag(setFlag("export.otlp.tls.mtls.secretName", ""))
			flag(setFlag("export.otlp.tls.serverName", ""))
		} else {
			if d.Insecure {
				flag(setFlag("export.otlp.tls.insecure", "true"))
			}
			if d.CAFile != "" {
				flag(setFlag("export.otlp.tls.caFile", d.CAFile))
			}
		}
		if d.AuthSecretName != "" {
			flag(setFlag("export.otlp.auth.headerName", d.AuthHeaderName))
			flag(setFlag("export.otlp.auth.secretName", d.AuthSecretName))
			flag(setFlag("export.otlp.auth.secretKey", d.AuthSecretKey))
		} else if reissue {
			flag(setFlag("export.otlp.auth.secretName", ""))
		}
	}
	mtlsOnly := op.ReceiverAuth == store.ReceiverAuthMTLS
	if mtlsOnly {
		flag("--set receiver.auth.enabled=false")
		flag("--set receiver.requireAuth=true")
		flag("--set receiver.tls.enabled=true")
		flag(setFlag("receiver.tls.secretName", operatorReceiverTLSSecretName(op)))
		flag("--set receiver.tls.mtls=true")
	} else {
		flag("--set receiver.auth.enabled=true")
		flag(setFlag("receiver.auth.secretName", secretName))
	}
	// For a bearer operator, mTLS on the receiver is additive to the bearer token above, not a
	// replacement - see this chart's own receiver.tls comment. Only set up when CreateOperator actually
	// minted the certificates.
	if !mtlsOnly && len(tlsBundle.ReceiverCertPEM) > 0 {
		flag("--set receiver.tls.enabled=true")
		flag(setFlag("receiver.tls.secretName", operatorReceiverTLSSecretName(op)))
		flag("--set receiver.tls.mtls=true")
	}
	if heartbeatURL != "" {
		flag(a.operatorHeartbeatSetFlags(op, heartbeatURL))
	}
	flag("--set-json operator=" + shellQuote(operatorProvenanceJSON(op)))
	// No image flags, on purpose. The operator runs the upstream OpenTelemetry Collector image the chart
	// already names - not the `continuum` image the configured registry holds - so pointing it at
	// <registry>/continuum-regional-operator (an image nothing ever published) made every install of it
	// fail with ImagePullBackOff. A cluster that must pull from its own registry sets image.repository
	// and image.tag itself (see the chart's values.yaml).
	if !mtlsOnly {
		secretCmd = withNamespace("continuum-system", applySecretCommand(secretName, "continuum-system", secretKV("token", secret)))
	}
	return b.String(), secretCmd
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
//
// Each upgrade states the whole destination block (see exportBlockFlags), and the client certificate Secret goes once per
// distinct namespace rather than once per cluster, so the private key is not repeated for every cluster that
// shares a namespace name; it is applied in each of the clusters listed above it. A Secret is only listed when a
// certificate was just minted (tlsBundle): without one the clusters keep the Secret they have.
func (a *Admin) operatorSourceReminders(r *http.Request, op store.Operator, tlsBundle OperatorTLSBundle) []string {
	hub := a.tn(r).Hub
	img := a.images(a.core(r))
	ref, version := a.chartRef(img), ""
	if ref == "" {
		ref = "./" + chart.Agent.Filename()
	} else if !strings.HasSuffix(ref, ".tgz") {
		version = " --version " + shellArg(a.agentChartVersion())
	}
	ref = shellArg(ref)
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
	type target struct{ cluster, ns, name string }
	targets := make([]target, 0, len(op.SourceClusterIDs))
	for _, cl := range op.SourceClusterIDs {
		ag, ok := byCluster[cl]
		ns, name := "", ""
		if ok {
			ns, name = hub.NamespaceOf(ag.ID), hub.ReleaseNameOf(ag.ID)
		}
		rns, rname, _ := releaseTarget(ns, name)
		targets = append(targets, target{cl, rns, rname})
	}
	out := make([]string, 0, 2*len(targets))
	// Each cluster holds its own client certificate (see OperatorTLSBundle.Senders), so each gets its own Secret
	// command, in its own namespace. Only wired in when that certificate was minted (see CreateOperator on why
	// that can fail without failing the operator): this is additive, the bearer token alone still works without it.
	for _, t := range targets {
		sc, ok := tlsBundle.forSender(t.cluster)
		if !ok {
			continue
		}
		if _, secretCmd := operatorDestinationCommand(op, a.operatorEndpoint(op), sc.CertPEM, sc.KeyPEM, tlsBundle.CACertPEM, t.ns); secretCmd != "" {
			out = append(out, fmt.Sprintf("# in cluster %s (namespace %s): create its client certificate Secret first\n%s", t.cluster, t.ns, secretCmd))
		}
	}
	for _, t := range targets {
		sc, _ := tlsBundle.forSender(t.cluster)
		setFlags, _ := operatorDestinationCommand(op, a.operatorEndpoint(op), sc.CertPEM, sc.KeyPEM, tlsBundle.CACertPEM, t.ns)
		upgrade := fmt.Sprintf("helm upgrade %s %s%s --namespace %s --reuse-values %s", shellArg(t.name), ref, version, shellArg(t.ns), setFlags)
		out = append(out, upgrade+fmt.Sprintf("  # cluster %s", t.cluster))
	}
	return out
}
