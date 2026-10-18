package testkit

import (
	"context"
	"crypto/ecdsa"
	"testing"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/agents"
	"continuum/internal/approval"
)

// Enroll makes a token and enrols an agent with it at the tier, returning the response, the token's secret and the approval code a
// human would type to approve it.
func Enroll(t *testing.T, ctx context.Context, ag *agents.Service, tier uint32, fingerprint string) (*continuumv1.EnrollResponse, string, string) {
	t.Helper()
	secret, _, err := ag.CreateToken(ctx, "admin", "edge-patras", 2)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := CSR(t)
	hash, code := ApprovalFor(t, d)
	resp, err := ag.Enroll(ctx, "10.0.0.1", &continuumv1.EnrollRequest{Token: secret, CsrDer: d, ClusterFingerprint: fingerprint, InstalledAccessTier: tier, AgentVersion: "0.1.0", ApprovalCodeHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	return resp, secret, code
}

// ApprovedAgent enrols and approves an agent, returning its id, private key and first certificate.
func ApprovedAgent(t *testing.T, ctx context.Context, ag *agents.Service, fingerprint string) (string, *ecdsa.PrivateKey, []byte) {
	t.Helper()
	secret, _, err := ag.CreateToken(ctx, "admin", "edge", 2)
	if err != nil {
		t.Fatal(err)
	}
	d, key := CSR(t)
	hash, code := ApprovalFor(t, d)
	resp, err := ag.Enroll(ctx, "10.0.0.1", &continuumv1.EnrollRequest{Token: secret, CsrDer: d, ClusterFingerprint: fingerprint, InstalledAccessTier: 2, ApprovalCodeHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	if err := ag.Approve(ctx, "alex", resp.AgentId, code, 2); err != nil {
		t.Fatal(err)
	}
	p, err := ag.Poll(ctx, "10.0.0.1", &continuumv1.PollRequest{AgentId: resp.AgentId, PollSecret: resp.PollSecret})
	if err != nil {
		t.Fatal(err)
	}
	return resp.AgentId, key, p.LeafDer
}

// Coded is one enrollment the way a current agent makes it: a key, a code, and only the code's hash sent.
type Coded struct {
	Resp  *continuumv1.EnrollResponse
	Req   *continuumv1.EnrollRequest
	Code  string
	Token string
}

// EnrollCoded makes a token and enrols with it.
func EnrollCoded(t *testing.T, ctx context.Context, ag *agents.Service, fingerprint string) Coded {
	t.Helper()
	secret, _, err := ag.CreateToken(ctx, "admin", "edge-patras", 2)
	if err != nil {
		t.Fatal(err)
	}
	return EnrollCodedWith(t, ctx, ag, secret, fingerprint, nil, "")
}

// EnrollCodedWith sends an enrollment with the given token; key (a CSR made earlier) and code are reused for a retry.
func EnrollCodedWith(t *testing.T, ctx context.Context, ag *agents.Service, token, fingerprint string, csrDER []byte, code string) Coded {
	t.Helper()
	c, err := TryEnroll(t, ctx, ag, token, fingerprint, csrDER, code)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TryEnroll is EnrollCodedWith, and returns the refusal instead of failing the test.
func TryEnroll(t *testing.T, ctx context.Context, ag *agents.Service, token, fingerprint string, csrDER []byte, code string) (Coded, error) {
	t.Helper()
	if csrDER == nil {
		csrDER, _ = CSR(t)
	}
	if code == "" {
		var err error
		if code, err = approval.New(); err != nil {
			t.Fatal(err)
		}
	}
	hash, err := approval.Hash(code, csrDER)
	if err != nil {
		t.Fatal(err)
	}
	req := &continuumv1.EnrollRequest{Token: token, CsrDer: csrDER, ClusterFingerprint: fingerprint, InstalledAccessTier: 2, AgentVersion: "0.1.0", ApprovalCodeHash: hash}
	resp, err := ag.Enroll(ctx, "10.0.0.1", req)
	return Coded{Resp: resp, Req: req, Code: code, Token: token}, err
}
