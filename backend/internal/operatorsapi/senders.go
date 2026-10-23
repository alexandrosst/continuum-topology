package operatorsapi

import (
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"ikhnos/internal/agents"
	"ikhnos/internal/connectivity"
	"ikhnos/internal/httpkit"
	"ikhnos/internal/operators"
	"ikhnos/internal/store"
	"ikhnos/internal/telemetry"
)

// The clusters that send to a regional operator, how each reaches it, and, where one is not getting through, which end
// the fault is on.
//
// Everything here is read from what the server already holds: the telemetry intents that name the operator (and, per
// signal type, the address each chose), and what each sending agent reports about its own exporters. Nothing is dialled
// from here. The diagnosis is a reading of that evidence, in words, and says so when the evidence does not settle it.

// SenderSource is who chose the address a sender dials.
type SenderSource string

// Who chose an address.
const (
	// SourceChosen: this sender picked it.
	SourceChosen SenderSource = "chosen"
	// SourceRecorded: the one recorded for the operator.
	SourceRecorded SenderSource = "recorded"
	// SourceInCluster: nothing is recorded, so the sender dials the operator's name inside its own cluster.
	SourceInCluster SenderSource = "in-cluster"
)

// WireWords is the list of SenderSource's words, as the generated types give it.
func (SenderSource) WireWords() []string {
	return []string{string(SourceChosen), string(SourceRecorded), string(SourceInCluster)}
}

// FaultAt is where the fault of a sender that is not getting through is likely to be.
type FaultAt string

// The places a fault can be, and unclear when the evidence does not settle it.
const (
	FaultSenderNetwork FaultAt = "sender-network"
	FaultAddress       FaultAt = "address"
	FaultCertificate   FaultAt = "certificate"
	FaultCredential    FaultAt = "credential"
	FaultOperator      FaultAt = "operator"
	FaultSender        FaultAt = "sender"
	FaultUnclear       FaultAt = "unclear"
)

// WireWords is the list of FaultAt's words, as the generated types give it.
func (FaultAt) WireWords() []string {
	return []string{string(FaultSenderNetwork), string(FaultAddress), string(FaultCertificate), string(FaultCredential), string(FaultOperator), string(FaultSender), string(FaultUnclear)}
}

// SenderAddressDoc is one address a sender dials for some of its signal types.
type SenderAddressDoc struct {
	Address string `json:"address"`
	// Source is chosen (this sender picked it), recorded (the one recorded for the operator) or in-cluster (nothing is
	// recorded, so the sender dials the operator's name inside its own cluster).
	Source  SenderSource `json:"source"`
	Signals []string     `json:"signals"`
	// Reach is the last connection test of the address from the sending cluster, which the server repeats on its own
	// (see address_drift.go). Absent when there is no recent one.
	Reach *connectivity.AddressReachDoc `json:"reach,omitempty"`
}

// SenderDeliveryDoc is what the sending agent reports about exporting to this operator.
type SenderDeliveryDoc struct {
	// State is the worst of the sender's exporters for the signal types that go here: failing, silent, exporting or waiting.
	State        agents.ExportState `json:"state"`
	Sent         uint64             `json:"sent"`
	Failed       uint64             `json:"failed"`
	LastError    string             `json:"lastError,omitempty"`
	LastSentAt   string             `json:"lastSentAt,omitempty"`
	LastFailedAt string             `json:"lastFailedAt,omitempty"`
}

// SenderDiagnosisDoc says where the fault is likely to be. Where is one of sender-network, address, certificate,
// credential, operator, sender or unclear; Title is one line and Detail says what the evidence is and what to do.
type SenderDiagnosisDoc struct {
	Where  FaultAt `json:"where"`
	Title  string  `json:"title"`
	Detail string  `json:"detail"`
}

