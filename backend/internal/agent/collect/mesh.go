package collect

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/facts"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/rest"
)

// ModMesh is the module name shown on the Agents page. It appears only when a mesh was found.
const ModMesh = "mesh"

// Service-mesh detection. Everything below works from what Kubernetes already tells the agent: namespace and
// workload markers, the container names in pods, the mesh's own workloads, and (for Istio) its PeerAuthentication
// policy objects. Nothing is read from the proxies, so the result says what the mesh is *configured* to do, not
// what a proxy did with a particular packet.

// proxyContainers are the container names that mean "a mesh proxy runs in this pod".
var proxyContainers = map[string]string{
	"istio-proxy":      facts.MeshIstio,
	"linkerd-proxy":    facts.MeshLinkerd,
	"consul-dataplane": facts.MeshConsul,
	"envoy-sidecar":    facts.MeshConsul,
	"kuma-sidecar":     facts.MeshKuma,
}

var linkerdNamespaces = map[string]bool{"linkerd": true, "linkerd-viz": true, "linkerd-jaeger": true, "linkerd-multicluster": true}

// controlPlaneOf names the mesh a workload is the control plane (or a gateway) of, or "".
func controlPlaneOf(w *continuumv1.WorkloadFacts, tmplLabels map[string]string) string {
	n := w.Name
	switch {
	case n == "istiod" || strings.HasPrefix(n, "istiod-"), tmplLabels["app"] == "istiod", tmplLabels["istio"] == "pilot",
		tmplLabels["istio"] == "ingressgateway", tmplLabels["istio"] == "egressgateway",
		n == "istio-ingressgateway", n == "istio-egressgateway",
		w.Kind == "DaemonSet" && (n == "ztunnel" || n == "istio-cni-node"):
		return facts.MeshIstio
	case linkerdNamespaces[w.Namespace] && (strings.HasPrefix(n, "linkerd-") || w.Namespace != "linkerd"):
		return facts.MeshLinkerd
	}
	return ""
}

func portList(dir string, v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, dir+":"+p)
		}
	}
	return out
}

// excludedPorts reads the port lists a workload keeps out of its proxy (the traffic on them is not intercepted).
func excludedPorts(ann map[string]string) []string {
	var out []string
	out = append(out, portList("in", ann["traffic.sidecar.istio.io/excludeInboundPorts"])...)
	out = append(out, portList("out", ann["traffic.sidecar.istio.io/excludeOutboundPorts"])...)
	out = append(out, portList("in", ann["config.linkerd.io/skip-inbound-ports"])...)
	out = append(out, portList("out", ann["config.linkerd.io/skip-outbound-ports"])...)
	sort.Strings(out)
	return out
}

// workloadMesh works out one workload's relation to the mesh. pods are its pods (as far as they were observed).
func workloadMesh(w *wl, nsLabels, nsAnn map[string]string) *continuumv1.WorkloadMesh {
	f := w.facts
	if k := controlPlaneOf(f, w.tmplLabels); k != "" {
		return &continuumv1.WorkloadMesh{Mesh: k, ControlPlane: true, Source: "workload"}
	}
	// 1. What the pods show.
	observed, proxy := "", ""
	for _, p := range w.pods {
		if strings.EqualFold(p.Annotations["ambient.istio.io/redirection"], "enabled") {
			observed, proxy = facts.MeshIstio, facts.ProxyAmbient
			break
		}
		for _, ct := range p.Spec.Containers {
			if k, ok := proxyContainers[ct.Name]; ok {
				observed, proxy = k, facts.ProxySidecar
			}
		}
		if observed != "" {
			break
		}
	}
	ann := merge(w.tmplAnn, f.Annotations)
	if observed != "" {
		return &continuumv1.WorkloadMesh{Mesh: observed, Proxy: proxy, ExcludedPorts: excludedPorts(ann), Source: "pods"}
	}
	// 2. What the workload itself asks for.
	inj := strings.ToLower(firstOf(w.tmplLabels["sidecar.istio.io/inject"], w.tmplAnn["sidecar.istio.io/inject"]))
	lk := strings.ToLower(w.tmplAnn["linkerd.io/inject"])
	switch {
	case inj == "false":
		return &continuumv1.WorkloadMesh{Mesh: facts.MeshIstio, Bypass: true, Source: "workload"}
	case lk == "disabled":
		return &continuumv1.WorkloadMesh{Mesh: facts.MeshLinkerd, Bypass: true, Source: "workload"}
	}
	// 3. What its namespace asks for. Pods that exist without a proxy are reported as not proxied; a workload with no
	// pods yet is shown as it will be.
	mesh, want, off := facts.NamespaceMesh(nsLabels, nsAnn)
	switch {
	case inj == "true":
		mesh, want, off = facts.MeshIstio, facts.ProxySidecar, false
	case lk == "enabled":
		mesh, want, off = facts.MeshLinkerd, facts.ProxySidecar, false
	}
	if mesh == "" {
		return nil
	}
	if off {
		return &continuumv1.WorkloadMesh{Mesh: mesh, Bypass: true, Source: "namespace"}
	}
	m := &continuumv1.WorkloadMesh{Mesh: mesh, ExcludedPorts: excludedPorts(ann), Source: "namespace"}
	if len(w.pods) == 0 {
		m.Proxy = want
	}
	return m
}

