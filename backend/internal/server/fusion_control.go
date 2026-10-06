package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
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
// CA, and it sends on to the three stores. The stores are not exposed outside the cluster; only the gateway may be.

// CentralOperatorID is the id of the server-owned regional operator that fronts FUSION. One FUSION per server, so one
// fixed id; only the server's primary organisation may turn FUSION on (see FusionControl.Org).
const CentralOperatorID = "op-central"

// fusionStore is one of the four workloads the switch drives.
type fusionStore struct {
	Component string // metrics | logs | traces | grafana | central
	Label     string // what the UI calls it
	Kind      string // statefulsets | deployments
	Name      string
	// Optional is a workload an install may not have (Grafana: turned off in the chart, or an older release): its absence
	// is not an error, it is simply not part of FUSION there.
	Optional bool
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
	// PrometheusUI and GrafanaUI are where the two pages are reached when it is not their Services (tests point them at fakes).
	PrometheusUI, GrafanaUI string

	addrMu sync.RWMutex // guards PublicAddress, which the admin can change while the server runs (SetPublicAddress)

	// StatusTTL is how long a read of the workloads is reused (0 = fusionStatusTTL, negative = never; tests that change
	// the fake cluster between calls turn the cache off).
	StatusTTL time.Duration

	cacheMu sync.Mutex
	cache   statusCache

	// lastData is the newest sample time seen in Prometheus, and when it was last asked (see lastDataAt).
	lastDataMu    sync.Mutex
	lastDataSeen  time.Time
	lastDataTried time.Time

	discoverMu sync.Mutex
	discoverAt time.Time // when the Service was last looked at for an address (see DiscoverAddress)

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
		{"metrics", "Prometheus", "statefulsets", f.Name + "-prometheus", false},
		{"logs", "Loki", "statefulsets", f.Name + "-loki", false},
		{"traces", "Tempo", "statefulsets", f.Name + "-tempo", false},
		{"grafana", "Grafana", "statefulsets", f.Name + "-grafana", true},
		{"central", "Central operator", "deployments", f.Name + "-central", false},
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

// setPublicAddressIfUnset is how the server learns an address: it never replaces one that was set meanwhile (by the
// administrator through SetPublicAddress, which always wins).
func (f *FusionControl) setPublicAddressIfUnset(addr string) {
	f.addrMu.Lock()
	if f.PublicAddress == "" {
		f.PublicAddress = addr
	}
	f.addrMu.Unlock()
}

// SetPublicAddress records where other clusters reach the gateway ("" = this cluster only). It is what the admin
// types under Reachable at on the central operator; the server's startup flag (--fusion-central-address) is only the
// first value. Nothing is reissued: senders verify the gateway by its stable name (operatorServerName).
func (f *FusionControl) SetPublicAddress(addr string) {
	f.addrMu.Lock()
	f.PublicAddress = addr
	f.addrMu.Unlock()
}

// How often the gateway's Service is looked at while it has no address (a cloud takes a minute or two to give a
// LoadBalancer one, so asking more often finds nothing new).
const fusionAddressEvery = 30 * time.Second

// DiscoverAddress records where other clusters can reach the gateway, when the gateway's Service says so (a
// LoadBalancer that has an address, or a NodePort) and nobody has recorded one: it is what saves the administrator the
// `kubectl get svc` and the typing. An address recorded by hand always wins and is never replaced, not even by a
// different one the Service now shows. It returns the address it recorded, "" if it recorded none; it is cheap to call
// often (the Service is read at most every fusionAddressEvery, and only while there is nothing recorded).
func (f *FusionControl) DiscoverAddress(ctx context.Context, c *Core) (string, error) {
	if f == nil || f.Kube == nil || (f.Org != "" && c.OrgID != f.Org) || f.publicAddress() != "" {
		return "", nil
	}
	f.discoverMu.Lock()
	defer f.discoverMu.Unlock()
	if now := f.now(); !f.discoverAt.IsZero() && now.Sub(f.discoverAt) < fusionAddressEvery {
		return "", nil
	}
	f.discoverAt = f.now()
	op, err := c.Store.GetOperator(ctx, CentralOperatorID)
	if err != nil || op.Status != store.OperatorActive || op.OrgID != c.OrgID {
		return "", nil // not set up yet (or revoked): there is nothing to record an address on
	}
	if op.Address != "" { // recorded by hand, here or by another replica: the server only has to learn it
		f.setPublicAddressIfUnset(op.Address)
		return "", nil
	}
	addr, err := f.Kube.ServiceAddress(ctx, f.ServiceName())
	if err != nil {
		if errors.Is(err, ErrKubeNotFound) || errors.Is(err, ErrKubeForbidden) { // an older Role cannot read it: no address, no complaint
			return "", nil
		}
		return "", err
	}
	if addr == "" {
		return "", nil
	}
	// Validated, audited and refused for the same reasons as the administrator's "Reachable at", but written only
	// while no address is recorded: the look at the operator above and this write are not one step, and a person
	// who typed an address in between must keep it (in the database and in what this server answers with).
	current, wrote, err := c.RecordOperatorAddressIfUnset(ctx, "system", CentralOperatorID, addr)
	if err != nil {
		return "", err
	}
	f.setPublicAddressIfUnset(current)
	if !wrote {
		return "", nil
	}
	return current, nil
}

// DiscoverAddressSoon is DiscoverAddress off the request path: for the screens that notice the gateway has no address
// yet. A failure is logged and tried again later.
func (f *FusionControl) DiscoverAddressSoon(c *Core) {
	if f == nil || f.Kube == nil || f.publicAddress() != "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), fusionReadTimeout)
		defer cancel()
		if _, err := f.DiscoverAddress(ctx, c); err != nil {
			c.Log.Warn("could not record the FUSION gateway's address", "err", err)
		}
	}()
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
	// Reason is, in plain words, why this part is not ready ("Cannot download the image", "Waiting for a volume"); empty
	// while it is ready, and when the cause cannot be told (no permission to read the pod, the pod does not exist yet).
	Reason string `json:"reason,omitempty"`

	hard     bool // Reason names a fault that will not clear by itself, so waiting longer will not help
	optional bool // not part of whether FUSION is up (Grafana)
}

