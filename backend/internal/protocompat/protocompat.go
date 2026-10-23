// Package protocompat says whether a protobuf file can still be read by a peer that was built from an older copy of it.
//
// The agent and the server are released apart, so a server speaks to agents built from older schemas and an agent to servers built from
// newer ones. Protobuf makes adding safe (an unknown field is skipped, a missing one is its zero value); it does not make a removal, a
// renumbering or a change of type safe, and nothing in a compiler notices one. Breaks compares two descriptors and names each change that
// an old peer would misread. What it lets through is only what the wire format allows: a new message, field, enum value, service or method.
package protocompat

import (
	"fmt"
	"sort"

	"google.golang.org/protobuf/types/descriptorpb"
)

// Breaks lists, in a stable order, every way the schema `after` cannot be read by a peer built from `before` (and the reverse). An empty
// list means `after` only adds to `before`.
func Breaks(before, after *descriptorpb.FileDescriptorProto) []string {
	var out []string
	add := func(format string, a ...any) { out = append(out, fmt.Sprintf(format, a...)) }

	if before.GetPackage() != after.GetPackage() {
		add("package changed from %q to %q: every type name on the wire moves with it", before.GetPackage(), after.GetPackage())
	}
	if before.GetSyntax() != after.GetSyntax() {
		add("syntax changed from %q to %q: defaults, presence and packing change with it", before.GetSyntax(), after.GetSyntax())
	}

	bm, am := messages(before), messages(after)
	for _, name := range sortedKeys(bm) {
		a, ok := am[name]
		if !ok {
			add("message %s was removed", name)
			continue
		}
		compareMessage(name, bm[name], a, add)
	}

	be, ae := enums(before), enums(after)
	for _, name := range sortedKeys(be) {
		a, ok := ae[name]
		if !ok {
			add("enum %s was removed", name)
			continue
		}
		compareEnum(name, be[name], a, add)
	}

	bs, as := services(before), services(after)
	for _, name := range sortedKeys(bs) {
		a, ok := as[name]
		if !ok {
			add("service %s was removed", name)
			continue
		}
		compareService(name, bs[name], a, add)
	}
	return out
}

func compareMessage(name string, before, after *descriptorpb.DescriptorProto, add func(string, ...any)) {
	bf, af := fieldsByNumber(before), fieldsByNumber(after)
	for _, n := range sortedInts(bf) {
		b := bf[n]
		a, ok := af[n]
		if !ok {
			add("%s: field %d (%s) was removed", name, n, b.GetName())
			continue
		}
		where := fmt.Sprintf("%s: field %d (%s)", name, n, b.GetName())
		if b.GetName() != a.GetName() {
			add("%s was renamed to %s: the number is the same, but a name is what generated code and JSON read", where, a.GetName())
		}
		if b.GetType() != a.GetType() {
			add("%s changed type from %s to %s", where, b.GetType(), a.GetType())
		}
		if b.GetType() == descriptorpb.FieldDescriptorProto_TYPE_MESSAGE || b.GetType() == descriptorpb.FieldDescriptorProto_TYPE_ENUM {
			if b.GetTypeName() != a.GetTypeName() {
				add("%s changed type name from %s to %s", where, b.GetTypeName(), a.GetTypeName())
			}
		}
		if b.GetLabel() != a.GetLabel() {
			add("%s changed from %s to %s", where, b.GetLabel(), a.GetLabel())
		}
		if packed(b) != packed(a) {
			add("%s changed how it is packed", where)
		}
		if b.GetProto3Optional() != a.GetProto3Optional() {
			add("%s changed whether its presence is tracked", where)
		}
		if b.OneofIndex != nil || a.OneofIndex != nil {
			bo, ao := oneofName(before, b), oneofName(after, a)
			if bo != ao {
				add("%s moved from oneof %q to oneof %q", where, bo, ao)
			}
		}
	}
	for _, n := range sortedInts(af) {
		if _, ok := bf[n]; ok {
			continue
		}
		if a := af[n]; a.GetLabel() == descriptorpb.FieldDescriptorProto_LABEL_REQUIRED {
			add("%s: field %d (%s) is new and required: a peer that does not send it is refused", name, n, a.GetName())
		}
		// A number the old schema had reserved is one an old peer may still send, or one a record may still hold.
		for _, r := range before.GetReservedRange() {
			if int32(n) >= r.GetStart() && int32(n) < r.GetEnd() {
				add("%s: field %d (%s) reuses a number the old schema reserved", name, n, af[n].GetName())
			}
		}
		for _, r := range before.GetReservedName() {
			if r == af[n].GetName() {
				add("%s: field %d reuses the reserved name %s", name, n, r)
			}
		}
	}
	bo, ao := oneofNames(before), oneofNames(after)
	for _, o := range bo {
		found := false
		for _, x := range ao {
			if x == o {
				found = true
			}
		}
		if !found {
			add("%s: oneof %s was removed", name, o)
		}
	}
}

