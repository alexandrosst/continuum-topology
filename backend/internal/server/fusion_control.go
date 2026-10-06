package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"continuum/internal/fusionapi"
	"continuum/internal/store"
)

// FUSION as part of the server. The server's own Helm chart carries FUSION - the three stores and a central gateway -
// standing by at zero replicas. This file is the switch: it scales those workloads between 0 and 1, and before the
// first start gives the gateway the certificates it needs. It does no rendering and installs nothing, so the
// permission it needs is the small Role in deploy/helm/continuum-server/templates/fusion-rbac.yaml (see KubeAPI).
//
// The gateway is also a regional operator in this server's own list (CentralOperatorID): the one door into FUSION.
// Other regional operators send to it over mTLS, with a client certificate this server issues from the gateway's own
// CA, and it sends on to the three stores. The stores are never exposed; only the gateway may be.

// CentralOperatorID is the id of the server-owned regional operator that fronts FUSION. One FUSION per server, so one
// fixed id; only the server's primary organisation may turn FUSION on (see FusionControl.Org).
const CentralOperatorID = "op-central"

// fusionStore is one of the four workloads the switch drives.
type fusionStore struct {
	Component string // metrics | logs | traces | central
	Label     string // what the UI calls it
	Kind      string // statefulsets | deployments
	Name      string
}

// FusionControl switches the bundled FUSION on and off. Name is the chart's own object-name prefix (what the chart
// computes as "fusion.name"), passed as --fusion-name so the server and the chart cannot disagree.
type FusionControl struct {
	Name          string
	Namespace     string
	PublicAddress string // host:port other clusters dial to reach the gateway ("" = reachable inside this cluster only)
	Kube          KubeAPI
	// Data reads what FUSION saved (the shared API, fusion_data.go). Nil means the stores' in-cluster addresses by the
	// chart's naming convention (see dataClient); tests point it at fakes.
	Data *fusionapi.Client
	// Org is the one organisation that may turn FUSION on: everything saved in it is shared by whoever sends to the
	// gateway, so it belongs to a single organisation.
	Org string
	Now func() time.Time

	addrMu sync.RWMutex // guards PublicAddress, which the admin can change while the server runs (SetPublicAddress)

	mu    sync.Mutex
	data  *fusionapi.Client
	since time.Time // when the stores were last asked to start; zero when off or unknown
	// notReady is when the stores were first seen not all ready, zero while they are (or FUSION is off). The grace
	// period counts from here, not from `since`: a pod that restarts a month into a healthy run is "starting"
	// again, not "attention after 43,200 minutes".
	notReady time.Time
}

func (f *FusionControl) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

func (f *FusionControl) stores() []fusionStore {
	return []fusionStore{
		{"metrics", "Prometheus", "statefulsets", f.Name + "-prometheus"},
		{"logs", "Loki", "statefulsets", f.Name + "-loki"},
		{"traces", "Tempo", "statefulsets", f.Name + "-tempo"},
		{"central", "Central operator", "deployments", f.Name + "-central"},
	}
}

func (f *FusionControl) centralHost() string { return f.Name + "-central." + f.Namespace + ".svc" }

// CentralEndpoint is what an exporting regional operator is pointed at: the public address when there is one, else
// the gateway's in-cluster Service (OTLP gRPC).
func (f *FusionControl) CentralEndpoint() string {
	if a := f.publicAddress(); a != "" {
		return a
	}
	return f.centralHost() + ":4317"
}

// Exposed is whether operators in other clusters can reach the gateway.
func (f *FusionControl) Exposed() bool { return f.publicAddress() != "" }

func (f *FusionControl) publicAddress() string {
	f.addrMu.RLock()
	defer f.addrMu.RUnlock()
	return f.PublicAddress
}

// SetPublicAddress records where other clusters reach the gateway ("" = this cluster only). It is what the admin
// types under Reachable at on the central operator; the server's startup flag (--fusion-central-address) is only the
// first value. Nothing is reissued: senders verify the gateway by its stable name (operatorServerName).
func (f *FusionControl) SetPublicAddress(addr string) {
	f.addrMu.Lock()
	f.PublicAddress = addr
	f.addrMu.Unlock()
}

