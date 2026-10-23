package estate

import (
	"fmt"
	"testing"
	"time"

	ikhnosv1 "ikhnos/gen/ikhnosv1"
	"ikhnos/internal/facts"

	"google.golang.org/protobuf/proto"
)

// What an agent sends is whatever its bytes say, so the intake is held to what it promises for any message: a picture the checks accept
// leaves the server's state within the limits and with no value no kubelet could have read, and the checks agree with themselves when
// they are run again. The seeds run with every `go test`; `go test -fuzz=FuzzSyncIntake ./internal/estate` looks for more.

func syncSeeds() []*ikhnosv1.Sync {
	node := func(key string, pod *int32, cpu *float64) *ikhnosv1.NodeFacts {
		return &ikhnosv1.NodeFacts{Key: key, Name: key, PodCount: pod, Probe: &ikhnosv1.HostProbe{CpuPressurePct: cpu, HostWatts: f64(-3)}}
	}
	return []*ikhnosv1.Sync{
		{Seq: 1, Full: true, Cluster: &ikhnosv1.ClusterFacts{Uid: "c1", PendingPodCount: i32(-1)},
			Nodes:      []*ikhnosv1.NodeFacts{node("n1", i32(4), f64(12)), node("n2", i32(-7), f64(180)), node("n1", nil, nil)},
			Namespaces: []*ikhnosv1.NamespaceFacts{{Key: "ns", Name: "shop"}},
			Workloads:  []*ikhnosv1.WorkloadFacts{{Key: "w", Namespace: "shop", Name: "web"}}},
		{Seq: 2, DeletedNodes: []string{"n1", "gone"}, DeletedWorkloads: []string{"w"}},
		{Seq: 3, Nodes: []*ikhnosv1.NodeFacts{{Key: ""}}},
		{Seq: 4, Full: true},
	}
}

func FuzzSyncIntake(f *testing.F) {
	for _, s := range syncSeeds() {
		b, err := proto.Marshal(s)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		s := &ikhnosv1.Sync{}
		if proto.Unmarshal(data, s) != nil {
			return
		}
		if validateSync(s) != nil {
			return
		}
		boundSyncFacts(s)
		if n := boundSyncFacts(s); n != 0 {
			t.Fatalf("a second pass found %d more impossible values", n)
		}
		if c := s.Cluster; c != nil && c.PendingPodCount != nil && *c.PendingPodCount < 0 {
			t.Fatalf("a negative pending pod count survived: %d", *c.PendingPodCount)
		}
		for _, n := range s.Nodes {
			if n.PodCount != nil && *n.PodCount < 0 {
				t.Fatalf("a negative pod count survived: %d", *n.PodCount)
			}
			if p := n.Probe; p != nil {
				for name, v := range map[string]*float64{"cpu": p.CpuPressurePct, "memory": p.MemoryPressurePct, "io": p.IoPressurePct} {
					if v != nil && (*v != *v || *v < 0 || *v > 100) {
						t.Fatalf("a %s pressure of %v survived", name, *v)
					}
				}
				if p.HostWatts != nil && (*p.HostWatts != *p.HostWatts || *p.HostWatts < 0) {
					t.Fatalf("host watts of %v survived", *p.HostWatts)
				}
			}
		}
		l := facts.DefaultLimits()
		st := facts.New()
		if st.Check(s, l) != nil {
			return
		}
		st.Apply(s)
		if err := st.CheckState(l); err != nil {
			t.Fatalf("Check accepted a picture and the state it made breaks the limits: %v", err)
		}
		if s.Full {
			if d := st.CheckDrift(s); d.Total() != 0 {
				t.Fatalf("a full picture, once applied, differs from itself: %s", d)
			}
		}
		// The same picture again changes nothing about what the server holds.
		before := len(st.Nodes) + len(st.Namespaces) + len(st.Workloads)
		st.Apply(s)
		if after := len(st.Nodes) + len(st.Namespaces) + len(st.Workloads); after != before {
			t.Fatalf("applying a picture twice changed the count of what is held: %d then %d", before, after)
		}
	})
}

// FuzzChunkSeries feeds the assembly any run of pieces, the way the stream's loop does (a piece that is not part of a series ends it, a
// first piece starts one, a broken rule ends the stream), and holds it to: a whole picture comes out only after exactly the pieces the
// first one announced, in order and of one series, and it holds exactly what those pieces carried.
func FuzzChunkSeries(f *testing.F) {
	f.Add([]byte{0, 3, 1, 1, 1, 2, 1, 3, 1, 1, 1, 1, 2, 3, 1, 1, 1, 0})
	f.Add([]byte{0, 2, 1, 5, 0, 0, 2, 1, 5, 0, 1, 2, 1, 5, 0, 1, 2, 1, 5, 0})
	f.Add([]byte{1, 3, 1, 1, 1, 0, 3, 2, 1, 1})
	f.Add([]byte{0, 200, 1, 0, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		now := time.Unix(1_700_000_000, 0)
		l := facts.DefaultLimits()
		var asm *assembly
		var accepted, announced, carried int
		var id string
		for len(data) >= 5 {
			b := data[:5]
			data = data[5:]
			s := &ikhnosv1.Sync{ChunkIndex: uint32(b[0] % 6), ChunkTotal: uint32(b[1] % 6), SyncId: []string{"", "a", "b"}[b[2]%3], Seq: uint64(b[3] % 2), Full: b[3]&2 != 0}
			for i := 0; i < int(b[4]%4); i++ {
				s.Nodes = append(s.Nodes, &ikhnosv1.NodeFacts{Key: fmt.Sprint("n", len(data), i)})
			}
			if !isChunk(s) {
				asm = nil
				continue
			}
			if s.ChunkIndex == 0 {
				var err error
				if asm, err = newAssembly(s, now); err != nil {
					return // the stream ends
				}
				accepted, announced, carried, id = 0, int(s.ChunkTotal), 0, s.SyncId
			} else if asm == nil {
				return // a piece with no first piece: the stream ends
			}
			whole, err := asm.add(s, l)
			if err != nil {
				return // the stream ends
			}
			accepted++
			carried += len(s.Nodes)
			if accepted > announced {
				t.Fatalf("%d pieces accepted of a series that announced %d", accepted, announced)
			}
			if whole == nil {
				if accepted == announced {
					t.Fatalf("all %d pieces are in and no picture came out", announced)
				}
				continue
			}
			if accepted != announced {
				t.Fatalf("a picture came out after %d pieces of %d", accepted, announced)
			}
			if len(whole.Nodes) != carried {
				t.Fatalf("the picture holds %d nodes, the pieces carried %d", len(whole.Nodes), carried)
			}
			if s.SyncId != id {
				t.Fatalf("a picture of series %q was completed by a piece of %q", id, s.SyncId)
			}
			asm = nil
		}
	})
}
