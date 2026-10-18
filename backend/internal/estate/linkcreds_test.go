package estate

import (
	"strings"
	"testing"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/connectivity"
	"continuum/internal/kernel"
	"continuum/internal/store"
)

func TestTheConfigHashFollowsTheCredentialsWithoutHoldingThem(t *testing.T) {
	cr := newCredRig(t)
	cr.save(t, cr.pair(true))
	hash := func(a store.Agent) string {
		cr.cfg(a)
		cr.hub.Mu.Lock()
		defer cr.hub.Mu.Unlock()
		return cr.hub.Views[a.ID].CfgHash
	}
	h1, h2 := hash(cr.first), hash(cr.second)
	if hash(cr.first) != h1 || hash(cr.second) != h2 {
		t.Fatal("the hash of an unchanged Config changed")
	}
	if err := cr.core.Connectivity.RotateLinkCredentials(cr.ctx, []kernel.LinkSetting{cr.pair(true)}, "", cr.first.ClusterID, cr.second.ClusterID); err != nil {
		t.Fatal(err)
	}
	if hash(cr.first) == h1 || hash(cr.second) == h2 {
		t.Error("a rotated password did not change the hash, so the agents would never be sent it")
	}
	h1, h2 = hash(cr.first), hash(cr.second)
	if err := cr.core.Connectivity.RotateLinkCredentials(cr.ctx, []kernel.LinkSetting{cr.pair(true)}, cr.second.ClusterID, "", ""); err != nil {
		t.Fatal(err)
	}
	if hash(cr.first) == h1 || hash(cr.second) == h2 {
		t.Error("a rotated key pair did not change the hash")
	}
	// The hash is of the Config text and holds no credential: build one with a distinctive password.
	c := &continuumv1.Config{ClusterId: "cl-a", LinkPeers: []*continuumv1.LinkPeer{{ClusterId: "cl-b", Throughput: true, IperfClient: &continuumv1.LinkIperfClient{PublicKeyPem: "pub", Username: "u", Password: "PASSWORDMARK"}}}, LinkIperfServer: &continuumv1.LinkIperfServer{PrivateKeyPem: "KEYMARK", UsersCsv: "u,HASHMARK"}}
	h := connectivity.ConfigHash(c)
	for _, m := range []string{"PASSWORDMARK", "KEYMARK", "HASHMARK"} {
		if strings.Contains(h, m) {
			t.Errorf("the hash contains %s", m)
		}
	}
	c2 := &continuumv1.Config{ClusterId: "cl-a", LinkPeers: []*continuumv1.LinkPeer{{ClusterId: "cl-b", Throughput: true, IperfClient: &continuumv1.LinkIperfClient{PublicKeyPem: "pub", Username: "u", Password: "PASSWORDMARK2"}}}, LinkIperfServer: c.LinkIperfServer}
	if connectivity.ConfigHash(c2) == h {
		t.Error("a different password gave the same hash")
	}
	c3 := &continuumv1.Config{ClusterId: "cl-a", LinkPeers: c.LinkPeers, LinkIperfServer: &continuumv1.LinkIperfServer{PrivateKeyPem: "KEYMARK2", UsersCsv: "u,HASHMARK"}}
	if connectivity.ConfigHash(c3) == h {
		t.Error("a different server key gave the same hash")
	}
	// A Config without credentials hashes as it always did.
	plain := &continuumv1.Config{ClusterId: "cl-a", LinkPeers: []*continuumv1.LinkPeer{{ClusterId: "cl-b", Name: "B", Addresses: []string{"10.0.0.1"}}}}
	if connectivity.ConfigHash(plain) != connectivity.ConfigHash(&continuumv1.Config{ClusterId: "cl-a", LinkPeers: []*continuumv1.LinkPeer{{ClusterId: "cl-b", Name: "B", Addresses: []string{"10.0.0.1"}}}}) {
		t.Error("unstable hash")
	}
}
