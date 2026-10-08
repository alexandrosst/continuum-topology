package server

import (
	"context"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"net"
	"strings"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/pki"
	"continuum/internal/store"
)

// RenewTelemetryCert renews an operator certificate for whoever holds its private key: a regional operator's receiver
// certificate, its exporter's client certificate towards another operator, or the client certificate a cluster
// presents to an operator. Nothing is configured per holder and no secret is issued for this: the old certificate is
// the credential, and the proof (a signature by its key over the new request) shows the caller still holds it. The new
// key is made by the caller and never seen here.
//
// Every call decides again whether the holder is entitled to a certificate, so removing the entitlement is the
// revocation: a cluster taken out of an operator's scope or of its telemetry intents, an agent that was revoked, an
// operator that was revoked or deleted simply stops being renewed and its certificate ends within pki.OperatorTLSTTL.
// A certificate that must stop working at once still means replacing that operator's CA (recreate the operator): a
// receiver cannot check a revocation list.
//
// The call is anonymous like Rejoin and bounded the same way: per address, and per certificate. The certificate must
// have been signed by the operator's own CA and ended no more than pki.OperatorRenewGrace ago. What the new
// certificate says (name, organisation, usage, hosts) is copied from the old one, which this server wrote; nothing
// the caller asks for is used except the public key.
func (c *Core) RenewTelemetryCert(ctx context.Context, ip string, req *continuumv1.RenewTelemetryCertRequest) (*continuumv1.RenewTelemetryCertResponse, error) {
	if !c.EnrollRL.Allow("renew-cert:" + LimitKey(ip)) {
		return nil, errf(KindRateLimited, "too many attempts, slow down")
	}
	deny := errf(KindUnauthenticated, "this certificate cannot be renewed; install it again from the operators page")
	if len(req.OldLeafDer) == 0 || len(req.OldLeafDer) > 4096 {
		return nil, deny
	}
	old, err := x509.ParseCertificate(req.OldLeafDer)
	if err != nil || len(old.Subject.Organization) != 1 {
		return nil, deny
	}
	// Whose is it: the operator id is the start of the name (a receiver's is exactly it, a client's is
	// "<operator>-export-<sender>"). The signature below is what makes the claim worth anything.
	opID, _, _ := strings.Cut(old.Subject.CommonName, "-export")
	var eku x509.ExtKeyUsage
	switch {
	case len(old.ExtKeyUsage) == 1 && old.ExtKeyUsage[0] == x509.ExtKeyUsageServerAuth && old.Subject.CommonName == opID:
		eku = x509.ExtKeyUsageServerAuth
	case len(old.ExtKeyUsage) == 1 && old.ExtKeyUsage[0] == x509.ExtKeyUsageClientAuth && old.Subject.CommonName != opID:
		eku = x509.ExtKeyUsageClientAuth
	default:
		return nil, deny
	}
	op, err := c.Store.GetOperator(ctx, opID)
	if err != nil || op.OrgID != old.Subject.Organization[0] || len(op.ClientCACertPEM) == 0 {
		return nil, deny
	}
	oc := c.ForOrg(op.OrgID)
	oc.depMu.RLock() // see Core.depMu: a revoke or delete cannot come between the checks below and the ledger write
	defer oc.depMu.RUnlock()
	if op, err = oc.operatorInOrg(ctx, op.ID); err != nil || op.Status != store.OperatorActive {
		return nil, deny
	}
	issuer, caPEM, err := oc.operatorIssuer(ctx, op)
	if err != nil {
		c.Log.Error("operator CA not available for a certificate renewal", "operator", op.ID, "err", err)
		return nil, errf(KindInternal, "cannot renew right now")
	}
	if _, err := issuer.VerifyExpired(req.OldLeafDer, eku); err != nil {
		return nil, deny
	}
	now := c.Now()
	if now.Before(old.NotBefore) || now.Sub(old.NotAfter) > pki.OperatorRenewGrace {
		return nil, deny
	}
	if err := pki.VerifyRenewalProof(old, req.CsrDer, req.Proof); err != nil {
		return nil, deny
	}
	csr, err := pki.ParseCSR(req.CsrDer)
	if err != nil {
		return nil, errf(KindInvalid, "%v", err)
	}
	serial := old.SerialNumber.Text(16)
	if c.RenewRL != nil && !c.RenewRL.Allow("renew-cert:"+serial) {
		return nil, errf(KindRateLimited, "renewing too often")
	}

	// From here the caller has proved it holds the key, so it may be told why it is refused.
	kind, sender := store.OperatorCertReceiver, ""
	if eku == x509.ExtKeyUsageClientAuth {
		kind = store.OperatorCertClient
		var ok bool
		if sender, ok, err = oc.entitledSender(ctx, op, old.Subject.CommonName); err != nil {
			return nil, err
		}
		if !ok {
			oc.audit(ctx, "renewal", "operator-cert-renewal-refused", "operator", op.ID, "client certificate "+old.Subject.CommonName+" (serial "+serial+") is no longer held by a sender of this operator")
			return nil, errf(KindForbidden, "this certificate's holder no longer sends to operator %s; it is not renewed and will stop working on %s", op.ID, old.NotAfter.UTC().Format("2006-01-02"))
		}
	} else if op.ID == CentralOperatorID {
		return nil, errf(KindForbidden, "the central operator's own certificate is renewed by the server")
	}

	hosts := append([]string(nil), old.DNSNames...)
	for _, a := range old.IPAddresses {
		hosts = append(hosts, net.IP(a).String())
	}
	subject := pkix.Name{CommonName: old.Subject.CommonName, Organization: old.Subject.Organization}
	der, err := issuer.SignLeaf(csr.PublicKey, subject, pki.OperatorTLSTTL, eku, hosts)
	if err != nil {
		return nil, err
	}
	leafPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	// A certificate that cannot be recorded is not handed out, as with every other issue: the ledger is how the
	// server knows who holds what.
	if err := oc.recordOperatorCert(ctx, "renewal", op, kind, sender, leafPEM); err != nil {
		return nil, err
	}
	oc.noteCertEnds(ctx, op)
	notAfter := time.Time{}
	if t := certNotAfter(leafPEM); t != nil {
		notAfter = *t
	}
	return &continuumv1.RenewTelemetryCertResponse{LeafDer: der, CaPem: caPEM, NotAfter: tsProto(&notAfter)}, nil
}

// entitledSender says which current sender of op a client certificate named cn belongs to. The name is made from
// the sender's id by one function (pki.OperatorClientCN), so asking it for each current sender and comparing is
// exact, and works for a certificate issued before the ledger existed.
func (c *Core) entitledSender(ctx context.Context, op store.Operator, cn string) (string, bool, error) {
	senders, err := c.operatorSenders(ctx)
	if err != nil {
		return "", false, fmt.Errorf("cannot decide right now: %w", err)
	}
	for s := range senders[op.ID] {
		if pki.OperatorClientCN(op.ID, s) == cn {
			return s, true, nil
		}
	}
	return "", false, nil
}