// ServiceName is the gateway's Service (what `kubectl get svc` names to read its address).
func (f *FusionControl) ServiceName() string { return f.Name + "-central" }

// certHosts are the names the gateway's server certificate must carry: its Service in every spelling, the stable name
// senders verify it by, and the public address's host.
func (f *FusionControl) certHosts() []string {
	// The last name is the one that never changes (operatorServerName): senders that reach the gateway at its public
	// address verify against it, so the address, or the IP behind it, can change without reissuing anything.
	h := []string{f.Name + "-central", f.Name + "-central." + f.Namespace, f.centralHost(), f.centralHost() + ".cluster.local",
		operatorServerName(store.Operator{ID: CentralOperatorID})}
	if pa := f.publicAddress(); pa != "" {
		host := pa
		if i := strings.LastIndex(host, ":"); i >= 0 && !strings.HasSuffix(host, "]") {
			host = host[:i]
		}
		h = append(h, strings.Trim(host, "[]"))
	}
	return h
}

// dataClient is how the shared API reaches the three stores: their ClusterIP Services, plain HTTP, on the ports the
// chart gives them (see fusionRoutes, which uses the same convention for the OTLP side).
func (f *FusionControl) dataClient() *fusionapi.Client {
	if f.Data != nil {
		return f.Data
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.data == nil {
		host := func(suffix string, port int) string {
			return fmt.Sprintf("http://%s-%s.%s.svc:%d", f.Name, suffix, f.Namespace, port)
		}
		f.data = &fusionapi.Client{Prometheus: host("prometheus", 9090), Loki: host("loki", 3100), Tempo: host("tempo", 3200)}
	}
	return f.data
}

func (f *FusionControl) tlsSecretName() string { return f.Name + "-central-receiver-tls" }

// destination is the central operator's own destination: the three stores, by the chart's naming convention.
func (f *FusionControl) destination() store.Destination {
	return store.Destination{Kind: store.DestinationFusion, FusionRelease: f.Name, FusionNamespace: f.Namespace}
}

// FusionComponent is one workload's state, as the UI shows it.
type FusionComponent struct {
	Component string `json:"component"`
	Label     string `json:"label"`
	Desired   int    `json:"desired"`
	Ready     int    `json:"ready"`
}

// FusionStatus is the switch's whole state.
type FusionStatus struct {
	Available  bool              `json:"available"`
	Reason     string            `json:"reason,omitempty"`
	State      string            `json:"state"` // off | starting | running | attention
	Message    string            `json:"message,omitempty"`
	Since      *time.Time        `json:"since,omitempty"`
	Components []FusionComponent `json:"components,omitempty"`
}

// How long the stores may take to start before the switch says something is wrong: pulling three images and
// binding volumes is slow on a fresh node, but not this slow.
const fusionStartGrace = 5 * time.Minute

// Status reads the four workloads. Anything it cannot read - no permission, the workloads are not there (an older
// server chart) - is reported as unavailable with the reason, never as an error: the screen shows it as a fact.
func (f *FusionControl) Status(ctx context.Context) FusionStatus {
	if f == nil || f.Kube == nil {
		return FusionStatus{State: "off", Reason: "not-configured", Message: "This server cannot switch FUSION: it was installed without it, or without the permission to scale it (fusion.enabled and fusionControl.enabled in the server chart)."}
	}
	st := FusionStatus{Available: true}
	var desired, ready, total int
	for _, s := range f.stores() {
		w, err := f.Kube.Workload(ctx, s.Kind, s.Name)
		switch {
		case errors.Is(err, ErrKubeNotFound):
			return FusionStatus{State: "off", Reason: "not-installed", Message: "FUSION's workloads are not in this release. Upgrade the server chart (fusion.enabled=true) to get them."}
		case errors.Is(err, ErrKubeForbidden):
			return FusionStatus{State: "off", Reason: "no-access", Message: "This server is not allowed to read or scale FUSION's workloads. Check fusionControl.enabled in the server chart."}
		case err != nil:
			st.State, st.Message = "attention", "The Kubernetes API did not answer: "+err.Error()
			return st
		}
		st.Components = append(st.Components, FusionComponent{s.Component, s.Label, w.Desired, w.Ready})
		desired += w.Desired
		ready += w.Ready
		total++
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case desired == 0:
		st.State = "off"
		f.since, f.notReady = time.Time{}, time.Time{}
	case ready >= desired:
		st.State = "running"
		f.notReady = time.Time{}
	default:
		st.State = "starting"
		if f.since.IsZero() { // the server restarted while FUSION was starting: count from now
			f.since = f.now()
		}
		if f.notReady.IsZero() { // first seen not ready (a fresh start, a restart of the server, or a pod that fell over)
			f.notReady = f.now()
		}
		if f.now().Sub(f.notReady) > fusionStartGrace {
			st.State, st.Message = "attention", fmt.Sprintf("%d of %d parts are up after %d minutes. Check the pods (kubectl -n %s get pods) - most often an image that cannot be pulled or a volume that cannot be bound.", ready, desired, int(f.now().Sub(f.notReady).Minutes()), f.Namespace)
		}
	}
	if !f.since.IsZero() {
		t := f.since
		st.Since = &t
	}
	return st
}

// Enable gets the central operator and its certificates ready, then starts the four workloads. Safe to repeat.
func (f *FusionControl) Enable(ctx context.Context, c *Core, actor string) (FusionStatus, error) {
	if st := f.Status(ctx); !st.Available {
		return st, errf(KindConflict, "%s", st.Message)
	}
	if f.Org != "" && c.OrgID != f.Org {
		return FusionStatus{}, errf(KindForbidden, "FUSION belongs to this server's main organisation, which is the only one that can turn it on")
	}
	_, bundle, err := c.EnsureCentralOperator(ctx, actor, f.destination(), f.certHosts())
	if err != nil {
		return FusionStatus{}, err
	}
	if err := f.Kube.PatchSecret(ctx, f.tlsSecretName(), map[string][]byte{
		"tls.crt": bundle.ReceiverCertPEM, "tls.key": bundle.ReceiverKeyPEM, "ca.crt": bundle.CACertPEM,
	}); err != nil {
		return FusionStatus{}, kubeFail("put the gateway's certificate in place", err)
	}
	f.mu.Lock()
	f.since, f.notReady = f.now(), f.now()
	f.mu.Unlock()
	if err := f.scaleAll(ctx, 1); err != nil {
		return FusionStatus{}, err
	}
	c.audit(ctx, actor, "fusion-enabled", "fusion", f.Name, "")
	return f.Status(ctx), nil
}

// Renew reissues the gateway's server certificate and puts it in its Secret, if FUSION is on and has been set up (the
// central operator exists). The certificate is good for a year and a gateway that is running keeps the one it started
// with, so without this FUSION would stop accepting senders once it expired; with it, the certificate is replaced well
// before that and, because the gateway reloads its certificate files, the running gateway takes it up on its own. It
// also gives the certificate the names the server now knows (a public address set after FUSION was first enabled).
// Nothing is audited: it changes no one's access, and it runs daily.
func (f *FusionControl) Renew(ctx context.Context, c *Core) error {
	if f == nil || f.Kube == nil || (f.Org != "" && c.OrgID != f.Org) {
		return nil
	}
	if st := f.Status(ctx); !st.Available || st.State == "off" {
		return nil
	}
	if _, err := c.Store.GetOperator(ctx, CentralOperatorID); err != nil {
		return nil // never enabled from here: nothing to renew
	}
	_, bundle, err := c.EnsureCentralOperator(ctx, "system", f.destination(), f.certHosts())
	if err != nil {
		return err
	}
	return f.Kube.PatchSecret(ctx, f.tlsSecretName(), map[string][]byte{
		"tls.crt": bundle.ReceiverCertPEM, "tls.key": bundle.ReceiverKeyPEM, "ca.crt": bundle.CACertPEM,
	})
}

// Disable stops the four workloads. Their volumes stay, so turning FUSION on again brings the data back.
func (f *FusionControl) Disable(ctx context.Context, c *Core, actor string) (FusionStatus, error) {
	if st := f.Status(ctx); !st.Available {
		return st, errf(KindConflict, "%s", st.Message)
	}
	if f.Org != "" && c.OrgID != f.Org {
		return FusionStatus{}, errf(KindForbidden, "FUSION belongs to this server's main organisation, which is the only one that can turn it off")
	}
	if err := f.scaleAll(ctx, 0); err != nil {
		return FusionStatus{}, err
	}
	f.mu.Lock()
	f.since, f.notReady = time.Time{}, time.Time{}
	f.mu.Unlock()
	c.audit(ctx, actor, "fusion-disabled", "fusion", f.Name, "")
	return f.Status(ctx), nil
}

// scaleAll sets every workload, the stores before the gateway on the way up and the gateway first on the way down, so
// nothing is ever sending into something that is not there.
func (f *FusionControl) scaleAll(ctx context.Context, replicas int) error {
	ws := f.stores()
	if replicas == 0 {
		for i, j := 0, len(ws)-1; i < j; i, j = i+1, j-1 {
			ws[i], ws[j] = ws[j], ws[i]
		}
	}
	for _, s := range ws {
		if err := f.Kube.Scale(ctx, s.Kind, s.Name, replicas); err != nil {
			return kubeFail("scale "+s.Label, err)
		}
	}
	return nil
}

func kubeFail(what string, err error) error {
	if errors.Is(err, ErrKubeForbidden) {
		return errf(KindForbidden, "this server is not allowed to %s. Check fusionControl.enabled in the server chart", what)
	}
	return errf(KindConflict, "could not %s: %v", what, err)
}

// EnsureCentralOperator makes sure the central operator exists in this organisation and issues a fresh server
// certificate for its receiver (valid for hosts) from the operator's own CA. The first call mints the operator and its
// CA; later calls only reissue the certificate, which is safe because senders trust the CA, not one certificate.
func (c *Core) EnsureCentralOperator(ctx context.Context, actor string, dest store.Destination, hosts []string) (store.Operator, OperatorTLSBundle, error) {
	op, err := c.Store.GetOperator(ctx, CentralOperatorID)
	if errors.Is(err, store.ErrNotFound) {
		bundle, caKeyPEM, err := mintOperatorTLS(c, CentralOperatorID, hosts)
		if err != nil {
			return store.Operator{}, OperatorTLSBundle{}, errf(KindInternal, "could not mint the central operator's certificates: %v", err)
		}
		op = store.Operator{
			ID: CentralOperatorID, OrgID: c.OrgID, Name: "Central (FUSION)", Status: store.OperatorActive,
			Destination: dest, ReceiverAuth: store.ReceiverAuthMTLS,
			ClientCACertPEM: bundle.CACertPEM, ClientCAKeyPEM: caKeyPEM,
			CreatedBy: actor, CreatedAt: c.Now(),
		}
		if err := c.audited(ctx, actor, "operator-created", "operator", op.ID, "central (FUSION)", func() error {
			return c.Store.CreateOperator(ctx, op, nil)
		}); err != nil {
			return store.Operator{}, OperatorTLSBundle{}, err
		}
		return op, bundle, nil
	}
	if err != nil {
		return store.Operator{}, OperatorTLSBundle{}, err
	}
	if op.OrgID != c.OrgID {
		return store.Operator{}, OperatorTLSBundle{}, errf(KindConflict, "the central operator belongs to another organisation")
	}
	if op.Status != store.OperatorActive {
		return store.Operator{}, OperatorTLSBundle{}, errf(KindConflict, "the central operator was revoked")
	}
	issuer, caPEM, err := c.operatorIssuer(ctx, op)
	if err != nil {
		return store.Operator{}, OperatorTLSBundle{}, err
	}
	certPEM, keyPEM, err := issuer.IssueOperatorReceiverTLS(op.ID, c.OrgID, hosts)
	if err != nil {
		return store.Operator{}, OperatorTLSBundle{}, err
	}
	return op, OperatorTLSBundle{ReceiverCertPEM: certPEM, ReceiverKeyPEM: keyPEM, CACertPEM: caPEM}, nil
}

// guardCentral refuses to change the server-owned operator through the ordinary operator calls.
func guardCentral(id string) error {
	if id == CentralOperatorID {
		return errf(KindConflict, "the central operator is managed by FUSION: turn FUSION off or on instead of changing it")
	}
	return nil
}
