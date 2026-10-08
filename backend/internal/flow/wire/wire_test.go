package wire

import (
	"maps"
	"slices"
	"strings"
	"testing"

	continuumv1 "continuum/gen/continuumv1"
)

func ep(kind continuumv1.FlowEndpoint_Kind, ref, ip string) *continuumv1.FlowEndpoint {
	return &continuumv1.FlowEndpoint{Kind: kind, Ref: ref, Ip: ip}
}

func good() *continuumv1.Flow {
	return &continuumv1.Flow{Src: ep(continuumv1.FlowEndpoint_WORKLOAD, "a/Deployment/x", ""), Dst: ep(continuumv1.FlowEndpoint_EXTERNAL, "", "93.184.216.34"), Port: 443, Protocol: "tcp", Method: "ebpf"}
}

func TestCheck(t *testing.T) {
	if err := Check(good()); err != nil {
		t.Fatal(err)
	}
	node := good()
	node.Src = ep(continuumv1.FlowEndpoint_NODE, "n1", "")
	if err := Check(node); err != nil {
		t.Errorf("a node-level flow is well-formed: %v", err)
	}
	for name, mutate := range map[string]func(*continuumv1.Flow){
		"no src":           func(f *continuumv1.Flow) { f.Src = nil },
		"port 0":           func(f *continuumv1.Flow) { f.Port = 0 },
		"port 65536":       func(f *continuumv1.Flow) { f.Port = 65536 },
		"sctp":             func(f *continuumv1.Flow) { f.Protocol = "sctp" },
		"unknown method":   func(f *continuumv1.Flow) { f.Method = "x" },
		"unknown noise":    func(f *continuumv1.Flow) { f.Noise = "fun" },
		"not an address":   func(f *continuumv1.Flow) { f.Dst.Ip = "evil.example.com" },
		"non-canonical ip": func(f *continuumv1.Flow) { f.Dst.Ip = "::ffff:1.2.3.4" },
		"unresolved kind":  func(f *continuumv1.Flow) { f.Dst = ep(continuumv1.FlowEndpoint_UNRESOLVED, "", "1.2.3.4") },
		"two outside ends": func(f *continuumv1.Flow) { f.Src = ep(continuumv1.FlowEndpoint_EXTERNAL, "", "1.2.3.4") },
		"empty ref":        func(f *continuumv1.Flow) { f.Src.Ref = "" },
		"long ref":         func(f *continuumv1.Flow) { f.Src.Ref = strings.Repeat("r", MaxRef+1) },
		"long pod name":    func(f *continuumv1.Flow) { f.DstPod = strings.Repeat("p", MaxName+1) },
	} {
		f := good()
		mutate(f)
		if Check(f) == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if CheckKey(&continuumv1.Flow{Src: good().Src, Dst: good().Dst, Port: 1, Protocol: "udp"}) != nil {
		t.Error("a stored key has no method to check")
	}
}

func TestClipCutsFreeText(t *testing.T) {
	f := good()
	f.SniHost, f.Iface = strings.Repeat("é", 300), strings.Repeat("i", 100)
	for range 20 {
		f.DnsQueryNames = append(f.DnsQueryNames, strings.Repeat("d", 300))
	}
	Clip(f)
	if len(f.SniHost) > MaxName || len(f.Iface) != MaxIface || len(f.DnsQueryNames) != MaxDNSNames || len(f.DnsQueryNames[0]) != MaxName || Check(f) != nil {
		t.Errorf("sni %d iface %d names %d/%d", len(f.SniHost), len(f.Iface), len(f.DnsQueryNames), len(f.DnsQueryNames[0]))
	}
}

func TestMergeGaugesReplacesReadingsAndKeepsWhatIsMissing(t *testing.T) {
	zero, five := uint32(0), uint32(5)
	cur := good()
	cur.Iface, cur.RttUs, cur.Method, cur.Noise, cur.RcvWndBytes = "eth0", 100, "conntrack", "dns", &five
	add := &continuumv1.Flow{Method: "ebpf", RcvWndBytes: &zero, SniHost: "a.example", BytesKnown: true}
	MergeGauges(cur, add)
	if cur.Iface != "eth0" || cur.RttUs != 100 {
		t.Errorf("an absent reading replaced a real one: %v", cur)
	}
	if cur.Method != "ebpf" || cur.Noise != "" || !cur.BytesKnown || cur.SniHost != "a.example" {
		t.Errorf("method %q noise %q bytesKnown %v sni %q", cur.Method, cur.Noise, cur.BytesKnown, cur.SniHost)
	}
	if cur.RcvWndBytes == nil || *cur.RcvWndBytes != 0 {
		t.Error("a present 0 is a real sample and must replace the last one")
	}
	MergeGauges(cur, &continuumv1.Flow{Iface: "eth1", RttUs: 7, Method: "conntrack"})
	if cur.Iface != "eth1" || cur.RttUs != 7 || cur.Method != "ebpf" {
		t.Errorf("the latest sample replaces the last, and ebpf is not downgraded: %v", cur)
	}
}

func TestMergeDNSNames(t *testing.T) {
	got := MergeDNSNames([]string{"a"}, []string{"q1", "", "a", "q2"})
	if !slices.Equal(got, []string{"q2", "q1", "a"}) {
		t.Errorf("= %v, want the newest distinct first, empty names and repeats skipped", got)
	}
	var many []string
	for i := range 20 {
		many = append(many, strings.Repeat("n", i+1))
	}
	if got := MergeDNSNames(nil, many); len(got) != MaxDNSNames {
		t.Errorf("kept %d names", len(got))
	}
}

func TestOldestPicksTheSmallestAges(t *testing.T) {
	ages := map[string]int{"newest": 5, "oldest": 0, "middle1": 1, "middle2": 2, "middle3": 3}
	got := Oldest(2, maps.All(ages))
	slices.Sort(got)
	if !slices.Equal(got, []string{"middle1", "oldest"}) {
		t.Fatalf("Oldest(2) = %v, want exactly {oldest, middle1}", got)
	}
	if all := Oldest(len(ages), maps.All(ages)); len(all) != len(ages) {
		t.Fatalf("n covering the whole table returned %d keys of %d", len(all), len(ages))
	}
	if got := Oldest(0, maps.All(ages)); len(got) != 0 {
		t.Fatalf("Oldest(0) = %v, want none", got)
	}
	if got := Oldest(10, maps.All(ages)); len(got) != len(ages) {
		t.Fatalf("n over the table size returned %d keys", len(got))
	}
}
