package server

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/facts"
	"continuum/internal/flow"
	"continuum/internal/flow/wire"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// A module reason is raw error text from the agent. Over facts.MaxReason it is cut, not a reason to refuse the
// message (which ended the stream and so hid the very error the reason was reporting).
func TestValidateModulesCutsALongReason(t *testing.T) {
	ms := []*continuumv1.ModuleStatus{{Name: "services", State: continuumv1.ModuleStatus_ERROR, Reason: strings.Repeat("é", 1000) + "\n"}}
	if err := validateModules(ms); err != nil {
		t.Fatalf("a long reason was refused: %v", err)
	}
	if len(ms[0].Reason) > facts.MaxReason || !utf8.ValidString(ms[0].Reason) {
		t.Errorf("reason is %d bytes, valid utf-8 %v", len(ms[0].Reason), utf8.ValidString(ms[0].Reason))
	}
	if err := validateModules(make([]*continuumv1.ModuleStatus, 65)); err == nil {
		t.Error("more than 64 modules must still be refused")
	}
}

// ---- the agent-to-server contract ----
//
// What the agent can produce must be accepted by the server, or be dropped and counted - never a refusal, which
// ends the stream. These tests build the agent's output at its limits and run it through the server's own checks.

func ep(kind continuumv1.FlowEndpoint_Kind, i int) *continuumv1.FlowEndpoint {
	switch kind {
	case continuumv1.FlowEndpoint_EXTERNAL:
		return xep(fmt.Sprintf("93.184.%d.%d", i/250, i%250+1))
	case continuumv1.FlowEndpoint_WORKLOAD:
		return wep(fmt.Sprintf("ns/Deployment/w%d", i))
	}
	return &continuumv1.FlowEndpoint{Kind: kind, Ref: fmt.Sprintf("node%d", i), Ip: ""}
}

// Every combination of endpoint kinds, method, noise class, protocol, port extreme and TLS outcome goes through the
// agent's own aggregator and out as a batch. The batch is never refused; what is dropped is exactly what cannot be
// used (an unresolved end, two outside ends, a node-level end), and everything kept is stored and survives a restart.
func TestWhatTheAgentCanSendIsAcceptedOrCountedAsDropped(t *testing.T) {
	agg := flow.NewAggregator()
	kinds := []continuumv1.FlowEndpoint_Kind{continuumv1.FlowEndpoint_UNRESOLVED, continuumv1.FlowEndpoint_WORKLOAD, continuumv1.FlowEndpoint_NODE, continuumv1.FlowEndpoint_EXTERNAL}
	var tls []continuumv1.TlsHandshakeOutcome
	for v := range continuumv1.TlsHandshakeOutcome_name {
		tls = append(tls, continuumv1.TlsHandshakeOutcome(v))
	}
	wantKept, wantDropped, n := 0, 0, 0
	for _, sk := range kinds {
		for _, dk := range kinds {
			usable := (sk == continuumv1.FlowEndpoint_WORKLOAD || dk == continuumv1.FlowEndpoint_WORKLOAD) &&
				sk != continuumv1.FlowEndpoint_UNRESOLVED && dk != continuumv1.FlowEndpoint_UNRESOLVED && sk != continuumv1.FlowEndpoint_NODE && dk != continuumv1.FlowEndpoint_NODE
			for _, method := range []string{"ebpf", "conntrack"} {
				for _, noise := range []string{"", "dns", "system"} {
					for _, proto := range []string{"tcp", "udp"} {
						for _, port := range []uint32{1, 65535} {
							for _, outcome := range tls {
								n++
								f := &continuumv1.Flow{Src: ep(sk, n), Dst: ep(dk, n), Port: port, Protocol: proto, Method: method, Noise: noise, TlsHandshake: outcome, Connections: 1, BytesKnown: n%2 == 0,
									SniHost: strings.Repeat("h", 5000), Iface: strings.Repeat("i", 5000), DnsQueryNames: slices.Repeat([]string{strings.Repeat("d", 5000)}, 50),
									SrcPod: strings.Repeat("p", 253), DstPod: strings.Repeat("q", 253)}
								agg.Add(f)
								if usable {
									wantKept++
								} else {
									wantDropped++
								}
							}
						}
					}
				}
			}
		}
	}
	if wantKept+wantDropped > flow.MaxFlowsPerBatch {
		t.Fatalf("the matrix (%d flows) no longer fits one batch (%d)", wantKept+wantDropped, flow.MaxFlowsPerBatch)
	}
	b := agg.Flush()
	if b == nil || len(b.Flows) != wantKept+wantDropped {
		t.Fatalf("the aggregator kept %v flows, want %d", b, wantKept+wantDropped)
	}
	// A pod flow is a flow that names a pod: the batch carries both, and the same rules apply to both.
	d, err := sanitizeFlowBatch(b)
	if err != nil {
		t.Fatalf("a batch of agent-made flows was refused: %v", err)
	}
	if len(b.Flows) != wantKept || len(b.PodFlows) > wantKept || d.n < wantDropped {
		t.Fatalf("%d flows kept, want %d; %d pod flows; %d dropped (%v), want at least %d", len(b.Flows), wantKept, len(b.PodFlows), d.n, d.first, wantDropped)
	}
	for _, f := range append(slices.Clone(b.Flows), b.PodFlows...) {
		if err := wire.Check(f); err != nil || f.Src.Kind == continuumv1.FlowEndpoint_NODE || f.Dst.Kind == continuumv1.FlowEndpoint_NODE {
			t.Fatalf("a kept flow is not acceptable: %v %v", err, f)
		}
		if len(f.SniHost) > wire.MaxName || len(f.Iface) > wire.MaxIface || len(f.DnsQueryNames) > wire.MaxDNSNames {
			t.Fatalf("a kept flow carries unbounded text: %d %d %d", len(f.SniHost), len(f.Iface), len(f.DnsQueryNames))
		}
	}
	tb := newFlowTable()
	tb.apply(b, time.Now())
	if len(tb.edges) != wantKept {
		t.Fatalf("%d edges stored, want %d", len(tb.edges), wantKept)
	}
	data, err := tb.marshal()
	if err != nil {
		t.Fatal(err)
	}
	if back, err := loadFlowTable(data); err != nil || len(back.edges) != wantKept {
		t.Fatalf("the stored table does not load again: %v", err)
	}
}

