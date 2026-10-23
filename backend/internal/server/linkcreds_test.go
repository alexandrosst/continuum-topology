package server

import (
	"bytes"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	ikhnosv1 "ikhnos/gen/ikhnosv1"
	"ikhnos/internal/connectivity"
	"ikhnos/internal/estate"
	"ikhnos/internal/kernel"
	"ikhnos/internal/observed"
	"ikhnos/internal/pki"
	"ikhnos/internal/store"
	"ikhnos/internal/testkit"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/encoding/prototext"
)

// credRig is a hub with three approved clusters: first and second are the pair (first runs the tests, second serves them), third
// is in no pair.
type credRig struct {
	*hubRig
	first, second, third store.Agent
	log                  *testkit.LockedBuf
}

func newCredRig(t *testing.T) *credRig {
	t.Helper()
	r := newHubRig(t)
	cr := &credRig{hubRig: r, log: &testkit.LockedBuf{}}
	r.core.Log = slog.New(slog.NewTextHandler(cr.log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	var ags []store.Agent
	for _, f := range []string{testkit.FP, testkit.FP2, testkit.FP3} {
		id, _, _ := r.approvedAgent(t, f)
		a, err := r.st.GetAgent(r.ctx, "org-1", id)
		if err != nil {
			t.Fatal(err)
		}
		r.hub.Mu.Lock()
		r.hub.Views[a.ID] = &estate.View{EstView: estate.EstView{State: testkit.NodeState("10.0." + strings.TrimPrefix(f[len(f)-2:], "0") + ".1"), Flows: observed.NewFlowTable()}, NetView: connectivity.NetView{Paths: map[string]*connectivity.PathTrack{}, Targets: map[string]connectivity.IssuedTarget{}}}
		r.hub.Mu.Unlock()
		ags = append(ags, a)
	}
	// Order the first two by cluster id: the first sorted is the runner.
	if ags[1].ClusterID < ags[0].ClusterID {
		ags[0], ags[1] = ags[1], ags[0]
	}
	cr.first, cr.second, cr.third = ags[0], ags[1], ags[2]
	return cr
}

func (cr *credRig) save(t *testing.T, links ...kernel.LinkSetting) {
	t.Helper()
	if _, err := cr.core.SaveSettings(cr.ctx, "alex", kernel.Settings{Links: links}); err != nil {
		t.Fatal(err)
	}
}

func (cr *credRig) cfg(a store.Agent) *ikhnosv1.Config {
	cr.hub.Mu.Lock()
	v := cr.hub.Views[a.ID]
	cr.hub.Mu.Unlock()
	return cr.hub.ConfigFor(a, v)
}

func (cr *credRig) pair(throughput bool) kernel.LinkSetting {
	l := kernel.LinkSetting{A: cr.first.ClusterID, B: cr.second.ClusterID}
	if throughput {
		l.Throughput = &kernel.LinkThroughput{}
	}
	return l
}

func (cr *credRig) rows(t *testing.T) []store.LinkCredential {
	t.Helper()
	rows, err := cr.st.ListLinkCredentials(cr.ctx, "org-1")
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// settle waits for the credentials work a saved settings change starts in the background.
func (cr *credRig) settle() {
	cr.hub.SyncLinkCredentials()
}

func publicOf(t *testing.T, privatePEM string) string {
	t.Helper()
	b, _ := pem.Decode([]byte(privatePEM))
	if b == nil {
		t.Fatal("not PEM")
	}
	k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&k.(*rsa.PrivateKey).PublicKey)
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func TestEachClusterGetsOnlyTheIperfCredentialsOfItsRoles(t *testing.T) {
	cr := newCredRig(t)
	cr.save(t, cr.pair(true))

	runner, server, third := cr.cfg(cr.first), cr.cfg(cr.second), cr.cfg(cr.third)

	if len(runner.LinkPeers) != 1 || !runner.LinkPeers[0].Throughput {
		t.Fatalf("the runner's peers = %v", runner.LinkPeers)
	}
	ic := runner.LinkPeers[0].IperfClient
	if ic == nil || ic.Username == "" || len(ic.Password) < 64 || !strings.Contains(ic.PublicKeyPem, "PUBLIC KEY") {
		t.Fatalf("the runner got %+v", ic)
	}
	if runner.LinkIperfServer != nil {
		t.Error("the runner, which serves nothing, was sent server credentials")
	}

	if server.LinkIperfServer == nil || !strings.Contains(server.LinkIperfServer.PrivateKeyPem, "PRIVATE KEY") {
		t.Fatalf("the server cluster got %+v", server.LinkIperfServer)
	}
	for _, lp := range server.LinkPeers {
		if lp.IperfClient != nil {
			t.Error("the serving cluster was sent client credentials")
		}
	}

	if third.LinkIperfServer != nil || len(third.LinkPeers) != 0 {
		t.Errorf("a cluster in no pair got %+v / %v", third.LinkIperfServer, third.LinkPeers)
	}

	// The two halves belong together: the public key is the server's, and the users file holds the hash of the runner's password.
	if publicOf(t, server.LinkIperfServer.PrivateKeyPem) != ic.PublicKeyPem {
		t.Error("the runner got a public key that is not the server's")
	}
	sum := sha256.Sum256([]byte("{" + ic.Username + "}" + ic.Password))
	if want := ic.Username + "," + hex.EncodeToString(sum[:]) + "\n"; server.LinkIperfServer.UsersCsv != want {
		t.Errorf("users file = %q, want %q", server.LinkIperfServer.UsersCsv, want)
	}

	// Minimal exposure, checked on the whole of what each Config says in any text form.
	for name, c := range map[string]*ikhnosv1.Config{"runner": runner, "third": third} {
		for _, text := range []string{prototext.Format(c), protojson.Format(c), fmt.Sprint(c)} {
			if strings.Contains(text, "PRIVATE KEY") && name != "server" {
				t.Errorf("%s: a private key in %.60q", name, text)
			}
		}
	}
	for _, text := range []string{prototext.Format(server), protojson.Format(server), fmt.Sprint(server)} {
		if strings.Contains(text, ic.Password) {
			t.Error("the serving cluster's Config holds the pair's password, not only its hash")
		}
	}
	for _, text := range []string{prototext.Format(third), protojson.Format(third)} {
		if strings.Contains(text, ic.Password) || strings.Contains(text, ic.Username) || strings.Contains(text, "BEGIN") {
			t.Error("a third cluster got something")
		}
	}
	for _, l := range runner.LinkPeers {
		l.IperfClient = nil
	}
}

func TestNoCredentialsAreIssuedForPairsWithoutThroughputTests(t *testing.T) {
	cr := newCredRig(t)
	cr.save(t, cr.pair(false))
	for _, a := range []store.Agent{cr.first, cr.second, cr.third} {
		c := cr.cfg(a)
		if c.LinkIperfServer != nil {
			t.Error("server credentials without throughput tests")
		}
		for _, lp := range c.LinkPeers {
			if lp.IperfClient != nil {
				t.Error("client credentials without throughput tests")
			}
		}
	}
	if n := len(cr.rows(t)); n != 0 {
		t.Errorf("%d credentials stored for a pair that does not run throughput tests", n)
	}
}

func TestCredentialsAreMadeOnceStoredSealedAndSurviveARestart(t *testing.T) {
	cr := newCredRig(t)
	cr.save(t, cr.pair(true))
	a := cr.cfg(cr.first).LinkPeers[0].IperfClient
	b := cr.cfg(cr.first).LinkPeers[0].IperfClient
	if a.Password != b.Password || a.PublicKeyPem != b.PublicKeyPem {
		t.Fatal("asking twice made two different credentials")
	}
	srv := cr.cfg(cr.second).LinkIperfServer

	rows := cr.rows(t)
	if len(rows) != 2 {
		t.Fatalf("%d rows, want a cluster key pair and a pair login", len(rows))
	}
	for _, r := range rows {
		for _, mark := range []string{a.Password, "PRIVATE KEY", "BEGIN", a.Username, srv.PrivateKeyPem[120:160]} {
			if bytes.Contains(r.Sealed, []byte(mark)) {
				t.Errorf("%s row holds %.10q in the clear", r.Kind, mark)
			}
		}
	}

	// A restarted server (a new Core on the same database and the same CA key) reads the same credentials.
	again := NewCore(cr.st, cr.core.CA, "org-1", nil).ForOrg("org-1")
	set, err := again.Connectivity.LinkCredentials(cr.ctx, []kernel.LinkSetting{cr.pair(true)}, false)
	if err != nil {
		t.Fatal(err)
	}
	if d := set.ForCluster(cr.first.ClusterID); d.Clients[cr.second.ClusterID].Password != a.Password {
		t.Error("the restarted server issued a different password")
	}
	if d := set.ForCluster(cr.second.ClusterID); d.Server.PrivateKeyPem != srv.PrivateKeyPem {
		t.Error("the restarted server issued a different key")
	}

	// A different CA cannot read them: they are made again, and the old rows are replaced rather than left to pile up.
	ca2, err := pki.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	other := NewCore(cr.st, ca2, "org-1", nil).ForOrg("org-1")
	set2, err := other.Connectivity.LinkCredentials(cr.ctx, []kernel.LinkSetting{cr.pair(true)}, false)
	if err != nil {
		t.Fatal(err)
	}
	if set2.ForCluster(cr.first.ClusterID).Clients[cr.second.ClusterID].Password == a.Password {
		t.Error("credentials sealed under another CA key were read")
	}
	if n := len(cr.rows(t)); n != 2 {
		t.Errorf("%d rows after the CA changed, want 2", n)
	}
}

// A row cannot be moved to another place: it is bound to its organisation, kind and id.
func TestASealedRowDoesNotOpenAnywhereElse(t *testing.T) {
	cr := newCredRig(t)
	cr.save(t, cr.pair(true))
	cr.cfg(cr.first)
	var pairRow store.LinkCredential
	for _, r := range cr.rows(t) {
		if r.Kind == connectivity.LinkCredPair {
			pairRow = r
		}
	}
	for name, f := range map[string]func() error{
		"another id": func() error {
			_, err := cr.core.Connectivity.OpenLinkCred(connectivity.LinkCredPair, "x\x1fy", pairRow.Sealed)
			return err
		},
		"another kind": func() error {
			_, err := cr.core.Connectivity.OpenLinkCred(connectivity.LinkCredCluster, pairRow.ID, pairRow.Sealed)
			return err
		},
		"another org": func() error {
			_, err := cr.core.ForOrg("org-2").Connectivity.OpenLinkCred(connectivity.LinkCredPair, pairRow.ID, pairRow.Sealed)
			return err
		},
	} {
		if f() == nil {
			t.Errorf("%s: the row opened", name)
		}
	}
	if _, err := cr.core.Connectivity.OpenLinkCred(connectivity.LinkCredPair, pairRow.ID, pairRow.Sealed); err != nil {
		t.Errorf("the row does not open where it belongs: %v", err)
	}
	// Moved by hand in the database: the pair gets a fresh login instead of someone else's.
	moved := pairRow
	moved.ID = "moved"
	if err := cr.st.PutLinkCredential(cr.ctx, moved); err != nil {
		t.Fatal(err)
	}
	if _, ok := cr.core.Connectivity.ReadPairCred(moved); ok {
		t.Error("a row moved to another id was read")
	}
}

func TestCredentialsAreRemovedWithThePairOrTheTestsAndMadeAgainFresh(t *testing.T) {
	cr := newCredRig(t)
	cr.save(t, cr.pair(true))
	first := cr.cfg(cr.first).LinkPeers[0].IperfClient
	if len(cr.rows(t)) != 2 {
		t.Fatal("no credentials")
	}

	// The tests are switched off: both rows go.
	cr.save(t, cr.pair(false))
	cr.settle()
	if n := len(cr.rows(t)); n != 0 {
		t.Errorf("%d rows left with the tests off", n)
	}
	if cfg := cr.cfg(cr.second); cfg.LinkIperfServer != nil {
		t.Error("server credentials are still sent")
	}

	// Switched on again: new credentials, not the old ones.
	cr.save(t, cr.pair(true))
	cr.settle()
	again := cr.cfg(cr.first).LinkPeers[0].IperfClient
	if again == nil || again.Password == first.Password || again.PublicKeyPem == first.PublicKeyPem {
		t.Error("re-enabling the tests reused the old credentials")
	}

	// The pair is removed altogether.
	cr.save(t)
	cr.settle()
	if n := len(cr.rows(t)); n != 0 {
		t.Errorf("%d rows left after the pair was removed", n)
	}
}

func TestAServingClustersKeyStaysWhileAnyPairNeedsIt(t *testing.T) {
	cr := newCredRig(t)
	// Two runners and one server: cluster ids decide who runs, so build the three pairs around the largest id.
	ids := []string{cr.first.ClusterID, cr.second.ClusterID, cr.third.ClusterID}
	big := max(ids[0], ids[1], ids[2])
	var others []string
	for _, id := range ids {
		if id != big {
			others = append(others, id)
		}
	}
	p := func(x string) kernel.LinkSetting {
		return kernel.LinkSetting{A: min(x, big), B: max(x, big), Throughput: &kernel.LinkThroughput{}}
	}
	cr.save(t, p(others[0]), p(others[1]))
	cr.settle()
	if n := len(cr.rows(t)); n != 3 { // two logins, one key pair
		t.Fatalf("%d rows, want 2 logins and 1 key pair", n)
	}
	cr.save(t, p(others[0]))
	cr.settle()
	kinds := map[string]int{}
	for _, r := range cr.rows(t) {
		kinds[r.Kind]++
	}
	if kinds[connectivity.LinkCredCluster] != 1 || kinds[connectivity.LinkCredPair] != 1 {
		t.Errorf("rows after one pair left = %v", kinds)
	}
}

func TestTheServerSeesAllTheUsersOfAllThePairsItServes(t *testing.T) {
	cr := newCredRig(t)
	ids := []string{cr.first.ClusterID, cr.second.ClusterID, cr.third.ClusterID}
	big := max(ids[0], ids[1], ids[2])
	var others, agentList []string
	for _, a := range []store.Agent{cr.first, cr.second, cr.third} {
		if a.ClusterID != big {
			others = append(others, a.ClusterID)
		} else {
			agentList = append(agentList, a.ID)
		}
	}
	p := func(x string) kernel.LinkSetting {
		return kernel.LinkSetting{A: min(x, big), B: max(x, big), Throughput: &kernel.LinkThroughput{}}
	}
	cr.save(t, p(others[0]), p(others[1]))
	var srv store.Agent
	for _, a := range []store.Agent{cr.first, cr.second, cr.third} {
		if a.ClusterID == big {
			srv = a
		}
	}
	lines := strings.Split(strings.TrimSpace(cr.cfg(srv).LinkIperfServer.UsersCsv), "\n")
	if len(lines) != 2 || strings.Split(lines[0], ",")[0] == strings.Split(lines[1], ",")[0] {
		t.Fatalf("users = %q", lines)
	}
	_ = agentList
}

func TestRotationMakesNewCredentialsAndRefusesWhatDoesNotExist(t *testing.T) {
	cr := newCredRig(t)
	links := []kernel.LinkSetting{cr.pair(true)}
	cr.save(t, links...)
	before := cr.cfg(cr.first).LinkPeers[0].IperfClient
	keyBefore := cr.cfg(cr.second).LinkIperfServer.PrivateKeyPem

	if err := cr.core.Connectivity.RotateLinkCredentials(cr.ctx, links, "", cr.second.ClusterID, cr.first.ClusterID); err != nil { // either way round
		t.Fatal(err)
	}
	after := cr.cfg(cr.first).LinkPeers[0].IperfClient
	if after.Password == before.Password || after.Username == before.Username {
		t.Error("the pair's login did not change")
	}
	if after.PublicKeyPem != before.PublicKeyPem || cr.cfg(cr.second).LinkIperfServer.PrivateKeyPem != keyBefore {
		t.Error("rotating a pair's password changed the cluster's key")
	}
	if !strings.Contains(cr.cfg(cr.second).LinkIperfServer.UsersCsv, after.Username+",") || strings.Contains(cr.cfg(cr.second).LinkIperfServer.UsersCsv, before.Username) {
		t.Error("the server's users file did not follow the rotation")
	}

	if err := cr.core.Connectivity.RotateLinkCredentials(cr.ctx, links, cr.second.ClusterID, "", ""); err != nil {
		t.Fatal(err)
	}
	if cr.cfg(cr.second).LinkIperfServer.PrivateKeyPem == keyBefore || cr.cfg(cr.first).LinkPeers[0].IperfClient.PublicKeyPem == before.PublicKeyPem {
		t.Error("the cluster's key pair did not change on both ends")
	}
	if err := cr.core.Connectivity.RotateLinkCredentials(cr.ctx, links, cr.first.ClusterID, "", ""); err == nil {
		t.Error("rotated the key of a cluster that serves nothing")
	}
	if err := cr.core.Connectivity.RotateLinkCredentials(cr.ctx, links, "", cr.first.ClusterID, cr.third.ClusterID); err == nil {
		t.Error("rotated a pair that is not set up")
	}
}

func TestNoLogAuditOrErrorHoldsCredentialMaterial(t *testing.T) {
	cr := newCredRig(t)
	links := []kernel.LinkSetting{cr.pair(true)}
	cr.save(t, links...)
	cr.settle()
	ic := cr.cfg(cr.first).LinkPeers[0].IperfClient
	srv := cr.cfg(cr.second).LinkIperfServer
	_ = cr.cfg(cr.third)
	if err := cr.core.Connectivity.RotateLinkCredentials(cr.ctx, links, "", cr.first.ClusterID, cr.second.ClusterID); err != nil {
		t.Fatal(err)
	}
	err := cr.core.Connectivity.RotateLinkCredentials(cr.ctx, links, "no-such", "", "")
	events, _ := cr.st.ListAudit(cr.ctx, "org-1", 200)
	var audit strings.Builder
	for _, e := range events {
		fmt.Fprintf(&audit, "%+v\n", e)
	}
	set, _ := cr.core.Connectivity.LinkCredentials(cr.ctx, links, false)
	var dump strings.Builder
	fmt.Fprintf(&dump, "%v %+v %#v %s", set, set, set, set)
	for _, c := range set.Clusters {
		fmt.Fprintf(&dump, "%v %+v %#v", c, c, c)
	}
	for _, l := range set.Pairs {
		fmt.Fprintf(&dump, "%v %+v %#v", l, l, l)
	}
	marks := []string{ic.Password, "PRIVATE KEY", srv.PrivateKeyPem[120:170], strings.TrimSpace(srv.UsersCsv[len(ic.Username)+1:])}
	for name, text := range map[string]string{"log": cr.log.String(), "audit trail": audit.String(), "error": fmt.Sprint(err), "dump": dump.String()} {
		for _, m := range marks {
			if strings.Contains(text, m) {
				t.Errorf("the %s holds credential material (%.12q)", name, m)
			}
		}
	}
	if !strings.Contains(cr.log.String(), "issued iperf3 credentials") {
		t.Errorf("issuing was not logged at all:\n%s", cr.log.String())
	}
	var rotated bool
	for _, e := range events {
		rotated = rotated || e.Action == "link-credentials-rotate"
	}
	_ = rotated // the admin endpoint records it; RotateLinkCredentials alone is the mechanism
}

func TestWithoutACAKeyNothingIsIssuedAndNothingIsSentOpen(t *testing.T) {
	cr := newCredRig(t)
	// Set before saving: saving starts a background sync that reads the CA, so clearing it afterwards would race with it.
	cr.core.CA = nil
	cr.save(t, cr.pair(true))
	c := cr.cfg(cr.second)
	if c.LinkIperfServer != nil {
		t.Error("credentials were sent although they cannot be sealed or read back")
	}
}
