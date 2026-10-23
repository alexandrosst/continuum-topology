package connectivity

import (
	"testing"

	ikhnosv1 "ikhnos/gen/ikhnosv1"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// The Config the server sends an agent is pushed only when its hash changes. ConfigHash is written out by hand, so a field added to
// the Config and forgotten there is never pushed: the agent keeps running on the old value and nothing says so.
//
// This test is the gate for that. It fills every field of a Config (and of every message inside it) with a value, then takes the
// fields away one at a time and requires the hash to change. A field that is in the proto and not in the hash makes it fail,
// whoever adds it and wherever they forget.
//
// A field that is meant to stay out of the hash is listed below with the reason. That is the only way to leave one out.
var outOfTheConfigHash = map[protoreflect.FullName]string{
	"ikhnos.v1.Config.server_time_unix": "the server's clock at the moment of sending: it changes on every send, and a Config that differs only by it is the same Config",
	"ikhnos.v1.Config.digest":           "the hash itself, set after it is computed",
}

func TestEveryFieldOfAConfigIsInItsHash(t *testing.T) {
	full := &ikhnosv1.Config{}
	fillEveryField(t, full.ProtoReflect())
	base := ConfigHash(full)
	if base == "" || base != ConfigHash(proto.Clone(full).(*ikhnosv1.Config)) {
		t.Fatalf("the hash of a full Config is empty or not stable: %q", base)
	}
	if base == ConfigHash(&ikhnosv1.Config{}) {
		t.Fatal("a full Config hashes as an empty one")
	}

	used := map[protoreflect.FullName]bool{}
	var visit func(path string, md protoreflect.MessageDescriptor, at func(root proto.Message) protoreflect.Message)
	visit = func(path string, md protoreflect.MessageDescriptor, at func(root proto.Message) protoreflect.Message) {
		fields := md.Fields()
		for i := 0; i < fields.Len(); i++ {
			fd := fields.Get(i)
			name := path + string(fd.Name())
			if why, skip := outOfTheConfigHash[fd.FullName()]; skip {
				used[fd.FullName()] = true
				_ = why
				continue
			}
			changes := func(what string, edit func(m protoreflect.Message)) {
				t.Helper()
				c := proto.Clone(full).(*ikhnosv1.Config)
				edit(at(c))
				if ConfigHash(c) == base {
					t.Errorf("%s: %s does not change the hash, so a change to it would never be pushed to an agent. Add it to configHash, or list it in outOfTheConfigHash with the reason", fd.FullName(), what)
				}
			}
			changes(name+" taken away", func(m protoreflect.Message) { m.Clear(fd) })
			if fd.IsList() && !fd.IsMap() {
				changes(name+", the last of its entries dropped", func(m protoreflect.Message) {
					l := m.Mutable(fd).List()
					l.Truncate(l.Len() - 1)
				})
			}
			if fd.Message() != nil && !fd.IsMap() {
				child := fd.Message()
				visit(name+".", child, func(root proto.Message) protoreflect.Message {
					v := at(root).Get(fd)
					if fd.IsList() {
						l := v.List()
						return l.Get(l.Len() - 1).Message() // the last entry: a hash that reads only the first one is caught
					}
					return v.Message()
				})
			}
		}
	}
	visit("", full.ProtoReflect().Descriptor(), func(root proto.Message) protoreflect.Message { return root.ProtoReflect() })

	for name := range outOfTheConfigHash {
		if !used[name] {
			t.Errorf("outOfTheConfigHash names %s, which is not a field of the Config: remove it", name)
		}
	}
}

// fillEveryField gives every field of m a value that is not its zero, two entries to every list, so that taking a field away or
// dropping an entry is always a change.
func fillEveryField(t *testing.T, m protoreflect.Message) {
	t.Helper()
	fields := m.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if fd.IsMap() {
			t.Fatalf("%s is a map: teach fillEveryField (and check configHash orders it) before adding one", fd.FullName())
		}
		if fd.IsList() {
			l := m.Mutable(fd).List()
			for n := 1; n <= 2; n++ {
				l.Append(aSampleOfTheField(t, fd, l, n))
			}
			continue
		}
		if fd.Message() != nil {
			fillEveryField(t, m.Mutable(fd).Message())
			continue
		}
		m.Set(fd, aSampleOfTheField(t, fd, nil, 1))
	}
}

func aSampleOfTheField(t *testing.T, fd protoreflect.FieldDescriptor, l protoreflect.List, n int) protoreflect.Value {
	t.Helper()
	switch fd.Kind() {
	case protoreflect.StringKind:
		return protoreflect.ValueOfString(string(fd.Name()) + "-" + string(rune('a'+n-1)))
	case protoreflect.BytesKind:
		return protoreflect.ValueOfBytes([]byte{byte(n)})
	case protoreflect.BoolKind:
		return protoreflect.ValueOfBool(true)
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return protoreflect.ValueOfInt32(int32(6 + n))
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return protoreflect.ValueOfInt64(int64(6 + n))
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return protoreflect.ValueOfUint32(uint32(6 + n))
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return protoreflect.ValueOfUint64(uint64(6 + n))
	case protoreflect.FloatKind:
		return protoreflect.ValueOfFloat32(float32(n) + 0.5)
	case protoreflect.DoubleKind:
		return protoreflect.ValueOfFloat64(float64(n) + 0.5)
	case protoreflect.EnumKind:
		vals := fd.Enum().Values()
		return protoreflect.ValueOfEnum(vals.Get(vals.Len() - 1).Number())
	case protoreflect.MessageKind, protoreflect.GroupKind:
		msg := l.NewElement().Message()
		fillEveryField(t, msg)
		return protoreflect.ValueOfMessage(msg)
	}
	t.Fatalf("%s: no sample for kind %s", fd.FullName(), fd.Kind())
	return protoreflect.Value{}
}
