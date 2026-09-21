package agent

import (
	"context"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/agent/collect"
	"continuum/internal/flow"
	"continuum/internal/measure"
	"continuum/internal/pki"
	"continuum/internal/probe"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type Config struct {
	Server   string // host:port
	CAPin    string // sha256 of the server CA, from the install command
	Token    string // one-time; only used until enrolled
	Tier     int    // highest tier the installed RBAC allows (0-2)
	Kube     kubernetes.Interface
	APIHost  string
	Identity IdentityStore
	Version  string
	Log      *slog.Logger
	// Namespace is this agent's own Kubernetes namespace (its pod's, not a value it can be told). Sent in Hello
	// so the server can print a `helm upgrade`/`helm uninstall` that targets the release that is actually
	// running, instead of guessing "continuum-system". Empty outside a cluster (dev, tests).
	Namespace string
	// ReleaseName is the Helm release this agent was installed as (the chart templates `.Release.Name` in as an
	// environment variable, since Helm has no downward-API equivalent for it). Sent in Hello for the same reason as
	// Namespace: so the server's `helm upgrade`/`helm uninstall` commands name the right release instead of guessing
	// "continuum-agent". Empty outside the chart (dev, tests, an install that predates this field).
	ReleaseName string

	// RBACSelfCheck, when true, periodically asks the cluster (SelfSubjectAccessReview, which every ServiceAccount
	// may always ask about itself, needing no permission of its own) whether it still grants more than Tier
	// declares. Tier is only ever what the container was told at install time; it is never re-verified against the
	// cluster on its own, so if a `helm upgrade --set access.tier=N` to narrow an install was never run, or ran only
	// partway, the leftover ClusterRole/ClusterRoleBinding would otherwise go unnoticed even though it is a real
	// permission a compromised agent could use. The check never changes what the agent actually collects (that stays
	// governed by the server-approved tier capped at Tier, as always); it only raises or clears a problem. Off by
	// default so existing tests and a bare Config{} see no behaviour change; the CLI turns it on.
	RBACSelfCheck bool
	// RBACCheckEvery is how often the check above runs (0: 10 minutes; tests shorten it).
	RBACCheckEvery time.Duration

	// RBACNamespaced is true when tier 2's installed RBAC is a Role per namespace (the chart's rbac.mode=
	// namespaced) instead of one cluster-wide ClusterRole. The collector then watches each namespace in
	// Scope.Include individually and never attempts to read Namespaces or PersistentVolumes (no Role, in any
	// namespace, can grant either — both are cluster-scoped types). RBACSelfCheck also asks about those
	// namespaces instead of the whole cluster for its tier-2 probe: a leftover per-namespace Role would not
	// show up in a cluster-wide check the way a leftover ClusterRoleBinding does. Off by default (cluster mode).
	RBACNamespaced bool

	// Probes, when set, holds what the optional node probe reported; it is attached to each node's
	// facts. ProbeListen is where the agent listens for those reports (empty: do not listen).
	Probes *probe.Receiver
	// Flows, when set, receives the node flow collectors' reports and attributes them to workloads.
	// Both are served on ProbeListen (empty: do not listen).
	Flows       *flow.Pipeline
	ProbeListen string
	FlowWindow  time.Duration // how often observed traffic is sent up (default 60 s)

	// Measure allows the agent to time TCP connections to the addresses the server names. Off by
	// default: the agent otherwise never opens a connection the cluster did not already make itself.
	Measure bool
	// MeasureDial replaces the network connection used for measuring (tests only).
	MeasureDial measure.Dialer

	// Scope limits which namespaces are reported (nil: all of them).
	Scope *collect.Scope

	// Floors lowers the minimum timings the server may set (tests only).
	Floors Floors

	Debounce time.Duration // wait after a change before sending (default 2 s)
	Resync   time.Duration // full snapshot interval (default 15 min)

	// ProbeInterval and FlowInterval are how often the node probe and the flow collectors were told to report (the chart's
	// nodeProbe.interval and flowObserver.interval). The agent calls a collector silent after three of its own intervals
	// without a word, so it needs to know them (0: the chart's defaults, 3 minutes and 30 seconds).
	ProbeInterval, FlowInterval time.Duration

	// ChunkBytes is the encoded size at which a picture is split into another message (0: ChunkBytes, one megabyte).
	ChunkBytes int
	// SyncWait is how long the collector waits for the first list of every kind before it carries on and reports what
	// it could not read (0: 30 seconds). Tests shorten it.
	SyncWait time.Duration

	// Health, when set, is told what the agent is doing so that Kubernetes probes can ask (nil: nobody asks).
	Health *Health

	// RevokedHold is how long a restarted agent whose identity was revoked waits before it exits with
	// ErrRevoked (0: a random 5 to 10 minutes; negative: do not wait). Tests only.
	RevokedHold time.Duration
}

var ErrRevoked = errors.New("this agent was revoked or rejected by the server")

// Run enrolls if needed, then keeps a stream open until ctx ends. It never gives up on transient
// errors, and stops for good only when the server revokes it.
func Run(ctx context.Context, cfg Config) error {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Debounce == 0 {
		cfg.Debounce = 2 * time.Second
	}
	if cfg.Resync == 0 {
		cfg.Resync = 15 * time.Minute
	}
	if cfg.FlowWindow == 0 {
		cfg.FlowWindow = 60 * time.Second
	}
	a := &runner{cfg: cfg, log: cfg.Log, root: ctx, started: time.Now(), probs: newProblemSet(), dg: newDiagState()}
	if cfg.RBACSelfCheck && cfg.Kube != nil {
		go a.rbacCheckLoop(ctx)
	}
	if (cfg.Probes != nil || cfg.Flows != nil) && cfg.ProbeListen != "" {
		routes := map[string]http.Handler{}
		if cfg.Probes != nil {
			routes[probe.PathReport] = cfg.Probes.Handler()
		}
		if cfg.Flows != nil {
			routes[flow.PathReport] = cfg.Flows.Handler()
			cfg.Flows.Resolver.SetSource(a.index)
		}
		stopProbes, err := serveReceivers(cfg.ProbeListen, routes, cfg.Log)
		if err != nil {
			return fmt.Errorf("listen for node reports: %w", err)
		}
		defer stopProbes()
	}
	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		a.cfg.Health.Beat()
		err := a.onceSafe(ctx)
		if err != nil && !errors.Is(err, ErrRevoked) && !errors.Is(err, errReconfigure) {
			a.noteStreamError(err)
		}
		if errors.Is(err, ErrRevoked) {
			a.cfg.Health.Revoked()
		} else {
			a.cfg.Health.Disconnected()
		}
		if time.Since(started) > 30*time.Second {
			backoff = time.Second // it was a healthy session; start the backoff over
		}
		if errors.Is(err, ErrRevoked) {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil || errors.Is(err, errReconfigure) {
			backoff = time.Second
			if err == nil {
				a.log.Info("reconnecting with the renewed certificate")
			} else {
				a.log.Info(err.Error())
			}
		} else {
			a.log.Warn("disconnected, retrying", "err", err, "in", backoff, "hint", clockHint(err))
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
		if backoff < time.Minute {
			backoff *= 2
		}
	}
	return ctx.Err()
}

type runner struct {
	cfg Config
	log *slog.Logger

	root    context.Context // Run's: the collector's watches end with it, and so does everything else
	started time.Time
	probs   *problemSet
	dg      *diagState
	panics  atomic.Int64
	// identityOK is set once the agent has shown it can write its identity where it keeps it.
	identityOK atomic.Bool

	mu          sync.Mutex
	id          *Identity
	collector   *collect.Collector
	collTier    int
	collCancel  context.CancelFunc
	collStarted time.Time
	uid         string
	created     *timestamppb.Timestamp // kube-system's creation time: the cluster's age
	version     string
	versionAt   time.Time

	ex extra // enrollment end state and clock skew (enroll.go)
}

func (r *runner) dial(id *Identity) (*grpc.ClientConn, error) {
	host, _, err := net.SplitHostPort(r.cfg.Server)
	if err != nil {
		return nil, fmt.Errorf("server address must be host:port: %w", err)
	}
	var cert *tls.Certificate
	if id.HasCert() {
		cert = &tls.Certificate{Certificate: [][]byte{id.CertDER}, PrivateKey: id.Key}
	}
	return grpc.NewClient(r.cfg.Server,
		grpc.WithTransportCredentials(credentials.NewTLS(pki.ClientTLS(r.cfg.CAPin, host, cert))),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 30 * time.Second, Timeout: 20 * time.Second, PermitWithoutStream: true}))
}

