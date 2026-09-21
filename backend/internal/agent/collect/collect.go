// Package collect watches a cluster with read-only informers and turns what it sees into
// the raw facts the server interprets.
//
// Nothing here reads Secrets or ConfigMaps, and the agent's RBAC does not permit it. Objects
// are stripped on arrival (informer transforms) so container environment variables, commands,
// arguments and volumes are discarded before they are ever stored in memory.
package collect

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	continuumv1 "continuum/gen/continuumv1"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

// Module names shown in the UI's Discovery page.
const (
	ModInfrastructure = "infrastructure"
	ModServices       = "services"
	// Optional extras on top of the services tier: they need extra read permissions, and a cluster
	// that does not grant or serve them simply lacks those columns.
	ModStorage = "storage"
	ModScaling = "scaling"
)

// itemLister is every currently known object of one kind, for consumers that only ever range over "everything of
// this kind" (snapshot.go, attribution.go). In cluster mode (the default) it is one cluster-wide informer's store.
// In namespaced mode (rbac.mode=namespaced: RBAC grants tier 2 with a Role per namespace, never a ClusterRole)
// there is one real, independent informer per namespace instead — each with its own list/watch, so a Role missing
// or refused in one namespace is reported and recovered from on its own, not mistaken for every namespace failing
// — and itemLister merges their stores so every other consumer keeps working exactly as it did in cluster mode,
// unaware which one it is.
type itemLister interface {
	List() []any
}

// singleLister adapts one cluster-wide informer's store to itemLister.
type singleLister struct{ inf cache.SharedIndexInformer }

func (s singleLister) List() []any { return s.inf.GetStore().List() }

// unionLister merges several informers' stores (one per namespace) into one list.
type unionLister struct{ infs []cache.SharedIndexInformer }

func (u unionLister) List() []any {
	var out []any
	for _, inf := range u.infs {
		out = append(out, inf.GetStore().List()...)
	}
	return out
}

type Collector struct {
	client  kubernetes.Interface
	tier    int
	apiHost string

	factory informers.SharedInformerFactory
	nodes   cache.SharedIndexInformer
	sc      cache.SharedIndexInformer
	ic      cache.SharedIndexInformer
	ns      cache.SharedIndexInformer // never populated in namespaced mode: no Role, in any namespace, can grant it
	pods    itemLister
	rs      itemLister
	deploys itemLister
	sts     itemLister
	ds      itemLister
	svcs    itemLister
	ings    itemLister
	// optional (tier 2 plus a successful probe): nil when unavailable
	pvcs itemLister
	pvs  cache.SharedIndexInformer // never populated in namespaced mode, same reason as ns
	hpas itemLister
	pdbs itemLister

	// namespaced is true when tier 2's RBAC is a Role per namespace (rbac.mode=namespaced) rather than one
	// cluster-wide ClusterRole. Set with SetNamespacedRBAC before Start; Start then requires scope.Include (it
	// cannot discover namespaces on its own — that needs exactly the cluster-wide list permission this mode
	// avoids) and watches each of them individually instead of the whole cluster for every tier-2 kind.
	namespaced bool

	optional map[string]string // module -> reason it is off
	scope    *Scope            // nil: every namespace; the agent's own rule, from its install
	extraEx  []string          // namespaces the server asked to leave out on top of scope (only ever narrows it)
	infs     []*informerState  // every informer, in the order started, for the agent's account of itself

	// SyncWait is how long Start waits for the first list of every kind before it lets the agent carry on and report
	// what is and is not readable (0: 30 seconds). Tests shorten it.
	SyncWait time.Duration
	// OnPanic, when set, is told about a panic in one of the collector's own goroutines (which it recovers from).
	OnPanic func(task string, p any)

	// Service-mesh policy (Istio PeerAuthentication) is a custom resource, listed on a timer.
	pol         policyCache
	fetchPolicy policyFetcher // tests replace it
	meshMod     *continuumv1.ModuleStatus

	changed chan struct{}

	mu sync.Mutex
}