// SenderDoc is one telemetry intent that sends to the operator.
type SenderDoc struct {
	IntentID   string              `json:"intentId"`
	IntentName string              `json:"intentName"`
	AgentID    string              `json:"agentId"`
	AgentName  string              `json:"agentName"`
	ClusterID  string              `json:"clusterId,omitempty"`
	Connected  bool                `json:"connected"`
	Addresses  []SenderAddressDoc  `json:"addresses"`
	Delivery   *SenderDeliveryDoc  `json:"delivery,omitempty"`
	Diagnosis  *SenderDiagnosisDoc `json:"diagnosis,omitempty"`
}

// SendersDoc is the answer for one operator.
type SendersDoc struct {
	// OperatorState is the operator's own health as it reports it (online, offline, unknown...).
	OperatorState operators.HealthState `json:"operatorState"`
	Senders       []SenderDoc           `json:"senders"`
}

var modalityOrder = []store.Modality{store.ModalityMetrics, store.ModalityLogs, store.ModalityTraces}

// intentModalities are the signal types an intent has a signal on.
func intentModalities(ti store.TelemetryIntent) map[store.Modality]bool {
	out := map[store.Modality]bool{}
	for _, s := range ti.Signals {
		if m, ok := telemetry.SignalModality[s.ID]; ok {
			out[m] = true
		}
	}
	return out
}

// senderAddresses groups, by address, the signal types of the intent that go to op.
func (h *API) senderAddresses(op store.Operator, ti store.TelemetryIntent) ([]SenderAddressDoc, []store.Modality) {
	on := intentModalities(ti)
	byAddr := map[string]*SenderAddressDoc{}
	var order []string
	var mods []store.Modality
	for _, m := range modalityOrder {
		if !on[m] {
			continue
		}
		d := ti.Destination
		if r, ok := ti.Routes[m]; ok {
			d = r
		}
		if d.Kind != store.DestinationOperator || d.TargetOperatorID != op.ID {
			continue
		}
		mods = append(mods, m)
		addr := h.DestEndpoint(op, d)
		src := SourceInCluster
		switch {
		case d.Address != "":
			src = SourceChosen
		case op.Address != "":
			src = SourceRecorded
		}
		e := byAddr[addr]
		if e == nil {
			e = &SenderAddressDoc{Address: addr, Source: src, Signals: []string{}}
			byAddr[addr] = e
			order = append(order, addr)
		}
		e.Signals = append(e.Signals, string(m))
	}
	out := make([]SenderAddressDoc, 0, len(order))
	for _, k := range order {
		out = append(out, *byAddr[k])
	}
	return out, mods
}

var exportRank = map[agents.ExportState]int{agents.ExportWaiting: 0, agents.ExportExporting: 1, agents.ExportSilent: 2, agents.ExportFailing: 3}

// senderDelivery reads the worst state of the agent's exporters for the given signal types. Nil when the agent reports no counters.
func senderDelivery(h *agents.ExportHealthDoc, mods []store.Modality) *SenderDeliveryDoc {
	if h == nil || (h.PodsReached == 0 && h.PodsFailed > 0) {
		return nil // nothing reported, or the counters could not be read: that says nothing about the data
	}
	want := map[string]bool{}
	for _, m := range mods {
		want[string(m)] = true
	}
	d := &SenderDeliveryDoc{State: agents.ExportWaiting}
	for _, r := range h.Routes {
		if !want[r.Signal] {
			continue
		}
		d.Sent += r.Sent
		d.Failed += r.Failed
		if exportRank[r.State] > exportRank[d.State] {
			d.State = r.State
		}
		if r.LastSentAt > d.LastSentAt {
			d.LastSentAt = r.LastSentAt
		}
		if r.LastFailedAt > d.LastFailedAt {
			d.LastFailedAt = r.LastFailedAt
		}
	}
	if d.State == agents.ExportFailing {
		d.LastError = h.LastError
	}
	return d
}

// peerOutcome is how another sender fares against the same operator.
type peerOutcome struct {
	Name, Address string
	State         agents.ExportState
}