// componentUp says whether the named workload exists in this install and is fully ready.
func (st FusionStatus) componentUp(component string) bool {
	for _, c := range st.Components {
		if c.Component == component {
			return c.Desired > 0 && c.Ready >= c.Desired
		}
	}
	return false
}

// FusionStatus is the switch's whole state.
type FusionStatus struct {
	Available  bool              `json:"available"`
	Reason     string            `json:"reason,omitempty"`
	State      string            `json:"state"` // off | starting | running | attention
	Message    string            `json:"message,omitempty"`
	Since      *time.Time        `json:"since,omitempty"`
	Components []FusionComponent `json:"components,omitempty"`
	// LastDataAt is when Prometheus last held a fresh sample, so "running" can be told from "running, nothing received
	// yet". Omitted when unknown.
	LastDataAt *time.Time `json:"lastDataAt,omitempty"`
}

// clone is a copy that shares nothing with the cached one, so a caller may change what it was given.
func (st FusionStatus) clone() FusionStatus {
	st.Components = append([]FusionComponent(nil), st.Components...)
	if st.Since != nil {
		t := *st.Since
		st.Since = &t
	}
	if st.LastDataAt != nil {
		t := *st.LastDataAt
		st.LastDataAt = &t
	}
	return st
}

const (
	// How long the stores may take to start before the switch says something is wrong: pulling three images and
	// binding volumes is slow on a fresh node, but not this slow.
	fusionStartGrace = 5 * time.Minute
	// With a fault that will not clear by itself (an image that cannot be pulled, a volume that cannot be bound) there
	// is no point waiting the full grace; this is long enough for a pull or a provisioner to finish a normal job.
	fusionHardGrace = 90 * time.Second

	// The workloads are read at most this often however many screens, tokens and health checks ask: they all share one
	// read (see statusWithin).
	fusionStatusTTL = 2500 * time.Millisecond
	// A FUSION access token's status call is answered from what the last read found, up to this old.
	fusionTokenStatusAge = time.Minute
	// Each Kubernetes call, and the whole read, has its own limit, so a slow API server cannot hold a request open.
	fusionKubeTimeout = 4 * time.Second
	fusionReadTimeout = 10 * time.Second
	// Prometheus is asked for the time of its newest sample no more often than this.
	fusionLastDataEvery = 15 * time.Second
	// fusionLastDataQuery is the time of the newest sample of target_info, which Prometheus's OTLP receiver writes once per
	// sending resource (with the batch's newest timestamp) whenever the resource carries attributes beyond service.name
	// / instance - and every regional operator stamps continuum.operator.id and .name on what it forwards. Nothing is
	// scraped here (everything is pushed over OTLP), so `up` and prometheus_tsdb_head_max_time do not exist; the obvious
	// "newest sample of anything", max(timestamp({__name__=~".+"})), reads every series the lookback window touches on
	// each ask, which is the whole store on a busy install. target_info is one series per sender.
	fusionLastDataQuery = `max(timestamp(target_info))`
)

