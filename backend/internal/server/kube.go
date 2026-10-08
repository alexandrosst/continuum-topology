package server

import (
	"bytes"
	"cmp"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// KubeAPI is the whole of what this server may do with the Kubernetes API, and it is deliberately tiny: read the
// replica counts of the FUSION workloads, scale them, put certificates into the central gateway's Secret, and read
// (never change) the few objects that explain why a store is not starting or where the gateway is reachable. The chart's
// Role (deploy/helm/continuum-server/templates/fusion-rbac.yaml) grants exactly these calls on exactly these named
// objects and nothing more - there is no list, no create, no delete, no way to read a Secret back.
type KubeAPI interface {
	// Workload reads a StatefulSet or Deployment ("statefulsets", "deployments") by name.
	Workload(ctx context.Context, kind, name string) (KubeWorkload, error)
	// Scale sets a workload's replica count through its scale subresource.
	Scale(ctx context.Context, kind, name string, replicas int) error
	// PatchSecret merges the given keys into an existing Secret's data.
	PatchSecret(ctx context.Context, name string, data map[string][]byte) error
	// Pod reads one pod by name, for the reason it is not ready. An older Role has no such grant: the call is then
	// ErrKubeForbidden and the switch simply goes without a reason.
	Pod(ctx context.Context, name string) (KubePod, error)
	// ClaimPhase reads a PersistentVolumeClaim's phase ("Bound", "Pending", ...).
	ClaimPhase(ctx context.Context, name string) (string, error)
	// ServiceAddress is the host:port other clusters can dial the named Service at, or "" while it has none (a
	// ClusterIP Service, or a LoadBalancer the cloud has not given an address yet).
	ServiceAddress(ctx context.Context, name string) (string, error)
}

// KubeWorkload is the part of a workload's state the switch shows.
type KubeWorkload struct {
	Desired int // spec.replicas
	Ready   int // status.readyReplicas
}

// KubePod is the part of a pod's state that says why it is not ready.
type KubePod struct {
	Phase string // Pending, Running, ...
	// WaitingReason is the Kubernetes reason a container is waiting ("ImagePullBackOff", "CrashLoopBackOff",
	// "ContainerCreating", ...), init containers first; "" when none is.
	WaitingReason string
	// LastTerminatedReason is why that container's previous run ended ("OOMKilled", "Error", ...).
	LastTerminatedReason string
	// Unschedulable is PodScheduled=False: no node was found for it. ScheduleMessage is the scheduler's own sentence.
	Unschedulable   bool
	ScheduleMessage string
}

// Why a Kubernetes call failed, in the three ways the switch tells apart.
var (
	ErrKubeNotFound  = errors.New("kubernetes: object not found")
	ErrKubeForbidden = errors.New("kubernetes: this server is not allowed to do that")
)

// kubeDenied is ErrKubeForbidden with the API server's own sentence, which tells a refusal by the Role from a refusal by
// the cluster's admission (a volume whose storage class cannot grow).
type kubeDenied struct{ say string }

func (e *kubeDenied) Error() string { return ErrKubeForbidden.Error() }
func (e *kubeDenied) Unwrap() error { return ErrKubeForbidden }

// kubeClient talks to the API server over plain net/http: four calls do not need client-go and its dependency tree.
type kubeClient struct {
	base      string
	namespace string
	http      *http.Client
	token     func() (string, error)
}

// saDir is where the pod's service account is mounted (a variable only so a test can point it at a directory).
var saDir = "/var/run/secrets/kubernetes.io/serviceaccount"

// InClusterKube is a KubeAPI for the pod this server runs in, using the service account token the chart mounts
// when fusionControl is on. It returns an error when there is none (no token mounted = FUSION cannot be switched
// from here), which the caller treats as "not available", not as a failure.
func InClusterKube(namespace string) (KubeAPI, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, errors.New("not running inside a Kubernetes cluster")
	}
	if _, err := os.Stat(saDir + "/token"); err != nil {
		return nil, errors.New("no service account token is mounted in this pod")
	}
	ca, err := os.ReadFile(saDir + "/ca.crt")
	if err != nil {
		return nil, fmt.Errorf("reading the cluster CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, errors.New("the cluster CA is not a certificate")
	}
	if namespace == "" {
		b, err := os.ReadFile(saDir + "/namespace")
		if err != nil {
			return nil, errors.New("this pod's namespace is unknown (no --release-namespace and none mounted)")
		}
		namespace = strings.TrimSpace(string(b))
	}
	return &kubeClient{
		base:      "https://" + hostPort(host, port),
		namespace: namespace,
		http: &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		}},
		// Projected tokens rotate, so it is read again for every call.
		token: func() (string, error) {
			b, err := os.ReadFile(saDir + "/token")
			return strings.TrimSpace(string(b)), err
		},
	}, nil
}

