package link

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/probe"
)

// Distinctive strings, so that a test can look for them in anything that must not hold them.
const (
	testPassword = "pw-0123456789abcdef0123456789abcdef-SECRETMARK"
	testUser     = "ik-test0001"
)

var (
	keyOnce         sync.Once
	testPrivatePEM  string
	testPublicPEM   string
	testRequestSecr = []byte("request-secret-sixteen-plus-chars")
)

func testKeys(t testing.TB) (priv, pub string) {
	t.Helper()
	keyOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		der, _ := x509.MarshalPKCS8PrivateKey(k)
		pd, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
		testPrivatePEM = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
		testPublicPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pd}))
	})
	return testPrivatePEM, testPublicPEM
}

func usersCSV(user, password string) string {
	sum := sha256.Sum256([]byte("{" + user + "}" + password))
	return user + "," + hex.EncodeToString(sum[:]) + "\n"
}

func clientProto(t testing.TB) *continuumv1.LinkIperfClient {
	_, pub := testKeys(t)
	return &continuumv1.LinkIperfClient{PublicKeyPem: pub, Username: testUser, Password: testPassword}
}

func serverProto(t testing.TB) *continuumv1.LinkIperfServer {
	priv, _ := testKeys(t)
	return &continuumv1.LinkIperfServer{PrivateKeyPem: priv, UsersCsv: usersCSV(testUser, testPassword)}
}

// secretMarks are the things that must never appear in a log line, an error, a metric or an answer that is not the sealed one.
func secretMarks(t testing.TB) []string {
	priv, _ := testKeys(t)
	hash := strings.TrimSpace(usersCSV(testUser, testPassword))[len(testUser)+1:]
	return []string{testPassword, "SECRETMARK", "PRIVATE KEY", strings.TrimSpace(strings.Split(priv, "\n")[1]), hash}
}

func mustNotContain(t *testing.T, what, s string) {
	t.Helper()
	for _, m := range secretMarks(t) {
		if m != "" && strings.Contains(s, m) {
			t.Errorf("%s holds credential material (%q...)", what, m[:min(len(m), 12)])
		}
	}
}

// ---- redaction ----

func TestCredentialsNeverPrintThemselves(t *testing.T) {
	c := IperfClient{PublicKeyPEM: "pub", Username: testUser, Password: Secret(testPassword)}
	priv, _ := testKeys(t)
	s := IperfServer{PrivateKeyPEM: Secret(priv), UsersCSV: Secret(usersCSV(testUser, testPassword))}
	p := Peer{ID: "cl-a", Throughput: true, Iperf: &c}
	d := PeerDoc{ID: "cl-a", Iperf: &c}
	var sb strings.Builder
	for _, v := range []any{c, &c, s, &s, c.Password, p, &p, []Peer{p}, d, &d, PeersDoc{Peers: []PeerDoc{d}}, map[string]any{"c": c, "pw": c.Password, "s": s}} {
		for _, f := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d"} {
			fmt.Fprintf(&sb, f+"\n", v)
		}
		fmt.Fprintln(&sb, v)
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		sb.Write(b)
	}
	var txt, js bytes.Buffer
	slog.New(slog.NewTextHandler(&txt, nil)).Info("m", "c", c, "s", s, "pw", c.Password, "p", p, "pp", &p)
	slog.New(slog.NewJSONHandler(&js, nil)).Info("m", "c", c, "s", s, "pw", c.Password, "p", p, "pp", &p)
	sb.Write(txt.Bytes())
	sb.Write(js.Bytes())
	mustNotContain(t, "formatted credentials", sb.String())
	if !strings.Contains(sb.String(), "redacted") {
		t.Error("nothing was redacted: the test is not testing")
	}
}

