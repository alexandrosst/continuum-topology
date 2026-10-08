package chart

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	sigsyaml "sigs.k8s.io/yaml"
)

// Helm's `lookup` is how the charts see what is already in the cluster (a mounted Secret's contents, a workload's live
// replica count). `helm template` has no cluster, so it answers every lookup with nothing; only
// `helm template --dry-run=server` asks one. These tests give it a stand-in API server that serves just enough of
// Kubernetes - discovery, and Secrets by name - to make a lookup return real data, so the render-time behaviour that
// depends on it is tested rather than assumed.

// fakeKubernetes is the stand-in API server: Secrets by namespace/name -> key -> value (plaintext; it serves them
// base64-encoded as the real API does).
type fakeKubernetes struct {
	mu      sync.Mutex
	secrets map[string]map[string]string
	gets    []string
}

// set replaces the contents of a Secret in the default namespace.
func (f *fakeKubernetes) set(name string, data map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.secrets["default/"+name] = data
}

func (f *fakeKubernetes) lookups() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.gets...)
}

type fakeResource struct {
	name       string
	kind       string
	namespaced bool
}

// The kinds the three charts render. Helm resolves each through discovery before it will look at, or build, one.
var fakeDiscovery = map[string][]fakeResource{
	"v1":                   {{"secrets", "Secret", true}, {"configmaps", "ConfigMap", true}, {"services", "Service", true}, {"serviceaccounts", "ServiceAccount", true}},
	"apps/v1":              {{"deployments", "Deployment", true}, {"daemonsets", "DaemonSet", true}, {"statefulsets", "StatefulSet", true}},
	"networking.k8s.io/v1": {{"networkpolicies", "NetworkPolicy", true}},
	"rbac.authorization.k8s.io/v1": {{"clusterroles", "ClusterRole", false}, {"clusterrolebindings", "ClusterRoleBinding", false},
		{"roles", "Role", true}, {"rolebindings", "RoleBinding", true}},
}

var secretPath = regexp.MustCompile(`^/api/v1/namespaces/([^/]+)/secrets/([^/]+)$`)

func (f *fakeKubernetes) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	reply := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	list := func(gv string) map[string]any {
		var rs []map[string]any
		for _, x := range fakeDiscovery[gv] {
			rs = append(rs, map[string]any{"name": x.name, "kind": x.kind, "namespaced": x.namespaced, "singularName": "", "verbs": []string{"get", "list"}})
		}
		return map[string]any{"kind": "APIResourceList", "apiVersion": "v1", "groupVersion": gv, "resources": rs}
	}
	switch p := r.URL.Path; {
	case p == "/version":
		reply(200, map[string]string{"major": "1", "minor": "30", "gitVersion": "v1.30.0"})
	case p == "/api":
		reply(200, map[string]any{"kind": "APIVersions", "versions": []string{"v1"}})
	case p == "/apis":
		var groups []map[string]any
		for gv := range fakeDiscovery {
			if g, v, ok := strings.Cut(gv, "/"); ok {
				ver := map[string]string{"groupVersion": gv, "version": v}
				groups = append(groups, map[string]any{"name": g, "versions": []any{ver}, "preferredVersion": ver})
			}
		}
		reply(200, map[string]any{"kind": "APIGroupList", "apiVersion": "v1", "groups": groups})
	case p == "/api/v1":
		reply(200, list("v1"))
	case strings.HasPrefix(p, "/apis/") && fakeDiscovery[strings.TrimPrefix(p, "/apis/")] != nil:
		reply(200, list(strings.TrimPrefix(p, "/apis/")))
	case secretPath.MatchString(p):
		m := secretPath.FindStringSubmatch(p)
		f.mu.Lock()
		f.gets = append(f.gets, m[1]+"/"+m[2])
		data, ok := f.secrets[m[1]+"/"+m[2]]
		f.mu.Unlock()
		if !ok {
			reply(404, map[string]any{"kind": "Status", "apiVersion": "v1", "status": "Failure", "reason": "NotFound", "code": 404, "message": "secrets \"" + m[2] + "\" not found"})
			return
		}
		enc := map[string]string{}
		for k, v := range data {
			enc[k] = base64.StdEncoding.EncodeToString([]byte(v))
		}
		reply(200, map[string]any{"kind": "Secret", "apiVersion": "v1", "metadata": map[string]string{"name": m[2], "namespace": m[1]}, "data": enc})
	default:
		reply(404, map[string]any{"kind": "Status", "apiVersion": "v1", "status": "Failure", "reason": "NotFound", "code": 404, "message": "not found"})
	}
}