// informerState is one watch on one kind of object, with what the agent needs to say about it if it goes wrong.
type informerState struct {
	name    string // what the object is called in the API, "deployments.apps"
	module  string // which module of the Discovery page it feeds
	inf     cache.SharedIndexInformer
	objects atomic.Int64
	err     string    // the last list or watch error (guarded by Collector.mu)
	verb    string    // list or watch, as far as it can be told
	forbid  bool      // that error was "forbidden" or "unauthorized"
	errAt   time.Time // when it last happened; an error not repeated for errFresh is over
}

// errFresh is how long a list or watch error still counts: the reflector retries with a back-off of at most 30 seconds,
// so an error that keeps happening is refreshed well within it, and one that stopped ages out.
const errFresh = 2 * time.Minute

// New builds a collector for the given access tier (0-2 in this release). apiHost is the API
// endpoint host as configured, reported for information only.
func New(client kubernetes.Interface, tier int, apiHost string) *Collector {
	return &Collector{client: client, tier: tier, apiHost: apiHost, changed: make(chan struct{}, 1)}
}

// Changes signals (coalesced) that the cluster changed since the last Snapshot.
func (c *Collector) Changes() <-chan struct{} { return c.changed }

func (c *Collector) poke() {
	select {
	case c.changed <- struct{}{}:
	default:
	}
}

