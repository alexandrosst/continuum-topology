package server

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/kernel"
	"continuum/internal/link"
	"continuum/internal/store"
)

// The iperf3 credentials of the throughput tests, issued and kept by the server so that nobody has to create Secrets by hand.
//
// What exists. For every cluster that SERVES the tests of at least one pair (the second of the pair, see linkRunner) the server
// holds an RSA-2048 key pair: that cluster's iperf3 server identity. For every pair with throughput tests (Settings.Links[i].Throughput
// != nil) it holds a user name and a random password. They are made the first time a pair asks for the tests (when the settings are
// saved, or when an agent's Config is built, whichever comes first) and again whenever they are missing, and they are deleted when the
// pair is removed or its tests switched off (a cluster's key pair when no pair needs it any more).
//
// Where they are. In the server's database, table link_iperf_credentials, one sealed row per cluster key pair and per pair. A row is
// AES-256-GCM under a key derived (HKDF) from the server's CA private key, with the organisation, kind and id as additional data, so a
// row cannot be moved to another place. The CA key is the root secret the server must read back for everything else; deriving from it
// adds no second key to look after, the credentials are protected at rest exactly as the CA key is (encrypted under the CA passphrase
// when one is configured, which is the default of the chart), they are inside the backup that holds the CA, and a copy of the
// database alone holds no usable credential. A CA replaced by a new key leaves the rows unreadable; they are then treated as missing and
// made again, which costs nothing but a minute of failed tests while the new ones reach the clusters.
//
// Who gets what. For a pair (runner R = first-sorted cluster = the client, S = the other = the server): R's agent is sent, in its LinkPeer
// for S, S's PUBLIC key, the user name and the password. S's agent is sent, in its Config, its OWN private key and the users lines of all
// the pairs it serves. No cluster is sent a credential for a role it does not have: a third cluster gets nothing, R never gets a private
// key, S never gets a password (only the hash in the users file).
//
// Nothing here logs, errors with or audits a key or a password. The types that hold them redact themselves (link.Secret).
const (
	linkCredCluster = "cluster"
	linkCredPair    = "pair"
	linkCredPurpose = "ikhnos/link-iperf-credentials/v1"
	linkCredVersion = 1
	rsaBits         = 2048
)

// linkCredMu serialises making and removing credentials, so two Configs built at once for the two clusters of a new pair do not each
// make a different password. One server process has one database.
var linkCredMu sync.Mutex

// clusterIdentity is a serving cluster's iperf3 key pair.
type clusterIdentity struct {
	Private link.Secret
	Public  string
}

// The fields are exported so that fmt, which cannot call a method on an unexported field, still goes through link.Secret's own
// formatting; the methods say the same for the struct as a whole.
func (c clusterIdentity) String() string             { return "clusterIdentity{[redacted]}" }
func (c clusterIdentity) GoString() string           { return c.String() }
func (c clusterIdentity) Format(f fmt.State, _ rune) { _, _ = fmt.Fprint(f, c.String()) }

// pairLogin is the user name and password of one pair.
type pairLogin struct {
	User     string
	Password link.Secret
}

func (p pairLogin) String() string             { return "pairLogin{user: " + p.User + ", password: [redacted]}" }
func (p pairLogin) GoString() string           { return p.String() }
func (p pairLogin) Format(f fmt.State, _ rune) { _, _ = fmt.Fprint(f, p.String()) }

// usersLine is the line of the iperf3 server's users file for this login: "USER,SHA256" where SHA256 is the hash of "{USER}PASSWORD".
func (p pairLogin) usersLine() string {
	sum := sha256.Sum256([]byte("{" + p.User + "}" + p.Password.Reveal()))
	return p.User + "," + hex.EncodeToString(sum[:])
}

// linkCredSet is what the server holds for the current pairs.
type linkCredSet struct {
	clusters map[string]clusterIdentity
	pairs    map[[2]string]pairLogin
}

func (s *linkCredSet) String() string {
	return fmt.Sprintf("linkCredSet{%d cluster keys, %d pair logins}", len(s.clusters), len(s.pairs))
}
func (s *linkCredSet) GoString() string           { return s.String() }
func (s *linkCredSet) Format(f fmt.State, _ rune) { _, _ = fmt.Fprint(f, s.String()) }

// linkServer is the cluster of a pair that serves the throughput tests: the one that is not the runner.
func linkServer(a, b string) string { return max(a, b) }