// startFakeKubernetes serves it and returns the kubeconfig file that points at it.
func startFakeKubernetes(t *testing.T) (*fakeKubernetes, string) {
	t.Helper()
	f := &fakeKubernetes{secrets: map[string]map[string]string{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	kc := filepath.Join(t.TempDir(), "kubeconfig")
	cfg := "apiVersion: v1\nkind: Config\nclusters: [{name: f, cluster: {server: \"" + srv.URL + "\"}}]\nusers: [{name: u, user: {}}]\ncontexts: [{name: c, context: {cluster: f, user: u}}]\ncurrent-context: c\n"
	if err := os.WriteFile(kc, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return f, kc
}

// helmTemplateInCluster renders c the way `helm upgrade` does, asking the (fake) cluster at kubeconfig for every
// lookup. Without a kubeconfig it is a plain offline `helm template`, which is what Argo CD and Flux run.
func helmTemplateInCluster(t *testing.T, c *Chart, kubeconfig string, args ...string) string {
	t.Helper()
	h, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed")
	}
	b, err := c.Package()
	if err != nil {
		t.Fatal(err)
	}
	tgz := filepath.Join(t.TempDir(), c.Filename())
	if err := os.WriteFile(tgz, b, 0o644); err != nil {
		t.Fatal(err)
	}
	full := append([]string{"template", "rel", tgz}, args...)
	cmd := exec.Command(h, full...)
	if kubeconfig != "" {
		cmd = exec.Command(h, append(full, "--dry-run=server", "--disable-openapi-validation")...)
		cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helm %v: %v\n%s", full, err, out)
	}
	return string(out)
}

// podAnnotations returns the annotations of every Deployment and DaemonSet pod template in a render, by name.
func podAnnotations(t *testing.T, rendered string) map[string]map[string]string {
	t.Helper()
	out := map[string]map[string]string{}
	for _, doc := range strings.Split(rendered, "\n---") {
		var o struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				Template struct {
					Metadata struct {
						Annotations map[string]string `json:"annotations"`
					} `json:"metadata"`
				} `json:"template"`
			} `json:"spec"`
		}
		if err := sigsyaml.Unmarshal([]byte(doc), &o); err != nil {
			t.Fatalf("not YAML: %v\n%s", err, doc)
		}
		if o.Kind == "Deployment" || o.Kind == "DaemonSet" {
			out[o.Metadata.Name] = o.Spec.Template.Metadata.Annotations
		}
	}
	return out
}