func TestACredentialIsCheckedAndTheErrorNamesNoValue(t *testing.T) {
	priv, pub := testKeys(t)
	good := IperfClient{PublicKeyPEM: pub, Username: testUser, Password: Secret(testPassword)}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]IperfClient{
		"no key":          {Username: testUser, Password: Secret(testPassword)},
		"private as pub":  {PublicKeyPEM: priv, Username: testUser, Password: Secret(testPassword)},
		"comma in user":   {PublicKeyPEM: pub, Username: "a,b", Password: Secret(testPassword)},
		"newline in user": {PublicKeyPEM: pub, Username: "a\nb", Password: Secret(testPassword)},
		"short password":  {PublicKeyPEM: pub, Username: testUser, Password: Secret("short")},
		"space in pw":     {PublicKeyPEM: pub, Username: testUser, Password: Secret(strings.Repeat("a b ", 8))},
	} {
		err := c.Validate()
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		mustNotContain(t, name+" error", err.Error())
	}
	srv := IperfServer{PrivateKeyPEM: Secret(priv), UsersCSV: Secret(usersCSV(testUser, testPassword))}
	if err := srv.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]IperfServer{
		"public as private": {PrivateKeyPEM: Secret(pub), UsersCSV: srv.UsersCSV},
		"empty users":       {PrivateKeyPEM: srv.PrivateKeyPEM, UsersCSV: ""},
		"bad line":          {PrivateKeyPEM: srv.PrivateKeyPEM, UsersCSV: "user,nothex\n"},
		"two users alike":   {PrivateKeyPEM: srv.PrivateKeyPEM, UsersCSV: srv.UsersCSV + srv.UsersCSV},
	} {
		err := s.Validate()
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		mustNotContain(t, name+" error", err.Error())
	}
	// A 1024-bit key is too small.
	k, _ := rsa.GenerateKey(rand.Reader, 1024)
	pd, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	small := IperfClient{PublicKeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pd})), Username: testUser, Password: Secret(testPassword)}
	if small.Validate() == nil {
		t.Error("a 1024-bit key was accepted")
	}
}

func TestAConfigsCredentialsAreKeptOnlyForThePeersThatRunTestsAndWhenValid(t *testing.T) {
	p := NewPeers()
	withCreds := func(id string, throughput bool, c *continuumv1.LinkIperfClient) *continuumv1.LinkPeer {
		lp := peer(id, id, "10.0.0."+strconv.Itoa(len(id)))
		lp.Throughput, lp.IperfClient = throughput, c
		return lp
	}
	bad := clientProto(t)
	bad.Password = "x"
	dropped := p.Set("cl-me", []*continuumv1.LinkPeer{withCreds("cl-a", true, clientProto(t)), withCreds("cl-bb", false, clientProto(t)), withCreds("cl-ccc", true, bad)})
	by := map[string]Peer{}
	for _, pe := range p.List() {
		by[pe.ID] = pe
	}
	if by["cl-a"].Iperf == nil || by["cl-a"].Iperf.Username != testUser || by["cl-a"].Iperf.Password.Reveal() != testPassword {
		t.Errorf("a peer the cluster tests lost its credentials: %+v", by["cl-a"].Iperf)
	}
	if by["cl-bb"].Iperf != nil {
		t.Error("credentials were kept for a peer this cluster does not test")
	}
	if by["cl-ccc"].Iperf != nil {
		t.Error("an invalid credential was kept")
	}
	if len(dropped) != 1 || !strings.Contains(dropped[0], "cl-ccc") {
		t.Errorf("dropped = %v", dropped)
	}
	mustNotContain(t, "the dropped notes", strings.Join(dropped, "\n"))

	// An agent that does not serve the credentials to a prober does not keep them either.
	q := NewPeers()
	q.DropIperf(true)
	q.Set("cl-me", []*continuumv1.LinkPeer{withCreds("cl-a", true, clientProto(t))})
	if q.List()[0].Iperf != nil {
		t.Error("an agent with a Secret of its own kept the issued credentials")
	}
}

// ---- the signed endpoints ----

type rig struct {
	srv   *httptest.Server
	meter *Meter
	now   time.Time
}

func newRig(t *testing.T, peersSecret, iperfSecret []byte) *rig {
	t.Helper()
	p := NewPeers()
	lp := peer("cl-a", "A", "10.0.0.1", "10.0.0.2")
	lp.Throughput, lp.IperfClient = true, clientProto(t)
	p.Set("cl-me", []*continuumv1.LinkPeer{lp})
	m := NewMeter(p)
	r := &rig{meter: m, now: time.Now()}
	m.now = func() time.Time { return r.now }
	if peersSecret != nil {
		m.ServePeers(peersSecret)
	}
	if iperfSecret != nil {
		m.ServeIperf(iperfSecret)
	}
	r.srv = httptest.NewServer(m.Handler())
	t.Cleanup(r.srv.Close)
	return r
}

var peersSecretT = []byte("peers-secret-sixteen-plus-chars")

