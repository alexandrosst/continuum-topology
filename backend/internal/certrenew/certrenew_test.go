package certrenew

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	ikhnosv1 "ikhnos/gen/ikhnosv1"
	"ikhnos/internal/pki"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// fakeServer answers RenewTelemetryCert the way the real one does when the holder is entitled: it checks the proof with
// the old certificate's key and signs the new key's request with its CA. refuse, when set, is the answer instead.
type fakeServer struct {
	ca     *pki.CA
	calls  int
	refuse error
}

func (f *fakeServer) RenewTelemetryCert(_ context.Context, in *ikhnosv1.RenewTelemetryCertRequest, _ ...grpc.CallOption) (*ikhnosv1.RenewTelemetryCertResponse, error) {
	f.calls++
	if f.refuse != nil {
		return nil, f.refuse
	}
	old, err := x509.ParseCertificate(in.OldLeafDer)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "bad certificate")
	}
	if err := pki.VerifyRenewalProof(old, in.CsrDer, in.Proof); err != nil {
		return nil, status.Error(codes.Unauthenticated, err.Error())
	}
	csr, err := pki.ParseCSR(in.CsrDer)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	der, err := f.ca.SignLeaf(csr.PublicKey, pkix.Name{CommonName: old.Subject.CommonName, Organization: old.Subject.Organization}, pki.OperatorTLSTTL, x509.ExtKeyUsageClientAuth, nil)
	if err != nil {
		return nil, err
	}
	return &ikhnosv1.RenewTelemetryCertResponse{LeafDer: der, CaPem: f.ca.CertPEM()}, nil
}

func (f *fakeServer) Enroll(context.Context, *ikhnosv1.EnrollRequest, ...grpc.CallOption) (*ikhnosv1.EnrollResponse, error) {
	return nil, errors.New("not used")
}
func (f *fakeServer) PollEnrollment(context.Context, *ikhnosv1.PollRequest, ...grpc.CallOption) (*ikhnosv1.PollResponse, error) {
	return nil, errors.New("not used")
}
func (f *fakeServer) Rejoin(context.Context, *ikhnosv1.RejoinRequest, ...grpc.CallOption) (*ikhnosv1.RenewResponse, error) {
	return nil, errors.New("not used")
}

type rig struct {
	srv    *fakeServer
	client *fake.Clientset
	cfg    Config
	key    *ecdsa.PrivateKey
}

// newRig puts a client certificate with `left` of life remaining into the Secret ns/tls.
func newRig(t *testing.T, left time.Duration) *rig {
	t.Helper()
	org, err := pki.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	issuer, _, _, err := org.NewOperatorCA("op-1", "org-1")
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, err := issuer.SignLeaf(&key.PublicKey, pkix.Name{CommonName: "op-1-export-cl", Organization: []string{"org-1"}}, left, x509.ExtKeyUsageClientAuth, nil)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "tls"}, Data: map[string][]byte{
		"tls.crt": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		"tls.key": pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		"ca.crt":  []byte("old ca"),
	}}
	client := fake.NewSimpleClientset(sec)
	srv := &fakeServer{ca: issuer}
	return &rig{srv: srv, client: client, key: key, cfg: Config{
		Client: client, Targets: []Target{{"ns", "tls"}},
		Dial: func() (ikhnosv1.EnrollmentClient, func(), error) { return srv, func() {}, nil },
	}}
}

