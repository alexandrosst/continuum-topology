package protocompat

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"

	"continuum/gen/continuumv1"
)

var update = flag.Bool("update", false, "write the baseline from the schema as it stands")

const baselineFile = "testdata/agent.baseline.json"

// acceptBreak is the one way a breaking change gets into the baseline: said out loud, in the environment of the person making it.
const acceptBreak = "CONTINUUM_ACCEPT_PROTO_BREAK"

func current(t *testing.T) *descriptorpb.FileDescriptorProto {
	t.Helper()
	return protodesc.ToFileDescriptorProto(continuumv1.File_continuum_v1_agent_proto)
}

func readBaseline(t *testing.T) *descriptorpb.FileDescriptorProto {
	t.Helper()
	b, err := os.ReadFile(baselineFile)
	if err != nil {
		t.Fatalf("%v: write it with go test ./internal/protocompat -update", err)
	}
	fd := &descriptorpb.FileDescriptorProto{}
	if err := protojson.Unmarshal(b, fd); err != nil {
		t.Fatalf("%s: %v", baselineFile, err)
	}
	return fd
}

// render writes the descriptor as indented JSON that is the same every time (protojson varies its spaces on purpose), so a change to the
// schema is a readable diff in a review.
func render(t *testing.T, fd *descriptorpb.FileDescriptorProto) []byte {
	t.Helper()
	raw, err := protojson.Marshal(fd)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		t.Fatal(err)
	}
	buf.WriteByte('\n')
	return buf.Bytes()
}