type diagnosisInput struct {
	ClusterName  string
	Connected    bool
	Delivery     *SenderDeliveryDoc
	Address      string
	AddrSource   SenderSource
	OperatorID   string
	OperatorName string
	// Central says the operator is the central one, whose health is FUSION's own (online, starting, attention, off) and
	// which has no certificates of its own to renew from a row.
	Central bool
	// OperatorState is the operator's own health; OperatorSeen is when it last reported, when it ever has.
	OperatorState operators.HealthState
	OperatorSeen  string
	Peers         []peerOutcome
	// Reach is the last test of Address from the sending cluster, when there is a recent one.
	Reach *connectivity.AddressReachDoc
}

func hostOf(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

// diagnoseSender reads the evidence about one sender. It returns nil when there is nothing wrong to explain.
func diagnoseSender(in diagnosisInput) *SenderDiagnosisDoc {
	who := in.ClusterName
	if who == "" {
		who = "This cluster"
	}
	if !in.Connected {
		return &SenderDiagnosisDoc{Where: FaultSender, Title: who + "'s agent is not connected",
			Detail: "Ikhnos cannot see how exporting is going until the agent reconnects, so nothing here says whether the operator or the address is at fault."}
	}
	// The address was tested from this cluster and nothing answered, and data is not flowing: the address is what to look at,
	// and it can be said before the exporter has failed at all.
	if in.Reach != nil && in.Reach.State == connectivity.Unreachable && (in.Delivery == nil || in.Delivery.State != agents.ExportExporting) {
		return &SenderDiagnosisDoc{Where: FaultSenderNetwork, Title: who + " cannot reach " + in.Address,
			Detail: "A connection test from " + who + " at " + in.Reach.At + " found nothing answering at " + in.Address + ". An address can change under a running install (a node replaced, a Service recreated with a new node port, a mesh peer renumbered): choose another in the telemetry wizard under Address this cluster dials, and use Test to try it before installing."}
	}
	if in.Delivery == nil || in.Delivery.State != agents.ExportFailing {
		return nil
	}
	errText := strings.ToLower(in.Delivery.LastError)
	has := func(ss ...string) bool {
		for _, s := range ss {
			if strings.Contains(errText, s) {
				return true
			}
		}
		return false
	}
	// What the other senders show. One that is exporting proves the operator is up and its receiver is answering.
	var working []peerOutcome
	for _, p := range in.Peers {
		if p.State == agents.ExportExporting {
			working = append(working, p)
		}
	}
	operatorUp := len(working) > 0 || in.OperatorState == operators.HealthOnline
	if in.Central && len(working) == 0 {
		// FUSION says itself whether its gateway can take anything: nothing a path could do is the first thing to look at.
		switch in.OperatorState {
		case operators.HealthOff:
			return &SenderDiagnosisDoc{Where: FaultOperator, Title: "FUSION is off",
				Detail: "The gateway that receives what this cluster sends is not running, so nothing is accepted anywhere. Turn FUSION on from its section; collectors keep what they cannot deliver and send it once it is up."}
		case operators.HealthStarting:
			return &SenderDiagnosisDoc{Where: FaultOperator, Title: "FUSION is still starting",
				Detail: "The gateway is not ready yet. This takes a minute or two after FUSION is turned on; collectors keep what they cannot deliver and send it once it is up."}
		case operators.HealthAttention:
			return &SenderDiagnosisDoc{Where: FaultOperator, Title: "FUSION reports a problem",
				Detail: "One of its parts is not working, and the gateway may not be taking anything. Open FUSION: its overview says which part and what to do."}
		}
	}
	seen := ""
	if in.OperatorSeen != "" {
		seen = " (last heard from " + in.OperatorSeen + ")"
	}
	quote := ""
	if in.Delivery.LastError != "" {
		quote = " The agent reports: " + in.Delivery.LastError
	}
	peerLine := ""
	if len(working) > 0 {
		p := working[0]
		peerLine = fmt.Sprintf("%s is exporting to this operator at %s, so the operator is up and answering.", p.Name, p.Address)
		if p.Address == in.Address {
			peerLine = fmt.Sprintf("%s is exporting to this operator at the same address, so the operator is up and answering there.", p.Name)
		}
	}

	switch {
	case has("x509", "certificate", "tls:", "handshake", "unknown authority"):
		d := "The connection was made, but the certificate was refused. The sender checks the operator by its name " + in.OperatorID + ".ikhnos-system.svc whatever address it dials, so the address itself is not the problem unless it leads to a different service."
		renew := " (Renew certificates on the operator's row)"
		if in.Central {
			renew = ""
		}
		switch {
		case len(working) > 0:
			d = peerLine + " That points at this cluster's own client certificate (expired, or from another operator): run its generated command again to install a fresh one."
		case in.OperatorState == operators.HealthOffline:
			d += " The operator has also stopped reporting" + seen + ": check that its receiver certificate has not expired" + renew + "."
		default:
			d += " Common causes: the operator's or this cluster's certificate has expired" + renew + ", this cluster's certificate Secret is missing or belongs to another operator."
		}
		return &SenderDiagnosisDoc{Where: FaultCertificate, Title: "The certificate was refused", Detail: d + quote}
	case has("unauthenticated", "unauthorized", "401", "403", "permission denied", "forbidden"):
		return &SenderDiagnosisDoc{Where: FaultCredential, Title: "The operator rejected this cluster's credentials",
			Detail: "The connection works, but what this cluster presented was not accepted: a receiver token that is wrong or was rotated, or a client certificate the operator does not trust. Run the cluster's generated command again; for an operator with a receiver token, check the Secret that holds it." + quote}
	}

	if in.OperatorState == operators.HealthOffline && len(working) == 0 {
		return &SenderDiagnosisDoc{Where: FaultOperator, Title: "The operator itself is not reporting",
			Detail: "No other cluster is getting through, and the operator has stopped reporting its own health" + seen + ". Look at the operator first: `kubectl -n ikhnos-system get pods` in its cluster. " + who + "'s path is not shown to be at fault." + quote}
	}

	private := false
	if ip := net.ParseIP(hostOf(in.Address)); ip != nil && ip.IsPrivate() {
		private = true
	}
	switch {
	case has("connection refused"):
		return &SenderDiagnosisDoc{Where: FaultAddress, Title: "Something answered, but nothing listens on that port",
			Detail: "The host at " + in.Address + " is reachable from " + who + ", and refused the connection. Check that the operator's Service still uses that port: a node port or load balancer port changes when the Service is recreated. Choose the address again in the telemetry wizard." + quote}
	case has("no such host", "lookup "):
		return &SenderDiagnosisDoc{Where: FaultAddress, Title: "The name does not resolve from this cluster",
			Detail: "The address " + in.Address + " is a name, and " + who + " cannot look it up. Use an IP address, or a name its DNS knows." + quote}
	}

	// A timeout, no route, an unreachable network, or an error that says nothing: the path.
	if operatorUp {
		d := peerLine
		if d == "" {
			d = "The operator reports itself online."
		}
		d += fmt.Sprintf(" The path from %s to %s does not get through.", who, in.Address)
		if private && in.AddrSource != SourceChosen {
			d += " That is a private address, and a private address works only from a network that can route to it."
		}
		if len(working) > 0 && working[0].Address != in.Address {
			d += " " + working[0].Name + " reaches it at " + working[0].Address + ": try that one from " + who + "."
		}
		if in.Reach != nil && in.Reach.State == connectivity.Reachable {
			d += " A connection test from " + who + " at " + in.Reach.At + " did connect to it, so the failure may be passing: check again shortly."
		}
		d += " Pick another address under Address this cluster dials in the telemetry wizard and use Test to try it from " + who + " before installing."
		return &SenderDiagnosisDoc{Where: FaultSenderNetwork, Title: who + " cannot reach " + in.Address, Detail: d + quote}
	}
	return &SenderDiagnosisDoc{Where: FaultUnclear, Title: "Cannot tell which end is at fault",
		Detail: "No other cluster is exporting to this operator and it is not reporting its health, so there is nothing to compare with. Use Test on " + in.Address + " from " + who + ": if that fails, the path or the address is the problem; if it works, look at the operator's pods." + quote}
}

// listOperatorSenders answers GET .../operators/{id}/senders.
func (h *API) listOperatorSenders(w http.ResponseWriter, r *http.Request) {
	c := h.For(r)
	op, err := c.GetOperator(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	intents, err := h.Intents(r).ListTelemetryIntents(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	agentList, err := c.Store.ListAgents(r.Context(), c.OrgID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	agentOf := make(map[string]store.Agent, len(agentList))
	for _, ag := range agentList {
		agentOf[ag.ID] = ag
	}
	hub := h.Hub(r)
	now := c.Now()
	health := h.sendersHealth(r, op, now)
	seen := ""
	if health.LastSeenAt != nil {
		seen = health.LastSeenAt.UTC().Format(time.RFC3339)
	}

	out := SendersDoc{OperatorState: health.State, Senders: []SenderDoc{}}
	type pending struct {
		doc  SenderDoc
		mods []store.Modality
	}
	var ps []pending
	for _, ti := range intents {
		if ti.Status != store.TelemetryIntentActive {
			continue
		}
		addrs, mods := h.senderAddresses(op, ti)
		if len(addrs) == 0 {
			continue
		}
		for i := range addrs {
			addrs[i].Reach = hub.ReachOf(ti.AgentID, addrs[i].Address, now)
		}
		ag := agentOf[ti.AgentID]
		d := SenderDoc{IntentID: ti.ID, IntentName: ti.Name, AgentID: ti.AgentID, AgentName: ag.Name, ClusterID: ag.ClusterID, Addresses: addrs}
		var export *agents.ExportHealthDoc
		d.Connected, export = hub.ExportHealthOf(ti.AgentID)
		d.Delivery = senderDelivery(export, mods)
		ps = append(ps, pending{d, mods})
	}
	// The diagnosis of each reads how the others fare, so it comes once every sender has its delivery.
	for i := range ps {
		d := &ps[i].doc
		in := diagnosisInput{ClusterName: d.AgentName, Connected: d.Connected, Delivery: d.Delivery, Address: d.Addresses[0].Address, AddrSource: d.Addresses[0].Source,
			OperatorID: op.ID, OperatorName: op.Name, OperatorState: health.State, OperatorSeen: seen, Central: op.ID == operators.CentralOperatorID, Reach: d.Addresses[0].Reach}
		for j := range ps {
			if j != i && ps[j].doc.Delivery != nil {
				in.Peers = append(in.Peers, peerOutcome{Name: ps[j].doc.AgentName, Address: ps[j].doc.Addresses[0].Address, State: ps[j].doc.Delivery.State})
			}
		}
		d.Diagnosis = diagnoseSender(in)
	}
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].doc.AgentName < ps[j].doc.AgentName })
	for _, p := range ps {
		out.Senders = append(out.Senders, p.doc)
	}
	httpkit.WriteJSON(w, 200, out)
}

// sendersHealth is the health a diagnosis reads: a regional operator's heartbeat, and for the central operator, which has
// none, FUSION's own state (the same as its row shows).
func (h *API) sendersHealth(r *http.Request, op store.Operator, now time.Time) operators.OperatorHealth {
	if op.ID != operators.CentralOperatorID {
		return operators.OperatorHealthAt(op, now)
	}
	managed := h.ManagedState(r)
	health := operators.CentralHealth(managed)
	health.LastSeenAt = managed.LastDataAt
	return health
}
