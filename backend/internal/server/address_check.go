package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"continuum/internal/store"
)

// What this server can say about the "Reachable at" address of an operator without being told, which is every
// statement an address can be checked against: the shape of the address itself, the gateway's own Service (for the
// central operator, which lives next to this server), and a look at what answers there (probeAddress).
//
// None of it refuses an address: a load balancer, an Ingress or a DNS name in front of a receiver can make an address
// that looks wrong right, and this server cannot see them. What it does is stop printing an address as if it were a
// fact: the document says what is doubtful about it, in words a person can act on.

// addressWarnings are the doubts about a recorded address, in plain words; empty when there is nothing to say. svc is
// the gateway's Service when the address is the central operator's and the server could read it, nil otherwise.
func addressWarnings(addr string, svc *KubeService) []string {
	if addr == "" {
		return nil
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil
	}
	var out []string
	if port == "4318" {
		out = append(out, "4318 is the OTLP/HTTP port. Every command this server prints exports over gRPC, which is on 4317: unless a load balancer or an Ingress maps 4318 to a receiver's gRPC port, exports to this address fail.")
	}
	if why := unreachableHostWhy(host); why == "a private address" {
		out = append(out, fmt.Sprintf("%s is a private address: only a network that can route to it (the same network, a VPN, peering) reaches it. A cluster elsewhere cannot.", host))
	}
	if svc != nil {
		out = append(out, gatewayServiceWarnings(host, port, *svc)...)
	}
	return out
}

// gatewayServiceWarnings compare an address with what the gateway's Service really publishes.
func gatewayServiceWarnings(host, port string, svc KubeService) []string {
	switch svc.Type {
	case "NodePort":
		if svc.NodePort != 0 && port != strconv.Itoa(svc.NodePort) {
			return []string{fmt.Sprintf("The gateway's Service is a NodePort: every node listens on %d for it, not on %s. Record <a node's address>:%d.", svc.NodePort, port, svc.NodePort)}
		}
	case "LoadBalancer":
		var out []string
		if svc.LoadBalancer == "" {
			out = append(out, "The gateway's Service is a LoadBalancer that has no address yet: nothing answers on this address until the cloud has made one (kubectl get svc shows it when it has).")
			return out
		}
		lbHost, lbPort, _ := net.SplitHostPort(svc.LoadBalancer)
		if port != lbPort {
			out = append(out, fmt.Sprintf("The load balancer publishes the gateway on port %s, not %s.", lbPort, port))
		}
		if !strings.EqualFold(host, lbHost) {
			out = append(out, fmt.Sprintf("The load balancer reports %s, not %s. That is only right if %s is a DNS name or proxy in front of it.", svc.LoadBalancer, host, host))
		}
		return out
	default: // ClusterIP (the chart's default), ExternalName, or a type this does not know
		t := svc.Type
		if t == "" {
			t = "ClusterIP"
		}
		return []string{fmt.Sprintf("The gateway's Service is a %s: it listens only inside this cluster, so nothing on %s:%s answers unless an Ingress, a Gateway route or a load balancer you set up forwards there. Setting central.service.type to LoadBalancer or NodePort in the FUSION chart exposes it.", t, host, port)}
	}
	return nil
}

// AddressProbe is what one look at an address found.
type AddressProbe struct {
	// State is reachable (a TLS server answered with this operator's own certificate), no-tls (a server answered, but not
	// with TLS), wrong-certificate (something answered with a certificate that is not this operator's), refused (nothing
	// listens on that port), no-answer (the connection timed out), unresolved (the name has no address) or error.
	State     string `json:"state"`
	Message   string `json:"message"`
	CheckedAt string `json:"checkedAt"`
	// From says where the look was taken: this server, which sees the network it sits in, not the one a sender sits in.
	From string `json:"from"`
}

const probeTimeout = 5 * time.Second