func TestTheProberGetsItsClientCredentialsSealedAndOnlyWhenItAsks(t *testing.T) {
	r := newRig(t, peersSecretT, testRequestSecr)
	// Asking for them: sealed, opened into the peer.
	d, err := FetchPeersWith(context.Background(), r.srv.Client(), r.srv.URL, peersSecretT, r.now, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Peers) != 1 || d.Peers[0].Iperf == nil || d.Peers[0].Iperf.Username != testUser || d.Peers[0].Iperf.Password.Reveal() != testPassword {
		t.Fatalf("%+v", d.Peers)
	}
	// The prober's side applies the same checks as a Config.
	p := NewPeers()
	if dropped := p.SetFromDoc(d); len(dropped) != 0 || p.List()[0].Iperf == nil {
		t.Fatalf("dropped %v, iperf %v", dropped, p.List()[0].Iperf)
	}
	// Not asking (an older prober, or an install with a Secret of its own): the list alone.
	d2, err := FetchPeers(context.Background(), r.srv.Client(), r.srv.URL, peersSecretT, r.now)
	if err != nil {
		t.Fatal(err)
	}
	if d2.Peers[0].Iperf != nil || d2.IperfSealed != "" {
		t.Error("credentials were sent to a prober that did not ask")
	}
	// What is on the wire: neither answer holds a credential in the clear.
	for _, iperf := range []bool{true, false} {
		body, _ := newCredRequest(iperf)
		if !iperf {
			body = nil
		}
		req, _, _ := signedCredRequest(context.Background(), r.srv.URL+PathPeers, peersContext, ProberNode, peersSecretT, r.now, body)
		resp, err := r.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		mustNotContain(t, "the peers answer on the wire", string(raw))
		if iperf && !bytes.Contains(raw, []byte("iperf_sealed")) {
			t.Error("the sealed credentials are not in the answer")
		}
		if !iperf && bytes.Contains(raw, []byte("iperf_sealed")) {
			t.Error("sealed credentials in an answer that was not asked for them")
		}
	}
	// A prober without the iperf option on the agent (a Secret of its own): nothing to give.
	r2 := newRig(t, peersSecretT, nil)
	d3, err := FetchPeersWith(context.Background(), r2.srv.Client(), r2.srv.URL, peersSecretT, r2.now, true)
	if err != nil || d3.Peers[0].Iperf != nil {
		t.Errorf("an agent that does not serve credentials sent some: %v %v", err, d3.Peers[0].Iperf)
	}
}

