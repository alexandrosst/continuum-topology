package link

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
)

func icmpFor(t *testing.T, peers ...*continuumv1.LinkPeer) *ICMP {
	t.Helper()
	p := NewPeers()
	p.Set("cl-me", peers)
	c := &ICMP{Peers: p, Dir: t.TempDir(), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	c.Defaults()
	return c
}

func TestTheConfigNamesEachPeerWithTheLabelsOfTheOtherSeries(t *testing.T) {
	skip := peer("cl-skip", "S", "10.1.0.1")
	skip.NoProbe = true
	c := icmpFor(t, peer("cl-b", "B", "10.0.0.9", "10.0.0.3", "10.0.0.5", "10.0.0.4", "10.0.0.7"), peer("cl-a", "A", "10.2.0.1"), skip)
	text, hosts := c.Config()
	want := `# Written by the link prober from the peer list; changes are overwritten.
targets:
- hosts: ["10.2.0.1"]
  interval: 2s
  protocol: udp
  size: 56
  labels: {cluster: "cl-me", peer: "cl-a"}
- hosts: ["10.0.0.3", "10.0.0.4", "10.0.0.5"]
  interval: 2s
  protocol: udp
  size: 56
  labels: {cluster: "cl-me", peer: "cl-b"}
`
	if text != want || hosts != 4 {
		t.Fatalf("config:\n%s\nhosts %d", text, hosts)
	}
	again, _ := c.Config()
	if again != text {
		t.Fatal("the same peers must give the same text, or the program restarts for nothing")
	}
}

func TestThereIsNothingToPingBeforeTheClusterKnowsItself(t *testing.T) {
	p := NewPeers()
	c := &ICMP{Peers: p}
	c.Defaults()
	if _, hosts := c.Config(); hosts != 0 {
		t.Fatalf("hosts = %d", hosts)
	}
}

const smokeSample = `# HELP smokeping_prober_build_info A metric with a constant '1' value labeled by version.
# TYPE smokeping_prober_build_info gauge
smokeping_prober_build_info{version=""} 1
# HELP smokeping_prober_errors_total The number of probers that returned an error.
# TYPE smokeping_prober_errors_total counter
smokeping_prober_errors_total 2
# HELP smokeping_requests_total Counter of pings sent.
# TYPE smokeping_requests_total counter
smokeping_requests_total{cluster="cl-me",host="10.0.0.3",ip="10.0.0.3",peer="cl-b",source="",tos="0"} 7
# HELP smokeping_response_duration_seconds A histogram of latencies for ping responses.
# TYPE smokeping_response_duration_seconds histogram
smokeping_response_duration_seconds_bucket{cluster="cl-me",host="10.0.0.3",le="0.0001",peer="cl-b"} 1
smokeping_response_duration_seconds_bucket{cluster="cl-me",host="10.0.0.3",le="+Inf",peer="cl-b"} 4
smokeping_response_duration_seconds_sum{cluster="cl-me",host="10.0.0.3",peer="cl-b"} 0.008
smokeping_response_duration_seconds_count{cluster="cl-me",host="10.0.0.3",peer="cl-b"} 4
go_goroutines 9
process_cpu_seconds_total 1
promhttp_metric_handler_requests_total{code="200"} 3
`

func TestOnlyTheLinkSeriesAreKeptAndTheyTakeOurNames(t *testing.T) {
	got := RenameICMP(smokeSample)
	for _, want := range []string{
		"# TYPE continuum_link_icmp_prober_errors_total counter\n",
		"continuum_link_icmp_prober_errors_total 2\n",
		"# HELP continuum_link_icmp_requests_total Counter of pings sent.\n",
		`continuum_link_icmp_requests_total{cluster="cl-me",host="10.0.0.3",ip="10.0.0.3",peer="cl-b",source="",tos="0"} 7` + "\n",
		"# TYPE continuum_link_icmp_response_duration_seconds histogram\n",
		`continuum_link_icmp_response_duration_seconds_bucket{cluster="cl-me",host="10.0.0.3",le="+Inf",peer="cl-b"} 4` + "\n",
		`continuum_link_icmp_response_duration_seconds_sum{cluster="cl-me",host="10.0.0.3",peer="cl-b"} 0.008` + "\n",
		`continuum_link_icmp_response_duration_seconds_count{cluster="cl-me",host="10.0.0.3",peer="cl-b"} 4` + "\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	for _, bad := range []string{"smokeping", "build_info", "go_", "process_", "promhttp"} {
		if strings.Contains(got, bad) {
			t.Errorf("%q should not be in the output:\n%s", bad, got)
		}
	}
}

type fakeProc struct{ done chan struct{} }

func fakeSpawn(starts *atomic.Int32, last *[]string, procs *[]*fakeProc) func(context.Context, string, []string, *slog.Logger) (*Child, error) {
	return func(_ context.Context, _ string, args []string, _ *slog.Logger) (*Child, error) {
		starts.Add(1)
		*last = args
		fp := &fakeProc{done: make(chan struct{})}
		*procs = append(*procs, fp)
		return &Child{Done: fp.done, Stop: func() {
			select {
			case <-fp.done:
			default:
				close(fp.done)
			}
		}}, nil
	}
}

func TestTheProgramFollowsThePeerList(t *testing.T) {
	var starts atomic.Int32
	var args []string
	var procs []*fakeProc
	c := icmpFor(t, peer("cl-a", "A", "10.0.0.1"))
	c.Spawn = fakeSpawn(&starts, &args, &procs)
	ctx := context.Background()

	c.reconcile(ctx)
	if starts.Load() != 1 || len(args) != 3 || !strings.HasPrefix(args[0], "--config.file=") {
		t.Fatalf("should have started once: %d %v", starts.Load(), args)
	}
	b, err := os.ReadFile(strings.TrimPrefix(args[0], "--config.file="))
	if err != nil || !strings.Contains(string(b), `"10.0.0.1"`) {
		t.Fatalf("config not written: %v %s", err, b)
	}

	c.reconcile(ctx)
	if starts.Load() != 1 {
		t.Fatal("nothing changed: nothing should restart")
	}

	// a new peer: restarted with both
	c.Peers.Set("cl-me", []*continuumv1.LinkPeer{peer("cl-a", "A", "10.0.0.1"), peer("cl-b", "B", "10.0.0.2")})
	c.reconcile(ctx)
	if starts.Load() != 2 || !isClosed(procs[0].done) {
		t.Fatalf("a changed list should replace the program: %d", starts.Load())
	}
	b, _ = os.ReadFile(strings.TrimPrefix(args[0], "--config.file="))
	if !strings.Contains(string(b), `"10.0.0.2"`) {
		t.Fatalf("new config not written: %s", b)
	}

	// a program that died is started again, but not at once and again if it keeps dying
	close(procs[1].done)
	c.reconcile(ctx)
	if starts.Load() != 2 {
		t.Fatal("restarting a program that just died within ten seconds is too eager")
	}
	c.mu.Lock()
	c.started = time.Now().Add(-time.Minute)
	c.mu.Unlock()
	c.reconcile(ctx)
	if starts.Load() != 3 {
		t.Fatalf("a dead program should be started again: %d", starts.Load())
	}

	// no peers left to ping: the program is stopped and not started
	c.Peers.Set("cl-me", nil)
	c.reconcile(ctx)
	if starts.Load() != 3 || !isClosed(procs[2].done) {
		t.Fatalf("with nothing to ping the program should be stopped: %d", starts.Load())
	}
}

func isClosed(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func TestTheScrapeCarriesTheProgramsSeriesOrSaysItIsNotRunning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, smokeSample) }))
	defer srv.Close()
	c := icmpFor(t, peer("cl-a", "A", "10.0.0.1"))
	c.Listen = strings.TrimPrefix(srv.URL, "http://")
	m := NewMeter(c.Peers)
	m.OnlyProbes()
	m.AddSource(c.WriteMetrics)
	got := render(m)
	if got[`continuum_link_icmp_running{cluster="cl-me"}`] != "1" {
		t.Fatalf("running: %v", got)
	}
	if got[`continuum_link_icmp_response_duration_seconds_count{cluster="cl-me",host="10.0.0.3",peer="cl-b"}`] != "4" {
		t.Fatalf("series not carried: %v", got)
	}
	srv.Close()
	got = render(m)
	if got[`continuum_link_icmp_running{cluster="cl-me"}`] != "0" {
		t.Fatalf("a program that does not answer is not running: %v", got)
	}
	for k := range got {
		if strings.Contains(k, "response_duration") {
			t.Fatalf("stale series must not be carried: %s", k)
		}
	}
	// nothing to ping: nothing written at all
	c.Peers.Set("cl-me", nil)
	for k := range render(m) {
		if strings.Contains(k, "icmp") {
			t.Fatalf("a cluster with nothing to ping has no ICMP series: %s", k)
		}
	}
}