// onceSafe is once, except that a panic inside it ends only this connection: it is logged with its stack, shown as a
// problem, and the caller reconnects as it would after any other failure.
func (r *runner) onceSafe(ctx context.Context) (err error) {
	defer func() {
		if p := recover(); p != nil {
			r.panicked("connection", p)
			err = fmt.Errorf("internal error in the connection loop (restarting it): %v", p)
		}
	}()
	return r.once(ctx)
}

func (r *runner) once(ctx context.Context) error {
	id, err := r.cfg.Identity.Load(ctx)
	if err != nil {
		return fmt.Errorf("load identity: %w", err)
	}
	if id.Ended() {
		// Revoked earlier. Only a different enrollment token (helm upgrade) starts over; the same one, or none,
		// means stay down without contacting the server.
		if r.cfg.Token == "" || tokenID(r.cfg.Token) == id.TokenID {
			r.cfg.Health.Revoked() // idle on purpose: not ready, but alive for the whole hold
			return r.holdEnded(ctx, id)
		}
		r.log.Info("a new enrollment token was given after this agent was revoked; enrolling again")
		_ = r.cfg.Identity.Clear(ctx)
		id = nil
	}
	if !id.HasCert() {
		if id, err = r.enroll(ctx, id); err != nil {
			return err
		}
	} else if leaf, perr := x509.ParseCertificate(id.CertDER); perr == nil && time.Now().After(leaf.NotAfter) {
		// Offline past the certificate's life (cluster outage, long node reboot).
		if id, err = r.rejoin(ctx, id); err != nil {
			return err
		}
	}
	r.mu.Lock()
	r.id = id
	r.mu.Unlock()
	if !r.identityOK.Load() && id.HasCert() {
		r.probeIdentity(ctx, id)
	}
	err = r.stream(ctx, id)
	if reason, ok := revokedDetail(err); ok {
		// Revoked while it was away: the server said so on the connection attempt.
		return r.ended(ctx, id, reason)
	}
	return err
}