func hostPort(host, port string) string {
	if strings.Contains(host, ":") { // an IPv6 service address
		return "[" + host + "]:" + port
	}
	return host + ":" + port
}

func (k *kubeClient) do(ctx context.Context, method, path, contentType string, body any) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, k.base+path, rd)
	if err != nil {
		return nil, err
	}
	tok, err := k.token()
	if err != nil {
		return nil, fmt.Errorf("reading the service account token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := k.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrKubeNotFound
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized:
		return nil, &kubeDenied{say: firstLine(string(out))}
	case resp.StatusCode >= 300:
		return nil, fmt.Errorf("kubernetes answered %d: %s", resp.StatusCode, firstLine(string(out)))
	}
	return out, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func (k *kubeClient) appsPath(kind, name, sub string) string {
	p := "/apis/apps/v1/namespaces/" + url.PathEscape(k.namespace) + "/" + kind + "/" + url.PathEscape(name)
	if sub != "" {
		p += "/" + sub
	}
	return p
}

func (k *kubeClient) Workload(ctx context.Context, kind, name string) (KubeWorkload, error) {
	b, err := k.do(ctx, http.MethodGet, k.appsPath(kind, name, ""), "", nil)
	if err != nil {
		return KubeWorkload{}, err
	}
	var w struct {
		Spec   struct{ Replicas *int }     `json:"spec"`
		Status struct{ ReadyReplicas int } `json:"status"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return KubeWorkload{}, err
	}
	out := KubeWorkload{Ready: w.Status.ReadyReplicas, Desired: 1} // an unset spec.replicas means 1
	if w.Spec.Replicas != nil {
		out.Desired = *w.Spec.Replicas
	}
	return out, nil
}

func (k *kubeClient) Scale(ctx context.Context, kind, name string, replicas int) error {
	_, err := k.do(ctx, http.MethodPatch, k.appsPath(kind, name, "scale"), "application/merge-patch+json",
		map[string]any{"spec": map[string]any{"replicas": replicas}})
	return err
}

func (k *kubeClient) PatchSecret(ctx context.Context, name string, data map[string][]byte) error {
	enc := make(map[string]string, len(data))
	for key, v := range data {
		enc[key] = base64.StdEncoding.EncodeToString(v)
	}
	_, err := k.do(ctx, http.MethodPatch, "/api/v1/namespaces/"+url.PathEscape(k.namespace)+"/secrets/"+url.PathEscape(name),
		"application/merge-patch+json", map[string]any{"data": enc})
	return err
}

func (k *kubeClient) corePath(resource, name string) string {
	return "/api/v1/namespaces/" + url.PathEscape(k.namespace) + "/" + resource + "/" + url.PathEscape(name)
}

func (k *kubeClient) Pod(ctx context.Context, name string) (KubePod, error) {
	b, err := k.do(ctx, http.MethodGet, k.corePath("pods", name), "", nil)
	if err != nil {
		return KubePod{}, err
	}
	type containerStatus struct {
		State struct {
			Waiting struct{ Reason string } `json:"waiting"`
		} `json:"state"`
		LastState struct {
			Terminated struct{ Reason string } `json:"terminated"`
		} `json:"lastState"`
	}
	var p struct {
		Status struct {
			Phase      string `json:"phase"`
			Conditions []struct {
				Type, Status, Reason, Message string
			} `json:"conditions"`
			InitContainerStatuses []containerStatus `json:"initContainerStatuses"`
			ContainerStatuses     []containerStatus `json:"containerStatuses"`
		} `json:"status"`
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return KubePod{}, err
	}
	out := KubePod{Phase: p.Status.Phase}
	for _, c := range p.Status.Conditions {
		if c.Type == "PodScheduled" && c.Status == "False" {
			out.Unschedulable, out.ScheduleMessage = true, c.Message
		}
	}
	// The first container that is waiting for something other than its own start-up explains more than one that is
	// merely being created; failing that, any waiting container.
	var fallback *containerStatus
	for _, list := range [][]containerStatus{p.Status.InitContainerStatuses, p.Status.ContainerStatuses} {
		for i := range list {
			c := &list[i]
			switch r := c.State.Waiting.Reason; r {
			case "":
			case "ContainerCreating", "PodInitializing":
				if fallback == nil {
					fallback = c
				}
			default:
				out.WaitingReason, out.LastTerminatedReason = r, c.LastState.Terminated.Reason
				return out, nil
			}
		}
	}
	if fallback != nil {
		out.WaitingReason, out.LastTerminatedReason = fallback.State.Waiting.Reason, fallback.LastState.Terminated.Reason
	}
	return out, nil
}

func (k *kubeClient) ClaimPhase(ctx context.Context, name string) (string, error) {
	b, err := k.do(ctx, http.MethodGet, k.corePath("persistentvolumeclaims", name), "", nil)
	if err != nil {
		return "", err
	}
	var c struct {
		Status struct{ Phase string } `json:"status"`
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return "", err
	}
	return c.Status.Phase, nil
}

// gatewayPort is the gateway's OTLP gRPC port, the one its Service publishes to other clusters.
const gatewayPort = 4317

// KubeService is the part of the gateway's Service that says how, and whether, it can be reached from outside the
// cluster.
type KubeService struct {
	// Type is the Service's spec.type: ClusterIP, NodePort, LoadBalancer or ExternalName.
	Type string
	// Port is the OTLP gRPC port the Service publishes (4317), and NodePort the port every node listens on for it (0 unless
	// the Service is a NodePort or a LoadBalancer that allocates one).
	Port, NodePort int
	// LoadBalancer is the host:port the cloud gave the Service ("" while it has none, and for any other type).
	LoadBalancer string
}

// serviceInspector is what a KubeAPI may also offer: the gateway Service's own spec. It is a separate interface so a
// KubeAPI that cannot read it (an older Role) still works: the address is then simply not checked against the Service.
type serviceInspector interface {
	Service(ctx context.Context, name string) (KubeService, error)
}

func (k *kubeClient) Service(ctx context.Context, name string) (KubeService, error) {
	b, err := k.do(ctx, http.MethodGet, k.corePath("services", name), "", nil)
	if err != nil {
		return KubeService{}, err
	}
	var s struct {
		Spec struct {
			Type  string `json:"type"`
			Ports []struct {
				Port     int `json:"port"`
				NodePort int `json:"nodePort"`
			} `json:"ports"`
		} `json:"spec"`
		Status struct {
			LoadBalancer struct {
				Ingress []struct{ IP, Hostname string } `json:"ingress"`
			} `json:"loadBalancer"`
		} `json:"status"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return KubeService{}, err
	}
	svc := KubeService{Type: s.Spec.Type}
	for _, p := range s.Spec.Ports {
		if p.Port == gatewayPort || svc.Port == 0 {
			svc.Port, svc.NodePort = p.Port, p.NodePort
		}
	}
	if svc.Type == "LoadBalancer" {
		for _, in := range s.Status.LoadBalancer.Ingress {
			if h := cmp.Or(in.IP, in.Hostname); h != "" && svc.Port != 0 {
				svc.LoadBalancer = net.JoinHostPort(h, strconv.Itoa(svc.Port))
				break
			}
		}
	}
	return svc, nil
}

func (k *kubeClient) ServiceAddress(ctx context.Context, name string) (string, error) {
	svc, err := k.Service(ctx, name)
	if err != nil {
		return "", err
	}
	// A NodePort is deliberately not an answer: it is one node's address, and a node's address is often a private one, so
	// recording it for the administrator would put a wrong "Reachable at" into every command. A person records it.
	return svc.LoadBalancer, nil
}

// KubeClaim is the part of a PersistentVolumeClaim the retention control shows and changes.
type KubeClaim struct {
	Phase        string
	StorageClass string
	// Requested is spec.resources.requests.storage and Capacity status.capacity.storage, in bytes. They differ while a
	// volume is being grown.
	Requested, Capacity int64
	Created             time.Time
	// Resizing is true while the cluster is still growing the volume or its file system; ResizeNote is its own sentence.
	Resizing   bool
	ResizeNote string
}

// ErrKubeNoResize is the cluster refusing to grow a claim: its storage class does not allow volume expansion.
var ErrKubeNoResize = errors.New("kubernetes: this volume's storage class does not allow it to be grown")

// retentionKube is what a KubeAPI may also offer for the retention control: the server-owned settings object, the
// stores' volume claims and a store's pod. A separate interface, like serviceInspector, so a KubeAPI that cannot do
// these (or a server whose Role predates them) still works and the control says why it is unavailable.
type retentionKube interface {
	// Settings reads the chart's settings ConfigMap.
	Settings(ctx context.Context, name string) (map[string]string, error)
	// PatchSettings merges keys into it.
	PatchSettings(ctx context.Context, name string, data map[string]string) error
	// Claim reads a volume claim.
	Claim(ctx context.Context, name string) (KubeClaim, error)
	// ResizeClaim raises a claim's requested size. ErrKubeNoResize when the storage class cannot grow volumes.
	ResizeClaim(ctx context.Context, name string, bytes int64) error
	// RestartPod deletes a pod so its StatefulSet starts it again (a missing pod is not an error).
	RestartPod(ctx context.Context, name string) error
}

func (k *kubeClient) Settings(ctx context.Context, name string) (map[string]string, error) {
	b, err := k.do(ctx, http.MethodGet, k.corePath("configmaps", name), "", nil)
	if err != nil {
		return nil, err
	}
	var c struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	if c.Data == nil {
		c.Data = map[string]string{}
	}
	return c.Data, nil
}

func (k *kubeClient) PatchSettings(ctx context.Context, name string, data map[string]string) error {
	_, err := k.do(ctx, http.MethodPatch, k.corePath("configmaps", name), "application/merge-patch+json", map[string]any{"data": data})
	return err
}

func (k *kubeClient) Claim(ctx context.Context, name string) (KubeClaim, error) {
	b, err := k.do(ctx, http.MethodGet, k.corePath("persistentvolumeclaims", name), "", nil)
	if err != nil {
		return KubeClaim{}, err
	}
	var c struct {
		Metadata struct {
			Created time.Time `json:"creationTimestamp"`
		} `json:"metadata"`
		Spec struct {
			StorageClassName *string `json:"storageClassName"`
			Resources        struct {
				Requests map[string]string `json:"requests"`
			} `json:"resources"`
		} `json:"spec"`
		Status struct {
			Phase      string            `json:"phase"`
			Capacity   map[string]string `json:"capacity"`
			Conditions []struct {
				Type, Status, Message string
			} `json:"conditions"`
		} `json:"status"`
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return KubeClaim{}, err
	}
	out := KubeClaim{Phase: c.Status.Phase, Created: c.Metadata.Created}
	if c.Spec.StorageClassName != nil {
		out.StorageClass = *c.Spec.StorageClassName
	}
	out.Requested, _ = parseQuantity(c.Spec.Resources.Requests["storage"])
	out.Capacity, _ = parseQuantity(c.Status.Capacity["storage"])
	for _, cd := range c.Status.Conditions {
		if (cd.Type == "Resizing" || cd.Type == "FileSystemResizePending") && cd.Status == "True" {
			out.Resizing, out.ResizeNote = true, firstLine(cd.Message)
		}
	}
	return out, nil
}

func (k *kubeClient) ResizeClaim(ctx context.Context, name string, bytes int64) error {
	_, err := k.do(ctx, http.MethodPatch, k.corePath("persistentvolumeclaims", name), "application/merge-patch+json",
		map[string]any{"spec": map[string]any{"resources": map[string]any{"requests": map[string]string{"storage": strconv.FormatInt(bytes, 10)}}}})
	var d *kubeDenied
	// The Role grants this patch, so a refusal that talks about resizing is the cluster's: the storage class does not
	// allow expansion (or the claim is not dynamically provisioned).
	if errors.As(err, &d) && strings.Contains(strings.ToLower(d.say), "resiz") {
		return ErrKubeNoResize
	}
	return err
}

func (k *kubeClient) RestartPod(ctx context.Context, name string) error {
	_, err := k.do(ctx, http.MethodDelete, k.corePath("pods", name), "", nil)
	if errors.Is(err, ErrKubeNotFound) {
		return nil
	}
	return err
}

// parseQuantity reads a Kubernetes quantity ("10Gi", "500M", "1073741824") in bytes. Fractions and the milli suffix are
// accepted and rounded down; anything else is an error.
func parseQuantity(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty quantity")
	}
	scales := []struct {
		suffix string
		scale  float64
	}{{"Ki", 1 << 10}, {"Mi", 1 << 20}, {"Gi", 1 << 30}, {"Ti", 1 << 40}, {"Pi", 1 << 50}, {"Ei", 1 << 60},
		{"k", 1e3}, {"K", 1e3}, {"M", 1e6}, {"G", 1e9}, {"T", 1e12}, {"P", 1e15}, {"E", 1e18}, {"m", 1e-3}}
	scale := 1.0
	for _, sc := range scales {
		if strings.HasSuffix(s, sc.suffix) {
			s, scale = strings.TrimSuffix(s, sc.suffix), sc.scale
			break
		}
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("not a quantity: %q", s)
	}
	return int64(n * scale), nil
}