func TestTheResponderGetsItsServerCredentialsAndNothingElseCanAsk(t *testing.T) {
	r := newRig(t, peersSecretT, testRequestSecr)
	if why := r.meter.SetIperfServer(serverProto(t)); why != "" {
		t.Fatal(why)
	}
	got, err := FetchIperfServer(context.Background(), r.srv.Client(), r.srv.URL, testRequestSecr, r.now)
	if err != nil {
		t.Fatal(err)
	}
	priv, _ := testKeys(t)
	if got.PrivateKeyPEM.Reveal() != priv || got.UsersCSV.Reveal() != usersCSV(testUser, testPassword) {
		t.Error("the responder did not get what the agent holds")
	}

	post := func(mutate func(*http.Request), secret []byte, ctx, node string, body []byte) (int, []byte) {
		t.Helper()
		if body == nil {
			body, _ = newCredRequest(false)
		}
		req, _, _ := signedCredRequest(context.Background(), r.srv.URL+PathIperfServer, ctx, node, secret, r.now, body)
		if mutate != nil {
			mutate(req)
		}
		resp, err := r.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, raw
	}
	// What a good request gets is sealed.
	code, raw := post(nil, testRequestSecr, iperfServerContext, IperfServerNode, nil)
	if code != 200 {
		t.Fatalf("a good request got %d", code)
	}
	mustNotContain(t, "the server-credentials answer on the wire", string(raw))

	for name, c := range map[string]struct {
		secret    []byte
		ctx, node string
		mutate    func(*http.Request)
		body      []byte
		want      int
	}{
		"the prober's secret":        {secret: peersSecretT, ctx: iperfServerContext, node: IperfServerNode, want: 401},
		"the prober's context":       {secret: testRequestSecr, ctx: peersContext, node: IperfServerNode, want: 401},
		"the default context":        {secret: testRequestSecr, ctx: "", node: IperfServerNode, want: 401},
		"another node name":          {secret: testRequestSecr, ctx: iperfServerContext, node: ProberNode, want: 400},
		"an old timestamp":           {secret: testRequestSecr, ctx: iperfServerContext, node: IperfServerNode, mutate: func(q *http.Request) { setTime(q, r.now.Add(-time.Hour), testRequestSecr) }, want: 401},
		"a future timestamp":         {secret: testRequestSecr, ctx: iperfServerContext, node: IperfServerNode, mutate: func(q *http.Request) { setTime(q, r.now.Add(time.Hour), testRequestSecr) }, want: 401},
		"a body that is not a nonce": {secret: testRequestSecr, ctx: iperfServerContext, node: IperfServerNode, body: []byte(`{}`), want: 400},
		"a missing signature":        {secret: testRequestSecr, ctx: iperfServerContext, node: IperfServerNode, mutate: func(q *http.Request) { q.Header.Del(probe.HeaderSig) }, want: 401},
	} {
		code, raw := post(c.mutate, c.secret, c.ctx, c.node, c.body)
		if code != c.want {
			t.Errorf("%s: %d, want %d", name, code, c.want)
		}
		mustNotContain(t, name+" answer", string(raw))
	}

	// A replay of a request the agent already answered is refused.
	body, _ := newCredRequest(false)
	send := func() int {
		req, _, _ := signedCredRequest(context.Background(), r.srv.URL+PathIperfServer, iperfServerContext, IperfServerNode, testRequestSecr, r.now, body)
		resp, err := r.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if a, b := send(), send(); a != 200 || b != 409 {
		t.Errorf("a request and its replay got %d and %d, want 200 and 409", a, b)
	}

	// The prober's secret cannot read the credentials, and the responder's cannot read the peers.
	if _, err := FetchIperfServer(context.Background(), r.srv.Client(), r.srv.URL, peersSecretT, r.now); err == nil {
		t.Error("the prober's secret fetched the server credentials")
	}
	if _, err := FetchPeers(context.Background(), r.srv.Client(), r.srv.URL, testRequestSecr, r.now); err == nil {
		t.Error("the responder's secret fetched the peer list")
	}
}

func setTime(req *http.Request, at time.Time, secret []byte) {
	ts := strconv.FormatInt(at.Unix(), 10)
	body, _ := io.ReadAll(req.Body)
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.Header.Set(probe.HeaderTime, ts)
	req.Header.Set(probe.HeaderSig, probe.SignContext(iperfServerContext, secret, ts, IperfServerNode, body))
}

func TestWithoutAnIperfSecretTheAgentHasNoSuchPathAndKeepsNothing(t *testing.T) {
	r := newRig(t, peersSecretT, nil)
	if why := r.meter.SetIperfServer(serverProto(t)); why != "" {
		t.Fatal(why)
	}
	if r.meter.iperfSrv != nil {
		t.Error("the agent kept server credentials that no pod may fetch")
	}
	if _, err := FetchIperfServer(context.Background(), r.srv.Client(), r.srv.URL, testRequestSecr, r.now); err == nil {
		t.Error("an agent that does not serve credentials answered")
	}
}

func TestAnAnswerBelongsToItsRequestAndOnlyOpensWithTheSecret(t *testing.T) {
	plain := []byte(`{"x":"` + testPassword + `"}`)
	sealed, err := seal(testRequestSecr, iperfServerSeal, "sig-1", plain)
	if err != nil {
		t.Fatal(err)
	}
	mustNotContain(t, "a sealed answer", string(sealed))
	if got, err := open(testRequestSecr, iperfServerSeal, "sig-1", sealed); err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("round trip: %v", err)
	}
	for name, f := range map[string]func() error{
		"another request": func() error { _, err := open(testRequestSecr, iperfServerSeal, "sig-2", sealed); return err },
		"another secret": func() error {
			_, err := open([]byte("another-secret-sixteen-plus"), iperfServerSeal, "sig-1", sealed)
			return err
		},
		"another label": func() error { _, err := open(testRequestSecr, peersIperfSeal, "sig-1", sealed); return err },
		"a flipped byte": func() error {
			b := append([]byte(nil), sealed...)
			b[len(b)-1] ^= 1
			_, err := open(testRequestSecr, iperfServerSeal, "sig-1", b)
			return err
		},
		"a short answer": func() error { _, err := open(testRequestSecr, iperfServerSeal, "sig-1", sealed[:5]); return err },
	} {
		if f() == nil {
			t.Errorf("%s: opened", name)
		}
	}
}

// What the agent serves on /metrics is not a place for credentials.
func TestMetricsAndErrorsCarryNoCredentials(t *testing.T) {
	r := newRig(t, peersSecretT, testRequestSecr)
	r.meter.SetIperfServer(serverProto(t))
	resp, err := r.srv.Client().Get(r.srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	mustNotContain(t, "/metrics", string(raw))
	var bad strings.Builder
	_, e1 := FetchIperfServer(context.Background(), r.srv.Client(), r.srv.URL, peersSecretT, r.now)
	_, e2 := FetchPeersWith(context.Background(), r.srv.Client(), r.srv.URL, []byte("not-the-secret-just-sixteen"), r.now, true)
	for _, e := range []error{e1, e2} {
		if e != nil {
			bad.WriteString(e.Error())
		}
	}
	mustNotContain(t, "errors", bad.String())
	_ = os.Stderr
}