func TestICMPSettingsAreChecked(t *testing.T) {
	bad := []*ICMP{{Dir: "", Interval: 2 * time.Second, Size: 56, HostsPerPeer: 3, Check: 30 * time.Second},
		{Dir: "/x", Interval: time.Millisecond, Size: 56, HostsPerPeer: 3, Check: 30 * time.Second},
		{Dir: "/x", Interval: 2 * time.Second, Size: 10, HostsPerPeer: 3, Check: 30 * time.Second},
		{Dir: "/x", Interval: 2 * time.Second, Size: 56, HostsPerPeer: 99, Check: 30 * time.Second},
		{Dir: "/x", Interval: 2 * time.Second, Size: 56, HostsPerPeer: 3, Check: time.Second}}
	for i, c := range bad {
		if c.Validate() == nil {
			t.Errorf("case %d should be refused", i)
		}
	}
	ok := ICMP{Dir: "/x"}
	ok.Defaults()
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
}

// TestWithTheRealProgram runs the real smokeping_prober against the loopback address. Set CONTINUUM_TEST_SMOKEPING to the program;
// the host must allow unprivileged ping sockets (net.ipv4.ping_group_range).
func TestWithTheRealProgram(t *testing.T) {
	bin := os.Getenv("CONTINUUM_TEST_SMOKEPING")
	if bin == "" {
		t.Skip("CONTINUUM_TEST_SMOKEPING not set")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listen := ln.Addr().String()
	ln.Close()
	p := NewPeers()
	p.cur.Store(&peerSet{own: "cl-me", peers: []Peer{{ID: "cl-a", Addresses: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}}, byIP: map[netip.Addr]string{}})
	c := &ICMP{Peers: p, Bin: bin, Dir: t.TempDir(), Listen: listen, Interval: 500 * time.Millisecond, Log: slog.New(slog.NewTextHandler(os.Stderr, nil))}
	c.Defaults()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c.reconcile(ctx)
	defer c.stop()
	m := NewMeter(p)
	m.OnlyProbes()
	m.AddSource(c.WriteMetrics)
	var got map[string]string
	deadline := time.Now().Add(15 * time.Second)
	key := `continuum_link_icmp_response_duration_seconds_count{cluster="cl-me",host="127.0.0.1",ip="127.0.0.1",peer="cl-a",source="",tos="0"}`
	for time.Now().Before(deadline) {
		got = render(m)
		if got[key] != "" && got[key] != "0" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if got[key] == "" || got[key] == "0" {
		t.Fatalf("no ping was answered: %v", got)
	}
	if got[`continuum_link_icmp_running{cluster="cl-me"}`] != "1" {
		t.Fatalf("running: %v", got)
	}
	if got[`continuum_link_icmp_prober_errors_total`] != "0" {
		t.Fatalf("the program reported errors: %v", got)
	}
}
