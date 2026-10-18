package server

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/interpret"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// The digest of a Config is what an agent echoes back to say which Config it has applied, and what the server compares to decide
// whether a Config must be pushed again. It was written by hand, field by field, so a field added to the Config and forgotten there
// was never pushed. It is now computed: each section (paths.go) is projected out of the Config, marshalled in a fixed way and
// hashed, and the digest of the Config is a hash of those. Every field a section owns is in it by construction, and
// TestEveryFieldOfAConfigIsInItsHash still checks that from the proto's own field list.
//
// The server time and the digest itself belong to no section and are not in it. The material of the iperf3 credentials is in the
// sections it belongs to, and so in a hash: the digest is compared, kept and could be logged, and never holds the material.

// sectionDigest is the digest of one section of a Config.
type sectionDigest struct {
	Section string
	Digest  string
}

// sectionDigests are the digests of the sections of c, in section order. A section whose digest differs is the part of the Config
// that changed.
func sectionDigests(c *continuumv1.Config) []sectionDigest {
	out := make([]sectionDigest, 0, len(configSections))
	src := c.ProtoReflect()
	fields := src.Descriptor().Fields()
	for _, s := range configSections {
		part := &continuumv1.Config{}
		dst := part.ProtoReflect()
		for _, name := range s.fields {
			if fd := fields.ByName(protoreflect.Name(name)); fd != nil && src.Has(fd) {
				dst.Set(fd, src.Get(fd))
			}
		}
		out = append(out, sectionDigest{s.name, digestOfSection(part)})
	}
	return out
}

func digestOfSection(part *continuumv1.Config) string {
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(part)
	if err != nil {
		// A string that is not valid UTF-8 cannot be marshalled. It must still change the digest when it changes, or a change to it
		// would never be pushed; the text form is not stable between runs of the program, which costs at worst one harmless re-apply.
		b = []byte("unmarshalable:" + part.String())
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

// configHash is the digest of the whole Config: a hash of the digests of its sections.
func ConfigHash(c *continuumv1.Config) string {
	ds := sectionDigests(c)
	parts := make([]string, 0, len(ds))
	for _, d := range ds {
		parts = append(parts, d.Section+"="+d.Digest)
	}
	return interpret.Hash("cfg", strings.Join(parts, "|"))
}