// statusCache is the last read of the workloads and the read in progress, if any.
type statusCache struct {
	st     FusionStatus
	at     time.Time
	valid  bool
	gen    uint64 // bumped when the cluster is changed from here: a read that began before it must not be kept
	flight *statusFlight
}

type statusFlight struct {
	done chan struct{}
	st   FusionStatus
}

func (f *FusionControl) statusTTL() time.Duration {
	if f.StatusTTL == 0 {
		return fusionStatusTTL
	}
	return max(f.StatusTTL, 0)
}

// Status is the switch's state. Reads of the cluster are shared: one per fusionStatusTTL however many ask at once, and
// anyone who arrives while one is under way waits for it instead of starting another. A change made from here (Enable,
// Disable) replaces what is kept.
func (f *FusionControl) Status(ctx context.Context) FusionStatus {
	if f == nil {
		return f.statusWithin(ctx, 0)
	}
	return f.statusWithin(ctx, f.statusTTL())
}

// TokenStatus is Status for a FUSION access token's call: a token holder may poll as fast as the rate limit allows, so
// it is answered from the last read unless that is a minute old.
func (f *FusionControl) TokenStatus(ctx context.Context) FusionStatus {
	if f == nil {
		return f.statusWithin(ctx, 0)
	}
	if f.statusTTL() == 0 {
		return f.statusWithin(ctx, 0)
	}
	return f.statusWithin(ctx, fusionTokenStatusAge)
}

func (f *FusionControl) statusWithin(ctx context.Context, maxAge time.Duration) FusionStatus {
	if f == nil || f.Kube == nil {
		return FusionStatus{State: "off", Reason: "not-configured", Message: "This server cannot switch FUSION: it was installed without it, or without the permission to scale it (fusion.enabled and fusionControl.enabled in the server chart)."}
	}
	if maxAge <= 0 {
		return f.readStatus(ctx)
	}
	f.cacheMu.Lock()
	if f.cache.valid && f.now().Sub(f.cache.at) < maxAge {
		st := f.cache.st.clone()
		f.cacheMu.Unlock()
		return st
	}
	if fl := f.cache.flight; fl != nil {
		f.cacheMu.Unlock()
		select {
		case <-fl.done:
			return fl.st.clone()
		case <-ctx.Done():
			return FusionStatus{Available: true, State: "attention", Message: "The Kubernetes API did not answer in time."}
		}
	}
	fl := &statusFlight{done: make(chan struct{}), st: FusionStatus{Available: true, State: "attention", Message: "The Kubernetes API did not answer in time."}}
	f.cache.flight = fl
	gen := f.cache.gen
	f.cacheMu.Unlock()

	// The read belongs to everyone waiting on it, not to the request that happened to start it. However it ends - a panic
	// included - the flight is closed, or everyone who joined it (and every later caller, who would join it too) would wait
	// for ever.
	read := false
	defer func() {
		f.cacheMu.Lock()
		if read && f.cache.gen == gen {
			f.cache.st, f.cache.at, f.cache.valid = fl.st.clone(), f.now(), true
		}
		if f.cache.flight == fl { // not one a later change already disowned (see invalidate)
			f.cache.flight = nil
		}
		close(fl.done)
		f.cacheMu.Unlock()
	}()
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fusionReadTimeout)
	defer cancel()
	fl.st = f.readStatus(rctx)
	read = true
	return fl.st.clone()
}