// throughputPairs are the pairs that ask for throughput tests, by (a, b) with a before b.
func throughputPairs(links []kernel.LinkSetting) [][2]string {
	var out [][2]string
	for _, l := range links {
		if l.Throughput != nil && l.A != "" && l.B != "" && l.A != l.B {
			out = append(out, [2]string{min(l.A, l.B), max(l.A, l.B)})
		}
	}
	return out
}

func pairRowID(p [2]string) string { return p[0] + "\x1f" + p[1] }

// ---- sealing ----

func (c *Core) linkCredAEAD() (cipher.AEAD, error) {
	if c.CA == nil {
		return nil, errors.New("no CA to protect link credentials with")
	}
	k, err := c.CA.DeriveKey(linkCredPurpose, 32)
	if err != nil {
		return nil, err
	}
	defer clear(k)
	blk, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(blk)
}

func (c *Core) linkCredAAD(kind, id string) []byte {
	return []byte(c.OrgID + "\x00" + kind + "\x00" + id)
}

func (c *Core) sealLinkCred(kind, id string, plain []byte) ([]byte, error) {
	g, err := c.linkCredAEAD()
	if err != nil {
		return nil, err
	}
	n := make([]byte, g.NonceSize())
	if _, err := rand.Read(n); err != nil {
		return nil, err
	}
	return g.Seal(append([]byte{linkCredVersion}, n...), n, plain, c.linkCredAAD(kind, id)), nil
}

func (c *Core) openLinkCred(kind, id string, sealed []byte) ([]byte, error) {
	g, err := c.linkCredAEAD()
	if err != nil {
		return nil, err
	}
	if len(sealed) < 1+g.NonceSize()+g.Overhead() || sealed[0] != linkCredVersion {
		return nil, errors.New("unreadable")
	}
	n := sealed[1 : 1+g.NonceSize()]
	out, err := g.Open(nil, n, sealed[1+g.NonceSize():], c.linkCredAAD(kind, id))
	if err != nil {
		return nil, errors.New("unreadable")
	}
	return out, nil
}

type clusterWire struct {
	Private string `json:"private"`
	Public  string `json:"public"`
}
type pairWire struct {
	User     string `json:"user"`
	Password string `json:"password"`
}

func (c *Core) readClusterCred(row store.LinkCredential) (clusterIdentity, bool) {
	plain, err := c.openLinkCred(linkCredCluster, row.ID, row.Sealed)
	if err != nil {
		return clusterIdentity{}, false
	}
	defer clear(plain)
	var w clusterWire
	if json.Unmarshal(plain, &w) != nil || w.Private == "" || w.Public == "" {
		return clusterIdentity{}, false
	}
	return clusterIdentity{Private: link.Secret(w.Private), Public: w.Public}, true
}

func (c *Core) readPairCred(row store.LinkCredential) (pairLogin, bool) {
	plain, err := c.openLinkCred(linkCredPair, row.ID, row.Sealed)
	if err != nil {
		return pairLogin{}, false
	}
	defer clear(plain)
	var w pairWire
	if json.Unmarshal(plain, &w) != nil || !link.ValidUsername(w.User) || len(w.Password) < 32 {
		return pairLogin{}, false
	}
	return pairLogin{User: w.User, Password: link.Secret(w.Password)}, true
}

// ---- making ----

func newClusterIdentity() (clusterIdentity, error) {
	k, err := rsa.GenerateKey(rand.Reader, rsaBits)
	if err != nil {
		return clusterIdentity{}, err
	}
	priv, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return clusterIdentity{}, err
	}
	pub, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		return clusterIdentity{}, err
	}
	return clusterIdentity{
		Private: link.Secret(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv})),
		Public:  string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub})),
	}, nil
}

// newPairLogin makes a user name and a password of 32 random bytes (64 hex characters).
func newPairLogin(taken map[string]bool) (pairLogin, error) {
	for range 8 {
		u, p := make([]byte, 6), make([]byte, 32)
		if _, err := rand.Read(u); err != nil {
			return pairLogin{}, err
		}
		if _, err := rand.Read(p); err != nil {
			return pairLogin{}, err
		}
		name := "ik-" + hex.EncodeToString(u)
		if !taken[name] {
			return pairLogin{User: name, Password: link.Secret(hex.EncodeToString(p))}, nil
		}
	}
	return pairLogin{}, errors.New("could not find an unused user name")
}