// ---- enrollment ----

func (r *runner) clusterIdentity(ctx context.Context) (fingerprint, version string, err error) {
	ns, err := r.cfg.Kube.CoreV1().Namespaces().Get(ctx, "kube-system", metav1.GetOptions{})
	if err != nil {
		return "", "", fmt.Errorf("read the kube-system namespace (its UID identifies the cluster): %w", err)
	}
	v, err := r.cfg.Kube.Discovery().ServerVersion()
	if err != nil {
		return "", "", err
	}
	if !ns.CreationTimestamp.IsZero() {
		r.mu.Lock()
		r.created = timestamppb.New(ns.CreationTimestamp.Time)
		r.mu.Unlock()
	}
	return string(ns.UID), v.GitVersion, nil
}

// rejoin gets a fresh certificate for an agent whose certificate expired while it was offline. It
// proves itself with the expired certificate and its private key; the server still checks the
// agent is approved. If the server refuses, the agent must be enrolled again with a new token.
func (r *runner) rejoin(ctx context.Context, id *Identity) (*Identity, error) {
	csr, err := NewCSR(id.Key) // same key: the request's signature is the proof of possession
	if err != nil {
		return nil, err
	}
	conn, err := r.dial(&Identity{})
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	resp, err := continuumv1.NewEnrollmentClient(conn).Rejoin(ctx, &continuumv1.RejoinRequest{ExpiredLeafDer: id.CertDER, CsrDer: csr})
	if status.Code(err) == codes.Unauthenticated {
		return nil, r.ended(ctx, id, "the certificate expired too long ago or the agent was revoked")
	}
	if err != nil {
		return nil, fmt.Errorf("rejoin: %w", err)
	}
	if !pki.PinMatches(r.cfg.CAPin, resp.CaDer) {
		return nil, errors.New("rejoin returned a CA that does not match the pin")
	}
	leaf, err := x509.ParseCertificate(resp.LeafDer)
	if err != nil {
		return nil, err
	}
	if pub, ok := leaf.PublicKey.(interface{ Equal(x crypto.PublicKey) bool }); !ok || !pub.Equal(&id.Key.PublicKey) {
		return nil, errors.New("server returned a certificate for a different key")
	}
	next := &Identity{AgentID: id.AgentID, Key: id.Key, CertDER: resp.LeafDer, CADER: resp.CaDer, TokenID: id.TokenID}
	if err := r.cfg.Identity.Save(ctx, next); err != nil {
		return nil, err
	}
	r.log.Info("rejoined with a new certificate after being offline", "expires", resp.NotAfter.AsTime())
	return next, nil
}