// The regional operator restarts when a TLS Secret it mounts changes, so that running the install command again
// after renewing a certificate takes effect at once; where Helm cannot look at the cluster it renders no checksum
// and never fails.
func TestRegionalOperatorRestartsWhenAMountedTLSSecretChanges(t *testing.T) {
	k, kc := startFakeKubernetes(t)
	k.set("op-receiver-tls", map[string]string{"tls.crt": "cert-1", "tls.key": "key-1", "ca.crt": "ca-1"})
	k.set("op-client-tls", map[string]string{"tls.crt": "client-1", "tls.key": "ckey-1", "ca.crt": "ca-1"})
	k.set("unrelated", map[string]string{"x": "1"})
	args := []string{"--set", "export.otlp.endpoint=collector.example:4317",
		"--set", "receiver.tls.enabled=true", "--set", "receiver.tls.secretName=op-receiver-tls",
		"--set", "export.otlp.tls.mtls.enabled=true", "--set", "export.otlp.tls.mtls.secretName=op-client-tls"}
	const name = "rel-regional-operator"
	sum := func(extra ...string) (string, map[string]string) {
		a := podAnnotations(t, helmTemplateInCluster(t, RegionalOperator, kc, append(append([]string{}, args...), extra...)...))[name]
		return a["checksum/mtls"], a
	}

	first, ann := sum()
	if first == "" {
		t.Fatalf("no checksum/mtls annotation although the Secrets exist: %v (lookups: %v)", ann, k.lookups())
	}
	if again, _ := sum(); again != first {
		t.Errorf("the checksum is not stable: %s then %s", first, again)
	}

	k.set("unrelated", map[string]string{"x": "2"})
	if got, _ := sum(); got != first {
		t.Errorf("a Secret the pod does not mount changed the checksum")
	}
	k.set("op-receiver-tls", map[string]string{"tls.crt": "cert-2", "tls.key": "key-2", "ca.crt": "ca-1"})
	second, ann := sum()
	if second == first || second == "" {
		t.Errorf("renewing the receiver certificate did not change the checksum: %q -> %q", first, second)
	}
	if ann["checksum/config"] == "" {
		t.Errorf("checksum/config is gone: %v", ann)
	}
	k.set("op-client-tls", map[string]string{"tls.crt": "client-2", "tls.key": "ckey-2", "ca.crt": "ca-1"})
	if third, _ := sum(); third == second || third == "" {
		t.Errorf("renewing the client certificate did not change the checksum: %q -> %q", second, third)
	}

	// The opt-out must not even ask the cluster: that is its point for an account that may not read Secrets.
	before := len(k.lookups())
	if got, _ := sum("--set", "rolloutOnSecretChange=false"); got != "" {
		t.Errorf("rolloutOnSecretChange=false still rendered %q", got)
	}
	if len(k.lookups()) != before {
		t.Errorf("rolloutOnSecretChange=false still looked at Secrets: %v", k.lookups()[before:])
	}

	// Offline (helm template, Argo CD, Flux) and a Secret that is not there yet both render, with no checksum.
	if a := podAnnotations(t, helmTemplateInCluster(t, RegionalOperator, "", args...))[name]; a["checksum/mtls"] != "" || a["checksum/config"] == "" {
		t.Errorf("offline render annotations = %v", a)
	}
	empty, ekc := startFakeKubernetes(t)
	_ = empty
	if a := podAnnotations(t, helmTemplateInCluster(t, RegionalOperator, ekc, args...))[name]; a["checksum/mtls"] != "" || a["checksum/config"] == "" {
		t.Errorf("render against a cluster without the Secrets: %v", a)
	}
}

// The heartbeat's CA Secret is mounted too, so it is part of the checksum when the heartbeat uses one.
func TestRegionalOperatorChecksumCoversTheHeartbeatCA(t *testing.T) {
	k, kc := startFakeKubernetes(t)
	k.set("hb-ca", map[string]string{"ca.crt": "ca-1"})
	args := []string{"--set", "export.otlp.endpoint=collector.example:4317",
		"--set", "heartbeat.enabled=true", "--set", "heartbeat.url=https://continuum.example.com/api/v1/operator-heartbeat",
		"--set", "heartbeat.auth.secretName=hb-auth", "--set", "heartbeat.tls.caSecretName=hb-ca"}
	read := func() string {
		return podAnnotations(t, helmTemplateInCluster(t, RegionalOperator, kc, args...))["rel-regional-operator"]["checksum/mtls"]
	}
	first := read()
	if first == "" {
		t.Fatal("the heartbeat CA Secret is not in the checksum")
	}
	k.set("hb-ca", map[string]string{"ca.crt": "ca-2"})
	if read() == first {
		t.Error("replacing the heartbeat CA did not change the checksum")
	}
}