func (c *Core) putLinkCred(ctx context.Context, kind, id string, v any) error {
	plain, err := json.Marshal(v)
	if err != nil {
		return err
	}
	defer clear(plain)
	sealed, err := c.sealLinkCred(kind, id, plain)
	if err != nil {
		return err
	}
	return c.Store.PutLinkCredential(ctx, store.LinkCredential{OrgID: c.OrgID, Kind: kind, ID: id, Sealed: sealed, CreatedAt: c.Now()})
}

// linkCredentials returns what the server holds for the pairs that ask for throughput tests, making what is missing (or
// unreadable). With prune it also deletes what no pair needs any more; only a saved change of the settings does that, never the
// ordinary building of a Config, so a settings document that failed to load cannot empty the table.
func (c *Core) linkCredentials(ctx context.Context, links []kernel.LinkSetting, prune bool) (*linkCredSet, error) {
	pairs := throughputPairs(links)
	if len(pairs) == 0 && !prune {
		return &linkCredSet{}, nil
	}
	linkCredMu.Lock()
	defer linkCredMu.Unlock()
	rows, err := c.Store.ListLinkCredentials(ctx, c.OrgID)
	if err != nil {
		return nil, errors.New("could not read the link credentials")
	}
	have := map[string]store.LinkCredential{}
	for _, r := range rows {
		have[r.Kind+"\x00"+r.ID] = r
	}
	set := &linkCredSet{clusters: map[string]clusterIdentity{}, pairs: map[[2]string]pairLogin{}}
	needCluster := map[string]bool{}
	needRow := map[string]bool{}
	taken := map[string]bool{}
	var missing [][2]string
	for _, p := range pairs {
		id := pairRowID(p)
		needRow[linkCredPair+"\x00"+id] = true
		needCluster[linkServer(p[0], p[1])] = true
		if r, ok := have[linkCredPair+"\x00"+id]; ok {
			if l, ok := c.readPairCred(r); ok {
				set.pairs[p] = l
				taken[l.User] = true
				continue
			}
		}
		missing = append(missing, p)
	}
	// User names already in other rows must not be reused by a new pair.
	for _, r := range rows {
		if r.Kind == linkCredPair {
			if l, ok := c.readPairCred(r); ok {
				taken[l.User] = true
			}
		}
	}
	for _, p := range missing {
		l, err := newPairLogin(taken)
		if err != nil {
			return nil, err
		}
		if err := c.putLinkCred(ctx, linkCredPair, pairRowID(p), pairWire{User: l.User, Password: l.Password.Reveal()}); err != nil {
			return nil, fmt.Errorf("could not store a link credential: %w", err)
		}
		taken[l.User] = true
		set.pairs[p] = l
		c.Log.Info("issued iperf3 credentials for a cluster pair's throughput tests", "a", p[0], "b", p[1])
	}
	ids := make([]string, 0, len(needCluster))
	for id := range needCluster {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		needRow[linkCredCluster+"\x00"+id] = true
		if r, ok := have[linkCredCluster+"\x00"+id]; ok {
			if k, ok := c.readClusterCred(r); ok {
				set.clusters[id] = k
				continue
			}
		}
		k, err := newClusterIdentity()
		if err != nil {
			return nil, errors.New("could not make an iperf3 key pair")
		}
		if err := c.putLinkCred(ctx, linkCredCluster, id, clusterWire{Private: k.Private.Reveal(), Public: k.Public}); err != nil {
			return nil, fmt.Errorf("could not store a link credential: %w", err)
		}
		set.clusters[id] = k
		c.Log.Info("issued an iperf3 server key pair for a cluster", "cluster", id)
	}
	if prune {
		for k, r := range have {
			if !needRow[k] {
				if err := c.Store.DeleteLinkCredential(ctx, c.OrgID, r.Kind, r.ID); err != nil {
					c.Log.Warn("could not remove a link credential that is no longer needed", "kind", r.Kind)
				}
			}
		}
	}
	return set, nil
}

// ---- who gets what ----

// linkCredDelivery is what one cluster's Config carries.
type linkCredDelivery struct {
	// clients are the client credentials by the id of the peer they test (this cluster is the runner of those pairs).
	clients map[string]*continuumv1.LinkIperfClient
	// server is this cluster's own server identity and users; nil when it serves no pair.
	server *continuumv1.LinkIperfServer
}