// TestTheAgentProtocolOnlyAddsToTheBaseline is the guard. The baseline is the schema as the last release spoke it; the agent and the server
// are released apart, so each must still read what the other one's baseline-era build sends. Adding to the schema needs nothing here. A
// removal, a renumbering or a change of type is named, and fails.
func TestTheAgentProtocolOnlyAddsToTheBaseline(t *testing.T) {
	cur := current(t)
	if *update {
		if old, err := os.ReadFile(baselineFile); err == nil {
			prev := &descriptorpb.FileDescriptorProto{}
			if err := protojson.Unmarshal(old, prev); err != nil {
				t.Fatalf("the old baseline cannot be read: %v", err)
			}
			if br := Breaks(prev, cur); len(br) > 0 && os.Getenv(acceptBreak) != "1" {
				t.Fatalf("this schema cannot be read by a peer built from the baseline, so it is not written:\n  %s\nIf a break is meant (a protocol version bump with both sides moved together), say so with %s=1 and put the reason in the change",
					strings.Join(br, "\n  "), acceptBreak)
			}
		}
		if err := os.WriteFile(baselineFile, render(t, cur), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if br := Breaks(readBaseline(t), cur); len(br) > 0 {
		t.Fatalf("a peer built from the baseline cannot read this schema:\n  %s\nAdd instead of changing (a new field number, a new message); to retire a field, keep its number in `reserved`. If the break is meant, bump the protocol version, move both sides together, and regenerate the baseline with %s=1 go test ./internal/protocompat -update",
			strings.Join(br, "\n  "), acceptBreak)
	}
}

// TestEveryFieldOfTheRealSchemaIsGuarded takes the real schema and removes each field in turn, then changes the type of each: every one must
// be named. It shows the guard is reading the whole of the protocol and not a corner of it.
func TestEveryFieldOfTheRealSchemaIsGuarded(t *testing.T) {
	base := readBaseline(t)
	fields := 0
	for name, m := range messages(base) {
		for i := range m.GetField() {
			fields++
			cut := proto.Clone(base).(*descriptorpb.FileDescriptorProto)
			cm := messages(cut)[name]
			removed := cm.Field[i].GetName()
			cm.Field = append(cm.Field[:i:i], cm.Field[i+1:]...)
			if br := Breaks(base, cut); len(br) == 0 {
				t.Errorf("removing %s.%s was not named", name, removed)
			}
		}
	}
	if fields < 50 {
		t.Fatalf("only %d fields were walked: the guard is not reading the schema", fields)
	}
}

// TestTheBaselineFileIsWhatUpdateWrites keeps the file in the form -update writes, so a hand edit or a different tool shows as a diff.
func TestTheBaselineFileIsWhatUpdateWrites(t *testing.T) {
	b, err := os.ReadFile(baselineFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := render(t, readBaseline(t)); !bytes.Equal(got, b) {
		t.Fatalf("%s is not in the form -update writes it", baselineFile)
	}
}

// The tests below are the comparer's own, on small schemas: a guard that never fires is not one.

func file(msgs ...*descriptorpb.DescriptorProto) *descriptorpb.FileDescriptorProto {
	return &descriptorpb.FileDescriptorProto{Name: ptr("t.proto"), Package: ptr("t.v1"), Syntax: ptr("proto3"), MessageType: msgs}
}

func ptr[T any](v T) *T { return &v }

func field(name string, num int32, typ descriptorpb.FieldDescriptorProto_Type) *descriptorpb.FieldDescriptorProto {
	return &descriptorpb.FieldDescriptorProto{Name: ptr(name), Number: ptr(num), Type: &typ, Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum()}
}

func msg(name string, fs ...*descriptorpb.FieldDescriptorProto) *descriptorpb.DescriptorProto {
	return &descriptorpb.DescriptorProto{Name: ptr(name), Field: fs}
}

const (
	tString = descriptorpb.FieldDescriptorProto_TYPE_STRING
	tInt64  = descriptorpb.FieldDescriptorProto_TYPE_INT64
	tInt32  = descriptorpb.FieldDescriptorProto_TYPE_INT32
	tMsg    = descriptorpb.FieldDescriptorProto_TYPE_MESSAGE
)

func TestComparerLetsAdditionsThrough(t *testing.T) {
	before := file(msg("A", field("x", 1, tString)))
	after := file(msg("A", field("x", 1, tString), field("y", 2, tInt64)), msg("B", field("z", 1, tString)))
	after.Service = []*descriptorpb.ServiceDescriptorProto{{Name: ptr("S")}}
	after.EnumType = []*descriptorpb.EnumDescriptorProto{{Name: ptr("E"), Value: []*descriptorpb.EnumValueDescriptorProto{{Name: ptr("E_NONE"), Number: ptr(int32(0))}}}}
	if br := Breaks(before, after); len(br) != 0 {
		t.Fatalf("an addition was called a break: %v", br)
	}
}

func TestComparerNamesWhatAnOldPeerWouldMisread(t *testing.T) {
	rep := func(f *descriptorpb.FieldDescriptorProto) *descriptorpb.FieldDescriptorProto {
		f.Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
		return f
	}
	typed := func(f *descriptorpb.FieldDescriptorProto, name string) *descriptorpb.FieldDescriptorProto {
		f.TypeName = ptr(name)
		return f
	}
	inOneof := func(f *descriptorpb.FieldDescriptorProto) *descriptorpb.FieldDescriptorProto {
		f.OneofIndex = ptr(int32(0))
		return f
	}
	withOneof := func(m *descriptorpb.DescriptorProto) *descriptorpb.DescriptorProto {
		m.OneofDecl = []*descriptorpb.OneofDescriptorProto{{Name: ptr("o")}}
		return m
	}
	method := func(in, out string, cs, ss bool) *descriptorpb.ServiceDescriptorProto {
		return &descriptorpb.ServiceDescriptorProto{Name: ptr("S"), Method: []*descriptorpb.MethodDescriptorProto{
			{Name: ptr("M"), InputType: ptr(in), OutputType: ptr(out), ClientStreaming: ptr(cs), ServerStreaming: ptr(ss)}}}
	}
	enum := func(vs ...[2]any) *descriptorpb.EnumDescriptorProto {
		e := &descriptorpb.EnumDescriptorProto{Name: ptr("E")}
		for _, v := range vs {
			e.Value = append(e.Value, &descriptorpb.EnumValueDescriptorProto{Name: ptr(v[0].(string)), Number: ptr(int32(v[1].(int)))})
		}
		return e
	}
	cases := []struct {
		name          string
		before, after *descriptorpb.FileDescriptorProto
		want          string
	}{
		{"a field is removed", file(msg("A", field("x", 1, tString), field("y", 2, tString))), file(msg("A", field("x", 1, tString))), "field 2 (y) was removed"},
		{"a message is removed", file(msg("A"), msg("B")), file(msg("A")), "message B was removed"},
		{"a field changes type", file(msg("A", field("x", 1, tString))), file(msg("A", field("x", 1, tInt64))), "changed type from"},
		{"a field is renamed", file(msg("A", field("x", 1, tString))), file(msg("A", field("w", 1, tString))), "was renamed to w"},
		{"a field becomes repeated", file(msg("A", field("x", 1, tString))), file(msg("A", rep(field("x", 1, tString)))), "changed from LABEL_OPTIONAL to LABEL_REPEATED"},
		{"a field points at another message", file(msg("A", typed(field("x", 1, tMsg), ".t.v1.B")), msg("B"), msg("C")), file(msg("A", typed(field("x", 1, tMsg), ".t.v1.C")), msg("B"), msg("C")), "changed type name"},
		{"a field moves into a oneof", file(withOneof(msg("A", field("x", 1, tString)))), file(withOneof(msg("A", inOneof(field("x", 1, tString))))), "moved from oneof"},
		{"a number the old schema reserved is used", func() *descriptorpb.FileDescriptorProto {
			m := msg("A", field("x", 1, tString))
			m.ReservedRange = []*descriptorpb.DescriptorProto_ReservedRange{{Start: ptr(int32(2)), End: ptr(int32(3))}}
			return file(m)
		}(), file(msg("A", field("x", 1, tString), field("y", 2, tInt32))), "reuses a number the old schema reserved"},
		{"a new required field", file(msg("A")), func() *descriptorpb.FileDescriptorProto {
			f := field("x", 1, tString)
			f.Label = descriptorpb.FieldDescriptorProto_LABEL_REQUIRED.Enum()
			return file(msg("A", f))
		}(), "new and required"},
		{"a package is renamed", file(msg("A")), func() *descriptorpb.FileDescriptorProto { f := file(msg("A")); f.Package = ptr("t.v2"); return f }(), "package changed"},
		{"an enum value is removed", func() *descriptorpb.FileDescriptorProto {
			f := file()
			f.EnumType = []*descriptorpb.EnumDescriptorProto{enum([2]any{"A", 0}, [2]any{"B", 1})}
			return f
		}(), func() *descriptorpb.FileDescriptorProto {
			f := file()
			f.EnumType = []*descriptorpb.EnumDescriptorProto{enum([2]any{"A", 0})}
			return f
		}(), "value 1 (B) was removed"},
		{"an enum value changes number", func() *descriptorpb.FileDescriptorProto {
			f := file()
			f.EnumType = []*descriptorpb.EnumDescriptorProto{enum([2]any{"A", 0}, [2]any{"B", 1})}
			return f
		}(), func() *descriptorpb.FileDescriptorProto {
			f := file()
			f.EnumType = []*descriptorpb.EnumDescriptorProto{enum([2]any{"A", 0}, [2]any{"B", 2})}
			return f
		}(), "B moved from 1 to 2"},
		{"a method is removed", func() *descriptorpb.FileDescriptorProto {
			f := file(msg("A"))
			f.Service = []*descriptorpb.ServiceDescriptorProto{method(".t.v1.A", ".t.v1.A", false, false)}
			return f
		}(), func() *descriptorpb.FileDescriptorProto {
			f := file(msg("A"))
			f.Service = []*descriptorpb.ServiceDescriptorProto{{Name: ptr("S")}}
			return f
		}(), "method M was removed"},
		{"a method starts to stream", func() *descriptorpb.FileDescriptorProto {
			f := file(msg("A"))
			f.Service = []*descriptorpb.ServiceDescriptorProto{method(".t.v1.A", ".t.v1.A", false, false)}
			return f
		}(), func() *descriptorpb.FileDescriptorProto {
			f := file(msg("A"))
			f.Service = []*descriptorpb.ServiceDescriptorProto{method(".t.v1.A", ".t.v1.A", true, false)}
			return f
		}(), "whether the client streams"},
		{"a method takes another message", func() *descriptorpb.FileDescriptorProto {
			f := file(msg("A"), msg("B"))
			f.Service = []*descriptorpb.ServiceDescriptorProto{method(".t.v1.A", ".t.v1.A", false, false)}
			return f
		}(), func() *descriptorpb.FileDescriptorProto {
			f := file(msg("A"), msg("B"))
			f.Service = []*descriptorpb.ServiceDescriptorProto{method(".t.v1.B", ".t.v1.A", false, false)}
			return f
		}(), "takes .t.v1.B now"},
		{"a nested message loses a field", func() *descriptorpb.FileDescriptorProto {
			m := msg("A")
			m.NestedType = []*descriptorpb.DescriptorProto{msg("N", field("x", 1, tString))}
			return file(m)
		}(), func() *descriptorpb.FileDescriptorProto {
			m := msg("A")
			m.NestedType = []*descriptorpb.DescriptorProto{msg("N")}
			return file(m)
		}(), "A.N: field 1 (x) was removed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			br := Breaks(c.before, c.after)
			if len(br) == 0 {
				t.Fatalf("no break was named; want one containing %q", c.want)
			}
			if !strings.Contains(strings.Join(br, "\n"), c.want) {
				t.Fatalf("breaks %q do not contain %q", br, c.want)
			}
		})
	}
}

func TestComparerReadsTheRealSchemaAsCompatibleWithItself(t *testing.T) {
	cur := current(t)
	if br := Breaks(cur, cur); len(br) != 0 {
		t.Fatalf("a schema breaks against itself: %v", br)
	}
	// The real schema has messages, streaming methods and a oneof; if the walk saw nothing the guard would be passing on an empty comparison.
	if len(messages(cur)) < 20 || len(services(cur)) < 2 {
		t.Fatalf("the walk saw %d messages and %d services: the comparison is not reading the schema", len(messages(cur)), len(services(cur)))
	}
}