// Start begins watching and blocks until the caches have synced or ctx ends.
func (c *Collector) Start(ctx context.Context) error {
	c.factory = informers.NewSharedInformerFactory(c.client, 0)
	var syncs []cache.InformerSynced
	add := func(name, module string, inf cache.SharedIndexInformer, tf cache.TransformFunc) cache.SharedIndexInformer {
		if tf != nil {
			_ = inf.SetTransform(tf)
		}
		st := &informerState{name: name, module: module, inf: inf}
		c.infs = append(c.infs, st)
		_, _ = inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc:    func(any) { st.objects.Add(1); c.poke() },
			UpdateFunc: func(_, _ any) { c.poke() },
			DeleteFunc: func(any) { st.objects.Add(-1); c.poke() },
		})
		// A refused or failed list or watch is what "the agent cannot see this" looks like from the inside. The reflector
		// keeps retrying on its own; this only records what it ran into so the agent can say so.
		_ = inf.SetWatchErrorHandler(func(r *cache.Reflector, err error) {
			c.noteInformerError(st, err)
			cache.DefaultWatchErrorHandler(r, err)
		})
		syncs = append(syncs, inf.HasSynced)
		return inf
	}
	f := c.factory
	if c.tier >= 1 {
		c.nodes = add("nodes", ModInfrastructure, f.Core().V1().Nodes().Informer(), stripNode)
		c.sc = add("storageclasses.storage.k8s.io", ModInfrastructure, f.Storage().V1().StorageClasses().Informer(), stripStorageClass)
		c.ic = add("ingressclasses.networking.k8s.io", ModInfrastructure, f.Networking().V1().IngressClasses().Informer(), stripIngressClass)
	}
	// factoryFor maps each namespace this tier watches to the factory its informers come from. Cluster mode (the
	// default) watches one namespace, "" (cluster-wide), from the same factory as tier 0/1; namespaced mode watches
	// scope.Include, each from its own factory restricted to that namespace (informers.WithNamespace), because a
	// Role can only ever be asked to list one namespace at a time - there is no namespaced equivalent of a
	// cluster-wide list. Every one of these factories still needs its own Start(ctx.Done()) below.
	factoryFor := map[string]informers.SharedInformerFactory{}
	if c.tier >= 2 {
		var nss []string
		if c.namespaced {
			if c.scope == nil || len(c.scope.Include) == 0 {
				return fmt.Errorf("collect: rbac.mode=namespaced needs at least one namespace (Scope.Include); it cannot discover namespaces on its own, since that needs exactly the cluster-wide list permission this mode exists to avoid")
			}
			nss = c.scope.Include
			for _, ns := range nss {
				factoryFor[ns] = informers.NewSharedInformerFactoryWithOptions(c.client, 0, informers.WithNamespace(ns))
			}
		} else {
			nss = []string{""}
			factoryFor[""] = f
			// Namespaces are cluster-scoped: no Role, in any namespace, can grant reading them, so this is skipped
			// entirely in namespaced mode. c.ns stays nil there, exactly as it already does below tier 2, and every
			// consumer that reads it already handles that (namespace metadata becomes unavailable, not empty).
			c.ns = add("namespaces", ModServices, f.Core().V1().Namespaces().Informer(), stripNamespace)
		}
		// kind starts one real informer per namespace in nss (just one, cluster-wide, in cluster mode) and merges
		// them into a single itemLister, so every consumer of c.pods/c.svcs/... keeps working across a whole
		// cluster-wide watch or several per-namespace ones alike, unaware which one it is looking at. Each
		// namespace's informer is registered on its own with `add`, so a Role missing or refused in just one
		// namespace is reported (and recovered from, once fixed) on its own, not mistaken for every namespace.
		kind := func(name, module string, tf cache.TransformFunc, mk func(informers.SharedInformerFactory) cache.SharedIndexInformer) itemLister {
			infs := make([]cache.SharedIndexInformer, 0, len(nss))
			for _, ns := range nss {
				label := name
				if ns != "" {
					label = fmt.Sprintf("%s (namespace %s)", name, ns)
				}
				infs = append(infs, add(label, module, mk(factoryFor[ns]), tf))
			}
			if len(infs) == 1 {
				return singleLister{infs[0]}
			}
			return unionLister{infs}
		}
		c.pods = kind("pods", ModServices, stripPod, func(fa informers.SharedInformerFactory) cache.SharedIndexInformer { return fa.Core().V1().Pods().Informer() })
		c.rs = kind("replicasets.apps", ModServices, stripReplicaSet, func(fa informers.SharedInformerFactory) cache.SharedIndexInformer { return fa.Apps().V1().ReplicaSets().Informer() })
		c.deploys = kind("deployments.apps", ModServices, stripDeployment, func(fa informers.SharedInformerFactory) cache.SharedIndexInformer { return fa.Apps().V1().Deployments().Informer() })
		c.sts = kind("statefulsets.apps", ModServices, stripStatefulSet, func(fa informers.SharedInformerFactory) cache.SharedIndexInformer { return fa.Apps().V1().StatefulSets().Informer() })
		c.ds = kind("daemonsets.apps", ModServices, stripDaemonSet, func(fa informers.SharedInformerFactory) cache.SharedIndexInformer { return fa.Apps().V1().DaemonSets().Informer() })
		c.svcs = kind("services", ModServices, stripService, func(fa informers.SharedInformerFactory) cache.SharedIndexInformer { return fa.Core().V1().Services().Informer() })
		c.ings = kind("ingresses.networking.k8s.io", ModServices, stripIngress, func(fa informers.SharedInformerFactory) cache.SharedIndexInformer { return fa.Networking().V1().Ingresses().Informer() })

		// Optional extras. Each is probed with a one-item list first (once per namespace in namespaced mode, since
		// there is no cluster-wide list to ask instead) so a missing permission or an API the cluster does not
		// serve turns one module off instead of stalling every cache.
		c.optional = map[string]string{}
		if why := c.probeAcross(ctx, nss, func(ctx context.Context, ns string) error {
			if _, err := c.client.CoreV1().PersistentVolumeClaims(ns).List(ctx, metav1.ListOptions{Limit: 1}); err != nil {
				return err
			}
			if c.namespaced {
				// PersistentVolumes are cluster-scoped: no Role, in any namespace, can grant them. Unlike cluster
				// mode, this is not bundled with the PVC check above - a cluster that permits and serves PVCs
				// should show them here even though PVs are never available in this mode.
				return nil
			}
			_, err := c.client.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{Limit: 1})
			return err
		}); why != "" {
			c.optional[ModStorage] = why
		} else {
			c.pvcs = kind("persistentvolumeclaims", ModStorage, stripPVC, func(fa informers.SharedInformerFactory) cache.SharedIndexInformer { return fa.Core().V1().PersistentVolumeClaims().Informer() })
			if !c.namespaced {
				c.pvs = add("persistentvolumes", ModStorage, f.Core().V1().PersistentVolumes().Informer(), stripPV)
			}
		}
		if why := c.probeAcross(ctx, nss, func(ctx context.Context, ns string) error {
			if _, err := c.client.AutoscalingV2().HorizontalPodAutoscalers(ns).List(ctx, metav1.ListOptions{Limit: 1}); err != nil {
				return err
			}
			_, err := c.client.PolicyV1().PodDisruptionBudgets(ns).List(ctx, metav1.ListOptions{Limit: 1})
			return err
		}); why != "" {
			c.optional[ModScaling] = why
		} else {
			c.hpas = kind("horizontalpodautoscalers.autoscaling", ModScaling, stripHPA, func(fa informers.SharedInformerFactory) cache.SharedIndexInformer { return fa.Autoscaling().V2().HorizontalPodAutoscalers().Informer() })
			c.pdbs = kind("poddisruptionbudgets.policy", ModScaling, stripPDB, func(fa informers.SharedInformerFactory) cache.SharedIndexInformer { return fa.Policy().V1().PodDisruptionBudgets().Informer() })
		}
	}
	f.Start(ctx.Done())
	for ns, fa := range factoryFor {
		if ns != "" { // "" is f itself (cluster mode), already started above
			fa.Start(ctx.Done())
		}
	}
	// Wait for the first list of every kind, but not for ever: a kind the cluster refuses (403) never syncs, and an
	// agent that waited for it would say nothing at all. Whatever has not synced by then is reported as it is (see
	// Informers and Ready) and the agent carries on with what it can read.
	wait := c.SyncWait
	if wait <= 0 {
		wait = 30 * time.Second
	}
	deadline := time.After(wait)
	started := time.Now()