func (d linkCredDelivery) String() string {
	return fmt.Sprintf("linkCredDelivery{%d client credentials, server credentials: %t}", len(d.clients), d.server != nil)
}
func (d linkCredDelivery) GoString() string           { return d.String() }
func (d linkCredDelivery) Format(f fmt.State, _ rune) { _, _ = fmt.Fprint(f, d.String()) }

// forCluster picks, from everything the server holds, exactly what cluster needs for the roles it has in the pairs.
func (s *linkCredSet) forCluster(cluster string) linkCredDelivery {
	var d linkCredDelivery
	var lines []string
	for p, login := range s.pairs {
		runner, server := linkRunner(p[0], p[1]), linkServer(p[0], p[1])
		switch cluster {
		case runner:
			if k, ok := s.clusters[server]; ok {
				if d.clients == nil {
					d.clients = map[string]*continuumv1.LinkIperfClient{}
				}
				d.clients[server] = &continuumv1.LinkIperfClient{PublicKeyPem: k.Public, Username: login.User, Password: login.Password.Reveal()}
			}
		case server:
			lines = append(lines, login.usersLine())
		}
	}
	if len(lines) > 0 {
		if k, ok := s.clusters[cluster]; ok {
			sort.Strings(lines)
			d.server = &continuumv1.LinkIperfServer{PrivateKeyPem: k.Private.Reveal(), UsersCsv: strings.Join(lines, "\n") + "\n"}
		}
	}
	return d
}

// linkCredsFor is the delivery for one agent's Config, or the zero delivery when the cluster takes part in no pair with throughput
// tests or the credentials cannot be had (which is logged, without material, and leaves the tests failing closed).
func (h *Hub) linkCredsFor(a store.Agent, links []kernel.LinkSetting) linkCredDelivery {
	if a.ClusterID == "" {
		return linkCredDelivery{}
	}
	involved := false
	for _, p := range throughputPairs(links) {
		if p[0] == a.ClusterID || p[1] == a.ClusterID {
			involved = true
			break
		}
	}
	if !involved {
		return linkCredDelivery{}
	}
	set, err := h.C.linkCredentials(bg(), links, false)
	if err != nil {
		h.C.Log.Warn("link credentials unavailable; throughput tests of the pair will fail until they are", "err", err.Error())
		return linkCredDelivery{}
	}
	return set.forCluster(a.ClusterID)
}

// syncLinkCredentials makes the credentials of the saved pairs and removes those of pairs that no longer ask for tests. It reads the
// settings as they are when it runs, so two of them running out of order still end at the current state.
func (h *Hub) syncLinkCredentials() {
	if _, err := h.C.linkCredentials(bg(), h.C.Settings().Links, true); err != nil {
		h.C.Log.Warn("could not update the link credentials", "err", err.Error())
	}
}

// credDigest identifies credential material in a Config hash without containing it.
func credDigest(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// ---- rotation ----

// RotateLinkCredentials replaces the credentials the server issued: a cluster's iperf3 key pair (cluster set), or the user name and
// password of a pair (a and b set). Both are made again and reach the clusters in their next Config, within a minute; the tests of
// the pair fail with `auth` until both ends have the new ones. It returns an error if there is nothing to rotate (no such pair asks
// for throughput tests).
func (c *Core) RotateLinkCredentials(ctx context.Context, links []kernel.LinkSetting, cluster, a, b string) error {
	pairs := throughputPairs(links)
	linkCredMu.Lock()
	switch {
	case cluster != "":
		served := false
		for _, p := range pairs {
			if linkServer(p[0], p[1]) == cluster {
				served = true
			}
		}
		if !served {
			linkCredMu.Unlock()
			return errors.New("that cluster serves no pair with throughput tests")
		}
		if err := c.Store.DeleteLinkCredential(ctx, c.OrgID, linkCredCluster, cluster); err != nil {
			linkCredMu.Unlock()
			return errors.New("could not remove the old key pair")
		}
	default:
		p := [2]string{min(a, b), max(a, b)}
		if !containsPair(pairs, p) {
			linkCredMu.Unlock()
			return errors.New("that pair does not ask for throughput tests")
		}
		if err := c.Store.DeleteLinkCredential(ctx, c.OrgID, linkCredPair, pairRowID(p)); err != nil {
			linkCredMu.Unlock()
			return errors.New("could not remove the old credential")
		}
	}
	linkCredMu.Unlock()
	_, err := c.linkCredentials(ctx, links, false)
	return err
}

func containsPair(ps [][2]string, p [2]string) bool {
	for _, q := range ps {
		if q == p {
			return true
		}
	}
	return false
}