// Maximum-length references and a full collector list are accepted; one past the limit is dropped, not refused.
func TestFlowReferencesAtTheirLimits(t *testing.T) {
	ref := func(n int) string { return "ns/Deployment/" + strings.Repeat("r", n-len("ns/Deployment/")) }
	atLimit := flowOf(wep(ref(wire.MaxRef)), xep("1.2.3.4"), 443, 1)
	atLimit.SrcPod = strings.Repeat("p", wire.MaxName)
	over := flowOf(wep(ref(wire.MaxRef+1)), xep("1.2.3.4"), 443, 1)
	var cs []*continuumv1.CollectorInfo
	for i := 0; i < 5000; i++ {
		cs = append(cs, &continuumv1.CollectorInfo{Node: strings.Repeat("n", wire.MaxName-5) + fmt.Sprint(i%10000), Method: "conntrack"})
	}
	b := &continuumv1.FlowBatch{WindowSeconds: 24 * 3600, Flows: []*continuumv1.Flow{atLimit, over}, PodFlows: []*continuumv1.Flow{atLimit}, Collectors: cs}
	d, err := sanitizeFlowBatch(b)
	if err != nil || d.n != 1 || len(b.Flows) != 1 || len(b.PodFlows) != 1 || len(b.Collectors) != 5000 {
		t.Fatalf("err %v, dropped %d, %d flows, %d pod flows, %d collectors", err, d.n, len(b.Flows), len(b.PodFlows), len(b.Collectors))
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
		s := &continuumv1.Sync{Full: true, Cluster: &continuumv1.ClusterFacts{}, Nodes: []*continuumv1.NodeFacts{{}}, Namespaces: []*continuumv1.NamespaceFacts{{}}, Workloads: []*continuumv1.WorkloadFacts{{}},
			Modules: []*continuumv1.ModuleStatus{{}, {}}}
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