// invalidate drops what is kept and disowns any read already under way: the cluster is about to change.
func (f *FusionControl) invalidate() {
	f.cacheMu.Lock()
	f.cache.valid = false
	f.cache.gen++
	f.cache.flight = nil // a read under way began before the change: nobody asking from now on may join it and be told the old state
	f.cacheMu.Unlock()
}

// keep stores a status the switch worked out itself, after a change it has just made.
func (f *FusionControl) keep(st FusionStatus) {
	f.cacheMu.Lock()
	f.cache.gen++
	f.cache.flight = nil // as in invalidate: what a read under way is about to find is older than this
	f.cache.st, f.cache.at, f.cache.valid = st.clone(), f.now(), true
	f.cacheMu.Unlock()
}

// storeRead is what one workload's reads found.
type storeRead struct {
	w      KubeWorkload
	err    error
	reason string
	hard   bool
}

// readStatus reads the workloads, all at once, and works out the state. Anything it cannot read - no permission, the
// workloads are not there (an older server chart) - is reported as unavailable with the reason, never as an error: the
// screen shows it as a fact.
func (f *FusionControl) readStatus(ctx context.Context) FusionStatus {
	stores := f.stores()
	reads := make([]storeRead, len(stores))
	var wg sync.WaitGroup
	for i, s := range stores {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reads[i] = f.readStore(ctx, s)
		}()
	}
	wg.Wait()

	st := FusionStatus{Available: true}
	var comps []FusionComponent
	for i, s := range stores {
		r := reads[i]
		switch {
		case s.Optional && (errors.Is(r.err, ErrKubeNotFound) || errors.Is(r.err, ErrKubeForbidden)):
			continue // Grafana is off in this install (or this Role predates it): the rest of FUSION is unaffected
		case errors.Is(r.err, ErrKubeNotFound):
			return FusionStatus{State: "off", Reason: "not-installed", Message: "FUSION's workloads are not in this release. Upgrade the server chart (fusion.enabled=true) to get them."}
		case errors.Is(r.err, ErrKubeForbidden):
			return FusionStatus{State: "off", Reason: "no-access", Message: "This server is not allowed to read or scale FUSION's workloads. Check fusionControl.enabled in the server chart."}
		case r.err != nil:
			st.State, st.Message = "attention", "The Kubernetes API did not answer: "+r.err.Error()
			return st
		}
		comps = append(comps, FusionComponent{Component: s.Component, Label: s.Label, Desired: r.w.Desired, Ready: r.w.Ready,
			Reason: r.reason, hard: r.hard, optional: s.Optional})
	}
	st = f.assess(comps)
	if st.componentUp("metrics") {
		st.LastDataAt = f.lastDataAt(ctx)
	}
	return st
}

// readStore reads one workload and, if it has not come up, the reason why.
func (f *FusionControl) readStore(ctx context.Context, s fusionStore) storeRead {
	cctx, cancel := context.WithTimeout(ctx, fusionKubeTimeout)
	defer cancel()
	w, err := f.Kube.Workload(cctx, s.Kind, s.Name)
	r := storeRead{w: w, err: err}
	if err == nil && w.Desired > w.Ready && s.Kind == "statefulsets" {
		dctx, dcancel := context.WithTimeout(ctx, fusionKubeTimeout)
		defer dcancel()
		r.reason, r.hard = f.whyNotReady(dctx, s)
	}
	return r
}

// A store's one pod, and its volume, are named by the chart's own rule (a StatefulSet's pod is <name>-0, its claim
// <template>-<pod>, and the chart's template is "data"): the server's Role grants exactly those names, and the chart
// test in internal/chart pins them to what the chart renders.
func storePod(s fusionStore) string   { return s.Name + "-0" }
func storeClaim(s fusionStore) string { return "data-" + storePod(s) }

