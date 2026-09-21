package collector

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/agent/collect"
	"continuum/internal/flow"
)

type fake struct {
	method string
	bytes  bool
	next   [][]*continuumv1.RawFlow
	closed bool
}

func (f *fake) Method() string   { return f.method }
func (f *fake) BytesKnown() bool { return f.bytes }
func (f *fake) Close() error     { f.closed = true; return nil }
func (f *fake) Collect() ([]*continuumv1.RawFlow, uint64, error) {
	if len(f.next) == 0 {
		return nil, 0, nil
	}
	r := f.next[0]
	f.next = f.next[1:]
	return r, 0, nil
}

func TestChooseFallsBackAndSaysWhy(t *testing.T) {
	bad := func() (Source, error) { return nil, errors.New("no BTF") }
	ct := &fake{method: "conntrack"}
	good := func() (Source, error) { return ct, nil }

	s, why, err := Choose("auto", bad, good)
	if err != nil || s.Method() != "conntrack" || len(why) != 1 || why[0] != "ebpf: no BTF" {
		t.Fatalf("auto = %v %v %v", s, why, err)
	}
	if s, _, err := Choose("auto", func() (Source, error) { return &fake{method: "ebpf"}, nil }, good); err != nil || s.Method() != "ebpf" {
		t.Errorf("eBPF must win when it works: %v %v", s, err)
	}
	// Forcing a method never falls back to another one silently.
	if _, _, err := Choose("ebpf", bad, good); err == nil {
		t.Error("ebpf was forced and cannot run; that has to be an error")
	}
	if _, _, err := Choose("auto", bad, bad); err == nil {
		t.Error("nothing works: expected an error naming the reasons")
	}
	if _, _, err := Choose("dtrace", bad, good); err == nil {
		t.Error("unknown method accepted")
	}
}

func rf(local, peer string, port uint32, n uint64) *continuumv1.RawFlow {
	return &continuumv1.RawFlow{Client: true, LocalIp: local, PeerIp: peer, Port: port, Protocol: "tcp", Connections: n, BytesOut: n * 10, BytesIn: n * 20}
}

func TestRunDeliversSignedReportsAndKeepsWhatCouldNotBeDelivered(t *testing.T) {
	secret := []byte("0123456789abcdef-flow-secret")
	ix := &collect.Index{Pods: map[string]string{"10.42.0.5": "shop/web"}, Services: map[string][]string{"10.43.1.1": {"shop/db"}}}
	p := flow.NewPipeline(secret, func() *collect.Index { return ix }, nil)

	var up atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up.Load() {
			http.Error(w, "restarting", http.StatusServiceUnavailable)
			return
		}
		p.Handler().ServeHTTP(w, r)
	}))
	defer srv.Close()

	src := &fake{method: "ebpf", bytes: true, next: [][]*continuumv1.RawFlow{
		{rf("10.42.0.5", "10.43.1.1", 5432, 3)}, // window 1: the agent is down
		{rf("10.42.0.5", "10.43.1.1", 5432, 4)}, // window 2: the agent is back
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		Run(ctx, src, srv.URL, secret, "node-a", 100*time.Millisecond, func(string, ...any) {})
		close(done)
	}()
	time.Sleep(150 * time.Millisecond) // first window fails
	up.Store(true)

	deadline := time.Now().Add(3 * time.Second)
	var batch *continuumv1.FlowBatch
	for time.Now().Before(deadline) {
		if b := p.Aggregator.Flush(); b != nil && len(b.Flows) > 0 {
			batch = b
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done
	if batch == nil {
		t.Fatal("nothing arrived at the agent")
	}
	f := batch.Flows[0]
	if f.Src.Ref != "shop/web" || f.Dst.Ref != "shop/db" || f.Port != 5432 || f.Method != "ebpf" || !f.BytesKnown {
		t.Errorf("flow = %+v", f)
	}
	if f.Connections != 7 || f.BytesOut != 70 || f.BytesIn != 140 {
		t.Errorf("the window that could not be delivered was not merged into the next: connections=%d out=%d in=%d, want 7/70/140", f.Connections, f.BytesOut, f.BytesIn)
	}
}

func TestRunWithTheWrongSecretDeliversNothing(t *testing.T) {
	p := flow.NewPipeline([]byte("0123456789abcdef-right-secret"), func() *collect.Index { return &collect.Index{Pods: map[string]string{"10.42.0.5": "shop/web"}} }, nil)
	srv := httptest.NewServer(p.Handler())
	defer srv.Close()
	src := &fake{method: "conntrack", next: [][]*continuumv1.RawFlow{{rf("10.42.0.5", "8.8.8.8", 443, 1)}}}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	Run(ctx, src, srv.URL, []byte("0123456789abcdef-wrong-secret!"), "node-a", 100*time.Millisecond, func(string, ...any) {})
	if b := p.Aggregator.Flush(); b != nil && len(b.Flows) > 0 {
		t.Fatalf("a report signed with the wrong secret was accepted: %+v", b.Flows)
	}
}

func TestReportBoundsAndSaysWhatItDropped(t *testing.T) {
	var many []*continuumv1.RawFlow
	for i := 0; i < flow.MaxRawFlows+10; i++ {
		many = append(many, rf("10.42.0.5", "10.43.1.1", uint32(1+i%60000), uint64(1+i%3)))
	}
	rep := Report(&fake{method: "ebpf", bytes: true}, "n", 30*time.Second, many, 2)
	if len(rep.Flows) != flow.MaxRawFlows || rep.Lost != 12 || rep.WindowSeconds != 30 {
		t.Errorf("flows=%d lost=%d window=%d", len(rep.Flows), rep.Lost, rep.WindowSeconds)
	}
}
