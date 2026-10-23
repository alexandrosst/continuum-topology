package connectivity

import (
	"strings"
	"testing"

	ikhnosv1 "ikhnos/gen/ikhnosv1"

	"google.golang.org/protobuf/proto"
)

func fullConfig(t *testing.T) *ikhnosv1.Config {
	t.Helper()
	cfg, _ := CompileConfig(baseFacts())
	return cfg
}

func digestsOf(c *ikhnosv1.Config) map[string]string {
	m := map[string]string{}
	for _, d := range sectionDigests(c) {
		m[d.Section] = d.Digest
	}
	return m
}

func TestOnlyTheSectionThatChangedChangesItsDigest(t *testing.T) {
	base := fullConfig(t)
	baseDigests := digestsOf(base)
	if len(baseDigests) != len(configSections) {
		t.Fatalf("%d section digests for %d sections", len(baseDigests), len(configSections))
	}
	for section, edit := range map[string]func(c *ikhnosv1.Config){
		"policy":  func(c *ikhnosv1.Config) { c.PausedCollectors = append(c.PausedCollectors, "flow") },
		"timing":  func(c *ikhnosv1.Config) { c.HeartbeatSeconds++ },
		"cluster": func(c *ikhnosv1.Config) { c.ClusterId = "cl-other" },
		"links":   func(c *ikhnosv1.Config) { c.LinkPeers[0].Addresses = append(c.LinkPeers[0].Addresses, "10.9.9.9") },
		"probes":  func(c *ikhnosv1.Config) { c.ProbeTargets[0].Port++ },
	} {
		c := proto.Clone(base).(*ikhnosv1.Config)
		edit(c)
		got := digestsOf(c)
		for name, d := range baseDigests {
			if changed := got[name] != d; changed != (name == section) {
				t.Errorf("editing %s: section %s changed = %v", section, name, changed)
			}
		}
		if ConfigHash(c) == ConfigHash(base) {
			t.Errorf("editing %s did not change the digest of the Config", section)
		}
	}
}

func TestTheServerTimeAndTheDigestAreNotInTheDigest(t *testing.T) {
	base := fullConfig(t)
	c := proto.Clone(base).(*ikhnosv1.Config)
	c.ServerTimeUnix += 3600
	c.Digest = "something else"
	if ConfigHash(c) != ConfigHash(base) {
		t.Fatal("the moment a Config was made, or the digest it carries, changed its digest")
	}
}

func TestADigestIsTheSameForTheSameConfigAndNeverHoldsACredential(t *testing.T) {
	a, b := fullConfig(t), fullConfig(t)
	if ConfigHash(a) != ConfigHash(b) || a.Digest != b.Digest || a.Digest == "" {
		t.Fatalf("two compiles of the same facts give %q and %q", a.Digest, b.Digest)
	}
	secret := proto.Clone(a).(*ikhnosv1.Config)
	secret.LinkPeers[0].IperfClient.Password = "S3cret-Passw0rd-Material"
	secret.LinkIperfServer.UsersCsv = "someone:S3cret-Server-Material"
	all := ConfigHash(secret)
	for _, d := range sectionDigests(secret) {
		all += d.Digest
	}
	if strings.Contains(all, "S3cret") || strings.Contains(all, "Material") {
		t.Fatal("a digest carries credential material")
	}
	// Rotating a credential changes the digest, so the rotation reaches the agent.
	c := proto.Clone(a).(*ikhnosv1.Config)
	c.LinkPeers[0].IperfClient.Password = "rotated"
	if ConfigHash(c) == ConfigHash(a) {
		t.Fatal("a rotated client password did not change the digest")
	}
	c = proto.Clone(a).(*ikhnosv1.Config)
	c.LinkIperfServer.UsersCsv = "u:rotated"
	if ConfigHash(c) == ConfigHash(a) {
		t.Fatal("a rotated server user list did not change the digest")
	}
}

func TestAChangeToAStringThatCannotBeMarshalledStillChangesTheDigest(t *testing.T) {
	a := &ikhnosv1.Config{ClusterId: "cl-a", LinkPeers: []*ikhnosv1.LinkPeer{{ClusterId: "cl-b", Name: "bad\xffname"}}}
	b := proto.Clone(a).(*ikhnosv1.Config)
	b.LinkPeers[0].Name = "bad\xfeother"
	if ConfigHash(a) == "" || ConfigHash(a) != ConfigHash(a) {
		t.Fatal("no stable digest for a Config with a string that is not valid UTF-8")
	}
	if ConfigHash(a) == ConfigHash(b) {
		t.Fatal("a change to a string that is not valid UTF-8 did not change the digest, so it would never be pushed")
	}
}
