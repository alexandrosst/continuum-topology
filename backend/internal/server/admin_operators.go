package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"continuum/internal/chart"
	"continuum/internal/install"
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
	// Address is only meaningful for destination kind "operator" - see store.Destination's own field.
	Address string `json:"address,omitempty"`
	// FusionRelease and FusionNamespace are only meaningful for destination kind "fusion" - see
	// store.Destination's own fields. Both default (to "fusion" and "continuum-system") when left empty.
	FusionRelease   string `json:"fusionRelease,omitempty"`
	FusionNamespace string `json:"fusionNamespace,omitempty"`
}

func toDestinationDoc(d store.Destination) destinationDoc {
	return destinationDoc{Kind: string(d.Kind), Endpoint: d.Endpoint, Insecure: d.Insecure, CAFile: d.CAFile, AuthHeaderName: d.AuthHeaderName, AuthSecretName: d.AuthSecretName, AuthSecretKey: d.AuthSecretKey, TargetOperatorID: d.TargetOperatorID, Address: d.Address, FusionRelease: d.FusionRelease, FusionNamespace: d.FusionNamespace}
}

func (d destinationDoc) toStore() store.Destination {
	return store.Destination{Kind: store.DestinationKind(d.Kind), Endpoint: d.Endpoint, Insecure: d.Insecure, CAFile: d.CAFile, AuthHeaderName: d.AuthHeaderName, AuthSecretName: d.AuthSecretName, AuthSecretKey: d.AuthSecretKey, TargetOperatorID: d.TargetOperatorID, Address: d.Address, FusionRelease: d.FusionRelease, FusionNamespace: d.FusionNamespace}
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
	// Certs and CertState say when the operator's certificates stop working and whether they are being renewed ("ok";
	// "renewal-failing": the day they should have been renewed by has passed and they still run; "expired").
	// CertStateSince is that day. Absent for an operator with no certificate the server can vouch for (a bearer one) and
	// for a revoked one.
	Certs          *certsDoc `json:"certs,omitempty"`
	CertState      string    `json:"certState,omitempty"`
	CertStateSince string    `json:"certStateSince,omitempty"`
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
	// AddressWarnings are the doubts about Address, in words: a port that is the HTTP one, a private address, and - for the
	// central operator - a gateway Service that is not exposed or listens elsewhere. A recorded address is what a person
	// typed (or what the cloud reported), not something the server has seen answer: these say where it looks wrong, and
	// POST .../address/check looks at what answers. Absent when there is nothing to say.
	AddressWarnings []string `json:"addressWarnings,omitempty"`
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
			var since *time.Time
			d.CertState, since = c.certState(now)
			d.CertStateSince = rfcp(since)
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
	d.AddressWarnings = addressWarnings(op.Address, nil)
	if op.ID == CentralOperatorID {
		h := centralHealth(fusion)
		h.LastSeenAt = fusion.LastDataAt
		d.Health = toHealthDoc(h)
		if a.Fusion != nil {
			if d.ReachableFromOtherClusters = a.Fusion.Exposed(); d.ReachableFromOtherClusters {
				d.Address = a.Fusion.CentralEndpoint()
				d.AddressState = addressSet
				d.AddressWarnings = a.Fusion.AddressWarnings(r.Context()) // checked against the gateway's own Service
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
		// ExtraProcessors are the OTel Collector processors to add to the operator's pipelines. They are chart values and nothing more: the
		// operator record does not keep them, so they are in this command and in no other.
		ExtraProcessors []install.Processor `json:"extraProcessors,omitempty"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	processors, err := install.OperatorProcessors(req.ExtraProcessors)
	if err != nil {
		var pe *install.ProblemsError
		if errors.As(err, &pe) {
			a.fail(w, errf(KindInvalid, "%s", pe.Problems[0]))
			return
		}
		a.fail(w, err)
		return
	}
	labels := make([]store.OperatorLabel, 0, len(req.Labels))
	for _, l := range req.Labels {
		labels = append(labels, store.OperatorLabel{Key: l.Key, Value: l.Value})
	}
	// The receiver token and the certificates are shown once, with the command that uses them: a command that cannot be written is
	// found before they exist.
	if err := a.checkOperatorInstall(a.images(a.core(r))); err != nil {
		a.fail(w, a.installConfigError(err))
		return
	}
	// req.Exposure "" stays "": a client that was not asked has not said "this cluster only". The core validates it.
	op, secret, tlsBundle, hbSecret, err := a.core(r).CreateOperatorWithOptions(r.Context(), actor(r), req.Name, req.SourceClusterIDs, req.Destination.toStore(), modalitiesFromDoc(req.AcceptedModalities), OperatorOptions{Heartbeat: req.Heartbeat, Labels: labels, Exposure: req.Exposure})
	if err != nil {
		a.fail(w, err)
		return
	}
	doc, err := a.operatorInstallDoc(r, op, installMaterial{Secret: secret, TLS: tlsBundle, HeartbeatSecret: hbSecret, Processors: processors})
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 201, doc)
}

// installMaterial is what was minted for one install and is shown once: a bearer operator's receiver token, the
// certificates of an mTLS one, the heartbeat secret of one that reports. Reissue says the operator already runs, so
// the commands upgrade it in place instead of installing it.
type installMaterial struct {
	Secret          string
	TLS             OperatorTLSBundle
	HeartbeatSecret string
	Reissue         bool
	// Processors are the chart values of the extra processors a person asked for, which follow the rest of the command.
	Processors []install.Value
}

// operatorInstallDoc is the response of creating an operator and of installing it again: the operator, the install
// command, what to apply in the source clusters, and the Secrets the install points at.
func (a *Admin) operatorInstallDoc(r *http.Request, op store.Operator, m installMaterial) (map[string]any, error) {
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
	cmd, secretCmd, err := a.operatorInstallCommandWith(img, m.Secret, op, m.TLS, hbURL, target, m.Reissue)
	if err != nil {
		return nil, a.installConfigError(err)
	}
	resp := map[string]any{
		"operator":  a.opDoc(r, op),
		"install":   strings.TrimRight(cmd, "\n ") + install.Continued(m.Processors),
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
		hbCmd, err := operatorHeartbeatSecretCommand(op, m.HeartbeatSecret)
		if err != nil {
			return nil, a.installConfigError(err)
		}
		resp["heartbeatSecretCommand"] = hbCmd
		resp["heartbeatUrl"] = hbURL
		resp["heartbeatIntervalSeconds"] = int(OperatorHeartbeatInterval / time.Second)
		if warn := heartbeatURLWarning(hbURL); warn != "" {
			resp["heartbeatWarning"] = warn
		}
		caCmd, err := a.heartbeatCASecretCommand(op)
		if err != nil {
			return nil, a.installConfigError(err)
		}
		if caCmd != "" {
			resp["heartbeatCaSecretCommand"] = caCmd
		}
	}
	tlsCmd, err := operatorTLSSecretCommand(op, m.TLS)
	if err != nil {
		return nil, a.installConfigError(err)
	}
	if tlsCmd != "" {
		resp["tlsSecretCommand"] = tlsCmd
	}
	if m.Reissue {
		// A Secret's new value is not seen by a running pod until it is restarted (its certificates reload on their
		// own, a changed token or heartbeat secret does not).
		restart, err := operatorRestartCommand(op)
		if err != nil {
			return nil, a.installConfigError(err)
		}
		resp["restartCommand"] = restart
	}
	a.addOperatorTargetExport(r, resp, op)
	return resp, nil
}

// reissueOperatorInstall is "install this operator again / renew its certificates", available at any time: a fresh
// receiver and client certificate from the operator's stored CA, with the commands to apply them, and a new
// receiver token or heartbeat secret where the operator has one (they are hashed and cannot be shown again).
func (a *Admin) reissueOperatorInstall(w http.ResponseWriter, r *http.Request) {
	if err := a.checkOperatorInstall(a.images(a.core(r))); err != nil {
		a.fail(w, a.installConfigError(err))
		return
	}
	op, mat, err := a.core(r).ReissueOperatorInstall(r.Context(), actor(r), r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	doc, err := a.operatorInstallDoc(r, op, installMaterial{Secret: mat.ReceiverToken, TLS: mat.TLS, HeartbeatSecret: mat.HeartbeatSecret, Reissue: true})
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 200, doc)
}

// issuedCertDoc is one ledger entry. state is "ok" or "expired": whether renewal is happening is a property of the
// holder (the operator's certState), not of one of the certificates it has already replaced.
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
		state := CertStateOK
		if !end.After(now) {
			state = CertStateExpired
		}
		out = append(out, issuedCertDoc{
			Serial: c.Serial, Kind: string(c.Kind), Subject: c.Subject, Sender: c.Sender, IssuedBy: c.IssuedBy,
			IssuedAt: rfc(c.IssuedAt), NotBefore: rfc(c.NotBefore), NotAfter: rfc(c.NotAfter), State: state,
		})
	}
	writeJSON(w, 200, map[string]any{"certificates": out})
}

// operatorRestartCommand restarts the operator's pods, which is how they pick up a Secret read into the environment.
func operatorRestartCommand(op store.Operator) (string, error) {
	return install.RestartDeployment{Deployment: operatorServiceName(op.ID), Namespace: "continuum-system"}.Render()
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
	if id == CentralOperatorID && a.Fusion != nil {
		req.Address = a.Fusion.WithGatewayPort(r.Context(), req.Address) // a NodePort is not on 4317
	}
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
func operatorUninstallCommand(id string) (string, error) {
	return install.Uninstall{Release: id, Namespace: "continuum-system"}.Render()
}

// uninstallDoc is the response of revoking or deleting an operator, which has been done by now: the command that removes the running
// receiver, or no command (and a line in the log) when it cannot be written, which the operator's id, a name this server made, makes
// unlikely.
func (a *Admin) uninstallDoc(id string) map[string]any {
	cmd, err := operatorUninstallCommand(id)
	if err != nil {
		a.installConfigError(err)
		return map[string]any{}
	}
	return map[string]any{"uninstall": cmd}
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
	writeJSON(w, 200, a.uninstallDoc(id))
}

func (a *Admin) deleteOperator(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := a.core(r).DeleteOperatorForce(r.Context(), actor(r), id, r.URL.Query().Get("force") == "true"); err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 200, a.uninstallDoc(id))
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
func operatorTLSSecretCommand(op store.Operator, b OperatorTLSBundle) (string, error) {
	if len(b.ReceiverCertPEM) == 0 {
		return "", nil
	}
	return install.Secret{Name: operatorReceiverTLSSecretName(op), Namespace: "continuum-system", EnsureNamespace: true, Entries: []install.Entry{
		{Key: "tls.crt", Value: string(b.ReceiverCertPEM)}, {Key: "tls.key", Value: string(b.ReceiverKeyPEM)}, {Key: "ca.crt", Value: string(b.CACertPEM)}}}.Render()
}

// exportBlock states the whole destination block of one exporter (telemetry.export.otlp, or one route of
// telemetry.export.routes) that sends to the regional operator op at endpoint: endpoint, protocol, certificate
// checking, the client certificate and the name it is verified by, and no credential header. Every field is stated,
// the unused ones empty, because the command is applied with --reset-then-reuse-values: a field it leaves out keeps whatever an
// earlier destination set (an operator's mTLS Secret, protocol=http, insecure=true), and the exporter would carry on
// half pointed at the old one. withMTLS is whether the exporter presents a client certificate; a bearer operator
// reached without one leaves the credential header to the person, who holds the token.
func exportBlock(prefix string, op store.Operator, endpoint string, withMTLS bool) []install.Value {
	values := []install.Value{
		install.Set(prefix+".endpoint", endpoint),
		install.Set(prefix+".protocol", "grpc"),
		install.Set(prefix+".tls.insecure", "false"),
		install.Set(prefix+".tls.caFile", ""),
	}
	serverName := ""
	if operatorNeedsServerName(op) {
		serverName = operatorServerName(op)
	}
	if withMTLS {
		values = append(values, install.Set(prefix+".tls.mtls.enabled", "true"), install.Set(prefix+".tls.mtls.secretName", operatorClientTLSSecretName(op)))
	} else {
		values = append(values, install.Set(prefix+".tls.mtls.enabled", "false"), install.Set(prefix+".tls.mtls.secretName", ""))
	}
	values = append(values, install.Set(prefix+".tls.serverName", serverName))
	if withMTLS {
		values = append(values, install.Set(prefix+".auth.secretName", ""))
	}
	return values
}

// operatorDestinationCommand builds the export.otlp.* values (and, if a fresh client cert was minted,
// the Secret command for it) that point one agent's telemetry export at this operator -
// the same shape operatorSourceReminders already builds per source cluster at creation/scope-update time,
// factored out so TelemetryIntent's own /command endpoint (admin_telemetry_intents.go) can call it for a
// single agent on demand, reusing a freshly reissued client cert rather than requiring one from creation time.
// The flags are the whole destination block (see exportBlock) whether or not a certificate is passed: an mTLS
// operator is always reached with a client certificate, and one that is not minted this time is the Secret already in
// the cluster.
func operatorDestinationCommand(op store.Operator, endpoint string, certPEM, keyPEM, caPEM []byte, namespace string) (values []install.Value, secretCmd string, err error) {
	withMTLS := len(certPEM) > 0 || op.ReceiverAuth == store.ReceiverAuthMTLS
	values = exportBlock("telemetry.export.otlp", op, endpoint, withMTLS)
	if len(certPEM) > 0 {
		// mTLS is additive: every source cluster of this operator presents a client certificate verified
		// against the CA bundle in the same Secret - see pki.IssueOperatorClientTLS.
		if secretCmd, err = operatorClientSecretCommand(op, certPEM, keyPEM, caPEM, namespace, false); err != nil {
			return nil, "", err
		}
	}
	return values, secretCmd, nil
}

// operatorClientSecretCommand is the Secret holding the client certificate an agent
// presents to this operator. One Secret per operator, whatever number of the agent's signal types export to
// it: every route to the same operator names this same Secret. ensureNamespace says the Secret is applied before the install that
// would create its namespace.
func operatorClientSecretCommand(op store.Operator, certPEM, keyPEM, caPEM []byte, namespace string, ensureNamespace bool) (string, error) {
	return install.Secret{Name: operatorClientTLSSecretName(op), Namespace: namespace, EnsureNamespace: ensureNamespace, Entries: []install.Entry{
		{Key: "tls.crt", Value: string(certPEM)}, {Key: "tls.key", Value: string(keyPEM)}, {Key: "ca.crt", Value: string(caPEM)}}}.Render()
}

// routeModalities are the signal types that can have a destination of their own (telemetry.export.routes.<type>).
var routeModalities = []store.Modality{store.ModalityMetrics, store.ModalityLogs, store.ModalityTraces}

// clearRoutes states that the given signal types have no destination of their own: an empty route endpoint is how
// the chart reads "no route", so they go to the default destination again.
func clearRoutes(ms ...store.Modality) []install.Value {
	values := make([]install.Value, len(ms))
	for i, m := range ms {
		values[i] = install.Set(fmt.Sprintf("telemetry.export.routes.%s.endpoint", m), "")
	}
	return values
}

// operatorRouteValues are the values that make one signal type's own route (telemetry.export.routes.<m>)
// export to this operator over mutual TLS: its receiver, gRPC, certificate verified, and the Secret from
// operatorClientSecretCommand. Stated in full (see exportBlock) so the route does not depend on anything an
// earlier command set.
func operatorRouteValues(op store.Operator, endpoint string, m store.Modality) []install.Value {
	return exportBlock(fmt.Sprintf("telemetry.export.routes.%s", m), op, endpoint, true)
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

// destEndpoint is the endpoint a sender dials to reach op as its destination d: the address that sender chose
// (d.Address) when it chose one, else op's own (operatorEndpoint). Only a destination that names op counts.
func (a *Admin) destEndpoint(op store.Operator, d store.Destination) string {
	if d.Kind == store.DestinationOperator && d.TargetOperatorID == op.ID && d.Address != "" {
		return d.Address
	}
	return a.operatorEndpoint(op)
}

// destWarnings are the doubts about sending to op as destination d. An address the sender chose is held to the
// same checks as a recorded one; without one, targetWarnings says what the operator's own address allows.
func (a *Admin) destWarnings(ctx context.Context, op store.Operator, d store.Destination) []string {
	if d.Kind == store.DestinationOperator && d.TargetOperatorID == op.ID && d.Address != "" {
		name := op.Name
		if op.ID == CentralOperatorID && name == "" {
			name = centralOperatorName
		}
		var out []string
		for _, w := range addressWarningsFor(d.Address, nil, false) {
			out = append(out, name+": "+w)
		}
		return out
	}
	return a.targetWarnings(ctx, op)
}

// targetWarnings are the doubts about pointing something at op, for the commands that do: an operator with no address
// recorded is dialled by its in-cluster name, which resolves only inside the cluster it runs in, and an address that
// is recorded is only what a person typed (see addressWarnings). Empty when there is nothing to say.
func (a *Admin) targetWarnings(ctx context.Context, op store.Operator) []string {
	name := op.Name
	if op.ID == CentralOperatorID && name == "" {
		name = centralOperatorName
	}
	op = a.advertised(op)
	if op.Address == "" {
		return []string{fmt.Sprintf("%s has no address recorded, so this command dials its in-cluster name (%s), which resolves only inside the cluster it runs in. If the cluster being pointed at it is another one, record where %s is reached from outside first.", name, a.operatorEndpoint(op), name)}
	}
	var svc *KubeService
	if op.ID == CentralOperatorID && a.Fusion != nil {
		svc = a.Fusion.gatewayService(ctx)
	}
	var out []string
	for _, w := range addressWarnings(op.Address, svc) {
		out = append(out, name+": "+w)
	}
	return out
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
	secretCmd, err := operatorClientSecretCommand(target, certPEM, keyPEM, caPEM, "continuum-system", true)
	if err != nil {
		a.installConfigError(err) // logged; the response is without the Secret
		return
	}
	resp["exportSecretCommand"] = secretCmd
	endpoint := a.destEndpoint(target, op.Destination)
	exportTarget := map[string]any{"operatorId": target.ID, "name": target.Name, "endpoint": endpoint, "reachableFromOtherClusters": target.Address != "" || endpoint != a.operatorEndpoint(target)}
	if w := a.destWarnings(r.Context(), target, op.Destination); len(w) > 0 {
		exportTarget["warnings"] = w
	}
	resp["exportTarget"] = exportTarget
}

// checkOperatorInstall says whether the install command of a regional operator can be written with the chart this server names, before
// anything for it is minted. Only the chart and the versions can stop it: the operator's own words are names this server made.
func (a *Admin) checkOperatorInstall(img ImageConfig) error {
	probe := store.Operator{ID: "op-check", Name: "check", Status: store.OperatorActive, ReceiverAuth: store.ReceiverAuthMTLS,
		Destination: store.Destination{Kind: store.DestinationExternal, Endpoint: "check:4317"}}
	_, _, err := a.operatorInstallCommandWith(img, "check", probe, OperatorTLSBundle{}, "", nil, true)
	return err
}

// operatorChart is where the regional-operator chart is, as agentChart is for the agent's: the chart reference and, for a repository
// (not a packaged file), the version to fetch.
func (a *Admin) operatorChart(img ImageConfig) (ref, version string) {
	ref = a.operatorChartRef(img)
	if ref == "" {
		return "./" + chart.RegionalOperator.Filename(), ""
	}
	if !strings.HasSuffix(ref, ".tgz") {
		version = a.operatorChartVersion()
	}
	return ref, version
}

// operatorInstallCommandWith is installCommand's own twin for the regional-operator chart: simpler, since this
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
// When the operator's destination is another operator, target is that operator's record: its advertised address (if any) is then what
// the exporter dials, and the certificate is verified against the stable name on it. With target nil only the id is known, which fixes
// the in-cluster name and nothing more.
//
// With reissue the command upgrades a release that already runs: `helm upgrade --install --reset-then-reuse-values` (Helm 3.14 or
// newer; see upgradeCommand for why not --reuse-values) keeps what the person tuned (resources, image), so every value this command is
// about is stated, the unused ones empty - an export block left out would keep an old destination's settings.
func (a *Admin) operatorInstallCommandWith(img ImageConfig, secret string, op store.Operator, tlsBundle OperatorTLSBundle, heartbeatURL string, target *store.Operator, reissue bool) (cmd, secretCmd string, err error) {
	var v []install.Value
	set := func(key, value string) { v = append(v, install.Set(key, value)) }
	if op.Destination.Kind == store.DestinationOperator {
		// Another regional operator (the central one in front of FUSION, usually): its receiver, over mutual TLS with
		// a client certificate from the target's own CA (the Secret addOperatorTargetExport hands out).
		tgt := store.Operator{ID: op.Destination.TargetOperatorID} // the id alone fixes the in-cluster endpoint and the Secret name
		if target != nil {
			tgt = *target
		}
		set("export.otlp.endpoint", a.destEndpoint(tgt, op.Destination))
		if reissue {
			set("export.otlp.protocol", "grpc")
			set("export.otlp.tls.insecure", "false")
			set("export.otlp.tls.caFile", "")
		}
		set("export.otlp.tls.mtls.enabled", "true")
		set("export.otlp.tls.mtls.secretName", operatorClientTLSSecretName(tgt))
		if operatorNeedsServerName(tgt) {
			set("export.otlp.tls.serverName", operatorServerName(tgt))
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
		v = append(v, a.operatorHeartbeatValues(op, heartbeatURL)...)
	}
	// Keeps this operator's certificates from running out: the chart's cert-renew sidecar (the discovery agent's image, a
	// different role) asks this server for a new one before the old one ends. It only needs to know where the server is
	// and which CA to trust; there is no token or key to hand out. Only when the operator holds a certificate at all.
	if a.AgentAddr != "" && (mtlsOnly || len(tlsBundle.ReceiverCertPEM) > 0 || op.Destination.Kind == store.DestinationOperator) {
		set("renew.enabled", "true")
		set("renew.server", a.AgentAddr)
		set("renew.caPin", a.C.CA.SPKIPin())
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
	if svcType, _ := serviceTypeFor(op.Exposure); svcType != "" {
		set("service.type", svcType)
	}
	ref, version := a.operatorChart(img)
	h := install.Helm{Verb: install.Install, Release: op.ID, Chart: ref, Version: version, Namespace: "continuum-system", CreateNamespace: true, Values: v, Layout: install.Wrapped}
	if reissue {
		h.Verb, h.ResetThenReuse = install.UpgradeInstall, true
	}
	if cmd, err = h.Render(); err != nil {
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

// operatorSourceReminders is informational only, not applied: for each source cluster, the exact `helm
// upgrade` a person would run against that cluster's own continuum-agent release to actually point its
// telemetry.export.otlp.endpoint at this operator - there is no live reparenting in this release (see the
// plan), so nothing here is executed on the caller's behalf. Uses the same release-name/namespace guess
// upgradeCommand/teardownCommands already fall back to when an agent has never reported its own.
//
// Each upgrade states the whole destination block (see exportBlock), and the client certificate Secret goes once per
// distinct namespace rather than once per cluster, so the private key is not repeated for every cluster that
// shares a namespace name; it is applied in each of the clusters listed above it. A Secret is only listed when a
// certificate was just minted (tlsBundle): without one the clusters keep the Secret they have.
func (a *Admin) operatorSourceReminders(r *http.Request, op store.Operator, tlsBundle OperatorTLSBundle) []string {
	hub := a.tn(r).Hub
	img := a.images(a.core(r))
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
	targets := make([]sourceTarget, 0, len(op.SourceClusterIDs))
	for _, cl := range op.SourceClusterIDs {
		ag, ok := byCluster[cl]
		ns, name := "", ""
		if ok {
			ns, name = hub.NamespaceOf(ag.ID), hub.ReleaseNameOf(ag.ID)
		}
		rns, rname, _ := releaseTarget(ns, name)
		targets = append(targets, sourceTarget{cl, rns, rname})
	}
	out, err := a.sourceReminders(img, op, tlsBundle, targets)
	if err != nil {
		a.installConfigError(err) // logged; the operator is shown without the commands for its sources
		return nil
	}
	return out
}

// sourceTarget is one source cluster of an operator and the release that is told to send to it: where it runs and what it is
// called, as releaseTarget settles them.
type sourceTarget struct{ cluster, ns, name string }

// sourceReminders is operatorSourceReminders once the clusters are known: it reads nothing, so the text it builds is pinned
// by a test (install_commands_test.go) like the other commands.
func (a *Admin) sourceReminders(img ImageConfig, op store.Operator, tlsBundle OperatorTLSBundle, targets []sourceTarget) ([]string, error) {
	ref, version := a.agentChart(img)
	out := make([]string, 0, 2*len(targets))
	// Each cluster holds its own client certificate (see OperatorTLSBundle.Senders), so each gets its own Secret
	// command, in its own namespace. Only wired in when that certificate was minted (see CreateOperator on why
	// that can fail without failing the operator): this is additive, the bearer token alone still works without it.
	for _, t := range targets {
		sc, ok := tlsBundle.forSender(t.cluster)
		if !ok {
			continue
		}
		_, secretCmd, err := operatorDestinationCommand(op, a.operatorEndpoint(op), sc.CertPEM, sc.KeyPEM, tlsBundle.CACertPEM, t.ns)
		if err != nil {
			return nil, err
		}
		if secretCmd != "" {
			out = append(out, fmt.Sprintf("# in cluster %s (namespace %s): create its client certificate Secret first\n%s", t.cluster, t.ns, secretCmd))
		}
	}
	for _, t := range targets {
		sc, _ := tlsBundle.forSender(t.cluster)
		values, _, err := operatorDestinationCommand(op, a.operatorEndpoint(op), sc.CertPEM, sc.KeyPEM, tlsBundle.CACertPEM, t.ns)
		if err != nil {
			return nil, err
		}
		// Everything goes to this operator: no per-signal route may stay (a route outranks the default, and one an
		// earlier command set is kept by --reset-then-reuse-values).
		values = append(values, clearRoutes(routeModalities...)...)
		upgrade, err := install.Helm{Verb: install.Upgrade, Release: t.name, Chart: ref, Version: version, Namespace: t.ns, ResetThenReuse: true, Values: values}.Render()
		if err != nil {
			return nil, err
		}
		out = append(out, upgrade+fmt.Sprintf("  # cluster %s", t.cluster))
	}
	return out, nil
}
