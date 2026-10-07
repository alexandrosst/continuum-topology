package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAddressWarningsSayWhatLooksWrongAndNothingElse(t *testing.T) {
	has := func(ws []string, sub string) bool {
		for _, w := range ws {
			if strings.Contains(w, sub) {
				return true
			}
		}
		return false
	}
	for _, c := range []struct {
		name string
		addr string
		svc  *KubeService
		want []string // a warning containing each of these
		none bool     // no warning at all
	}{
		{name: "nothing recorded", addr: "", none: true},
		{name: "a public name on the gRPC port", addr: "otlp.example.com:4317", none: true},
		{name: "the HTTP port, which every printed command's gRPC export cannot use", addr: "otlp.example.com:4318", want: []string{"OTLP/HTTP"}},
		{name: "a private address", addr: "10.1.2.3:4317", want: []string{"private address"}},
		{name: "an IPv6 unique-local address", addr: "[fd00::5]:4317", want: []string{"private address"}},
		{name: "a ClusterIP gateway with an address nobody forwards", addr: "203.0.113.7:4317", svc: &KubeService{Type: "ClusterIP", Port: 4317}, want: []string{"ClusterIP", "listens only inside this cluster"}},
		{name: "a NodePort gateway at 4317, where no node listens", addr: "198.51.100.4:4317", svc: &KubeService{Type: "NodePort", Port: 4317, NodePort: 31317}, want: []string{"31317"}},
		{name: "a NodePort gateway at its node port", addr: "198.51.100.4:31317", svc: &KubeService{Type: "NodePort", Port: 4317, NodePort: 31317}, none: true},
		{name: "a load balancer with no address yet", addr: "203.0.113.7:4317", svc: &KubeService{Type: "LoadBalancer", Port: 4317}, want: []string{"no address yet"}},
		{name: "a load balancer that reports another address", addr: "203.0.113.7:4317", svc: &KubeService{Type: "LoadBalancer", Port: 4317, LoadBalancer: "203.0.113.99:4317"}, want: []string{"203.0.113.99:4317"}},
		{name: "a load balancer at its address", addr: "203.0.113.99:4317", svc: &KubeService{Type: "LoadBalancer", Port: 4317, LoadBalancer: "203.0.113.99:4317"}, none: true},
	} {
		got := addressWarnings(c.addr, c.svc)
		if c.none && len(got) != 0 {
			t.Errorf("%s: warned %v", c.name, got)
		}
		for _, w := range c.want {
			if !has(got, w) {
				t.Errorf("%s: no warning mentions %q: %v", c.name, w, got)
			}
		}
	}
}