func (r *runner) revoked(ctx context.Context, reason string) error {
	r.mu.Lock()
	id := r.id
	r.mu.Unlock()
	if reason == "" {
		reason = "revoked by an administrator"
	}
	return r.ended(ctx, id, reason)
}

func (r *runner) collectorChanges() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.collector == nil {
		return nil
	}
	return r.collector.Changes()
}

// ensureCollector makes sure the collector watches at exactly this tier. A different tier means a new collector: the old one
// is stopped, which stops its watches for real, so narrowing access leaves nothing of the higher tier running.
func (r *runner) ensureCollector(tier int) error {
	r.mu.Lock()
	if r.collector != nil && r.collTier == tier {
		r.mu.Unlock()
		return nil
	}
	old := r.collCancel
	r.collector, r.collTier, r.collCancel = nil, tier, nil
	r.mu.Unlock()
	if old != nil {
		old()
	}
	// The collector outlives individual streams, so it is tied to the agent's whole run, not to one connection: that keeps
	// its watches across a reconnect and still ends them when the process is asked to stop.
	cctx, cancel := context.WithCancel(r.root)
	c := collect.New(r.cfg.Kube, tier, r.cfg.APIHost)
	c.SetScope(r.cfg.Scope)
	c.SetNamespacedRBAC(r.cfg.RBACNamespaced)
	c.SyncWait = r.cfg.SyncWait
	c.OnPanic = func(name string, p any) { r.panicked(name, p) }
	if err := c.Start(cctx); err != nil { // only when the agent is asked to stop while it waits
		cancel()
		return err
	}
	r.mu.Lock()
	r.collector, r.collTier, r.collCancel, r.collStarted = c, tier, cancel, time.Now()
	r.mu.Unlock()
	return nil
}

// snapshot adds the cluster identity to what the collector saw. The cluster's UID never changes
// so it is read once; the version is refreshed every ten minutes.
func (r *runner) snapshot(ctx context.Context) *continuumv1.Sync {
	r.mu.Lock()
	c := r.collector
	needUID := r.uid == ""
	needVersion := time.Since(r.versionAt) > 10*time.Minute
	r.mu.Unlock()
	s := c.Snapshot()
	if needUID || needVersion {
		if fp, v, err := r.clusterIdentity(ctx); err == nil {
			r.mu.Lock()
			r.uid, r.version, r.versionAt = fp, v, time.Now()
			r.mu.Unlock()
		}
	}
	r.mu.Lock()
	s.Cluster.Uid, s.Cluster.Version, s.Cluster.CreatedAt = r.uid, r.version, r.created
	r.mu.Unlock()
	if r.cfg.Probes != nil && len(s.Nodes) > 0 {
		keep := map[string]bool{}
		for _, n := range s.Nodes {
			keep[n.Name] = true
			n.Probe = r.cfg.Probes.Get(n.Name) // only nodes the cluster really has get facts attached
		}
		r.cfg.Probes.Prune(keep)
	}
	return s
}

// index gives the flow pipeline the current address index of the cluster.
func (r *runner) index() *collect.Index {
	r.mu.Lock()
	c := r.collector
	r.mu.Unlock()
	if c == nil {
		return nil
	}
	return c.Index()
}

