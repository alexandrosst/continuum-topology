package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"continuum/internal/fusionapi"
	"continuum/internal/store"
)

// fakeKube is the cluster as FusionControl sees it: four named workloads, a Secret, and the calls made on them.
type fakeKube struct {
	mu       sync.Mutex
	replicas map[string]int
	ready    map[string]int
	missing  bool
	err      error
	calls    []string
	secret   map[string][]byte
	secretIn string

	reads                          int // Workload calls, to see how often the cluster is asked
	pods                           map[string]KubePod
	podErr                         error // what reading any pod fails with (nil = the pods in `pods`)
	claims                         map[string]string
	svcAddr                        string
	svcErr                         error
	slow                           time.Duration // how long a Workload read takes
	onWorkload                     func()        // runs inside a Workload read, once its sleep is over
	onService                      func()        // runs inside a Service read, before it answers: what happens while the server is waiting for the cluster
	podReads, claimReads, svcReads int
}

func newFakeKube(names ...string) *fakeKube {
	k := &fakeKube{replicas: map[string]int{}, ready: map[string]int{}, pods: map[string]KubePod{}, claims: map[string]string{}}
	for _, n := range names {
		k.replicas[n] = 0
	}
	return k
}

func (k *fakeKube) Workload(ctx context.Context, kind, name string) (KubeWorkload, error) {
	k.mu.Lock()
	k.reads++
	slow := k.slow
	k.mu.Unlock()
	select {
	case <-time.After(slow):
	case <-ctx.Done():
		return KubeWorkload{}, ctx.Err()
	}
	if k.onWorkload != nil {
		k.onWorkload()
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.err != nil {
		return KubeWorkload{}, k.err
	}
	r, ok := k.replicas[name]
	if !ok || k.missing {
		return KubeWorkload{}, ErrKubeNotFound
	}
	return KubeWorkload{Desired: r, Ready: k.ready[name]}, nil
}

func (k *fakeKube) Scale(_ context.Context, kind, name string, replicas int) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.err != nil {
		return k.err
	}
	if _, ok := k.replicas[name]; !ok { // the real API has no such workload to scale
		return ErrKubeNotFound
	}
	k.calls = append(k.calls, name)
	k.replicas[name] = replicas
	return nil
}

func (k *fakeKube) PatchSecret(_ context.Context, name string, data map[string][]byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.err != nil {
		return k.err
	}
	k.secretIn, k.secret = name, data
	return nil
}

func (k *fakeKube) Pod(_ context.Context, name string) (KubePod, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.podReads++
	if k.podErr != nil {
		return KubePod{}, k.podErr
	}
	p, ok := k.pods[name]
	if !ok {
		return KubePod{}, ErrKubeNotFound
	}
	return p, nil
}

func (k *fakeKube) ClaimPhase(_ context.Context, name string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.claimReads++
	if k.podErr != nil {
		return "", k.podErr
	}
	p, ok := k.claims[name]
	if !ok {
		return "", ErrKubeNotFound
	}
	return p, nil
}

