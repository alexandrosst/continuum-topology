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
