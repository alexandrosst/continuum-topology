package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testKube(t *testing.T, h http.HandlerFunc) *kubeClient {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &kubeClient{base: srv.URL, namespace: "obs", http: srv.Client(), token: func() (string, error) { return "tok", nil }}
}

func TestKubeClientScalesThroughTheScaleSubresource(t *testing.T) {
	var method, path, ct, auth, body string
	k := testKube(t, func(w http.ResponseWriter, r *http.Request) {
		method, path, ct, auth = r.Method, r.URL.Path, r.Header.Get("Content-Type"), r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Write([]byte(`{}`))
	})
	if err := k.Scale(context.Background(), "statefulsets", "f-fusion-loki", 1); err != nil {
		t.Fatal(err)
	}
	if method != "PATCH" || path != "/apis/apps/v1/namespaces/obs/statefulsets/f-fusion-loki/scale" || ct != "application/merge-patch+json" || auth != "Bearer tok" {
		t.Errorf("%s %s %s %s", method, path, ct, auth)
	}
	if body != `{"spec":{"replicas":1}}` {
		t.Errorf("body = %s", body)
	}
}

func TestKubeClientReadsReplicasAndReadiness(t *testing.T) {
	k := testKube(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apis/apps/v1/namespaces/obs/deployments/c" {
			t.Errorf("path %s", r.URL.Path)
		}
		w.Write([]byte(`{"spec":{"replicas":1},"status":{"readyReplicas":1}}`))
	})
	got, err := k.Workload(context.Background(), "deployments", "c")
	if err != nil || got != (KubeWorkload{Desired: 1, Ready: 1}) {
		t.Fatalf("%+v %v", got, err)
	}
	k2 := testKube(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"spec":{"replicas":0},"status":{}}`)) })
	if got, _ := k2.Workload(context.Background(), "deployments", "c"); got.Desired != 0 || got.Ready != 0 {
		t.Errorf("a workload at zero reads as %+v", got)
	}
}

func TestKubeClientPatchesSecretDataBase64(t *testing.T) {
	var body map[string]map[string]string
	k := testKube(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PATCH" || r.URL.Path != "/api/v1/namespaces/obs/secrets/s" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(`{}`))
	})
	if err := k.PatchSecret(context.Background(), "s", map[string][]byte{"tls.crt": []byte("hello")}); err != nil {
		t.Fatal(err)
	}
	if body["data"]["tls.crt"] != "aGVsbG8=" {
		t.Errorf("body = %v", body)
	}
}

func TestKubeClientTellsMissingFromForbidden(t *testing.T) {
	for code, want := range map[int]error{404: ErrKubeNotFound, 403: ErrKubeForbidden, 401: ErrKubeForbidden} {
		k := testKube(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) })
		if err := k.Scale(context.Background(), "deployments", "x", 0); !errors.Is(err, want) {
			t.Errorf("%d: %v", code, err)
		}
	}
	k := testKube(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom\nmore", 500) })
	if err := k.Scale(context.Background(), "deployments", "x", 0); err == nil || !strings.Contains(err.Error(), "500") || strings.Contains(err.Error(), "more") {
		t.Errorf("err = %v", err)
	}
}

func TestKubeClientReadsAPodsReason(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want KubePod
	}{
		"image pull": {`{"status":{"phase":"Pending","conditions":[{"type":"PodScheduled","status":"True"}],
			"containerStatuses":[{"state":{"waiting":{"reason":"ImagePullBackOff"}}}]}}`,
			KubePod{Phase: "Pending", WaitingReason: "ImagePullBackOff"}},
		"crash loop that was killed for memory": {`{"status":{"phase":"Running","containerStatuses":[
			{"state":{"waiting":{"reason":"CrashLoopBackOff"}},"lastState":{"terminated":{"reason":"OOMKilled"}}}]}}`,
			KubePod{Phase: "Running", WaitingReason: "CrashLoopBackOff", LastTerminatedReason: "OOMKilled"}},
		"unschedulable": {`{"status":{"phase":"Pending","conditions":[{"type":"PodScheduled","status":"False","reason":"Unschedulable","message":"0/3 nodes are available: 3 Insufficient memory."}]}}`,
			KubePod{Phase: "Pending", Unschedulable: true, ScheduleMessage: "0/3 nodes are available: 3 Insufficient memory."}},
		// An init container stuck on its image says more than the app container that is just waiting to be created.
		"init container first": {`{"status":{"phase":"Pending","initContainerStatuses":[{"state":{"waiting":{"reason":"ErrImagePull"}}}],
			"containerStatuses":[{"state":{"waiting":{"reason":"PodInitializing"}}}]}}`,
			KubePod{Phase: "Pending", WaitingReason: "ErrImagePull"}},
		"a real fault beats being created": {`{"status":{"phase":"Pending","containerStatuses":[
			{"state":{"waiting":{"reason":"ContainerCreating"}}},{"state":{"waiting":{"reason":"CreateContainerConfigError"}}}]}}`,
			KubePod{Phase: "Pending", WaitingReason: "CreateContainerConfigError"}},
		"only being created": {`{"status":{"phase":"Pending","containerStatuses":[{"state":{"waiting":{"reason":"ContainerCreating"}}}]}}`,
			KubePod{Phase: "Pending", WaitingReason: "ContainerCreating"}},
		"running": {`{"status":{"phase":"Running","conditions":[{"type":"PodScheduled","status":"True"}],"containerStatuses":[{"state":{"running":{}}}]}}`,
			KubePod{Phase: "Running"}},
	} {
		k := testKube(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" || r.URL.Path != "/api/v1/namespaces/obs/pods/f-fusion-loki-0" {
				t.Errorf("%s %s", r.Method, r.URL.Path)
			}
			w.Write([]byte(tc.body))
		})
		if got, err := k.Pod(context.Background(), "f-fusion-loki-0"); err != nil || got != tc.want {
			t.Errorf("%s: %+v %v, want %+v", name, got, err, tc.want)
		}
	}
	k := testKube(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) })
	if _, err := k.Pod(context.Background(), "x"); !errors.Is(err, ErrKubeForbidden) {
		t.Errorf("a Role without the pod grant: %v", err)
	}
}

func TestKubeClientReadsAClaimsPhase(t *testing.T) {
	k := testKube(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/namespaces/obs/persistentvolumeclaims/data-f-fusion-loki-0" {
			t.Errorf("path %s", r.URL.Path)
		}
		w.Write([]byte(`{"status":{"phase":"Pending"}}`))
	})
	if got, err := k.ClaimPhase(context.Background(), "data-f-fusion-loki-0"); err != nil || got != "Pending" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestKubeClientFindsTheGatewaysAddress(t *testing.T) {
	for name, tc := range map[string]struct {
		body, want string
	}{
		"load balancer with an ip": {`{"spec":{"type":"LoadBalancer","ports":[{"port":4318,"nodePort":31318},{"port":4317,"nodePort":31317}]},
			"status":{"loadBalancer":{"ingress":[{"ip":"203.0.113.7"}]}}}`, "203.0.113.7:4317"},
		"load balancer with a name": {`{"spec":{"type":"LoadBalancer","ports":[{"port":4317}]},
			"status":{"loadBalancer":{"ingress":[{"hostname":"abc.elb.example.com"}]}}}`, "abc.elb.example.com:4317"},
		"load balancer still being made": {`{"spec":{"type":"LoadBalancer","ports":[{"port":4317}]},"status":{"loadBalancer":{}}}`, ""},
		// One node's address, often a private one: never recorded for the administrator.
		"node port":    {`{"spec":{"type":"NodePort","ports":[{"port":4317,"nodePort":31317}]}}`, ""},
		"cluster only": {`{"spec":{"type":"ClusterIP","ports":[{"port":4317}]}}`, ""},
	} {
		k := testKube(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v1/namespaces/obs/services/f-fusion-central" {
				t.Errorf("path %s", r.URL.Path)
			}
			w.Write([]byte(tc.body))
		})
		if got, err := k.ServiceAddress(context.Background(), "f-fusion-central"); err != nil || got != tc.want {
			t.Errorf("%s: %q %v, want %q", name, got, err, tc.want)
		}
	}
}

func TestKubeClientReadsAClaimsSizeClassAndResize(t *testing.T) {
	k := testKube(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/namespaces/obs/persistentvolumeclaims/data-f-loki-0" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Write([]byte(`{"metadata":{"creationTimestamp":"2026-09-01T10:00:00Z"},
		 "spec":{"storageClassName":"gp3","resources":{"requests":{"storage":"20Gi"}}},
		 "status":{"phase":"Bound","capacity":{"storage":"10Gi"},"conditions":[{"type":"FileSystemResizePending","status":"True","message":"Waiting for user to (re-)start a pod\nsecond line"}]}}`))
	})
	c, err := k.Claim(context.Background(), "data-f-loki-0")
	if err != nil {
		t.Fatal(err)
	}
	if c.StorageClass != "gp3" || c.Requested != 20<<30 || c.Capacity != 10<<30 || c.Phase != "Bound" || !c.Resizing || c.Created.Year() != 2026 {
		t.Errorf("claim = %+v", c)
	}
	if strings.Contains(c.ResizeNote, "\n") {
		t.Errorf("note = %q", c.ResizeNote)
	}
}

func TestKubeClientSaysWhyAClaimCannotGrow(t *testing.T) {
	var body, ct string
	status := 403
	msg := `persistentvolumeclaims "data-f-loki-0" is forbidden: only dynamically provisioned pvc can be resized and the storageclass that provisions the pvc must support resize`
	k := testKube(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body, ct = string(b), r.Header.Get("Content-Type")
		w.WriteHeader(status)
		w.Write([]byte(`{"message":"` + msg + `"}`))
	})
	err := k.ResizeClaim(context.Background(), "data-f-loki-0", 20<<30)
	if !errors.Is(err, ErrKubeNoResize) {
		t.Fatalf("err = %v", err)
	}
	if ct != "application/merge-patch+json" || body != `{"spec":{"resources":{"requests":{"storage":"21474836480"}}}}` {
		t.Errorf("%s %s", ct, body)
	}
	// The Role refusing is a different thing, and must stay "forbidden".
	msg = `persistentvolumeclaims "data-f-loki-0" is forbidden: User "system:serviceaccount:obs:server" cannot patch resource "persistentvolumeclaims"`
	if err := k.ResizeClaim(context.Background(), "data-f-loki-0", 20<<30); !errors.Is(err, ErrKubeForbidden) || errors.Is(err, ErrKubeNoResize) {
		t.Errorf("a Role refusal was read as %v", err)
	}
}

func TestKubeClientSettingsAndRestart(t *testing.T) {
	var method, path, body string
	status := 200
	k := testKube(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		method, path, body = r.Method, r.URL.Path, string(b)
		w.WriteHeader(status)
		w.Write([]byte(`{"data":{"loki.retention":"168h"}}`))
	})
	ctx := context.Background()
	got, err := k.Settings(ctx, "f-settings")
	if err != nil || got["loki.retention"] != "168h" || path != "/api/v1/namespaces/obs/configmaps/f-settings" {
		t.Fatalf("%v %v %s", got, err, path)
	}
	if err := k.PatchSettings(ctx, "f-settings", map[string]string{"loki.retention": "336h"}); err != nil || method != "PATCH" || body != `{"data":{"loki.retention":"336h"}}` {
		t.Errorf("%v %s %s", err, method, body)
	}
	if err := k.RestartPod(ctx, "f-loki-0"); err != nil || method != "DELETE" || path != "/api/v1/namespaces/obs/pods/f-loki-0" {
		t.Errorf("%v %s %s", err, method, path)
	}
	status = 404 // already gone: the StatefulSet is starting it anyway
	if err := k.RestartPod(ctx, "f-loki-0"); err != nil {
		t.Errorf("a pod that is already gone: %v", err)
	}
}

func TestParseQuantity(t *testing.T) {
	for in, want := range map[string]int64{"10Gi": 10 << 30, "500Mi": 500 << 20, "1073741824": 1 << 30, "10G": 10e9, "1.5Gi": 3 << 29, "1Ti": 1 << 40, "2k": 2000} {
		if got, err := parseQuantity(in); err != nil || got != want {
			t.Errorf("parseQuantity(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "Gi", "ten", "-1Gi"} {
		if _, err := parseQuantity(bad); err == nil {
			t.Errorf("parseQuantity(%q) accepted", bad)
		}
	}
}
