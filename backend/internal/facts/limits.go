package facts

import (
	"fmt"
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

// Sanitize bounds every string, map and list inside m, whatever the field: an over-long string, a map
// with too many entries or a list that is too long is refused; a map value that is too long is cut in
// place. Walking the message by reflection means a field added to the protocol later is bounded too.
func Sanitize(m proto.Message) error { return walk(m.ProtoReflect(), 0) }

// SanitizeSync applies Sanitize to every fact a message carries. The message's own lists of nodes, namespaces
// and workloads are not subject to MaxRepeated (the counts are limited separately, in total, by Check).
func SanitizeSync(m *continuumv1.Sync) error {
	if m.Cluster != nil {
		if err := Sanitize(m.Cluster); err != nil {
			return err
		}
	}
	for _, n := range m.Nodes {
		if err := Sanitize(n); err != nil {
			return err
		}
	}
	for _, n := range m.Namespaces {
		if err := Sanitize(n); err != nil {
			return err
		}
	}
	for _, w := range m.Workloads {
		if err := Sanitize(w); err != nil {
			return err
		}
	}
	for _, x := range m.Modules {
		if err := Sanitize(x); err != nil {
			return err
		}
	}
	for _, keys := range [][]string{m.DeletedNodes, m.DeletedNamespaces, m.DeletedWorkloads} {
		for _, k := range keys {
			if len(k) > MaxString {
				return fmt.Errorf("a deleted key is longer than %d bytes", MaxString)
			}
		}
	}
	return nil
}

func cut(s string, n int) string {
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
	msg.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case fd.IsMap():
			err = walkMap(fd, v.Map(), depth)
		case fd.IsList():
			err = walkList(fd, v.List(), depth)
		case fd.Kind() == protoreflect.StringKind:
			if len(v.String()) > MaxString {
				err = tooLong(fd)
			}
		case fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind:
			err = walk(v.Message(), depth+1)
		}
		return err == nil
	})
	return err
}

func tooLong(fd protoreflect.FieldDescriptor) error {
	return fmt.Errorf("a value of %s is longer than %d bytes", fd.FullName(), MaxString)
}

func walkList(fd protoreflect.FieldDescriptor, l protoreflect.List, depth int) error {
	if l.Len() > MaxRepeated {
		return fmt.Errorf("%s has %d entries; at most %d are accepted", fd.FullName(), l.Len(), MaxRepeated)
	}
	switch fd.Kind() {
	case protoreflect.StringKind:
		for i := 0; i < l.Len(); i++ {
			if len(l.Get(i).String()) > MaxString {
				return tooLong(fd)
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
	if m.Len() > MaxMapEntries {
		return fmt.Errorf("%s has %d entries; at most %d are accepted", fd.FullName(), m.Len(), MaxMapEntries)
	}
	type fix struct {
		k protoreflect.MapKey
		v string
	}
	var cuts []fix
	var err error
	m.Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
		if fd.MapKey().Kind() == protoreflect.StringKind && len(k.String()) > MaxString {
			err = fmt.Errorf("a key of %s is longer than %d bytes", fd.FullName(), MaxString)
			return false
		}
		switch fd.MapValue().Kind() {
		case protoreflect.StringKind:
			if s := v.String(); len(s) > MaxMapValue {
				cuts = append(cuts, fix{k, cut(s, MaxMapValue)})
			}
		case protoreflect.MessageKind, protoreflect.GroupKind:
			if err = walk(v.Message(), depth+1); err != nil {
				return false
			}
		}
		return true
	})
	if err != nil {
		return err
	}
	for _, c := range cuts {
		m.Set(c.k, protoreflect.ValueOfString(c.v))
	}
	return nil
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
