// Package certrenew keeps the TLS certificates that telemetry collectors read from Kubernetes Secrets from
// running out, with nobody doing anything.
//
// A Secret holds tls.crt, tls.key and ca.crt: a regional operator's receiver certificate, the client certificate
// its exporter (or a cluster's collector) presents to another operator, and the CA that goes with them. This
// package finds the ones with little time left and renews each over the server's anonymous Enrollment listener
// (RenewTelemetryCert): it makes a NEW key here, asks for a certificate for it, and proves it holds the OLD key by
// signing the request with it. The key therefore never leaves the pod it was made in, and nothing has to be
// configured per certificate. The collector re-reads the files on its own (the chart's reload_interval), so no
// restart is involved.
//
// Two programs run it: the discovery agent (for the cluster's own collectors) and the "continuum cert-renew"
// sidecar of a regional operator (for the operator's own receiver and exporter). Each needs permission to get and
// update exactly the Secrets it names, nothing wider.
package certrenew

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/pki"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Target is one Secret to keep renewed.
type Target struct{ Namespace, Name string }

func (t Target) String() string { return t.Namespace + "/" + t.Name }

// ParseTargets reads "name1,name2" (as in CONTINUUM_RENEW_SECRETS) into targets in namespace.
func ParseTargets(namespace, list string) []Target {
	var out []Target
	seen := map[string]bool{}
	for _, n := range strings.Split(list, ",") {
		if n = strings.TrimSpace(n); n != "" && !seen[n] {
			seen[n] = true
			out = append(out, Target{Namespace: namespace, Name: n})
		}
	}
	return out
}

// Result is what one check of one Secret found, passed to Config.Report.
type Result struct {
	Target Target
	// NotAfter is when the certificate that was in the Secret ends (zero when none could be read).
	NotAfter time.Time
	// Renewed is true when a new certificate was written.
	Renewed bool
	// Err is why the certificate could not be renewed; nil when it was, or did not need to be.
	Err error
	// Refused is true when the server answered and said no (the holder is no longer entitled, or the certificate is
	// too old to renew). Retrying will not help; a person has to act. False for everything that may pass by itself.
	Refused bool
}

// Config says what to keep renewed, and where to ask.
type Config struct {
	Server  string // host:port of the Ikhnos server's agent listener
	CAPin   string // the server CA's pin, as for the agent
	Client  kubernetes.Interface
	Targets []Target
	Log     *slog.Logger
	// Every is how often the Secrets are looked at (0: an hour). RenewBefore is how much life a certificate may
	// have left before it is renewed (0: pki.OperatorRenewBefore).
	Every, RenewBefore time.Duration
	// Report, when set, is told what each check found, so a caller can show "renewal failing" somewhere people look.
	Report func(Result)
	// Dial, when set, replaces the pinned TLS dial (tests).
	Dial func() (continuumv1.EnrollmentClient, func(), error)
	// Now is the clock (tests).
	Now func() time.Time
}