wait:
	for {
		all := true
		for _, h := range syncs {
			all = all && h()
		}
		if all || c.ready(time.Since(started) > 2*time.Second) {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			break wait
		case <-time.After(50 * time.Millisecond):
		}
	}
	if c.tier >= 2 {
		c.watchPolicy(ctx)
	}
	return nil
}

// noteInformerError remembers the last thing that went wrong with one informer.
func (c *Collector) noteInformerError(st *informerState, err error) {
	if err == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	st.errAt = time.Now()
	st.err = err.Error()
	st.forbid = apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err)
	st.verb = "list"
	if m := verbRe.FindStringSubmatch(st.err); m != nil {
		st.verb = m[1]
	}
}

var verbRe = regexp.MustCompile(`cannot (\w+) resource`)

// InformerStatus is what the agent can say about one watch.
type InformerStatus struct {
	Name, Module string
	Synced       bool
	Objects      int64
	// Err is the last list or watch error while it is still happening ("" otherwise). Forbidden says it was a refusal
	// (403 or 401) and Verb what was refused.
	Err       string
	Forbidden bool
	Verb      string
}

// Informers lists every watch this collector runs, in the order they were started.
func (c *Collector) Informers() []InformerStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.infoLocked()
}

// infoLocked is Informers for a caller that already holds c.mu.
func (c *Collector) infoLocked() []InformerStatus {
	out := make([]InformerStatus, 0, len(c.infs))
	for _, st := range c.infs {
		s := InformerStatus{Name: st.name, Module: st.module, Synced: st.inf.HasSynced(), Objects: max(st.objects.Load(), 0)}
		if st.err != "" && time.Since(st.errAt) < errFresh {
			s.Err, s.Forbidden, s.Verb = st.err, st.forbid, st.verb
		}
		out = append(out, s)
	}
	return out
}

