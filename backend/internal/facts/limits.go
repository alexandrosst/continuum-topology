package facts

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	continuumv1 "continuum/gen/continuumv1"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// What one agent may make the server hold. A real cluster is far below every one of these; they exist so
// that a compromised (or merely buggy) agent cannot exhaust the server's memory, fill its database, or
// leave behind a stored picture that cannot be loaded again.
const (
	// MaxString bounds every string field of a fact, and every key of a map.
	MaxString = 1024
	// MaxMapValue bounds the value of a map<string,string> (labels, annotations). Longer values are cut,
	// not refused: a real annotation may be long, and what follows the first kilobyte is of no use here.
	MaxMapValue = 1024
	// MaxMapEntries and MaxRepeated bound any map and any repeated field inside a fact.
	MaxMapEntries = 1000
	MaxRepeated   = 5000
	// MaxReason bounds ModuleStatus.reason, which carries raw error text: the agent cuts it to this before
	// sending and the server cuts it again rather than refusing the message.
	MaxReason = 512

	// The most of each kind one cluster may hold once a message has been merged, and the most all of
	// them may take up together (their encoded size). Counts alone would not bound memory: an entity can
	// itself be large.
	MaxNodes      = 5000
	MaxNamespaces = 5000
	MaxWorkloads  = 50000
	MaxBytes      = 32 << 20

	maxDepth = 8
)

// Limits are the caps Check enforces. DefaultLimits is what the server uses.
type Limits struct {
	Nodes, Namespaces, Workloads int
	Bytes                        int64
}

func DefaultLimits() Limits {
	return Limits{Nodes: MaxNodes, Namespaces: MaxNamespaces, Workloads: MaxWorkloads, Bytes: MaxBytes}
}

// LimitError says which cap a message would break, in words the agent's operator can act on.
type LimitError struct {
	What      string
	Have, Max int64
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("this cluster's facts would exceed the server's limit for %s (%d, at most %d); narrow what the agent reports (scope) or split the cluster", e.What, e.Have, e.Max)
}

// Sanitize bounds every string, map and list inside m, whatever the field, by cutting in place: a string over
// MaxString is cut, a list over MaxRepeated is truncated, a map keeps its first MaxMapEntries keys (in order) and
// loses any entry whose key is over MaxString, and a map value over MaxMapValue is cut. The agent applies it to
// what it is about to send and the server to what it receives, so the two cannot disagree about a limit and an
// over-large cluster loses detail instead of being refused. Walking the message by reflection means a field
// added to the protocol later is bounded too. Only a message nested too deeply is an error.
func Sanitize(m proto.Message) error { return walk(m.ProtoReflect(), 0) }

// SanitizeSync applies Sanitize to every fact a message carries. The message's own lists of nodes, namespaces
// and workloads are not subject to MaxRepeated (the counts are limited separately, in total, by Check). A deleted
// key over MaxString cannot name anything the server holds and is dropped.
func SanitizeSync(m *continuumv1.Sync) error {
	var all []proto.Message
	if m.Cluster != nil {
		all = append(all, m.Cluster)
	}
	for _, n := range m.Nodes {
		all = append(all, n)
	}
	for _, n := range m.Namespaces {
		all = append(all, n)
	}
	for _, w := range m.Workloads {
		all = append(all, w)
	}
	for _, x := range m.Modules {
		all = append(all, x)
	}
	for _, x := range all {
		if err := Sanitize(x); err != nil {
			return err
		}
	}
	for _, keys := range []*[]string{&m.DeletedNodes, &m.DeletedNamespaces, &m.DeletedWorkloads} {
		*keys = slices.DeleteFunc(*keys, func(k string) bool { return len(k) > MaxString })
	}
	return nil
}

// Cut returns s cut to at most n bytes, never in the middle of a character.
func Cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func walk(msg protoreflect.Message, depth int) error {
	if depth > maxDepth {
		return fmt.Errorf("facts are nested too deeply")
	}
	var err error
	cuts := map[protoreflect.FieldDescriptor]string{} // set after Range: a message must not change while it is ranged over
	msg.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case fd.IsMap():
			err = walkMap(fd, v.Map(), depth)
		case fd.IsList():
			err = walkList(fd, v.List(), depth)
		case fd.Kind() == protoreflect.StringKind:
			if s := v.String(); len(s) > MaxString {
				cuts[fd] = Cut(s, MaxString)
			}
		case fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind:
			err = walk(v.Message(), depth+1)
		}
		return err == nil
	})
	for fd, s := range cuts {
		msg.Set(fd, protoreflect.ValueOfString(s))
	}
	return err
}