func (k *fakeKube) ServiceAddress(_ context.Context, name string) (string, error) {
	if k.onService != nil {
		k.onService()
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.svcReads++
	return k.svcAddr, k.svcErr
}

func (k *fakeKube) allReady() {
	for n, r := range k.replicas {
		k.ready[n] = r
	}
}

var fusionNames = []string{"continuum-fusion-prometheus", "continuum-fusion-loki", "continuum-fusion-tempo", "continuum-fusion-central"}

func newFusion(t *testing.T, a *adminRig) (*FusionControl, *fakeKube) {
	t.Helper()
	k := newFakeKube(fusionNames...)
	// No cache and no Prometheus to ask: a test changes the fake cluster between two calls and expects to see it. The
	// cache has its own tests (TestStatusIsReadOnceForEveryoneAsking and friends).
	f := &FusionControl{Name: "continuum-fusion", Namespace: "continuum", Kube: k, Org: a.a.C.OrgID, StatusTTL: -1, Data: &fusionapi.Client{}}
	return f, k
}

func TestFusionStatusFollowsTheWorkloads(t *testing.T) {
	a := newAdminRig(t)
	f, k := newFusion(t, a)
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	f.Now = func() time.Time { return now }
	ctx := context.Background()

	if st := f.Status(ctx); !st.Available || st.State != "off" || len(st.Components) != 4 {
		t.Fatalf("all at zero: %+v", st)
	}
	for n := range k.replicas {
		k.replicas[n] = 1
	}
	if st := f.Status(ctx); st.State != "starting" || st.Since == nil {
		t.Fatalf("started, none ready: %+v", st)
	}
	now = now.Add(2 * time.Minute)
	k.ready["continuum-fusion-loki"] = 1
	if st := f.Status(ctx); st.State != "starting" {
		t.Fatalf("some ready: %+v", st)
	}
	now = now.Add(10 * time.Minute)
	if st := f.Status(ctx); st.State != "attention" || !strings.Contains(st.Message, "1 of 4") {
		t.Fatalf("stuck: %+v", st)
	}
	k.allReady()
	if st := f.Status(ctx); st.State != "running" {
		t.Fatalf("all ready: %+v", st)
	}
	for n := range k.replicas {
		k.replicas[n] = 0
	}
	if st := f.Status(ctx); st.State != "off" || st.Since != nil {
		t.Fatalf("turned off: %+v", st)
	}
}

func TestFusionStatusSaysWhyItCannotBeSwitched(t *testing.T) {
	a := newAdminRig(t)
	var nilFusion *FusionControl
	if st := nilFusion.Status(context.Background()); st.Available || st.Reason != "not-configured" {
		t.Errorf("no switch: %+v", st)
	}
	f, k := newFusion(t, a)
	k.missing = true
	if st := f.Status(context.Background()); st.Available || st.Reason != "not-installed" {
		t.Errorf("workloads absent: %+v", st)
	}
	k.missing, k.err = false, ErrKubeForbidden
	if st := f.Status(context.Background()); st.Available || st.Reason != "no-access" {
		t.Errorf("forbidden: %+v", st)
	}
	k.err = errors.New("connection refused")
	if st := f.Status(context.Background()); !st.Available || st.State != "attention" || !strings.Contains(st.Message, "connection refused") {
		t.Errorf("API down: %+v", st)
	}
}

func TestEnableFusionPreparesTheCentralOperatorThenStartsEverything(t *testing.T) {
	a := newAdminRig(t)
	f, k := newFusion(t, a)
	f.PublicAddress = "fusion.example.com:4317"
	ctx := context.Background()

	st, err := f.Enable(ctx, a.a.C, "alex")
	if err != nil {
		t.Fatal(err)
	}
	if st.State != "starting" {
		t.Errorf("state after enable = %q", st.State)
	}
	// The gateway's certificate went in before anything started, under the name the chart's Secret has.
	if k.secretIn != "continuum-fusion-central-receiver-tls" || len(k.secret["tls.crt"]) == 0 || len(k.secret["tls.key"]) == 0 || len(k.secret["ca.crt"]) == 0 {
		t.Fatalf("secret %q keys %v", k.secretIn, keysOf(k.secret))
	}
	// Stores first, the gateway last.
	if got := strings.Join(k.calls, ","); got != "continuum-fusion-prometheus,continuum-fusion-loki,continuum-fusion-tempo,continuum-fusion-central" {
		t.Errorf("scale order = %s", got)
	}
	for n, r := range k.replicas {
		if r != 1 {
			t.Errorf("%s replicas = %d", n, r)
		}
	}
	// The central operator is a real, active, mTLS regional operator whose destination is the three stores.
	op, err := a.a.C.GetOperator(ctx, CentralOperatorID)
	if err != nil {
		t.Fatal(err)
	}
	if op.Status != store.OperatorActive || op.ReceiverAuth != store.ReceiverAuthMTLS || op.Destination.Kind != store.DestinationFusion || op.Destination.FusionRelease != "continuum-fusion" || op.Destination.FusionNamespace != "continuum" {
		t.Errorf("central operator = %+v", op)
	}
	// Its server certificate chains to its own CA and names every way the gateway is reached.
	block, _ := pem.Decode(k.secret["tls.crt"])
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(k.secret["ca.crt"])
	for _, host := range []string{"continuum-fusion-central.continuum.svc", "fusion.example.com", "op-central.continuum-system.svc"} {
		if _, err := cert.Verify(x509.VerifyOptions{Roots: pool, DNSName: host}); err != nil {
			t.Errorf("certificate not valid for %s: %v", host, err)
		}
	}
	if _, err := tls.X509KeyPair(k.secret["tls.crt"], k.secret["tls.key"]); err != nil {
		t.Errorf("certificate and key do not pair: %v", err)
	}

	// A sender's client certificate, issued from the same CA, is accepted by it.
	certPEM, _, caPEM, err := a.a.C.IssueOperatorClientCertFor(ctx, "alex", CentralOperatorID, "cl-test", "")
	if err != nil || string(caPEM) != string(k.secret["ca.crt"]) {
		t.Fatalf("client cert: %v (CA matches the gateway's: %v)", err, string(caPEM) == string(k.secret["ca.crt"]))
	}
	cb, _ := pem.Decode(certPEM)
	cc, _ := x509.ParseCertificate(cb.Bytes)
	if _, err := cc.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Errorf("a sender's client certificate does not chain to the gateway's CA: %v", err)
	}

	// Enabling again changes nothing about who is trusted: same operator, same CA, a fresh server certificate.
	first := string(k.secret["ca.crt"])
	if _, err := f.Enable(ctx, a.a.C, "alex"); err != nil {
		t.Fatal(err)
	}
	if string(k.secret["ca.crt"]) != first {
		t.Error("a second enable replaced the CA, which would orphan every sender's certificate")
	}
	ops, _ := a.a.C.ListOperators(ctx)
	n := 0
	for _, o := range ops {
		if o.ID == CentralOperatorID {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d central operators", n)
	}
}

func keysOf(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestDisableFusionStopsTheGatewayFirstAndKeepsTheOperator(t *testing.T) {
	a := newAdminRig(t)
	f, k := newFusion(t, a)
	ctx := context.Background()
	if _, err := f.Enable(ctx, a.a.C, "alex"); err != nil {
		t.Fatal(err)
	}
	k.calls = nil
	st, err := f.Disable(ctx, a.a.C, "alex")
	if err != nil {
		t.Fatal(err)
	}
	if st.State != "off" {
		t.Errorf("state = %q", st.State)
	}
	if got := strings.Join(k.calls, ","); got != "continuum-fusion-central,continuum-fusion-tempo,continuum-fusion-loki,continuum-fusion-prometheus" {
		t.Errorf("scale-down order = %s", got)
	}
	if op, err := a.a.C.GetOperator(ctx, CentralOperatorID); err != nil || op.Status != store.OperatorActive {
		t.Errorf("turning FUSION off must keep the central operator (its CA is what every sender trusts): %+v %v", op, err)
	}
}

func TestEnableFusionRefusesWhatItCannotDo(t *testing.T) {
	a := newAdminRig(t)
	ctx := context.Background()
	f, k := newFusion(t, a)
	k.err = ErrKubeForbidden
	if _, err := f.Enable(ctx, a.a.C, "alex"); err == nil {
		t.Error("enabled without permission")
	}
	if _, err := a.a.C.GetOperator(ctx, CentralOperatorID); err == nil {
		t.Error("a central operator was created though FUSION could not be started")
	}

	f, _ = newFusion(t, a)
	f.Org = "some-other-org"
	var e *Error
	if _, err := f.Enable(ctx, a.a.C, "alex"); !errors.As(err, &e) || e.Kind != KindForbidden {
		t.Errorf("another organisation enabled FUSION: %v", err)
	}
}

func TestTheCentralOperatorIsNotEditableByHand(t *testing.T) {
	a := newAdminRig(t)
	f, _ := newFusion(t, a)
	ctx := context.Background()
	if _, err := f.Enable(ctx, a.a.C, "alex"); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"revoke": a.a.C.RevokeOperator(ctx, "alex", CentralOperatorID, "x"),
		"delete": a.a.C.DeleteOperator(ctx, "alex", CentralOperatorID),
		"scope":  a.a.C.UpdateOperatorScope(ctx, "alex", CentralOperatorID, nil, store.Destination{Kind: store.DestinationExternal, Endpoint: "x:1"}, nil),
	} {
		var e *Error
		if !errors.As(err, &e) || e.Kind != KindConflict {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestFusionHTTPSwitch(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleAdmin)
	if r := a.do("GET", "/api/v1/fusion", nil, withCookie(cookie)); r.Code != 200 || r.json(t)["available"] != false || r.json(t)["reason"] != "not-configured" {
		t.Fatalf("no switch: %d %s", r.Code, r.Body.String())
	}
	if r := a.do("POST", "/api/v1/fusion/enable", nil, withCookie(cookie)); r.Code != 409 {
		t.Fatalf("enable without a switch: %d %s", r.Code, r.Body.String())
	}

	f, k := newFusion(t, a)
	a.a.Fusion = f
	r := a.do("GET", "/api/v1/fusion", nil, withCookie(cookie))
	if r.Code != 200 || r.json(t)["state"] != "off" || r.json(t)["available"] != true {
		t.Fatalf("off: %d %s", r.Code, r.Body.String())
	}
	r = a.do("POST", "/api/v1/fusion/enable", nil, withCookie(cookie))
	if r.Code != 200 || r.json(t)["state"] != "starting" {
		t.Fatalf("enable: %d %s", r.Code, r.Body.String())
	}
	doc := r.json(t)
	central, _ := doc["central"].(map[string]any)
	if central["operatorId"] != CentralOperatorID || central["exposed"] != false || central["exists"] != true || central["endpoint"] != "continuum-fusion-central.continuum.svc:4317" {
		t.Errorf("central = %v", central)
	}
	k.allReady()
	if r := a.do("GET", "/api/v1/fusion", nil, withCookie(cookie)); r.json(t)["state"] != "running" {
		t.Errorf("running: %s", r.Body.String())
	}
	r = a.do("POST", "/api/v1/fusion/disable", nil, withCookie(cookie))
	if r.Code != 200 || r.json(t)["state"] != "off" {
		t.Fatalf("disable: %d %s", r.Code, r.Body.String())
	}

	// A viewer cannot touch it.
	_, viewer := a.user(t, "vera", RoleViewer)
	if r := a.do("POST", "/api/v1/fusion/enable", nil, withCookie(viewer)); r.Code != 403 {
		t.Errorf("viewer enable: %d", r.Code)
	}
}

// A regional operator that exports to the central operator is pointed at the gateway over mutual TLS with a client
// certificate from the gateway's own CA, and nothing else about FUSION appears in its command.
func TestAnOperatorSendingToTheCentralOperator(t *testing.T) {
	for _, tc := range []struct{ public, endpoint string }{
		{"", "continuum-fusion-central.continuum.svc:4317"},
		{"fusion.example.com:4317", "fusion.example.com:4317"},
	} {
		a := newAdminRig(t)
		_, cookie := a.user(t, "alex", RoleAdmin)
		f, _ := newFusion(t, a)
		f.PublicAddress = tc.public
		a.a.Fusion = f
		if r := a.do("POST", "/api/v1/fusion/enable", nil, withCookie(cookie)); r.Code != 200 {
			t.Fatal(r.Body.String())
		}
		cl := a.approvedCluster(t, fp)
		body := map[string]any{"name": "athens", "sourceClusterIds": []string{cl}, "destination": map[string]any{"kind": "operator", "targetOperatorId": CentralOperatorID}}
		r := a.do("POST", "/api/v1/operators", body, withCookie(cookie))
		if r.Code != 201 {
			t.Fatalf("create: %d %s", r.Code, r.Body.String())
		}
		created := r.json(t)
		install, _ := created["install"].(string)
		want := []string{
			"--set export.otlp.endpoint=" + tc.endpoint,
			"--set export.otlp.tls.mtls.enabled=true",
			"--set export.otlp.tls.mtls.secretName=op-central-export-mtls",
		}
		// Dialled at a public address, the gateway is verified by the name that never changes, so the address (or the IP
		// behind it) can move without reissuing its certificate; in-cluster the name is the one dialled.
		const stable = "--set export.otlp.tls.serverName=op-central.continuum-system.svc"
		if tc.public != "" {
			want = append(want, stable)
		} else if strings.Contains(install, "serverName") {
			t.Errorf("an in-cluster gateway needs no server name:\n%s", install)
		}
		for _, w := range want {
			if !strings.Contains(install, w) {
				t.Errorf("public=%q: install lacks %q:\n%s", tc.public, w, install)
			}
		}
		if strings.Contains(install, "export.routes") || strings.Contains(install, "-prometheus") || strings.Contains(install, "-loki") || strings.Contains(install, "-tempo") {
			t.Errorf("the command should know nothing of the stores behind the gateway:\n%s", install)
		}
		sec, _ := created["exportSecretCommand"].(string)
		if !strings.Contains(sec, "op-central-export-mtls") || !strings.Contains(sec, "  tls.crt: |") || !strings.Contains(sec, "  ca.crt: |") || strings.Contains(sec, "--from-literal") {
			t.Errorf("export secret command = %q", sec)
		}
		target, _ := created["exportTarget"].(map[string]any)
		if target["endpoint"] != tc.endpoint || target["reachableFromOtherClusters"] != (tc.public != "") {
			t.Errorf("exportTarget = %v", target)
		}
	}
}

// Renew replaces the gateway's certificate while FUSION is on and leaves everything alone while it is off or was never
// set up, so a daily call is harmless.
func TestRenewReissuesTheGatewayCertificateOnlyWhileFusionIsOn(t *testing.T) {
	a := newAdminRig(t)
	f, k := newFusion(t, a)
	ctx := context.Background()
	if err := f.Renew(ctx, a.a.C); err != nil || k.secretIn != "" {
		t.Fatalf("renewed while it was never enabled: %v (secret %q)", err, k.secretIn)
	}
	if _, err := f.Enable(ctx, a.a.C, "alex"); err != nil {
		t.Fatal(err)
	}
	first := string(k.secret["tls.crt"])
	// The address is set after FUSION was first enabled: the next renewal gives the certificate its name.
	f.PublicAddress = "fusion.example.com:4317"
	if err := f.Renew(ctx, a.a.C); err != nil {
		t.Fatal(err)
	}
	if string(k.secret["tls.crt"]) == first {
		t.Fatal("the certificate was not replaced")
	}
	block, _ := pem.Decode(k.secret["tls.crt"])
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(k.secret["ca.crt"])
	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool, DNSName: "fusion.example.com"}); err != nil {
		t.Errorf("the renewed certificate lacks the new address: %v", err)
	}
	if _, err := f.Disable(ctx, a.a.C, "alex"); err != nil {
		t.Fatal(err)
	}
	k.secret, k.secretIn = nil, ""
	if err := f.Renew(ctx, a.a.C); err != nil || k.secretIn != "" {
		t.Fatalf("renewed while off: %v (secret %q)", err, k.secretIn)
	}
}

// A pod that falls over long after a healthy start is "starting" again for the grace period, not "attention after
// weeks": the clock counts from when the stores were last seen not ready, not from when FUSION was switched on.
func TestARestartLongAfterAHealthyStartIsStartingNotAttention(t *testing.T) {
	a := newAdminRig(t)
	f, k := newFusion(t, a)
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	f.Now = func() time.Time { return now }
	ctx := context.Background()
	for n := range k.replicas {
		k.replicas[n] = 1
	}
	f.Status(ctx)
	k.allReady()
	if st := f.Status(ctx); st.State != "running" {
		t.Fatalf("healthy: %+v", st)
	}
	now = now.Add(30 * 24 * time.Hour)
	k.ready["continuum-fusion-loki"] = 0 // one pod restarts a month later
	if st := f.Status(ctx); st.State != "starting" || st.Message != "" {
		t.Fatalf("a month after a healthy start, one pod not ready: %+v", st)
	}
	now = now.Add(2 * time.Minute)
	if st := f.Status(ctx); st.State != "starting" {
		t.Fatalf("two minutes into the restart: %+v", st)
	}
	now = now.Add(6 * time.Minute)
	if st := f.Status(ctx); st.State != "attention" || !strings.Contains(st.Message, "after 8 minutes") {
		t.Fatalf("stuck for 8 minutes: %+v", st)
	}
	k.allReady()
	f.Status(ctx)
	k.ready["continuum-fusion-loki"] = 0
	if st := f.Status(ctx); st.State != "starting" {
		t.Fatalf("a second restart starts a fresh grace period: %+v", st)
	}
}
