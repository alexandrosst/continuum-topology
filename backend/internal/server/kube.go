package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// KubeAPI is the whole of what this server may do with the Kubernetes API, and it is deliberately tiny: read the
// replica counts of the FUSION workloads, scale them, and put certificates into the central gateway's Secret. The
// chart's Role (deploy/helm/continuum-server/templates/fusion-rbac.yaml) grants exactly these calls on exactly
// these named objects and nothing more - there is no list, no create, no delete, no way to read a Secret back.
type KubeAPI interface {
	// Workload reads a StatefulSet or Deployment ("statefulsets", "deployments") by name.
	Workload(ctx context.Context, kind, name string) (KubeWorkload, error)
	// Scale sets a workload's replica count through its scale subresource.
	Scale(ctx context.Context, kind, name string, replicas int) error
	// PatchSecret merges the given keys into an existing Secret's data.
	PatchSecret(ctx context.Context, name string, data map[string][]byte) error
}

// KubeWorkload is the part of a workload's state the switch shows.
type KubeWorkload struct {
	Desired int // spec.replicas
	Ready   int // status.readyReplicas
}

// Why a Kubernetes call failed, in the three ways the switch tells apart.
var (
	ErrKubeNotFound  = errors.New("kubernetes: object not found")
	ErrKubeForbidden = errors.New("kubernetes: this server is not allowed to do that")
)

// kubeClient talks to the API server over plain net/http: four calls do not need client-go and its dependency tree.
type kubeClient struct {
	base      string
	namespace string
	http      *http.Client
	token     func() (string, error)
}

const (
	saDir = "/var/run/secrets/kubernetes.io/serviceaccount"
)

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
		return nil, ErrKubeForbidden
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