func walkList(fd protoreflect.FieldDescriptor, l protoreflect.List, depth int) error {
	if l.Len() > MaxRepeated {
		l.Truncate(MaxRepeated)
	}
	switch fd.Kind() {
	case protoreflect.StringKind:
		for i := 0; i < l.Len(); i++ {
			if s := l.Get(i).String(); len(s) > MaxString {
				l.Set(i, protoreflect.ValueOfString(Cut(s, MaxString)))
			}
		}
	case protoreflect.MessageKind, protoreflect.GroupKind:
		for i := 0; i < l.Len(); i++ {
			if err := walk(l.Get(i).Message(), depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func walkMap(fd protoreflect.FieldDescriptor, m protoreflect.Map, depth int) error {
	type fix struct {
		k   protoreflect.MapKey
		v   string
		del bool
	}
	var fixes []fix // applied after Range: a map must not change while it is ranged over
	var err error
	m.Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
		if fd.MapKey().Kind() == protoreflect.StringKind && len(k.String()) > MaxString {
			fixes = append(fixes, fix{k: k, del: true})
			return true
		}
		switch fd.MapValue().Kind() {
		case protoreflect.StringKind:
			if s := v.String(); len(s) > MaxMapValue {
				fixes = append(fixes, fix{k: k, v: Cut(s, MaxMapValue)})
			}
		case protoreflect.MessageKind, protoreflect.GroupKind:
			err = walk(v.Message(), depth+1)
		}
		return err == nil
	})
	for _, f := range fixes {
		if f.del {
			m.Clear(f.k)
		} else {
			m.Set(f.k, protoreflect.ValueOfString(f.v))
		}
	}
	if m.Len() > MaxMapEntries { // keep the first MaxMapEntries keys in order, so the same input always loses the same entries
		var keys []protoreflect.MapKey
		m.Range(func(k protoreflect.MapKey, _ protoreflect.Value) bool { keys = append(keys, k); return true })
		slices.SortFunc(keys, func(a, b protoreflect.MapKey) int { return strings.Compare(a.String(), b.String()) })
		for _, k := range keys[MaxMapEntries:] {
			m.Clear(k)
		}
	}
	return err
}

// project is what one kind of entity would look like after a message: how many there would be and how
// many bytes they would take. Adding comes before deleting, as in Apply, so a key both added and deleted
// ends up gone.
func project[T proto.Message](cur map[string]T, curBytes int64, full bool, add []T, key func(T) string, del []string) (int, int64) {
	if full {
		cur, curBytes = nil, 0
	}
	added := make(map[string]int64, len(add))
	for _, a := range add {
		added[key(a)] = int64(proto.Size(a)) // the last one wins
	}
	deleted := make(map[string]bool, len(del))
	for _, k := range del {
		deleted[k] = true
	}
	n, size := len(cur), curBytes
	for k, sz := range added {
		old, had := cur[k]
		switch {
		case deleted[k]:
			if had {
				n--
				size -= int64(proto.Size(old))
			}
		case had:
			size += sz - int64(proto.Size(old))
		default:
			n++
			size += sz
		}
	}
	for k := range deleted {
		if _, isAdded := added[k]; isAdded {
			continue
		}
		if old, had := cur[k]; had {
			n--
			size -= int64(proto.Size(old))
		}
	}
	return n, size
}

// Check reports whether merging m would leave the state within the limits, without changing anything.
// Apply after a successful Check cannot exceed them.
func (s *State) Check(m *continuumv1.Sync, l Limits) error {
	nn, nb := project(s.Nodes, s.nodeBytes, m.Full, m.Nodes, func(n *continuumv1.NodeFacts) string { return n.Key }, m.DeletedNodes)
	sn, sb := project(s.Namespaces, s.nsBytes, m.Full, m.Namespaces, func(n *continuumv1.NamespaceFacts) string { return n.Key }, m.DeletedNamespaces)
	wn, wb := project(s.Workloads, s.wlBytes, m.Full, m.Workloads, func(w *continuumv1.WorkloadFacts) string { return w.Key }, m.DeletedWorkloads)
	switch {
	case nn > l.Nodes:
		return &LimitError{"nodes", int64(nn), int64(l.Nodes)}
	case sn > l.Namespaces:
		return &LimitError{"namespaces", int64(sn), int64(l.Namespaces)}
	case wn > l.Workloads:
		return &LimitError{"workloads", int64(wn), int64(l.Workloads)}
	}
	cluster, modules := s.otherBytes()
	if m.Cluster != nil {
		cluster = int64(proto.Size(m.Cluster))
	}
	if len(m.Modules) > 0 {
		modules = modulesSize(m.Modules)
	}
	if total := nb + sb + wb + cluster + modules; total > l.Bytes {
		return &LimitError{"the total size of its facts (bytes)", total, l.Bytes}
	}
	return nil
}

func modulesSize(ms []*continuumv1.ModuleStatus) int64 {
	var n int64
	for _, m := range ms {
		n += int64(proto.Size(m))
	}
	return n
}

// Bytes is the encoded size of everything the state holds.
func (s *State) Bytes() int64 {
	c, m := s.otherBytes()
	return s.nodeBytes + s.nsBytes + s.wlBytes + c + m
}

func (s *State) otherBytes() (cluster, modules int64) {
	if s.Cluster != nil {
		cluster = int64(proto.Size(s.Cluster))
	}
	return cluster, modulesSize(s.Modules)
}

// CheckState verifies a whole state (one just loaded from storage) against the limits.
func (s *State) CheckState(l Limits) error {
	switch {
	case len(s.Nodes) > l.Nodes:
		return &LimitError{"nodes", int64(len(s.Nodes)), int64(l.Nodes)}
	case len(s.Namespaces) > l.Namespaces:
		return &LimitError{"namespaces", int64(len(s.Namespaces)), int64(l.Namespaces)}
	case len(s.Workloads) > l.Workloads:
		return &LimitError{"workloads", int64(len(s.Workloads)), int64(l.Workloads)}
	case s.Bytes() > l.Bytes:
		return &LimitError{"the total size of its facts (bytes)", s.Bytes(), l.Bytes}
	}
	return nil
}