// ready reports whether every watch has either listed once or been refused. While one is neither (the API server is not
// answering yet) the agent holds back its picture, so a half-empty one does not replace a good one on the server.
// A refusal counts only after `settled`, so that a first 403 is not taken for the last word.
func (c *Collector) ready(settled bool) bool {
	for _, i := range c.Informers() {
		if !i.Synced && !(i.Forbidden && settled) {
			return false
		}
	}
	return true
}

// Ready is true when nothing is still waiting for its first list: every watch has listed or been refused.
func (c *Collector) Ready() bool { return c.ready(true) }

// probe runs a cheap read and turns the failure into a short reason for the UI ("" means usable).
func (c *Collector) probe(ctx context.Context, fn func(context.Context) error) string {
	pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	err := fn(pctx)
	switch {
	case err == nil:
		return ""
	case apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err):
		return "not permitted by installed RBAC"
	case apierrors.IsNotFound(err):
		return "not served by this cluster"
	default:
		return "unavailable: " + err.Error()
	}
}

// probeAcross runs probe once per namespace in nss (just once, cluster-wide, when nss is [""] in cluster mode) and
// returns the first non-empty reason, so an optional module is turned off the same way whether the refusal or
// missing API shows up cluster-wide or in just one of several namespaces.
func (c *Collector) probeAcross(ctx context.Context, nss []string, fn func(context.Context, string) error) string {
	for _, ns := range nss {
		if why := c.probe(ctx, func(ctx context.Context) error { return fn(ctx, ns) }); why != "" {
			return why
		}
	}
	return ""
}

func (c *Collector) moduleStatus() []*continuumv1.ModuleStatus {
	// A module is only as good as the watches behind it: one that is refused, or has not listed yet, makes it an error
	// with the reason, not a quiet OK, because what it reports is then missing rather than empty.
	infos := c.infoLocked()
	broken := func(name string) string {
		var why []string
		for _, i := range infos {
			if i.Module != name {
				continue
			}
			switch {
			case i.Forbidden:
				why = append(why, fmt.Sprintf("the cluster refuses this agent permission to %s %s", i.Verb, i.Name))
			case !i.Synced && i.Err != "":
				why = append(why, fmt.Sprintf("cannot read %s: %s", i.Name, i.Err))
			case !i.Synced:
				why = append(why, fmt.Sprintf("%s has not been read yet", i.Name))
			}
		}
		return strings.Join(why, "; ")
	}
	st := func(name string, need int) *continuumv1.ModuleStatus {
		if c.tier < need {
			return &continuumv1.ModuleStatus{Name: name, State: continuumv1.ModuleStatus_SKIPPED, Reason: fmt.Sprintf("installed access is tier %d, this needs tier %d", c.tier, need)}
		}
		if why := broken(name); why != "" {
			return &continuumv1.ModuleStatus{Name: name, State: continuumv1.ModuleStatus_ERROR, Reason: why}
		}
		return &continuumv1.ModuleStatus{Name: name, State: continuumv1.ModuleStatus_OK}
	}
	mods := []*continuumv1.ModuleStatus{st(ModInfrastructure, 1), st(ModServices, 2)}
	if c.tier >= 2 {
		for _, name := range []string{ModStorage, ModScaling} {
			if why, off := c.optional[name]; off {
				mods = append(mods, &continuumv1.ModuleStatus{Name: name, State: continuumv1.ModuleStatus_SKIPPED, Reason: why})
			} else if why := broken(name); why != "" {
				mods = append(mods, &continuumv1.ModuleStatus{Name: name, State: continuumv1.ModuleStatus_ERROR, Reason: why})
			} else {
				mods = append(mods, &continuumv1.ModuleStatus{Name: name, State: continuumv1.ModuleStatus_OK})
			}
		}
	}
	return mods
}