func compareEnum(name string, before, after *descriptorpb.EnumDescriptorProto, add func(string, ...any)) {
	bv, av := map[int32]string{}, map[int32]string{}
	for _, v := range before.GetValue() {
		bv[v.GetNumber()] = v.GetName()
	}
	for _, v := range after.GetValue() {
		av[v.GetNumber()] = v.GetName()
	}
	for _, n := range sortedInt32(bv) {
		a, ok := av[n]
		switch {
		case !ok:
			add("enum %s: value %d (%s) was removed: an old peer still sends it", name, n, bv[n])
		case a != bv[n]:
			add("enum %s: value %d was renamed from %s to %s", name, n, bv[n], a)
		}
	}
	// Two names for one number are legal only with allow_alias; a changed number for a name is the same as a removal plus an addition,
	// already named above. A name that moved to another number is named here so the cause reads as it is.
	byName := map[string]int32{}
	for n, s := range av {
		byName[s] = n
	}
	for n, s := range bv {
		if m, ok := byName[s]; ok && m != n {
			add("enum %s: %s moved from %d to %d", name, s, n, m)
		}
	}
}

func compareService(name string, before, after *descriptorpb.ServiceDescriptorProto, add func(string, ...any)) {
	am := map[string]*descriptorpb.MethodDescriptorProto{}
	for _, m := range after.GetMethod() {
		am[m.GetName()] = m
	}
	for _, b := range before.GetMethod() {
		a, ok := am[b.GetName()]
		if !ok {
			add("service %s: method %s was removed", name, b.GetName())
			continue
		}
		where := fmt.Sprintf("service %s: method %s", name, b.GetName())
		if b.GetInputType() != a.GetInputType() {
			add("%s takes %s now, it took %s", where, a.GetInputType(), b.GetInputType())
		}
		if b.GetOutputType() != a.GetOutputType() {
			add("%s returns %s now, it returned %s", where, a.GetOutputType(), b.GetOutputType())
		}
		if b.GetClientStreaming() != a.GetClientStreaming() {
			add("%s changed whether the client streams", where)
		}
		if b.GetServerStreaming() != a.GetServerStreaming() {
			add("%s changed whether the server streams", where)
		}
	}
}

// packed is whether a repeated scalar is written packed: proto3 packs by default, an explicit option overrides it.
func packed(f *descriptorpb.FieldDescriptorProto) bool {
	if f.GetLabel() != descriptorpb.FieldDescriptorProto_LABEL_REPEATED {
		return false
	}
	switch f.GetType() {
	case descriptorpb.FieldDescriptorProto_TYPE_STRING, descriptorpb.FieldDescriptorProto_TYPE_BYTES,
		descriptorpb.FieldDescriptorProto_TYPE_MESSAGE, descriptorpb.FieldDescriptorProto_TYPE_GROUP:
		return false
	}
	if f.GetOptions() != nil && f.GetOptions().Packed != nil {
		return f.GetOptions().GetPacked()
	}
	return true
}

func oneofName(m *descriptorpb.DescriptorProto, f *descriptorpb.FieldDescriptorProto) string {
	if f.OneofIndex == nil || int(f.GetOneofIndex()) >= len(m.GetOneofDecl()) {
		return ""
	}
	// A proto3 optional field sits in a oneof the compiler made up; it is not a oneof a reader can name.
	if f.GetProto3Optional() {
		return ""
	}
	return m.GetOneofDecl()[f.GetOneofIndex()].GetName()
}

func oneofNames(m *descriptorpb.DescriptorProto) []string {
	synthetic := map[int32]bool{}
	for _, f := range m.GetField() {
		if f.GetProto3Optional() && f.OneofIndex != nil {
			synthetic[f.GetOneofIndex()] = true
		}
	}
	var out []string
	for i, o := range m.GetOneofDecl() {
		if !synthetic[int32(i)] {
			out = append(out, o.GetName())
		}
	}
	return out
}

func fieldsByNumber(m *descriptorpb.DescriptorProto) map[int]*descriptorpb.FieldDescriptorProto {
	out := map[int]*descriptorpb.FieldDescriptorProto{}
	for _, f := range m.GetField() {
		out[int(f.GetNumber())] = f
	}
	return out
}

// messages is every message of the file, nested ones included, by full name (without the package).
func messages(f *descriptorpb.FileDescriptorProto) map[string]*descriptorpb.DescriptorProto {
	out := map[string]*descriptorpb.DescriptorProto{}
	var walk func(prefix string, ms []*descriptorpb.DescriptorProto)
	walk = func(prefix string, ms []*descriptorpb.DescriptorProto) {
		for _, m := range ms {
			name := prefix + m.GetName()
			out[name] = m
			walk(name+".", m.GetNestedType())
		}
	}
	walk("", f.GetMessageType())
	return out
}

func enums(f *descriptorpb.FileDescriptorProto) map[string]*descriptorpb.EnumDescriptorProto {
	out := map[string]*descriptorpb.EnumDescriptorProto{}
	for _, e := range f.GetEnumType() {
		out[e.GetName()] = e
	}
	var walk func(prefix string, ms []*descriptorpb.DescriptorProto)
	walk = func(prefix string, ms []*descriptorpb.DescriptorProto) {
		for _, m := range ms {
			name := prefix + m.GetName()
			for _, e := range m.GetEnumType() {
				out[name+"."+e.GetName()] = e
			}
			walk(name+".", m.GetNestedType())
		}
	}
	walk("", f.GetMessageType())
	return out
}

func services(f *descriptorpb.FileDescriptorProto) map[string]*descriptorpb.ServiceDescriptorProto {
	out := map[string]*descriptorpb.ServiceDescriptorProto{}
	for _, s := range f.GetService() {
		out[s.GetName()] = s
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedInts[V any](m map[int]V) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

func sortedInt32[V any](m map[int32]V) []int32 {
	out := make([]int32, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
