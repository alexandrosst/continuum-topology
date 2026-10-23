package estate

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	ikhnosv1 "ikhnos/gen/ikhnosv1"
	"ikhnos/internal/facts"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// A module reason is raw error text from the agent. Over facts.MaxReason it is cut, not a reason to refuse the
// message (which ended the stream and so hid the very error the reason was reporting).
func TestValidateModulesCutsALongReason(t *testing.T) {
	ms := []*ikhnosv1.ModuleStatus{{Name: "services", State: ikhnosv1.ModuleStatus_ERROR, Reason: strings.Repeat("é", 1000) + "\n"}}
	if err := validateModules(ms); err != nil {
		t.Fatalf("a long reason was refused: %v", err)
	}
	if len(ms[0].Reason) > facts.MaxReason || !utf8.ValidString(ms[0].Reason) {
		t.Errorf("reason is %d bytes, valid utf-8 %v", len(ms[0].Reason), utf8.ValidString(ms[0].Reason))
	}
	if err := validateModules(make([]*ikhnosv1.ModuleStatus, 65)); err == nil {
		t.Error("more than 64 modules must still be refused")
	}
}

// scalarFor is the most the agent could put in a field of this kind: a string far over the limit, a number, and (round
// by round) every value of an enum. Names and keys, which identify an entity and are bounded by Kubernetes itself, stay
// short.
func scalarFor(fd protoreflect.FieldDescriptor, round int, short bool) protoreflect.Value {
	switch fd.Kind() {
	case protoreflect.StringKind:
		if short {
			return protoreflect.ValueOfString("k")
		}
		return protoreflect.ValueOfString(strings.Repeat("é", facts.MaxString))
	case protoreflect.EnumKind:
		vals := fd.Enum().Values()
		return protoreflect.ValueOfEnum(vals.Get(round % vals.Len()).Number())
	case protoreflect.BoolKind:
		return protoreflect.ValueOfBool(true)
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return protoreflect.ValueOfInt32(7)
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return protoreflect.ValueOfInt64(7)
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return protoreflect.ValueOfUint32(7)
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return protoreflect.ValueOfUint64(7)
	case protoreflect.FloatKind:
		return protoreflect.ValueOfFloat32(7)
	case protoreflect.DoubleKind:
		return protoreflect.ValueOfFloat64(7)
	}
	return protoreflect.Value{}
}

// fill sets every field of m to the most the agent could put in it: strings far over the limit, lists and maps over
// theirs, and every value of every enum (one per round).
func fill(m protoreflect.Message, round, depth int) {
	fields := m.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		switch {
		case fd.IsMap():
			mp := m.Mutable(fd).Map()
			for j := 0; j < facts.MaxMapEntries+1; j++ {
				k := protoreflect.ValueOfString(fmt.Sprintf("k%05d", j)).MapKey()
				if fd.MapValue().Kind() == protoreflect.MessageKind {
					if j < 2 && depth < 4 {
						v := mp.NewValue()
						fill(v.Message(), round, depth+1)
						mp.Set(k, v)
					}
					continue
				}
				if fd.MapValue().Kind() == protoreflect.StringKind {
					mp.Set(k, protoreflect.ValueOfString(strings.Repeat("v", 2*facts.MaxMapValue)))
				} else {
					mp.Set(k, scalarFor(fd.MapValue(), round, false))
				}
			}
		case fd.IsList() && fd.Kind() == protoreflect.MessageKind:
			l := m.Mutable(fd).List()
			for j := 0; j < 2 && depth < 4; j++ {
				v := l.NewElement()
				fill(v.Message(), round, depth+1)
				l.Append(v)
			}
		case fd.IsList():
			l := m.Mutable(fd).List()
			for j := 0; j < facts.MaxRepeated+1; j++ {
				if fd.Kind() == protoreflect.StringKind {
					l.Append(protoreflect.ValueOfString("s"))
				} else {
					l.Append(scalarFor(fd, round, false))
				}
			}
		case fd.Kind() == protoreflect.MessageKind:
			if depth < 4 {
				sub := m.NewField(fd)
				fill(sub.Message(), round, depth+1)
				m.Set(fd, sub)
			}
		default:
			m.Set(fd, scalarFor(fd, round, fd.Name() == "key" || fd.Name() == "name" && m.Descriptor().Name() == "ModuleStatus"))
		}
	}
}

// A cluster whose every fact is at the agent's limits - every string over the limit, every list and map over it, every
// enum value in turn - goes through the agent's own cut (facts.SanitizeSync, as Snapshot does) and must then be
// accepted by the server, stored, written to the database and read back after a restart. A new field, or a new
// hand-written server limit that the agent does not apply, fails here instead of in a customer's cluster.
func TestAMaximalSnapshotIsAcceptedAndSurvivesARestart(t *testing.T) {
	for round := 0; round < 6; round++ {
		s := &ikhnosv1.Sync{Full: true, Cluster: &ikhnosv1.ClusterFacts{}, Nodes: []*ikhnosv1.NodeFacts{{}}, Namespaces: []*ikhnosv1.NamespaceFacts{{}}, Workloads: []*ikhnosv1.WorkloadFacts{{}},
			Modules: []*ikhnosv1.ModuleStatus{{}, {}}}
		for _, m := range []protoreflect.Message{s.Cluster.ProtoReflect(), s.Nodes[0].ProtoReflect(), s.Namespaces[0].ProtoReflect(), s.Workloads[0].ProtoReflect(), s.Modules[0].ProtoReflect(), s.Modules[1].ProtoReflect()} {
			fill(m, round, 0)
		}
		s.Cluster.Uid = "cluster-uid"
		if err := facts.SanitizeSync(s); err != nil {
			t.Fatal(err)
		}
		for _, m := range s.Modules {
			m.Reason = facts.Cut(m.Reason, facts.MaxReason) // Collector.Modules does this
		}
		if err := validateSync(s); err != nil {
			t.Fatalf("round %d: the server refused what the agent cuts to: %v", round, err)
		}
		st := facts.New()
		if err := st.Check(s, facts.DefaultLimits()); err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		st.Apply(s)
		data, err := st.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		if len(data) > snapshotCap(facts.DefaultLimits()) {
			t.Fatalf("round %d: the stored state is %d bytes, over the cap %d", round, len(data), snapshotCap(facts.DefaultLimits()))
		}
		if _, err := loadState(data, facts.DefaultLimits()); err != nil {
			t.Fatalf("round %d: the stored state does not load after a restart: %v", round, err)
		}
	}
}