// whyNotReady says in plain words why a store's pod is not ready: what Kubernetes is waiting on, from the pod and its
// volume. Every failure to find out - no such pod yet, a Role from before these reads existed, the API slow - is just
// "no reason": the count of parts up is still right.
func (f *FusionControl) whyNotReady(ctx context.Context, s fusionStore) (reason string, hard bool) {
	pod, err := f.Kube.Pod(ctx, storePod(s))
	if err != nil {
		return "", false
	}
	// Only a pod that has not started, or is being created, can be held up by its volume.
	var claim string
	if pod.Unschedulable || pod.WaitingReason == "ContainerCreating" || (pod.Phase == "Pending" && pod.WaitingReason == "") {
		claim, _ = f.Kube.ClaimPhase(ctx, storeClaim(s))
	}
	return explainPod(pod, claim)
}

// explainPod turns Kubernetes' reason codes into a short sentence for a person. hard is a fault that will not clear on
// its own. A code it does not know says nothing rather than show a raw identifier.
func explainPod(p KubePod, claimPhase string) (reason string, hard bool) {
	switch p.WaitingReason {
	case "ErrImagePull", "ImagePullBackOff":
		return "Cannot download the image", true
	case "InvalidImageName":
		return "The image name is not valid", true
	case "CrashLoopBackOff":
		if p.LastTerminatedReason == "OOMKilled" {
			return "Ran out of memory and keeps restarting", true
		}
		return "Keeps stopping and restarting", true
	case "CreateContainerConfigError":
		return "Its configuration is incomplete", true
	case "CreateContainerError", "RunContainerError":
		return "The container could not be started", true
	case "ContainerCreating":
		if claimPhase == "Pending" {
			return "Waiting for a volume", true
		}
		return "Starting the container", false
	case "PodInitializing":
		return "Starting up", false
	case "":
		switch {
		case claimPhase == "Pending" || (p.Unschedulable && strings.Contains(p.ScheduleMessage, "PersistentVolumeClaim")):
			return "Waiting for a volume", true
		case p.Unschedulable && strings.Contains(p.ScheduleMessage, "Insufficient"):
			return "No node has room", true
		case p.Unschedulable:
			return "No suitable node", true
		}
	}
	return "", false
}

// assess works out the state from the workloads' counts, and keeps the clocks that say how long they have not been up.
func (f *FusionControl) assess(comps []FusionComponent) FusionStatus {
	st := FusionStatus{Available: true, Components: comps}
	var desired, ready int
	hard := false
	var reasons []string
	for _, c := range comps {
		if c.Reason != "" && c.Desired > c.Ready {
			reasons = append(reasons, c.Label+": "+strings.ToLower(c.Reason[:1])+c.Reason[1:])
			hard = hard || c.hard
		}
		if !c.optional { // Grafana starting late does not make FUSION "starting": what is sent is already being kept
			desired += c.Desired
			ready += c.Ready
		}
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
		grace := fusionStartGrace
		if hard {
			grace = fusionHardGrace
		}
		if waited := f.now().Sub(f.notReady); waited > grace {
			if len(reasons) > 0 {
				st.State, st.Message = "attention", fmt.Sprintf("%d of %d parts are up after %s. %s. Details: kubectl -n %s get pods", ready, desired, waitedFor(waited), strings.Join(reasons, ". "), f.Namespace)
			} else {
				st.State, st.Message = "attention", fmt.Sprintf("%d of %d parts are up after %s. Check the pods (kubectl -n %s get pods) - most often an image that cannot be pulled or a volume that cannot be bound.", ready, desired, waitedFor(waited), f.Namespace)
			}
		}
	}
	if !f.since.IsZero() {
		t := f.since
		st.Since = &t
	}
	return st
}

func waitedFor(d time.Duration) string {
	if d < 2*time.Minute {
		return strconv.Itoa(int(d.Seconds())) + " seconds"
	}
	return strconv.Itoa(int(d.Minutes())) + " minutes"
}

// lastDataAt is when Prometheus last held a fresh sample, or nil when that is not known. Prometheus is asked at most once
// per fusionLastDataEvery; a failed ask leaves what was known (data seen more than the lookback ago no longer answers, but
// it was still the last data). Anything that goes wrong is simply "unknown": it is a nicety next to the state.
func (f *FusionControl) lastDataAt(ctx context.Context) *time.Time {
	f.lastDataMu.Lock()
	defer f.lastDataMu.Unlock()
	if now := f.now(); f.lastDataTried.IsZero() || now.Sub(f.lastDataTried) >= fusionLastDataEvery {
		f.lastDataTried = now
		qctx, cancel := context.WithTimeout(ctx, fusionKubeTimeout)
		data, err := f.dataClient().RawMetricQuery(qctx, fusionapi.AllSignals(), "query", url.Values{"query": {fusionLastDataQuery}})
		cancel()
		if err == nil {
			if t, ok := newestSample(data); ok {
				f.lastDataSeen = t
			}
		}
	}
	if f.lastDataSeen.IsZero() {
		return nil
	}
	t := f.lastDataSeen
	return &t
}

