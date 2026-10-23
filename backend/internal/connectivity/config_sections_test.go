package connectivity

import (
	"slices"
	"testing"

	ikhnosv1 "ikhnos/gen/ikhnosv1"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"ikhnos/internal/agents"
)

func configFieldNames() []string {
	var names []string
	fields := (&ikhnosv1.Config{}).ProtoReflect().Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		names = append(names, string(fields.Get(i).Name()))
	}
	return names
}

func TestEveryFieldOfTheConfigBelongsToExactlyOneSectionOrTheEnvelope(t *testing.T) {
	owners := map[string][]string{}
	for _, s := range configSections {
		for _, f := range s.fields {
			owners[f] = append(owners[f], s.name)
		}
	}
	for _, f := range configEnvelope {
		owners[f] = append(owners[f], "envelope")
	}
	for _, name := range configFieldNames() {
		if len(owners[name]) != 1 {
			t.Errorf("Config field %s is owned by %v: it needs exactly one owner (a section in paths.go, or the envelope)", name, owners[name])
		}
		delete(owners, name)
	}
	for name, by := range owners {
		t.Errorf("%v claim a field %q that the Config does not have", by, name)
	}
}

// A section that wrote a field it does not own would make the table above a lie.
func TestASectionSetsOnlyTheFieldsItOwns(t *testing.T) {
	f := baseFacts()
	f.Consent = agents.Consent{Paused: []string{"flow"}, Excluded: []string{"shop"}}
	f.Tier = 2
	for _, s := range configSections {
		b := &configBuild{cfg: &ikhnosv1.Config{}}
		s.fill(f, b)
		b.cfg.ProtoReflect().Range(func(fd protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
			if !slices.Contains(s.fields, string(fd.Name())) {
				t.Errorf("section %s set %s, which it does not own", s.name, fd.Name())
			}
			return true
		})
	}
}

// And every owned field is really set by its section when the facts have it (so a field listed but never filled is noticed).
func TestASectionSetsEachFieldItOwnsWhenTheFactsHaveIt(t *testing.T) {
	f := baseFacts()
	f.Consent = agents.Consent{Paused: []string{"x"}, Excluded: []string{"shop"}}
	f.Tier = 2
	for _, s := range configSections {
		b := &configBuild{cfg: &ikhnosv1.Config{}}
		s.fill(f, b)
		set := map[string]bool{}
		b.cfg.ProtoReflect().Range(func(fd protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
			set[string(fd.Name())] = true
			return true
		})
		for _, name := range s.fields {
			if !set[name] {
				t.Errorf("section %s owns %s but did not set it", s.name, name)
			}
		}
	}
}

func TestTheSectionsTogetherMakeTheSameConfigAsAWholeCompile(t *testing.T) {
	f := baseFacts()
	f.Consent = agents.Consent{Paused: []string{"x"}, Excluded: []string{"shop"}}
	whole, _ := CompileConfig(f)
	merged := &ikhnosv1.Config{}
	for _, s := range configSections {
		b := &configBuild{cfg: &ikhnosv1.Config{}}
		s.fill(f, b)
		proto.Merge(merged, b.cfg)
	}
	merged.ServerTimeUnix, merged.Digest = whole.ServerTimeUnix, whole.Digest
	if !proto.Equal(whole, merged) {
		t.Fatalf("the sections do not add up to the Config:\n%v\n%v", whole, merged)
	}
}