func (r *rig) secret(t *testing.T) *corev1.Secret {
	t.Helper()
	s, err := r.client.CoreV1().Secrets("ns").Get(context.Background(), "tls", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestACertificateWithPlentyOfLifeIsLeftAlone(t *testing.T) {
	r := newRig(t, 25*24*time.Hour)
	before := r.secret(t).DeepCopy()
	if failed := Once(context.Background(), r.cfg); failed != 0 {
		t.Fatal("failed")
	}
	if r.srv.calls != 0 || string(r.secret(t).Data["tls.crt"]) != string(before.Data["tls.crt"]) {
		t.Fatalf("a certificate with 25 days left was renewed (%d calls)", r.srv.calls)
	}
}

func TestACertificateNearItsEndIsReplacedInOneUpdate(t *testing.T) {
	r := newRig(t, 10*24*time.Hour)
	var got []Result
	r.cfg.Report = func(res Result) { got = append(got, res) }
	oldKey := r.secret(t).Data["tls.key"]
	if failed := Once(context.Background(), r.cfg); failed != 0 {
		t.Fatalf("failed: %+v", got)
	}
	if len(got) != 1 || !got[0].Renewed || got[0].Err != nil {
		t.Fatalf("report = %+v", got)
	}
	s := r.secret(t)
	cert, err := pki.ParseCertificate(s.Data["tls.crt"])
	if err != nil {
		t.Fatal(err)
	}
	if left := time.Until(cert.NotAfter); left < 29*24*time.Hour {
		t.Fatalf("renewed certificate has %v left", left)
	}
	if string(s.Data["tls.key"]) == string(oldKey) {
		t.Fatal("the key was not replaced: renewal must use a new key")
	}
	key, err := parseKey(s.Data["tls.key"])
	if err != nil || !cert.PublicKey.(*ecdsa.PublicKey).Equal(&key.PublicKey) {
		t.Fatalf("the stored key does not belong to the stored certificate: %v", err)
	}
	if string(s.Data["ca.crt"]) != string(r.srv.ca.CertPEM()) {
		t.Fatal("ca.crt was not updated to the CA that signed the certificate")
	}
	// It is now fresh, so the next pass does nothing.
	r.srv.calls = 0
	Once(context.Background(), r.cfg)
	if r.srv.calls != 0 {
		t.Fatal("renewed twice")
	}
}

func TestACertificateThatEndedStillRenewsBecauseTheServerDecidesTheGrace(t *testing.T) {
	r := newRig(t, 24*time.Hour)
	r.cfg.Now = func() time.Time { return time.Now().Add(3 * 24 * time.Hour) } // it ended two days ago
	if failed := Once(context.Background(), r.cfg); failed != 0 || r.srv.calls != 1 {
		t.Fatalf("failed %d, calls %d", failed, r.srv.calls)
	}
}

func TestARefusalIsReportedAsRefusedAndKeepsTheOldCertificate(t *testing.T) {
	for name, tc := range map[string]struct {
		err     error
		refused bool
	}{
		"no longer entitled": {status.Error(codes.PermissionDenied, "no longer sends"), true},
		"too old":            {status.Error(codes.Unauthenticated, "install it again"), true},
		"server away":        {status.Error(codes.Unavailable, "connection refused"), false},
		"rate limited":       {status.Error(codes.ResourceExhausted, "slow down"), false},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, 5*24*time.Hour)
			r.srv.refuse = tc.err
			var got Result
			r.cfg.Report = func(res Result) { got = res }
			before := r.secret(t).Data["tls.crt"]
			if failed := Once(context.Background(), r.cfg); failed != 1 {
				t.Fatalf("failed = %d", failed)
			}
			if got.Err == nil || got.Refused != tc.refused || got.NotAfter.IsZero() {
				t.Fatalf("result = %+v", got)
			}
			if string(r.secret(t).Data["tls.crt"]) != string(before) {
				t.Fatal("the certificate in the Secret changed")
			}
		})
	}
}

func TestSecretsWithNothingToRenewAreNotFailures(t *testing.T) {
	r := newRig(t, time.Hour)
	r.cfg.Targets = []Target{{"ns", "missing"}, {"ns", "empty"}}
	if _, err := r.client.CoreV1().Secrets("ns").Create(context.Background(), &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "empty"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if failed := Once(context.Background(), r.cfg); failed != 0 || r.srv.calls != 0 {
		t.Fatalf("failed %d, calls %d", failed, r.srv.calls)
	}
}

func TestParseTargets(t *testing.T) {
	got := ParseTargets("ns", " a, b ,,a")
	if len(got) != 2 || got[0] != (Target{"ns", "a"}) || got[1] != (Target{"ns", "b"}) {
		t.Fatalf("%v", got)
	}
	if len(ParseTargets("ns", "")) != 0 {
		t.Fatal("empty list")
	}
}

// The retry delay doubles after each failed round. It used to double without bound, overflowing int64 after
// about 28 failures (53 to reach exactly zero), after which time.After(<=0) made Run check in a hot loop.
func TestRunBackoffStaysCappedAfterManyFailures(t *testing.T) {
	cl := fake.NewSimpleClientset()
	cl.PrependReactor("get", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("apiserver unavailable")
	})
	var checks atomic.Int64
	// 2ms rounds pass the 53 doublings that reach zero within ~110ms; the rest of the window is the spin.
	every, window := 2*time.Millisecond, 600*time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), window)
	defer cancel()
	Run(ctx, Config{Every: every, Client: cl, Targets: []Target{{"ns", "s"}}, Report: func(Result) { checks.Add(1) }})
	// At most one check per Every (plus the first); the spin makes tens of thousands.
	if n, max := checks.Load(), int64(window/every)+1; n > max {
		t.Fatalf("%d checks in %s with Every=%s, want at most %d", n, window, every, max)
	}
}
