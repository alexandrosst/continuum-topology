package operatorsapi

import (
	"net/http"
	"time"

	"continuum/internal/kernel"
	"continuum/internal/operators"
	"continuum/internal/store"
)

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

func toHealthDoc(h operators.OperatorHealth) operatorHealthDoc {
	return operatorHealthDoc{State: h.State, LastSeenAt: kernel.RFCP(h.LastSeenAt), Reporting: h.Reporting, HeartbeatEnabledAt: kernel.RFCP(h.HeartbeatEnabledAt)}
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

func toUsedByDoc(u operators.OperatorUsage) *usedByDoc {
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

func AddressState(op store.Operator) string {
	switch {
	case op.Address != "":
		return addressSet
	case op.Exposure == "loadbalancer" || op.Exposure == "nodeport":
		return addressPending
	}
	return addressNone
}

type Doc struct {
	ID                 string                   `json:"id"`
	Name               string                   `json:"name"`
	SiteID             string                   `json:"siteId,omitempty"`
	Status             string                   `json:"status"`
	SourceClusterIDs   []string                 `json:"sourceClusterIds"`
	Destination        operators.DestinationDoc `json:"destination"`
	AcceptedModalities []string                 `json:"acceptedModalities,omitempty"`
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

func ToDoc(op store.Operator, now time.Time) Doc {
	d := Doc{
		ID: op.ID, Name: op.Name, SiteID: op.SiteID, Status: string(op.Status),
		SourceClusterIDs: op.SourceClusterIDs, Destination: operators.ToDestinationDoc(op.Destination),
		CreatedAt: kernel.RFC(op.CreatedAt), CreatedBy: op.CreatedBy, Reason: op.Reason, ReceiverAuth: string(op.ReceiverAuth), ClientCaScope: op.ClientCAScope(),
		Health: toHealthDoc(operators.OperatorHealthAt(op, now)),
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
		d.RevokedAt = kernel.RFC(*op.RevokedAt)
	}
	if op.Status == store.OperatorActive {
		if c := operators.OperatorCertDates(op); c.Known() {
			d.Certs = &certsDoc{ReceiverNotAfter: kernel.RFCP(c.Receiver), ClientNotAfter: kernel.RFCP(c.Client), CANotAfter: kernel.RFCP(c.CA)}
			var since *time.Time
			d.CertState, since = c.CertState(now)
			d.CertStateSince = kernel.RFCP(since)
		}
	}
	d.Address, d.ReachableFromOtherClusters, d.Exposure, d.AddressState = op.Address, op.Address != "", op.Exposure, AddressState(op)
	return d
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

// modalitiesFromDoc converts a request's plain-string modality list to store.Modality - validated later
// by Core (validModalities), the same deferral operators.DestinationDoc.ToStore's own untyped Kind follows.
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

// opDoc is ToDoc plus what only the server's own wiring knows: the central operator is reachable from
// other clusters exactly when FUSION says it is exposed, at the address FUSION gives, and its health is FUSION's.
// What depends on the operator is worked out here, so a caller with many operators passes the usages it already
// computed (opDocWith) instead of scanning once per operator.
func (h *API) opDoc(r *http.Request, op store.Operator) Doc {
	usages, _ := h.For(r).OperatorUsages(r.Context()) // best effort: without it the document simply has no usedBy
	return h.opDocWith(r, op, usages, h.ManagedState(r))
}

// ManagedState is the state of whoever runs the central operator, for its documents of the central operator; read once per request by the callers
// that build several. FUSION is managed from the server's main organisation only, so for any other it is not
// available (there is no central operator to speak of).
func (h *API) ManagedState(r *http.Request) operators.ManagedStatus {
	c := h.central()
	if c == nil {
		return operators.ManagedStatus{State: "off"}
	}
	if org := c.Org(); org != "" && h.For(r).OrgID != org {
		return operators.ManagedStatus{State: "off"}
	}
	return c.Managed(r.Context())
}

func (h *API) opDocWith(r *http.Request, op store.Operator, usages map[string]operators.OperatorUsage, managed operators.ManagedStatus) Doc {
	d := ToDoc(op, h.For(r).Now())
	d.Endpoint = h.Endpoint(op)
	d.UsedBy = toUsedByDoc(usages[op.ID])
	d.AddressWarnings = AddressWarnings(op.Address, nil)
	if op.ID == operators.CentralOperatorID {
		health := operators.CentralHealth(managed)
		health.LastSeenAt = managed.LastDataAt
		d.Health = toHealthDoc(health)
		if c := h.central(); c != nil {
			if d.ReachableFromOtherClusters = c.Exposed(); d.ReachableFromOtherClusters {
				d.Address = c.Endpoint()
				d.AddressState = addressSet
				d.AddressWarnings = CentralAddressWarnings(r.Context(), c) // checked against the gateway's own Service
			} else {
				d.AddressState = addressNone
			}
		}
	}
	return d
}