// Both agent collectors mount the client certificate, so both restart when its CA is replaced; a collector that does not
// mount it (a route that only the cluster collector uses) is not restarted by it. A renewed certificate and key restart
// nothing: the agent renews them in place.
func TestAgentCollectorsRestartWhenTheClientCertificateChanges(t *testing.T) {
	k, kc := startFakeKubernetes(t)
	k.set("op-client-tls", map[string]string{"tls.crt": "client-1", "tls.key": "ckey-1", "ca.crt": "ca-1"})
	k.set("logs-client-tls", map[string]string{"tls.crt": "l-1", "tls.key": "lk-1", "ca.crt": "ca-1"})
	args := append(append([]string{}, baseSet...),
		"--set", "telemetry.export.otlp.endpoint=collector.example:4317",
		"--set", "telemetry.export.otlp.tls.mtls.enabled=true", "--set", "telemetry.export.otlp.tls.mtls.secretName=op-client-tls",
		"--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.kubernetesState.metrics.enabled=true",
		"--set", "telemetry.kubernetesEvents.logs.enabled=true",
		"--set", "telemetry.export.routes.logs.endpoint=loki.example:4317",
		"--set", "telemetry.export.routes.logs.tls.mtls.enabled=true", "--set", "telemetry.export.routes.logs.tls.mtls.secretName=logs-client-tls")
	read := func(extra ...string) map[string]map[string]string {
		return podAnnotations(t, helmTemplateInCluster(t, Agent, kc, append(append([]string{}, args...), extra...)...))
	}
	const host, cluster = "continuum-telemetry-host", "continuum-telemetry-cluster"
	first := read()
	if first[host]["checksum/mtls"] == "" || first[cluster]["checksum/mtls"] == "" {
		t.Fatalf("a collector has no checksum/mtls: host %q cluster %q", first[host]["checksum/mtls"], first[cluster]["checksum/mtls"])
	}
	if first[host]["checksum/mtls"] == first[cluster]["checksum/mtls"] {
		t.Errorf("the cluster collector also mounts the logs route's certificate, the host collector does not: the digests should differ")
	}

	// The agent renews the client certificates itself (CONTINUUM_RENEW_SECRETS), every ~10 days: a new tls.crt and
	// tls.key must not restart the collectors, they re-read the files. A new CA must, it is not re-read.
	k.set("logs-client-tls", map[string]string{"tls.crt": "l-2", "tls.key": "lk-2", "ca.crt": "ca-1"})
	k.set("op-client-tls", map[string]string{"tls.crt": "client-2", "tls.key": "ckey-2", "ca.crt": "ca-1"})
	renewed := read()
	if renewed[host]["checksum/mtls"] != first[host]["checksum/mtls"] || renewed[cluster]["checksum/mtls"] != first[cluster]["checksum/mtls"] {
		t.Errorf("a renewed certificate and key restart the collectors: the agent renews them in place and the collectors re-read them")
	}
	k.set("logs-client-tls", map[string]string{"tls.crt": "l-2", "tls.key": "lk-2", "ca.crt": "ca-2"})
	second := read()
	if second[host]["checksum/mtls"] != first[host]["checksum/mtls"] {
		t.Errorf("the host collector does not mount the logs route's Secret but its checksum changed")
	}
	if second[cluster]["checksum/mtls"] == first[cluster]["checksum/mtls"] {
		t.Errorf("replacing the logs route's CA did not change the cluster collector's checksum")
	}
	k.set("op-client-tls", map[string]string{"tls.crt": "client-2", "tls.key": "ckey-2", "ca.crt": "ca-2"})
	third := read()
	if third[host]["checksum/mtls"] == second[host]["checksum/mtls"] || third[cluster]["checksum/mtls"] == second[cluster]["checksum/mtls"] {
		t.Errorf("replacing the default CA must change both collectors' checksums")
	}

	if got := read("--set", "telemetry.rolloutOnSecretChange=false"); got[host]["checksum/mtls"] != "" || got[cluster]["checksum/mtls"] != "" {
		t.Errorf("telemetry.rolloutOnSecretChange=false still rendered a checksum: %v", got)
	}
	off := podAnnotations(t, helmTemplateInCluster(t, Agent, "", args...))
	if off[host]["checksum/mtls"] != "" || off[cluster]["checksum/mtls"] != "" || off[host]["checksum/config"] == "" {
		t.Errorf("offline render annotations: %v", off)
	}
}