// newestSample reads the one value of an instant query's vector: the sample time Prometheus computed.
func newestSample(data json.RawMessage) (time.Time, bool) {
	var d struct {
		Result []struct {
			Value [2]json.RawMessage `json:"value"`
		} `json:"result"`
	}
	if json.Unmarshal(data, &d) != nil || len(d.Result) == 0 {
		return time.Time{}, false
	}
	var v string
	if json.Unmarshal(d.Result[0].Value[1], &v) != nil {
		return time.Time{}, false
	}
	secs, err := strconv.ParseFloat(v, 64)
	if err != nil || !(secs > 0) || math.IsInf(secs, 0) { // (a NaN fails the comparison)
		return time.Time{}, false
	}
	return time.Unix(0, int64(secs*float64(time.Second))).UTC(), true
}

// rememberedLastData is what lastDataAt last found, without asking again.
func (f *FusionControl) rememberedLastData() *time.Time {
	f.lastDataMu.Lock()
	defer f.lastDataMu.Unlock()
	if f.lastDataSeen.IsZero() {
		return nil
	}
	t := f.lastDataSeen
	return &t
}

// Enable gets the central operator and its certificates ready, then starts the four workloads. Safe to repeat.
func (f *FusionControl) Enable(ctx context.Context, c *Core, actor string) (FusionStatus, error) {
	prev := f.Status(ctx)
	if !prev.Available {
		return prev, errf(KindConflict, "%s", prev.Message)
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
	st, err := f.scaleAndReport(ctx, prev, 1)
	if err != nil {
		return FusionStatus{}, err
	}
	c.audit(ctx, actor, "fusion-enabled", "fusion", f.Name, "")
	return st, nil
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
	prev := f.Status(ctx)
	if !prev.Available {
		return prev, errf(KindConflict, "%s", prev.Message)
	}
	if f.Org != "" && c.OrgID != f.Org {
		return FusionStatus{}, errf(KindForbidden, "FUSION belongs to this server's main organisation, which is the only one that can turn it off")
	}
	st, err := f.scaleAndReport(ctx, prev, 0)
	if err != nil {
		return FusionStatus{}, err
	}
	c.audit(ctx, actor, "fusion-disabled", "fusion", f.Name, "")
	return st, nil
}

// scaleAndReport sets every workload to replicas and answers with the state that results, without reading the cluster
// again: what is wanted is known (every workload there is, at replicas) and what is ready can only be what was ready
// before, capped. It does this only once the scale calls have returned, and what was kept from before them is dropped
// first, so no screen shows the old state while the change is under way. If a call failed part way the cluster is in a
// state this does not know, so nothing is kept and the next ask reads it.
func (f *FusionControl) scaleAndReport(ctx context.Context, prev FusionStatus, replicas int) (FusionStatus, error) {
	f.invalidate()
	if err := f.scaleAll(ctx, replicas); err != nil {
		f.invalidate()
		return FusionStatus{}, err
	}
	if len(prev.Components) == 0 { // the read before it failed: there is nothing to build the answer from
		return f.Status(ctx), nil
	}
	comps := make([]FusionComponent, len(prev.Components))
	for i, c := range prev.Components {
		c.Desired, c.Ready, c.Reason, c.hard = replicas, min(c.Ready, replicas), "", false
		comps[i] = c
	}
	st := f.assess(comps)
	st.LastDataAt = f.rememberedLastData()
	f.keep(st)
	return st, nil
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
		err := f.Kube.Scale(ctx, s.Kind, s.Name, replicas)
		if s.Optional && (errors.Is(err, ErrKubeNotFound) || errors.Is(err, ErrKubeForbidden)) {
			continue
		}
		if err != nil {
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