func firstOf(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func merge(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range b {
		out[k] = v
	}
	for k, v := range a {
		out[k] = v
	}
	return out
}

// imageTag is the version part of an image reference ("" for latest, a digest or no tag).
func imageTag(image string) string {
	if i := strings.Index(image, "@"); i >= 0 {
		image = image[:i]
	}
	slash := strings.LastIndex(image, "/")
	colon := strings.LastIndex(image, ":")
	if colon <= slash {
		return ""
	}
	t := image[colon+1:]
	if t == "latest" {
		return ""
	}
	return strings.TrimPrefix(strings.TrimPrefix(t, "stable-"), "v")
}

// detectMesh sets the mesh markers on every workload and describes the mesh they add up to. vis limits the
// per-namespace policy to namespaces the agent may report.
func (c *Collector) detectMesh(all map[string]*wl, vis func(string) bool) *continuumv1.MeshFacts {
	nsLabels, nsAnn := map[string]map[string]string{}, map[string]map[string]string{}
	if c.ns != nil {
		each(c.ns.GetStore().List(), func(n *corev1.Namespace) { nsLabels[n.Name], nsAnn[n.Name] = n.Labels, n.Annotations })
	}
	counts := map[string]int{}
	cp := map[string][]string{}
	var cpFacts []*continuumv1.WorkloadFacts
	ztunnel := false
	for _, w := range all {
		m := workloadMesh(w, nsLabels[w.facts.Namespace], nsAnn[w.facts.Namespace])
		w.facts.Mesh = m
		if m == nil {
			continue
		}
		if m.ControlPlane {
			cp[m.Mesh] = append(cp[m.Mesh], w.facts.Key)
			cpFacts = append(cpFacts, w.facts)
			if w.facts.Name == "ztunnel" {
				ztunnel = true
			}
		} else if m.Proxy != "" {
			counts[m.Mesh]++
		}
	}
	// One mesh per cluster is reported: the one with a control plane here, else the one with the most proxies.
	kind := ""
	for _, k := range []string{facts.MeshIstio, facts.MeshLinkerd} {
		if len(cp[k]) > 0 {
			kind = k
			break
		}
	}
	if kind == "" {
		best := 0
		for k, n := range counts {
			if n > best || n == best && k < kind {
				kind, best = k, n
			}
		}
	}
	if kind == "" {
		return nil
	}
	mf := &continuumv1.MeshFacts{Kind: kind, Mode: facts.ProxySidecar, ControlPlane: cp[kind]}
	sort.Strings(mf.ControlPlane)
	if kind == facts.MeshIstio && ztunnel {
		mf.Mode = facts.ProxyAmbient
	}
	for _, f := range cpFacts {
		if f.Mesh.Mesh == kind && len(f.Images) > 0 {
			if v := imageTag(f.Images[0].Image); v != "" {
				mf.Version = v
				break
			}
		}
	}
	switch kind {
	case facts.MeshLinkerd:
		mf.Mtls, mf.PolicyRead = "automatic", true // every meshed connection is mutual TLS unless a port is skipped
	case facts.MeshIstio:
		c.istioPolicy(mf, cp[kind], all, vis)
	default:
		mf.Mtls, mf.PolicyNote = "unknown", "this mesh's policy objects are not read"
	}
	return mf
}

// ---- Istio PeerAuthentication ----

type peerAuth struct {
	ns       string
	selector bool
	mode     string // strict | permissive | disabled | unset
}

type policyCache struct {
	mu    sync.Mutex
	items []peerAuth
	read  bool
	note  string
}

// fetchPeerAuth lists PeerAuthentication objects. It is a field so tests can stand in for the API server.
type policyFetcher func(ctx context.Context) ([]peerAuth, error)

func (c *Collector) defaultFetchPeerAuth(ctx context.Context) ([]peerAuth, error) {
	rc := c.client.Discovery().RESTClient()
	if r, ok := rc.(*rest.RESTClient); rc == nil || ok && r == nil {
		return nil, errNoREST // a fake client in tests has no REST client
	}
	// Cluster mode's ClusterRole grants get/list on peerauthentications cluster-wide, so one request covers every
	// namespace. Namespaced mode's Role grants the same verbs but only inside each namespace in scope.Include (the
	// chart's rbac.yaml), so there is no cluster-wide path to ask instead: fetch each namespace and concatenate.
	if c.namespaced {
		var out []peerAuth
		for _, ns := range c.scope.Include {
			raw, err := rc.Get().AbsPath("/apis/security.istio.io/v1/namespaces/" + ns + "/peerauthentications").DoRaw(ctx)
			if err != nil {
				return nil, err
			}
			items, err := parsePeerAuthList(raw)
			if err != nil {
				return nil, err
			}
			out = append(out, items...)
		}
		return out, nil
	}
	raw, err := rc.Get().AbsPath("/apis/security.istio.io/v1/peerauthentications").DoRaw(ctx)
	if err != nil {
		return nil, err
	}
	return parsePeerAuthList(raw)
}

func parsePeerAuthList(raw []byte) ([]peerAuth, error) {
	var doc struct {
		Items []struct {
			Metadata struct{ Namespace string } `json:"metadata"`
			Spec     struct {
				Selector *struct{} `json:"selector"`
				Mtls     *struct {
					Mode string `json:"mode"`
				} `json:"mtls"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	var out []peerAuth
	for _, it := range doc.Items {
		pa := peerAuth{ns: it.Metadata.Namespace, selector: it.Spec.Selector != nil, mode: "unset"}
		if it.Spec.Mtls != nil {
			switch strings.ToUpper(it.Spec.Mtls.Mode) {
			case "STRICT":
				pa.mode = "strict"
			case "PERMISSIVE":
				pa.mode = "permissive"
			case "DISABLE":
				pa.mode = "disabled"
			}
		}
		out = append(out, pa)
	}
	return out, nil
}

type stringErr string

func (e stringErr) Error() string { return string(e) }

const errNoREST = stringErr("no REST client")

// refreshPolicy reads the policy objects once and reports whether anything changed.
func (c *Collector) refreshPolicy(ctx context.Context) bool {
	fetch := c.fetchPolicy
	if fetch == nil {
		fetch = c.defaultFetchPeerAuth
	}
	pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	items, err := fetch(pctx)
	note := ""
	switch {
	case err == nil:
	case apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err):
		note = "not permitted by installed RBAC: reinstall or upgrade the chart with access.tier=2"
	case apierrors.IsNotFound(err):
		note = "the cluster does not serve Istio's PeerAuthentication"
	default:
		note = "unavailable: " + err.Error()
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ns+"/"+items[i].mode < items[j].ns+"/"+items[j].mode })
	c.pol.mu.Lock()
	defer c.pol.mu.Unlock()
	changed := c.pol.read != (err == nil) || c.pol.note != note || len(items) != len(c.pol.items)
	if !changed {
		for i := range items {
			if items[i] != c.pol.items[i] {
				changed = true
				break
			}
		}
	}
	c.pol.items, c.pol.read, c.pol.note = items, err == nil, note
	return changed
}

// refreshPolicySafely runs one refreshPolicy and recovers a panic from it individually, so a single bad refresh
// never takes anything down with it - see watchPolicy for why that distinction matters. It is a named method
// (rather than a closure inline in watchPolicy) so a test can call it directly without waiting on a 2-minute
// ticker.
func (c *Collector) refreshPolicySafely(ctx context.Context) {
	defer func() {
		if p := recover(); p != nil && c.OnPanic != nil {
			c.OnPanic("service-mesh policy", p)
		}
	}()
	if c.refreshPolicy(ctx) {
		c.poke()
	}
}

// watchPolicy keeps the policy view fresh: PeerAuthentication objects are not in the informer set (they are a
// custom resource), so they are listed every couple of minutes.
//
// Each refresh (the first one, run synchronously here, and every ticked one) is recovered individually: a panic
// during one refresh is logged and skipped, but the ticker keeps running and tries again next tick. An earlier
// version recovered once around the whole ticker loop, which meant a single bad refresh silently ended mesh
// policy collection for the rest of the process - the same one-shot-recovery mistake as the agent's stream
// receive loop (see stream.go's "receive loop" goroutine), fixed the same way: recover the unit of work that can
// panic, not the loop that must keep running.
func (c *Collector) watchPolicy(ctx context.Context) {
	c.refreshPolicySafely(ctx)
	go func() {
		t := time.NewTicker(2 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				c.refreshPolicySafely(ctx)
			}
		}
	}()
}

// istioPolicy sets the mesh-wide and per-namespace mutual-TLS mode from the PeerAuthentication objects. A policy with a
// selector applies to some workloads only, so it does not change what is said about the whole namespace.
func (c *Collector) istioPolicy(mf *continuumv1.MeshFacts, controlPlane []string, all map[string]*wl, vis func(string) bool) {
	c.pol.mu.Lock()
	items, read, note := c.pol.items, c.pol.read, c.pol.note
	c.pol.mu.Unlock()
	mf.PolicyRead, mf.PolicyNote = read, note
	if !read {
		mf.Mtls = "unknown"
		return
	}
	root := "istio-system"
	if len(controlPlane) > 0 {
		root = strings.SplitN(controlPlane[0], "/", 2)[0]
	}
	mf.Mtls = "permissive" // Istio's default: mutual TLS between sidecars, plaintext still accepted
	nsMode := map[string]string{}
	for _, pa := range items {
		if pa.selector || pa.mode == "unset" {
			continue
		}
		if pa.ns == root {
			mf.Mtls = pa.mode
		} else if vis(pa.ns) {
			nsMode[pa.ns] = pa.mode
		}
	}
	for ns, m := range nsMode {
		if m != mf.Mtls {
			if mf.NamespaceMtls == nil {
				mf.NamespaceMtls = map[string]string{}
			}
			mf.NamespaceMtls[ns] = m
		}
	}
}
