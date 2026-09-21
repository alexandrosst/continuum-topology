// Package agent is the in-cluster component: it enrolls with the server, then streams what it discovers.
package agent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Identity is everything the agent keeps between restarts. There is deliberately no password
// or long-lived token in it: the key proves who it is, and the certificate expires within a day.
//
// It also holds a pending enrollment: the key and the approval code are saved BEFORE the first enroll
// call, so a crash or a lost response leads to the very same enrollment being sent again (the server
// recognises it), and the code an administrator has to type stays the same across restarts.
type Identity struct {
	AgentID    string
	PollSecret string // only until the first authenticated call
	Key        *ecdsa.PrivateKey
	CertDER    []byte // empty until approved
	CADER      []byte
	// ApprovalCode is what the administrator has to type to approve this enrollment (XXXX-XXXX). Only
	// while pending; the server never sees it, only a hash.
	ApprovalCode string
	// TokenID names the enrollment token this identity came from (a hash prefix, never the token), so
	// that a new token after a revocation is recognised as "start over".
	TokenID string
	// Revoked, when set, is why this identity ended for good (revoked, rejected, or no longer known to the
	// server). Nothing else is kept then: no key, no certificate.
	Revoked string
}

func (i *Identity) Enrolled() bool { return i != nil && i.AgentID != "" }
func (i *Identity) HasCert() bool  { return i != nil && len(i.CertDER) > 0 }

// Ended reports that the identity was revoked or rejected. It stays in the store as a marker, so a
// restarted agent does not knock on the server again with a token that can no longer work.
func (i *Identity) Ended() bool { return i != nil && i.Revoked != "" }

func NewKey() (*ecdsa.PrivateKey, error) { return ecdsa.GenerateKey(elliptic.P256(), rand.Reader) }

// NewCSR builds the certificate request. It carries no identity: the server decides that.
func NewCSR(key *ecdsa.PrivateKey) ([]byte, error) {
	return x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
}

type IdentityStore interface {
	Load(ctx context.Context) (*Identity, error) // nil, nil when nothing is stored yet
	Save(ctx context.Context, id *Identity) error
	Clear(ctx context.Context) error
}

// stored lists every entry an identity is made of. Save writes all of them (empty ones too), so a
// saved identity replaces the previous one completely.
var stored = []string{"agent-id", "poll-secret", "key.pem", "cert.der", "ca.der", "approval-code", "token-id", "revoked"}

func marshal(id *Identity) (map[string][]byte, error) {
	var keyPEM []byte
	if id.Key != nil {
		kd, err := x509.MarshalECPrivateKey(id.Key)
		if err != nil {
			return nil, err
		}
		keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd})
	}
	return map[string][]byte{
		"agent-id": []byte(id.AgentID), "poll-secret": []byte(id.PollSecret), "key.pem": keyPEM,
		"cert.der": id.CertDER, "ca.der": id.CADER,
		"approval-code": []byte(id.ApprovalCode), "token-id": []byte(id.TokenID), "revoked": []byte(id.Revoked),
	}, nil
}

func unmarshal(d map[string][]byte) (*Identity, error) {
	id := &Identity{AgentID: string(d["agent-id"]), PollSecret: string(d["poll-secret"]), CertDER: d["cert.der"], CADER: d["ca.der"],
		ApprovalCode: string(d["approval-code"]), TokenID: string(d["token-id"]), Revoked: string(d["revoked"])}
	if len(d["key.pem"]) > 0 {
		b, _ := pem.Decode(d["key.pem"])
		if b == nil {
			return nil, errors.New("stored identity has no valid key")
		}
		k, err := x509.ParseECPrivateKey(b.Bytes)
		if err != nil {
			return nil, err
		}
		id.Key = k
	}
	if id.AgentID == "" && id.Key == nil && id.Revoked == "" {
		return nil, nil // nothing stored yet
	}
	if id.Revoked == "" && id.Key == nil {
		return nil, errors.New("stored identity has no valid key")
	}
	return id, nil
}

// FileStore keeps the identity in a directory (development, or a host-installed agent).
type FileStore struct{ Dir string }

func (f FileStore) Load(context.Context) (*Identity, error) {
	d := map[string][]byte{}
	for _, n := range stored {
		b, err := os.ReadFile(filepath.Join(f.Dir, n))
		if err == nil {
			d[n] = b
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	return unmarshal(d)
}

func (f FileStore) Save(_ context.Context, id *Identity) error {
	if err := os.MkdirAll(f.Dir, 0o700); err != nil {
		return err
	}
	d, err := marshal(id)
	if err != nil {
		return err
	}
	for n, b := range d {
		tmp := filepath.Join(f.Dir, n+".tmp")
		if err := os.WriteFile(tmp, b, 0o600); err != nil {
			return err
		}
		if err := os.Rename(tmp, filepath.Join(f.Dir, n)); err != nil {
			return err
		}
	}
	return nil
}

func (f FileStore) Clear(context.Context) error { return os.RemoveAll(f.Dir) }

// SecretStore keeps the identity in one Kubernetes Secret in the agent's own namespace.
// The Helm chart creates the (empty) Secret and grants get/update/patch on that name only,
// so the agent has no way to read or create any other Secret.
type SecretStore struct {
	Client    kubernetes.Interface
	Namespace string
	Name      string
}

func (s SecretStore) Load(ctx context.Context) (*Identity, error) {
	sec, err := s.Client.CoreV1().Secrets(s.Namespace).Get(ctx, s.Name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return unmarshal(sec.Data)
}

func (s SecretStore) Save(ctx context.Context, id *Identity) error {
	d, err := marshal(id)
	if err != nil {
		return err
	}
	sec, err := s.Client.CoreV1().Secrets(s.Namespace).Get(ctx, s.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	sec.Data = d
	_, err = s.Client.CoreV1().Secrets(s.Namespace).Update(ctx, sec, metav1.UpdateOptions{})
	return err
}

func (s SecretStore) Clear(ctx context.Context) error {
	sec, err := s.Client.CoreV1().Secrets(s.Namespace).Get(ctx, s.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	sec.Data = nil
	_, err = s.Client.CoreV1().Secrets(s.Namespace).Update(ctx, sec, metav1.UpdateOptions{})
	return err
}