// probeAddress connects to addr and, for an operator whose receiver speaks TLS, completes a handshake that verifies the
// server certificate against the operator's own CA by its stable name (serverName), the same check a sending collector
// makes. It sends no credential and no telemetry. caPEM nil means the receiver is not known to speak TLS: only the
// connection is checked. dial is the network (net.Dialer's DialContext; a test replaces it).
func probeAddress(ctx context.Context, addr string, caPEM []byte, serverName string, now time.Time, dial func(ctx context.Context, network, addr string) (net.Conn, error)) AddressProbe {
	res := AddressProbe{CheckedAt: rfc(now), From: "this server"}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	conn, err := dial(ctx, "tcp", addr)
	if err != nil {
		res.State, res.Message = classifyDialError(err, addr)
		return res
	}
	defer conn.Close()
	if len(caPEM) == 0 {
		res.State, res.Message = "reachable", fmt.Sprintf("%s accepts connections. This operator's receiver is not known to use TLS, so nothing more was checked.", addr)
		return res
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		res.State, res.Message = "error", "the operator's CA certificate could not be read"
		return res
	}
	c := tls.Client(conn, &tls.Config{ServerName: serverName, RootCAs: pool, MinVersion: tls.VersionTLS12, NextProtos: []string{"h2"}})
	if err := c.HandshakeContext(ctx); err != nil {
		var (
			unknown  x509.UnknownAuthorityError
			hostErr  x509.HostnameError
			invalid  x509.CertificateInvalidError
			recHdr   tls.RecordHeaderError
			certVeri *tls.CertificateVerificationError
		)
		switch {
		case errors.As(err, &recHdr):
			res.State, res.Message = "no-tls", fmt.Sprintf("something answers on %s, but not with TLS: it is not a receiver that uses a certificate. A sender that verifies the receiver (every command this server prints does) cannot connect.", addr)
		case errors.As(err, &unknown), errors.As(err, &hostErr), errors.As(err, &invalid), errors.As(err, &certVeri):
			res.State, res.Message = "wrong-certificate", fmt.Sprintf("something answers on %s, but its certificate is not this operator's (%v). The address points at something else, or at a receiver that was installed with other certificates.", addr, err)
		case strings.Contains(err.Error(), "bad certificate") || strings.Contains(err.Error(), "certificate required"):
			// The server certificate was accepted and the server then asked for the client certificate this probe does not
			// hold: that is what a receiver which requires mutual TLS does.
			res.State, res.Message = "reachable", fmt.Sprintf("%s answers with this operator's certificate and asks for a client certificate, as it should.", addr)
		default:
			res.State, res.Message = classifyDialError(err, addr)
		}
		return res
	}
	res.State, res.Message = "reachable", fmt.Sprintf("%s answers with this operator's certificate.", addr)
	return res
}

func classifyDialError(err error, addr string) (state, msg string) {
	var dns *net.DNSError
	switch {
	case errors.As(err, &dns):
		return "unresolved", fmt.Sprintf("the name in %s does not resolve to an address.", addr)
	case errors.Is(err, syscall.ECONNREFUSED):
		return "refused", fmt.Sprintf("the connection to %s was refused: the host answers, nothing listens on that port.", addr)
	case errors.Is(err, context.DeadlineExceeded), isTimeout(err):
		return "no-answer", fmt.Sprintf("%s did not answer within %d seconds: nothing routes there, or a firewall drops the connection.", addr, int(probeTimeout/time.Second))
	}
	return "error", fmt.Sprintf("could not connect to %s: %v", addr, err)
}

func isTimeout(err error) bool {
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}

// addressProbes remembers the last look per operator, so asking again at once (a double click, several admins) is
// answered from it instead of making another connection.
type addressProbes struct {
	mu   sync.Mutex
	last map[string]probed
}

type probed struct {
	addr string
	at   time.Time
	res  AddressProbe
}

const probeReuse = 5 * time.Second

func (p *addressProbes) get(id, addr string, now time.Time) (AddressProbe, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.last[id]
	if ok && e.addr == addr && now.Sub(e.at) >= 0 && now.Sub(e.at) < probeReuse {
		return e.res, true
	}
	return AddressProbe{}, false
}

func (p *addressProbes) put(id, addr string, now time.Time, res AddressProbe) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.last == nil {
		p.last = map[string]probed{}
	}
	p.last[id] = probed{addr, now, res}
}

// probeSettings is what looking at an operator's address needs: where it is (the address recorded for it - never its
// in-cluster name, which says nothing about what a cluster elsewhere reaches), which CA signed its receiver certificate
// and the name that certificate is verified by. A receiver known to speak no TLS has no CA.
func (a *Admin) probeSettings(c *Core, op store.Operator) (addr string, caPEM []byte, serverName string) {
	op = a.advertised(op)
	addr = op.Address
	if op.ReceiverAuth == store.ReceiverAuthMTLS {
		if len(op.ClientCACertPEM) > 0 {
			caPEM = op.ClientCACertPEM
		} else if c.CA != nil {
			caPEM = c.CA.CertPEM()
		}
	}
	return addr, caPEM, operatorServerName(op)
}

// checkOperatorAddress is POST /operators/{id}/address/check: one look at the operator's recorded address from this
// server, reported as it is. It only ever connects to the address the operator already has recorded (nothing in the
// request chooses a target), at most once every probeReuse per operator, and tells what it found in coarse words.
func (a *Admin) checkOperatorAddress(w http.ResponseWriter, r *http.Request) {
	c := a.core(r)
	op, err := c.GetOperator(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	if op.Status != store.OperatorActive {
		a.fail(w, errf(KindConflict, "only an active operator's address can be checked"))
		return
	}
	addr, caPEM, serverName := a.probeSettings(c, op)
	now := c.Now()
	if addr == "" {
		writeJSON(w, 200, AddressProbe{State: "no-address", Message: "No address is recorded for this operator, so there is nothing to check.", CheckedAt: rfc(now), From: "this server"})
		return
	}
	if res, ok := a.probes.get(op.ID, addr, now); ok {
		writeJSON(w, 200, res)
		return
	}
	dial := a.probeDial
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	res := probeAddress(r.Context(), addr, caPEM, serverName, now, dial)
	a.probes.put(op.ID, addr, now, res)
	writeJSON(w, 200, res)
}