func (c *Collector) Modules() []*continuumv1.ModuleStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	mods := c.moduleStatus()
	if c.meshMod != nil {
		mods = append(mods, c.meshMod)
	}
	return mods
}

// nsName is what a namespace override may name: a DNS label, and never one of the cluster's own system namespaces
// (they are always read, because that is how the CNI and ingress controller are recognised).
var nsName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

// ValidExclusion says whether an extra exclusion asked for by the server can be honoured, and why not if it cannot.
func ValidExclusion(ns string) (bool, string) {
	switch {
	case !nsName.MatchString(ns):
		return false, "is not a valid namespace name"
	case isSystem(ns):
		return false, "is a system namespace, which the agent always reads to recognise the cluster's own components"
	}
	return true, ""
}

// SetExtraExclude leaves further namespaces out of everything the collector reports, on top of the agent's own scope.
// It can only narrow: the agent's own rule is untouched, so passing nil restores exactly what the install allowed.
// The watches keep running (they are cluster-wide lists that cannot be narrowed to a namespace), but the excluded
// namespaces are dropped when the picture is built, before anything is sent, so they leave the cluster no more than
// the agent's own exclusions do. It returns whether the set changed.
func (c *Collector) SetExtraExclude(names []string) bool {
	next := make([]string, 0, len(names))
	seen := map[string]bool{}
	for _, n := range names {
		if ok, _ := ValidExclusion(n); ok && !seen[n] {
			seen[n] = true
			next = append(next, n)
		}
	}
	sort.Strings(next)
	c.mu.Lock()
	changed := strings.Join(next, ",") != strings.Join(c.extraEx, ",")
	c.extraEx = next
	c.mu.Unlock()
	if changed {
		c.poke()
	}
	return changed
}

// scopeNow is the rule in force: the agent's own scope with the server's extra exclusions added.
func (c *Collector) scopeNow() *Scope {
	c.mu.Lock()
	extra := c.extraEx
	c.mu.Unlock()
	if len(extra) == 0 {
		return c.scope
	}
	n := &Scope{}
	if c.scope != nil {
		cp := *c.scope
		n = &cp
	}
	n.Exclude = append(append([]string{}, n.Exclude...), extra...)
	sort.Strings(n.Exclude)
	return n
}

// OwnScope is the rule the agent's install set, without anything the server added.
func (c *Collector) OwnScope() *Scope { return c.scope }

// ScopeSummary counts what the scope in force leaves out, whether or not the namespaces are read (below tier 2 they are
// not, and only the rule is known).
func (c *Collector) ScopeSummary() *continuumv1.ScopeFacts {
	sc := c.scopeNow()
	if c.ns == nil {
		return &continuumv1.ScopeFacts{Description: sc.Public()}
	}
	if f := c.scopeFacts(c.visible()); f != nil {
		return f
	}
	f := &continuumv1.ScopeFacts{Description: sc.Public()}
	each(c.ns.GetStore().List(), func(n *corev1.Namespace) {
		if !isSystem(n.Name) {
			f.NamespacesTotal++
			f.NamespacesInScope++
		}
	})
	return f
}

// OptionalOff lists the optional modules that were switched off at start, with why ("not permitted by installed RBAC",
// "not served by this cluster", "unavailable: ...").
func (c *Collector) OptionalOff() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]string, len(c.optional))
	for k, v := range c.optional {
		out[k] = v
	}
	return out
}

// Nodes is how many nodes the agent can see (0 below tier 1).
func (c *Collector) Nodes() int {
	if c.nodes == nil {
		return 0
	}
	return len(c.nodes.GetStore().ListKeys())
}

// ---- transforms: keep only what discovery needs ----