func TestKubeClientReadsTheGatewaysNodePort(t *testing.T) {
	k := testKube(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"spec":{"type":"NodePort","ports":[{"port":4318,"nodePort":31318},{"port":4317,"nodePort":31317}]}}`))
	})
	svc, err := k.Service(context.Background(), "f-fusion-central")
	if err != nil || svc.Type != "NodePort" || svc.Port != 4317 || svc.NodePort != 31317 || svc.LoadBalancer != "" {
		t.Fatalf("%+v %v", svc, err)
	}
}

// inspectingKube is a fakeKube that can also say what the gateway's Service is.
type inspectingKube struct {
	*fakeKube
	svc KubeService
	err error
}

func (k *inspectingKube) Service(context.Context, string) (KubeService, error) { return k.svc, k.err }

// The address of the central operator is checked against the gateway's own Service wherever the server prints it: the
// operator's document, FUSION's, and the command that points an agent at it. A ClusterIP Service with a recorded
// address was accepted and printed as a fact before.
func TestCentralAddressIsCheckedAgainstTheGatewaysService(t *testing.T) {
	a := newIntentRig(t)
	f, k := newFusion(t, a.adminRig)
	ik := &inspectingKube{fakeKube: k, svc: KubeService{Type: "ClusterIP", Port: 4317}}
	f.Kube = ik
	a.a.Fusion = f
	if _, err := f.Enable(a.ctx, a.core, "alex"); err != nil {
		t.Fatal(err)
	}
	k.allReady()

	set := func(addr string) map[string]any {
		r := a.do("POST", "/api/v1/operators/"+CentralOperatorID+"/address", map[string]any{"address": addr}, withCookie(a.cookie))
		if r.Code != 200 {
			t.Fatalf("set address %q: %d %s", addr, r.Code, r.Body.String())
		}
		return r.json(t)
	}
	warnings := func(doc map[string]any) string {
		ws, _ := doc["addressWarnings"].([]any)
		var b strings.Builder
		for _, w := range ws {
			b.WriteString(w.(string) + "\n")
		}
		return b.String()
	}

	// ClusterIP: accepted (an Ingress may forward to it), but not presented as fact.
	doc := set("203.0.113.7:4317")
	if got := warnings(doc); !strings.Contains(got, "ClusterIP") {
		t.Fatalf("a gateway that is only a ClusterIP, with an address recorded, says nothing: %v", doc)
	}
	fusion := a.do("GET", "/api/v1/fusion", nil, withCookie(a.cookie)).json(t)
	if ws, _ := fusion["central"].(map[string]any)["warnings"].([]any); len(ws) == 0 {
		t.Fatalf("FUSION's own document does not carry the doubt: %v", fusion["central"])
	}
	cmd := a.command(t, map[string]any{"agentId": a.agent(t), "name": "w", "destination": opDest(CentralOperatorID)})
	if ws, _ := cmd["warnings"].([]any); len(ws) == 0 || !strings.Contains(ws[0].(string), "ClusterIP") {
		t.Fatalf("the command that points an agent there does not say so: %v", cmd["warnings"])
	}

	// A NodePort: a node does not listen on 4317, and the port a person leaves out is the node port, not 4317.
	f.svcMu.Lock()
	f.svcAt = time.Time{} // the Service was read a moment ago
	f.svcMu.Unlock()
	ik.svc = KubeService{Type: "NodePort", Port: 4317, NodePort: 31317}
	if got := warnings(set("198.51.100.4:4317")); !strings.Contains(got, "31317") {
		t.Fatalf("4317 on a NodePort Service: %q", got)
	}
	doc = set("198.51.100.4")
	if doc["address"] != "198.51.100.4:31317" || warnings(doc) != "" {
		t.Fatalf("an address with no port on a NodePort Service: %v", doc)
	}

	// With no way to read the Service nothing is claimed either way.
	f.svcMu.Lock()
	f.svcAt = time.Time{}
	f.svcMu.Unlock()
	ik.err = ErrKubeForbidden
	if got := warnings(set("198.51.100.4:4317")); got != "" {
		t.Fatalf("a Service this server may not read was held against the address: %q", got)
	}
}

// A regional operator's address gets the doubts that need no Service, and a command that points at an operator with no
// address says that it dials a name that only resolves inside its own cluster.
func TestRegionalAddressDoubtsAndTheCommandForAnOperatorWithNoAddress(t *testing.T) {
	a := newIntentRig(t)
	op := a.operator(t, "athens")
	cmd := a.command(t, map[string]any{"agentId": a.agent(t), "name": "noaddr", "destination": opDest(op.ID)})
	ws, _ := cmd["warnings"].([]any)
	if len(ws) != 1 || !strings.Contains(ws[0].(string), "in-cluster name") {
		t.Fatalf("an operator with no address: %v", cmd["warnings"])
	}
	doc := a.do("POST", "/api/v1/operators/"+op.ID+"/address", map[string]any{"address": "otlp.example.com:4318"}, withCookie(a.cookie)).json(t)
	if got, _ := doc["addressWarnings"].([]any); len(got) != 1 || !strings.Contains(got[0].(string), "4318") {
		t.Fatalf("the HTTP port: %v", doc["addressWarnings"])
	}
	// Once recorded the address is not a doubt of the command's own kind.
	a.do("POST", "/api/v1/operators/"+op.ID+"/address", map[string]any{"address": "otlp.example.com:4317"}, withCookie(a.cookie))
	b := a.agent(t)
	cmd = a.command(t, map[string]any{"agentId": b, "name": "withaddr", "destination": opDest(op.ID)})
	if ws, _ := cmd["warnings"].([]any); len(ws) != 0 {
		t.Fatalf("a public name on 4317: %v", ws)
	}
}

// ---------------------------------------------------------------------------------------------------------------
// The look at an address.

// receiver starts a TLS server with the operator's receiver certificate that requires a client certificate and
// returns the address to dial it at.
func receiver(t *testing.T, certPEM, keyPEM, caPEM []byte) string {
	t.Helper()
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	l, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { // drive the handshake to its end, as a real server does
				defer c.Close()
				c.SetDeadline(time.Now().Add(2 * time.Second))
				c.Read(make([]byte, 1))
			}()
		}
	}()
	return l.Addr().String()
}

func TestAddressCheckTellsWhatAnswersThere(t *testing.T) {
	a := newIntentRig(t)
	doc := a.createOperatorDoc(t, a.cookie, extBody("athens", a.approvedCluster(t, "8f3c2a9e-0701-4222-8333-944455556666")))
	id := doc["operator"].(map[string]any)["id"].(string)
	other := a.createOperatorDoc(t, a.cookie, extBody("patras", a.approvedCluster(t, "8f3c2a9e-0702-4222-8333-944455556666")))
	tlsCmd := func(d map[string]any) (crt, key []byte) {
		return pemAfter(t, d["tlsSecretCommand"].(string), "tls.crt"), pemAfter(t, d["tlsSecretCommand"].(string), "tls.key")
	}
	crt, key := tlsCmd(doc)
	ca := pemAfter(t, doc["tlsSecretCommand"].(string), "ca.crt")
	oCrt, oKey := tlsCmd(other)
	oCA := pemAfter(t, other["tlsSecretCommand"].(string), "ca.crt")

	check := func() AddressProbe {
		r := a.do("POST", "/api/v1/operators/"+id+"/address/check", nil, withCookie(a.cookie))
		if r.Code != 200 {
			t.Fatalf("check: %d %s", r.Code, r.Body.String())
		}
		j := r.json(t)
		return AddressProbe{State: j["state"].(string), Message: j["message"].(string)}
	}
	if got := check(); got.State != "no-address" {
		t.Fatalf("before an address: %+v", got)
	}
	a.do("POST", "/api/v1/operators/"+id+"/address", map[string]any{"address": "203.0.113.9:4317"}, withCookie(a.cookie))
	redirect := func(to string) {
		a.a.probes = addressProbes{} // the last look is not reused
		a.a.probeDial = func(ctx context.Context, network, addr string) (net.Conn, error) {
			if addr != "203.0.113.9:4317" {
				t.Errorf("dialled %q, not the recorded address", addr)
			}
			return (&net.Dialer{}).DialContext(ctx, network, to)
		}
	}

	redirect(receiver(t, crt, key, ca))
	if got := check(); got.State != "reachable" || !strings.Contains(got.Message, "this operator's certificate") {
		t.Fatalf("the operator's own receiver: %+v", got)
	}
	// Something else answers with ANOTHER operator's certificate: the address points at the wrong receiver.
	redirect(receiver(t, oCrt, oKey, oCA))
	if got := check(); got.State != "wrong-certificate" {
		t.Fatalf("another operator's receiver: %+v", got)
	}
	// A listener that is not TLS at all.
	plain, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { plain.Close() })
	go func() {
		for {
			c, err := plain.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				c.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
				time.Sleep(200 * time.Millisecond)
			}()
		}
	}()
	redirect(plain.Addr().String())
	if got := check(); got.State != "no-tls" {
		t.Fatalf("a plain listener: %+v", got)
	}
	// Nothing listens: the port of a listener that was closed.
	dead, _ := net.Listen("tcp", "127.0.0.1:0")
	deadAddr := dead.Addr().String()
	dead.Close()
	redirect(deadAddr)
	if got := check(); got.State != "refused" {
		t.Fatalf("a closed port: %+v", got)
	}
	// The same look is not repeated at once.
	n := 0
	a.a.probes = addressProbes{}
	a.a.probeDial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		n++
		return nil, &net.DNSError{Err: "no such host", Name: "x", IsNotFound: true}
	}
	if got := check(); got.State != "unresolved" {
		t.Fatalf("a name with no address: %+v", got)
	}
	check()
	if n != 1 {
		t.Fatalf("%d connections for two asks within a moment", n)
	}
	// Only an administrator may ask.
	_, viewer := a.user(t, "vi", RoleEditor)
	if r := a.do("POST", "/api/v1/operators/"+id+"/address/check", nil, withCookie(viewer)); r.Code != 403 {
		t.Fatalf("an editor: %d", r.Code)
	}
}