// serveReceivers listens for signed reports from the node probes and flow collectors.
func serveReceivers(addr string, routes map[string]http.Handler, log *slog.Logger) (stop func(), err error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	for path, h := range routes {
		mux.Handle(path, h)
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 8 << 10}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("node probe receiver stopped", "err", err)
		}
	}()
	log.Info("listening for node reports", "addr", ln.Addr().String())
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}, nil
}

// ---- certificate renewal ----

func (r *runner) renewLoop(ctx context.Context, conn *grpc.ClientConn, id *Identity, at time.Time, renewed chan<- struct{}) {
	select {
	case <-time.After(time.Until(at)):
	case <-ctx.Done():
		return
	}
	for {
		if err := r.renew(ctx, conn, id); err == nil {
			close(renewed)
			return
		} else {
			r.log.Warn("certificate renewal failed, will retry", "err", err)
		}
		select {
		case <-time.After(time.Minute):
		case <-ctx.Done():
			return
		}
	}
}

func (r *runner) renew(ctx context.Context, conn *grpc.ClientConn, id *Identity) error {
	key, err := NewKey() // a fresh key with every certificate
	if err != nil {
		return err
	}
	csr, err := NewCSR(key)
	if err != nil {
		return err
	}
	resp, err := continuumv1.NewAgentServiceClient(conn).Renew(ctx, &continuumv1.RenewRequest{CsrDer: csr})
	if err != nil {
		return err
	}
	if !pki.PinMatches(r.cfg.CAPin, resp.CaDer) {
		return errors.New("renewal returned a CA that does not match the pin")
	}
	next := &Identity{AgentID: id.AgentID, Key: key, CertDER: resp.LeafDer, CADER: resp.CaDer, TokenID: id.TokenID}
	// Once the server has issued the certificate, storing it is not abandoned because the agent was just asked to stop:
	// a key and certificate half written are worse than either. It has its own, short, deadline.
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := r.cfg.Identity.Save(sctx, next); err != nil {
		r.identityUnwritable(err)
		return err
	}
	r.probs.clear("identity")
	r.identityOK.Store(true)
	r.log.Info("certificate renewed", "expires", resp.NotAfter.AsTime())
	return nil
}

// ---- deltas ----

// diff returns what changed from prev to cur, or nil if nothing did.
func diff(prev, cur *continuumv1.Sync) *continuumv1.Sync {
	out := &continuumv1.Sync{}
	changed := false
	if !proto.Equal(prev.Cluster, cur.Cluster) {
		out.Cluster, changed = cur.Cluster, true
	}
	pn := map[string]*continuumv1.NodeFacts{}
	for _, n := range prev.Nodes {
		pn[n.Key] = n
	}
	seen := map[string]bool{}
	for _, n := range cur.Nodes {
		seen[n.Key] = true
		if p, ok := pn[n.Key]; !ok || !proto.Equal(p, n) {
			out.Nodes, changed = append(out.Nodes, n), true
		}
	}
	for k := range pn {
		if !seen[k] {
			out.DeletedNodes, changed = append(out.DeletedNodes, k), true
		}
	}
	pns := map[string]*continuumv1.NamespaceFacts{}
	for _, n := range prev.Namespaces {
		pns[n.Key] = n
	}
	seen = map[string]bool{}
	for _, n := range cur.Namespaces {
		seen[n.Key] = true
		if p, ok := pns[n.Key]; !ok || !proto.Equal(p, n) {
			out.Namespaces, changed = append(out.Namespaces, n), true
		}
	}
	for k := range pns {
		if !seen[k] {
			out.DeletedNamespaces, changed = append(out.DeletedNamespaces, k), true
		}
	}
	pw := map[string]*continuumv1.WorkloadFacts{}
	for _, w := range prev.Workloads {
		pw[w.Key] = w
	}
	seen = map[string]bool{}
	for _, w := range cur.Workloads {
		seen[w.Key] = true
		if p, ok := pw[w.Key]; !ok || !proto.Equal(p, w) {
			out.Workloads, changed = append(out.Workloads, w), true
		}
	}
	for k := range pw {
		if !seen[k] {
			out.DeletedWorkloads, changed = append(out.DeletedWorkloads, k), true
		}
	}
	if !changed {
		return nil
	}
	return out
}