func (c *Config) defaults() {
	if c.Log == nil {
		c.Log = slog.Default()
	}
	if c.Every <= 0 {
		c.Every = time.Hour
	}
	if c.RenewBefore <= 0 {
		c.RenewBefore = pki.OperatorRenewBefore
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Dial == nil {
		c.Dial = c.dialServer
	}
}

func (c *Config) dialServer() (continuumv1.EnrollmentClient, func(), error) {
	host, _, err := net.SplitHostPort(c.Server)
	if err != nil {
		return nil, nil, fmt.Errorf("server address must be host:port: %w", err)
	}
	cc, err := grpc.NewClient(c.Server, grpc.WithTransportCredentials(credentials.NewTLS(pki.ClientTLS(c.CAPin, host, nil))))
	if err != nil {
		return nil, nil, err
	}
	return continuumv1.NewEnrollmentClient(cc), func() { cc.Close() }, nil
}

// Run checks the Secrets now and then every Config.Every until ctx ends. After a failure it tries again sooner
// (a minute, doubling), so a server that was briefly away is not waited for an hour.
func Run(ctx context.Context, cfg Config) {
	cfg.defaults()
	if len(cfg.Targets) == 0 {
		return
	}
	retry := time.Minute
	for {
		wait := cfg.Every
		if failed := Once(ctx, cfg); failed > 0 {
			wait = min(retry, cfg.Every)
			retry *= 2
		} else {
			retry = time.Minute
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// Once checks every target one time and returns how many failed. A Secret that does not exist yet, or holds no
// certificate yet, is not a failure: nothing was installed there to renew.
func Once(ctx context.Context, cfg Config) (failed int) {
	cfg.defaults()
	for _, t := range cfg.Targets {
		res := renewOne(ctx, &cfg, t)
		if res.Err != nil {
			failed++
			cfg.Log.Warn("certificate not renewed", "secret", t.String(), "not_after", res.NotAfter, "refused", res.Refused, "err", res.Err)
		} else if res.Renewed {
			cfg.Log.Info("certificate renewed", "secret", t.String(), "was_valid_until", res.NotAfter)
		}
		if cfg.Report != nil {
			cfg.Report(res)
		}
	}
	return failed
}

func renewOne(ctx context.Context, cfg *Config, t Target) Result {
	res := Result{Target: t}
	secrets := cfg.Client.CoreV1().Secrets(t.Namespace)
	sec, err := secrets.Get(ctx, t.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return res // not installed (yet)
	}
	if err != nil {
		res.Err = fmt.Errorf("reading the Secret: %w", err)
		return res
	}
	if len(sec.Data["tls.crt"]) == 0 || len(sec.Data["tls.key"]) == 0 {
		return res // created empty by a chart, waiting for its first certificate
	}
	old, err := pki.ParseCertificate(sec.Data["tls.crt"])
	if err != nil {
		res.Err = fmt.Errorf("tls.crt is not a certificate: %w", err)
		return res
	}
	res.NotAfter = old.NotAfter
	if old.NotAfter.Sub(cfg.Now()) > cfg.RenewBefore {
		return res
	}
	oldKey, err := parseKey(sec.Data["tls.key"])
	if err != nil {
		res.Err = fmt.Errorf("tls.key: %w", err)
		return res
	}
	newKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		res.Err = err
		return res
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, newKey)
	if err != nil {
		res.Err = err
		return res
	}
	proof, err := pki.ProveRenewal(oldKey, csr)
	if err != nil {
		res.Err = err
		return res
	}
	client, closeConn, err := cfg.Dial()
	if err != nil {
		res.Err = err
		return res
	}
	defer closeConn()
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := client.RenewTelemetryCert(callCtx, &continuumv1.RenewTelemetryCertRequest{OldLeafDer: old.Raw, CsrDer: csr, Proof: proof})
	if err != nil {
		res.Err = fmt.Errorf("the server did not renew it: %s", status.Convert(err).Message())
		switch status.Code(err) {
		case codes.Unauthenticated, codes.PermissionDenied, codes.InvalidArgument:
			res.Refused = true
		}
		return res
	}
	keyDER, err := x509.MarshalECPrivateKey(newKey)
	if err != nil {
		res.Err = err
		return res
	}
	// One update with all three files, so the kubelet swaps them in together: a collector never reads a new
	// certificate beside the old key.
	sec.Data["tls.crt"] = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: resp.LeafDer})
	sec.Data["tls.key"] = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if len(resp.CaPem) > 0 {
		sec.Data["ca.crt"] = resp.CaPem
	}
	if _, err := secrets.Update(ctx, sec, metav1.UpdateOptions{}); err != nil {
		// The server has issued a certificate that was never stored; the next attempt asks for another, which is
		// harmless (the old one is still the one on disk and still proves possession).
		res.Err = fmt.Errorf("writing the renewed certificate to the Secret: %w", err)
		return res
	}
	res.Renewed = true
	return res
}

func parseKey(keyPEM []byte) (*ecdsa.PrivateKey, error) {
	b, _ := pem.Decode(keyPEM)
	if b == nil {
		return nil, errors.New("not PEM")
	}
	if k, err := x509.ParseECPrivateKey(b.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		return nil, errors.New("not an EC private key")
	}
	ek, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("not an EC private key")
	}
	return ek, nil
}
