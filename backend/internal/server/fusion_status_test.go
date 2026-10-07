package server

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"continuum/internal/fusionapi"
)

// cachedFusion is a FusionControl with the status cache on and a clock the test moves, over a fake cluster with all four
// workloads switched on.
func cachedFusion(t *testing.T) (*FusionControl, *fakeKube, *time.Time) {
	t.Helper()
	a := newAdminRig(t)
	f, k := newFusion(t, a)
	f.StatusTTL = 0 // the default
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	f.Now = func() time.Time { return now }
	for n := range k.replicas {
		k.replicas[n] = 1
	}
	return f, k, &now
}

func (k *fakeKube) workloadReads() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.reads
}

// However many ask at once, the cluster is read once; a later ask inside the window costs nothing; one after it reads again.
func TestStatusIsReadOnceForEveryoneAsking(t *testing.T) {
	f, k, now := cachedFusion(t)
	k.slow = 40 * time.Millisecond
	ctx := context.Background()

	var wg sync.WaitGroup
	states := make([]string, 20)
	for i := range states {
		wg.Add(1)
		go func() {
			defer wg.Done()
			states[i] = f.Status(ctx).State
		}()
	}
	wg.Wait()
	// Five workloads are looked at in one read (the four, and Grafana, which this install does not have).
	if got := k.workloadReads(); got != 5 {
		t.Fatalf("20 simultaneous callers made %d workload reads, want one read of 5", got)
	}
	for _, s := range states {
		if s != "starting" {
			t.Fatalf("a caller got %q", s)
		}
	}
	f.Status(ctx)
	*now = now.Add(2 * time.Second)
	f.Status(ctx)
	if got := k.workloadReads(); got != 5 {
		t.Fatalf("asking again inside the window read the cluster again (%d reads)", got)
	}
	*now = now.Add(time.Second) // 3 s since the read: past the 2.5 s window
	k.allReady()
	if st := f.Status(ctx); st.State != "running" {
		t.Fatalf("after the window the new state is seen: %+v", st)
	}
	if got := k.workloadReads(); got != 10 {
		t.Fatalf("%d reads after the window, want 10", got)
	}
}

// What a caller is handed is its own: changing it must not change what the next caller is given.
func TestStatusHandsOutCopies(t *testing.T) {
	f, _, _ := cachedFusion(t)
	st := f.Status(context.Background())
	st.Components[0].Label = "changed"
	st.State = "changed"
	if again := f.Status(context.Background()); again.State == "changed" || again.Components[0].Label == "changed" {
		t.Fatalf("a caller changed the kept status: %+v", again)
	}
}

// The four (five) workloads are read at once, each under its own limit, and the read belongs to everyone waiting for it, not
// to the request that began it: if that one goes away the others still get their answer.
func TestStatusReadsConcurrentlyAndSurvivesTheCallerLeaving(t *testing.T) {
	f, k, _ := cachedFusion(t)
	k.slow = 120 * time.Millisecond
	start := time.Now()
	f.Status(context.Background())
	// Five reads of 120 ms one after another would take 600 ms.
	if took := time.Since(start); took > 400*time.Millisecond {
		t.Errorf("the workloads were read one by one: %v", took)
	}

	f2, k2, _ := cachedFusion(t)
	k2.slow = 80 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	leader, follower := make(chan FusionStatus, 1), make(chan FusionStatus, 1)
	go func() { leader <- f2.Status(ctx) }()
	time.Sleep(10 * time.Millisecond)
	go func() { follower <- f2.Status(context.Background()) }() // a second caller, waiting on the first one's read
	time.Sleep(10 * time.Millisecond)
	cancel() // the request that began the read is gone before it finishes
	if st := <-leader; st.State != "starting" || len(st.Components) != 4 {
		t.Fatalf("the caller that went away still gets the read it began: %+v", st)
	}
	if st := <-follower; st.State != "starting" || len(st.Components) != 4 {
		t.Fatalf("the caller waiting on it: %+v", st)
	}
}

// A change made from here replaces what is kept: the answer after Enable is the new state, worked out from what was
// asked for, with no read of the cluster in between; and a read that was under way when the change began is not kept.
func TestEnableAndDisableAnswerWithoutReadingAgain(t *testing.T) {
	a := newAdminRig(t)
	f, k := newFusion(t, a)
	f.StatusTTL = 0
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	f.Now = func() time.Time { return now }
	ctx := context.Background()

	st, err := f.Enable(ctx, a.a.C, "alex")
	if err != nil {
		t.Fatal(err)
	}
	reads := k.workloadReads()
	if reads != 5 {
		t.Fatalf("Enable read the workloads %d times; once before it changes anything is all it needs", reads)
	}
	if st.State != "starting" || st.Since == nil || len(st.Components) != 4 || st.Components[0].Desired != 1 || st.Components[0].Ready != 0 {
		t.Fatalf("after Enable: %+v", st)
	}
	if again := f.Status(ctx); again.State != "starting" || k.workloadReads() != reads {
		t.Fatalf("the screen's next read after Enable went to the cluster (%d reads) or saw %q", k.workloadReads(), again.State)
	}

	k.allReady()
	now = now.Add(time.Minute)
	if st := f.Status(ctx); st.State != "running" {
		t.Fatalf("a minute later: %+v", st)
	}
	st, err = f.Disable(ctx, a.a.C, "alex")
	if err != nil || st.State != "off" || st.Since != nil || st.Components[0].Desired != 0 {
		t.Fatalf("after Disable: %+v %v", st, err)
	}
	reads = k.workloadReads()
	if f.Status(ctx).State != "off" || k.workloadReads() != reads {
		t.Fatal("the read after Disable went to the cluster")
	}

	// Enabling something that is already running keeps what is ready: it must not flap to "starting".
	k.allReady()
	f.invalidate()
	f.Enable(ctx, a.a.C, "alex")
	k.allReady()
	now = now.Add(time.Minute)
	if st := f.Status(ctx); st.State != "running" {
		t.Fatalf("running: %+v", st)
	}
	st, err = f.Enable(ctx, a.a.C, "alex")
	if err != nil || st.State != "running" {
		t.Fatalf("a second Enable on a running FUSION: %+v %v", st, err)
	}
}

func TestAReadThatBeganBeforeAChangeIsNotKept(t *testing.T) {
	f, k, _ := cachedFusion(t)
	k.slow = 60 * time.Millisecond
	done := make(chan struct{})
	go func() { f.Status(context.Background()); close(done) }() // reads "on"
	time.Sleep(15 * time.Millisecond)
	f.invalidate() // the cluster is being changed
	<-done
	f.cacheMu.Lock()
	valid := f.cache.valid
	f.cacheMu.Unlock()
	if valid {
		t.Fatal("a read that began before the change was kept")
	}
}

// A change made from here disowns the read under way, not only its result: whoever asks after the change must get a read
// that began after it, not be handed the old state by joining the one in progress.
func TestSomeoneAskingAfterAChangeDoesNotJoinTheReadBeforeIt(t *testing.T) {
	f, k, _ := cachedFusion(t)
	k.slow = 60 * time.Millisecond
	done := make(chan struct{})
	go func() { f.Status(context.Background()); close(done) }()
	time.Sleep(15 * time.Millisecond)
	f.invalidate() // Enable / Disable began
	f.Status(context.Background())
	<-done
	// One read of the five workloads for the first caller, and a read of its own for the second.
	if got := k.workloadReads(); got != 10 {
		t.Fatalf("%d workload reads: the caller after the change joined the earlier read (want 10)", got)
	}
}

// A read that dies (a panic in the request's goroutine is recovered by the server) must not leave its flight open: the next
// caller would otherwise wait for a read nobody is making, for ever.
func TestAReadThatPanicsDoesNotStrandTheNextCaller(t *testing.T) {
	f, k, _ := cachedFusion(t)
	var boom atomic.Bool
	f.Now = func() time.Time {
		if boom.Load() {
			panic("clock failure")
		}
		return time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	}
	k.onWorkload = func() { boom.Store(true) } // the workloads are read, then the state is worked out and the clock is consulted
	func() {
		defer func() { _ = recover() }()
		f.Status(context.Background())
	}()
	boom.Store(false)
	k.onWorkload = nil
	got := make(chan FusionStatus, 1)
	go func() { got <- f.Status(context.Background()) }()
	select {
	case st := <-got:
		if st.State != "starting" {
			t.Fatalf("after the panic: %+v", st)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the next caller is waiting on a read that died")
	}
}

// A failed scale leaves the cluster in a state nobody knows, so nothing is kept and the next ask reads it.
func TestAFailedScaleKeepsNothing(t *testing.T) {
	a := newAdminRig(t)
	f, k := newFusion(t, a)
	f.StatusTTL = 0
	ctx := context.Background()
	f.Status(ctx)
	k.mu.Lock()
	delete(k.replicas, "continuum-fusion-central") // the gateway cannot be scaled: the stores before it were
	k.mu.Unlock()
	if _, err := f.Enable(ctx, a.a.C, "alex"); err == nil {
		t.Fatal("enabled with a workload missing")
	}
	f.cacheMu.Lock()
	valid := f.cache.valid
	f.cacheMu.Unlock()
	if valid {
		t.Fatal("a status was kept after a scale that failed part way")
	}
}

// A FUSION access token's status call is answered from what the last read found; the cluster is only asked when that is
// a minute old.
func TestTokenStatusIsServedFromTheCache(t *testing.T) {
	f, k, now := cachedFusion(t)
	ctx := context.Background()
	f.Status(ctx)
	reads := k.workloadReads()
	*now = now.Add(30 * time.Second) // well past what an administrator would accept, within what a token does
	if st := f.TokenStatus(ctx); st.State != "starting" {
		t.Fatalf("%+v", st)
	}
	if k.workloadReads() != reads {
		t.Fatal("a token's status call read the cluster though the last read was 30 s old")
	}
	k.allReady()
	*now = now.Add(31 * time.Second)
	if st := f.TokenStatus(ctx); st.State != "running" || k.workloadReads() == reads {
		t.Fatalf("a minute on, the token sees the cluster as it is: %+v", st)
	}
}

func TestTheTokenStatusRouteDoesNotReadTheCluster(t *testing.T) {
	d := newDataRig(t)
	k := d.a.Fusion.Kube.(*fakeKube)
	d.a.Fusion.StatusTTL = 0
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	d.a.Fusion.Now = func() time.Time { return now }
	secret, _ := d.mint(t, map[string]any{"name": "engine"})
	if r := d.get("/api/v1/fusion/status", withCookie(d.admin)); r.Code != 200 { // an administrator warms it
		t.Fatalf("%d", r.Code)
	}
	reads := k.workloadReads()
	now = now.Add(20 * time.Second)
	r := d.get("/api/v1/fusion/status", bearer(secret))
	if r.Code != 200 || k.workloadReads() != reads {
		t.Fatalf("token status %d, %d new reads", r.Code, k.workloadReads()-reads)
	}
	if fusion, _ := r.json(t)["fusion"].(map[string]any); fusion["state"] != "off" || fusion["components"] != nil || fusion["message"] != nil {
		t.Errorf("a token learns the state and nothing else: %v", fusion)
	}
}

// ---- why a part is not ready ----

func TestExplainPod(t *testing.T) {
	for name, tc := range map[string]struct {
		pod    KubePod
		claim  string
		reason string
		hard   bool
	}{
		"image pull":         {KubePod{WaitingReason: "ImagePullBackOff"}, "", "Cannot download the image", true},
		"first image pull":   {KubePod{WaitingReason: "ErrImagePull"}, "", "Cannot download the image", true},
		"bad image":          {KubePod{WaitingReason: "InvalidImageName"}, "", "The image name is not valid", true},
		"crash loop":         {KubePod{WaitingReason: "CrashLoopBackOff", LastTerminatedReason: "Error"}, "", "Keeps stopping and restarting", true},
		"out of memory":      {KubePod{WaitingReason: "CrashLoopBackOff", LastTerminatedReason: "OOMKilled"}, "", "Ran out of memory and keeps restarting", true},
		"bad config":         {KubePod{WaitingReason: "CreateContainerConfigError"}, "", "Its configuration is incomplete", true},
		"cannot start":       {KubePod{WaitingReason: "RunContainerError"}, "", "The container could not be started", true},
		"creating":           {KubePod{WaitingReason: "ContainerCreating"}, "Bound", "Starting the container", false},
		"creating, no claim": {KubePod{WaitingReason: "ContainerCreating"}, "", "Starting the container", false},
		"creating, volume":   {KubePod{WaitingReason: "ContainerCreating"}, "Pending", "Waiting for a volume", true},
		"init":               {KubePod{WaitingReason: "PodInitializing"}, "", "Starting up", false},
		"no room":            {KubePod{Phase: "Pending", Unschedulable: true, ScheduleMessage: "0/3 nodes are available: 3 Insufficient memory."}, "", "No node has room", true},
		"taints":             {KubePod{Phase: "Pending", Unschedulable: true, ScheduleMessage: "0/3 nodes are available: 3 node(s) had untolerated taint."}, "", "No suitable node", true},
		"unbound claim":      {KubePod{Phase: "Pending", Unschedulable: true, ScheduleMessage: "0/3 nodes are available: pod has unbound immediate PersistentVolumeClaims."}, "", "Waiting for a volume", true},
		"claim pending":      {KubePod{Phase: "Pending", Unschedulable: true, ScheduleMessage: "0/3 nodes are available: 3 Insufficient cpu."}, "Pending", "Waiting for a volume", true},
		"pending, volume":    {KubePod{Phase: "Pending"}, "Pending", "Waiting for a volume", true},
		"unknown code":       {KubePod{WaitingReason: "SomethingNew"}, "", "", false},
		"nothing wrong":      {KubePod{Phase: "Running"}, "Bound", "", false},
	} {
		if reason, hard := explainPod(tc.pod, tc.claim); reason != tc.reason || hard != tc.hard {
			t.Errorf("%s: %q hard=%v, want %q hard=%v", name, reason, hard, tc.reason, tc.hard)
		}
	}
}

// Only a part that is not ready is looked into, with the pod and claim the chart names; a part that is ready costs no
// extra call.
func TestStatusNamesWhyAPartIsNotReady(t *testing.T) {
	f, k, now := cachedFusion(t)
	f.StatusTTL = -1
	k.ready["continuum-fusion-loki"], k.ready["continuum-fusion-tempo"], k.ready["continuum-fusion-central"] = 1, 1, 1
	k.pods["continuum-fusion-prometheus-0"] = KubePod{Phase: "Pending", WaitingReason: "ContainerCreating"}
	k.claims["data-continuum-fusion-prometheus-0"] = "Pending"
	st := f.Status(context.Background())
	if st.State != "starting" {
		t.Fatalf("%+v", st)
	}
	for _, c := range st.Components {
		want := ""
		if c.Component == "metrics" {
			want = "Waiting for a volume"
		}
		if c.Reason != want {
			t.Errorf("%s reason = %q, want %q", c.Component, c.Reason, want)
		}
	}
	if k.podReads != 1 || k.claimReads != 1 {
		t.Errorf("%d pod and %d claim reads for one part that is not ready", k.podReads, k.claimReads)
	}
	// The reason goes out with the status.
	b, _ := json.Marshal(st)
	if !strings.Contains(string(b), `"reason":"Waiting for a volume"`) {
		t.Errorf("json = %s", b)
	}
	// Once it is up, no reason and no reads.
	*now = now.Add(time.Minute)
	k.allReady()
	k.podReads, k.claimReads = 0, 0
	st = f.Status(context.Background())
	if st.Components[0].Reason != "" || k.podReads != 0 {
		t.Errorf("a part that is ready was looked into: %+v (%d pod reads)", st.Components, k.podReads)
	}
}

// A Role from before the pod grants existed: nothing is lost but the reason.
func TestWhyIsLeftOutWhenThePodCannotBeRead(t *testing.T) {
	for name, err := range map[string]error{"forbidden": ErrKubeForbidden, "a failing API": errors.New("connection reset")} {
		f, k, _ := cachedFusion(t)
		f.StatusTTL = -1
		k.podErr = err
		st := f.Status(context.Background())
		if !st.Available || st.State != "starting" || len(st.Components) != 4 {
			t.Fatalf("%s: %+v", name, st)
		}
		for _, c := range st.Components {
			if c.Reason != "" {
				t.Errorf("%s: %s has reason %q", name, c.Component, c.Reason)
			}
		}
	}
	// A pod that does not exist yet (the StatefulSet has not made it) is the same.
	f, _, _ := cachedFusion(t)
	f.StatusTTL = -1
	if st := f.Status(context.Background()); st.Components[0].Reason != "" {
		t.Errorf("reason for a pod that is not there: %+v", st.Components[0])
	}
}

// A fault that will not clear by itself is called out after 90 seconds; a part that is merely slow gets the full five
// minutes.
func TestAHardReasonShortensTheGrace(t *testing.T) {
	f, k, now := cachedFusion(t)
	f.StatusTTL = -1
	k.pods["continuum-fusion-loki-0"] = KubePod{Phase: "Pending", WaitingReason: "ImagePullBackOff"}
	ctx := context.Background()
	if st := f.Status(ctx); st.State != "starting" {
		t.Fatalf("at the start: %+v", st)
	}
	*now = now.Add(80 * time.Second)
	if st := f.Status(ctx); st.State != "starting" {
		t.Fatalf("after 80 s: %+v", st)
	}
	*now = now.Add(20 * time.Second)
	st := f.Status(ctx)
	if st.State != "attention" || !strings.Contains(st.Message, "Loki: cannot download the image") || !strings.Contains(st.Message, "after 100 seconds") || !strings.Contains(st.Message, "0 of 4") {
		t.Fatalf("after 100 s with an image that cannot be pulled: %+v", st)
	}

	// The same wait with a part that is only being created, or one whose reason is unknown, is still just starting.
	for _, pod := range []KubePod{{Phase: "Pending", WaitingReason: "ContainerCreating"}, {Phase: "Pending", WaitingReason: "SomethingNew"}, {}} {
		f2, k2, now2 := cachedFusion(t)
		f2.StatusTTL = -1
		k2.pods["continuum-fusion-loki-0"] = pod
		f2.Status(ctx)
		*now2 = now2.Add(100 * time.Second)
		if st := f2.Status(ctx); st.State != "starting" {
			t.Errorf("%+v after 100 s: %+v", pod, st)
		}
		*now2 = now2.Add(5 * time.Minute)
		if st := f2.Status(ctx); st.State != "attention" {
			t.Errorf("%+v after 6 minutes: %+v", pod, st)
		}
	}
}

// ---- last data ----

type fakeProm struct {
	*httptest.Server
	queries atomic.Int32
	value   atomic.Value // string: the vector's value, "" = empty answer, "fail" = a 500
}

func newFakeProm(t *testing.T) *fakeProm {
	p := &fakeProm{}
	p.value.Store("")
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// FUSION asks Prometheus for its TSDB status, not for a query over series (see HeadMaxTime).
		if r.URL.Path != "/api/v1/status/tsdb" {
			http.NotFound(w, r)
			return
		}
		p.queries.Add(1)
		head := map[string]any{"numSeries": 0, "minTime": int64(math.MaxInt64), "maxTime": int64(math.MinInt64)} // an empty head
		switch v := p.value.Load().(string); v {
		case "fail":
			http.Error(w, "boom", 500)
			return
		case "":
		default:
			secs, err := strconv.ParseFloat(v, 64)
			if err != nil {
				t.Errorf("bad fake value %q", v)
			}
			head = map[string]any{"numSeries": 89, "minTime": int64(1), "maxTime": int64(secs * 1000)}
		}
		jsonOut(w, map[string]any{"status": "success", "data": map[string]any{"headStats": head}})
	}))
	t.Cleanup(p.Close)
	return p
}

func TestStatusCarriesTheTimeOfTheLastData(t *testing.T) {
	f, k, now := cachedFusion(t)
	f.StatusTTL = -1
	prom := newFakeProm(t)
	f.Data = &fusionapi.Client{Prometheus: prom.URL}
	ctx := context.Background()

	// Prometheus is not up yet: it is not asked.
	if st := f.Status(ctx); st.LastDataAt != nil || prom.queries.Load() != 0 {
		t.Fatalf("asked Prometheus before it was up: %+v (%d queries)", st.LastDataAt, prom.queries.Load())
	}
	k.allReady()
	st := f.Status(ctx)
	if st.State != "running" || st.LastDataAt != nil || prom.queries.Load() != 1 {
		t.Fatalf("running with nothing received: %+v (%d queries)", st, prom.queries.Load())
	}
	b, _ := json.Marshal(st)
	if strings.Contains(string(b), "lastDataAt") {
		t.Errorf("an unknown last-data time is left out: %s", b)
	}

	// Then there is some. It is asked again only after 15 s, however often the status is read.
	prom.value.Store("1790000000.5")
	for range 5 {
		f.Status(ctx)
	}
	if prom.queries.Load() != 1 {
		t.Fatalf("%d queries inside 15 s", prom.queries.Load())
	}
	*now = now.Add(16 * time.Second)
	st = f.Status(ctx)
	if prom.queries.Load() != 2 || st.LastDataAt == nil || !st.LastDataAt.Equal(time.Unix(1790000000, 500000000).UTC()) {
		t.Fatalf("after 16 s: %v (%d queries)", st.LastDataAt, prom.queries.Load())
	}

	// A failing Prometheus, or data older than its lookback, leaves what was known; it never turns into an error.
	for _, v := range []string{"fail", ""} {
		prom.value.Store(v)
		*now = now.Add(16 * time.Second)
		if st := f.Status(ctx); st.State != "running" || st.LastDataAt == nil || st.LastDataAt.Unix() != 1790000000 {
			t.Errorf("answer %q: %+v", v, st)
		}
	}
	// A server that never saw any, whose Prometheus fails, simply has none.
	f2, k2, _ := cachedFusion(t)
	f2.StatusTTL = -1
	k2.allReady()
	prom.value.Store("fail")
	f2.Data = &fusionapi.Client{Prometheus: prom.URL}
	if st := f2.Status(ctx); st.State != "running" || st.LastDataAt != nil {
		t.Errorf("failing Prometheus, nothing known: %+v", st)
	}
}

// ---- the gateway's address ----

func enabledCentral(t *testing.T) (*adminRig, *FusionControl, *fakeKube, *time.Time) {
	t.Helper()
	a := newAdminRig(t)
	f, k := newFusion(t, a)
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	f.Now = func() time.Time { return now }
	if _, err := f.Enable(context.Background(), a.a.C, "alex"); err != nil {
		t.Fatal(err)
	}
	return a, f, k, &now
}

func TestTheGatewaysAddressIsRecordedWhenNoneIs(t *testing.T) {
	a, f, k, now := enabledCentral(t)
	ctx := context.Background()

	// The cloud has not given the load balancer an address yet: nothing to record, and it is asked again later, not at once.
	if addr, err := f.DiscoverAddress(ctx, a.a.C); addr != "" || err != nil || f.Exposed() {
		t.Fatalf("no address yet: %q %v", addr, err)
	}
	k.svcAddr = "203.0.113.7:4317"
	if addr, _ := f.DiscoverAddress(ctx, a.a.C); addr != "" || k.svcReads != 1 {
		t.Fatalf("asked again at once: %q (%d reads)", addr, k.svcReads)
	}
	*now = now.Add(31 * time.Second)
	addr, err := f.DiscoverAddress(ctx, a.a.C)
	if err != nil || addr != "203.0.113.7:4317" {
		t.Fatalf("recorded %q: %v", addr, err)
	}
	// Recorded where the screens read it, and in use at once.
	op, _ := a.a.C.GetOperator(ctx, CentralOperatorID)
	if op.Address != "203.0.113.7:4317" || !f.Exposed() || f.CentralEndpoint() != "203.0.113.7:4317" {
		t.Fatalf("operator address %q, exposed %v, endpoint %q", op.Address, f.Exposed(), f.CentralEndpoint())
	}
	// Audited as the server's own doing.
	entries, _ := a.a.C.Store.AuditSince(ctx, 0, 500)
	found := false
	for _, e := range entries {
		found = found || (e.Action == "operator-address-changed" && e.Actor == "system" && e.Detail == "203.0.113.7:4317")
	}
	if !found {
		t.Errorf("no audit entry for the recorded address: %+v", entries)
	}
	// With one recorded the Service is not looked at again.
	reads := k.svcReads
	*now = now.Add(time.Hour)
	if addr, _ := f.DiscoverAddress(ctx, a.a.C); addr != "" || k.svcReads != reads {
		t.Errorf("looked again with an address recorded: %q (%d reads)", addr, k.svcReads)
	}
}

// What an administrator typed outranks the Service, always: it is learned, never replaced.
func TestAManuallyRecordedAddressIsNeverReplaced(t *testing.T) {
	a, f, k, _ := enabledCentral(t)
	ctx := context.Background()
	if _, err := a.a.C.SetOperatorAddress(ctx, "alex", CentralOperatorID, "fusion.example.com:4317"); err != nil {
		t.Fatal(err)
	}
	k.svcAddr = "203.0.113.7:4317"
	// This server has not heard of it (another replica, or it was typed before this one started): it learns it.
	if addr, err := f.DiscoverAddress(ctx, a.a.C); addr != "" || err != nil {
		t.Fatalf("%q %v", addr, err)
	}
	if op, _ := a.a.C.GetOperator(ctx, CentralOperatorID); op.Address != "fusion.example.com:4317" {
		t.Fatalf("the manual address was replaced by %q", op.Address)
	}
	if f.CentralEndpoint() != "fusion.example.com:4317" {
		t.Errorf("endpoint %q", f.CentralEndpoint())
	}
	if k.svcReads != 0 {
		t.Errorf("the Service was read though an address was recorded")
	}

	// A value from the server's start-up flag is one too.
	b, f2, k2, _ := enabledCentral(t)
	f2.PublicAddress = "flag.example.com:4317"
	k2.svcAddr = "203.0.113.7:4317"
	f2.DiscoverAddress(ctx, b.a.C)
	if op, _ := b.a.C.GetOperator(ctx, CentralOperatorID); op.Address != "" || f2.CentralEndpoint() != "flag.example.com:4317" || k2.svcReads != 0 {
		t.Errorf("the flag's address was overridden: operator %q, endpoint %q", op.Address, f2.CentralEndpoint())
	}
}

// The administrator types an address while the server is waiting for the cluster to answer: between the server's look at
// the operator (no address) and its write. The person's address must survive, in the database and in what the server
// dials, and the audit trail must not say the server's address was recorded.
func TestAnAddressTypedWhileTheServerIsAskingTheClusterIsKept(t *testing.T) {
	a, f, k, _ := enabledCentral(t)
	ctx := context.Background()
	k.svcAddr = "203.0.113.7:4317"
	k.onService = func() {
		// What the "Reachable at" handler does: record it, then tell the running switch.
		if _, err := a.a.C.SetOperatorAddress(ctx, "alex", CentralOperatorID, "fusion.example.com:4317"); err != nil {
			t.Error(err)
		}
		f.SetPublicAddress("fusion.example.com:4317")
	}
	if addr, err := f.DiscoverAddress(ctx, a.a.C); addr != "" || err != nil {
		t.Fatalf("the server reported recording %q (%v) over a person's address", addr, err)
	}
	if op, _ := a.a.C.GetOperator(ctx, CentralOperatorID); op.Address != "fusion.example.com:4317" {
		t.Fatalf("the person's address was overwritten by %q", op.Address)
	}
	if f.CentralEndpoint() != "fusion.example.com:4317" {
		t.Errorf("the server dials %q", f.CentralEndpoint())
	}
	entries, _ := a.a.C.Store.AuditSince(ctx, 0, 500)
	for _, e := range entries {
		if e.Action == "operator-address-changed" && e.Actor == "system" && e.Detail == "203.0.113.7:4317" {
			t.Errorf("the trail claims the server recorded its address: %+v", e)
		}
	}
}

func TestDiscoveringTheAddressNeverGetsInTheWay(t *testing.T) {
	ctx := context.Background()
	// FUSION never enabled: there is no central operator to record it on.
	a := newAdminRig(t)
	f, k := newFusion(t, a)
	k.svcAddr = "203.0.113.7:4317"
	if addr, err := f.DiscoverAddress(ctx, a.a.C); addr != "" || err != nil || k.svcReads != 0 {
		t.Errorf("before FUSION is enabled: %q %v (%d reads)", addr, err, k.svcReads)
	}
	// Another organisation never records one.
	f.Org = "some-other-org"
	if addr, _ := f.DiscoverAddress(ctx, a.a.C); addr != "" {
		t.Errorf("another organisation recorded %q", addr)
	}
	// A nil switch, or one with no cluster, is nothing to do.
	var none *FusionControl
	if addr, err := none.DiscoverAddress(ctx, a.a.C); addr != "" || err != nil {
		t.Errorf("nil: %q %v", addr, err)
	}

	// An older Role that cannot read the Service, or one that is not there: no address and no complaint.
	for name, kerr := range map[string]error{"forbidden": ErrKubeForbidden, "missing": ErrKubeNotFound} {
		b, f, k, _ := enabledCentral(t)
		k.svcErr = kerr
		if addr, err := f.DiscoverAddress(ctx, b.a.C); addr != "" || err != nil {
			t.Errorf("%s: %q %v", name, addr, err)
		}
	}
	// A failing API is reported to the caller, which logs it; an address the server would not accept is too.
	b, f3, k3, _ := enabledCentral(t)
	k3.svcErr = errors.New("connection refused")
	if _, err := f3.DiscoverAddress(ctx, b.a.C); err == nil {
		t.Error("a failing API went unreported")
	}
	c, f4, k4, now := enabledCentral(t)
	k4.svcAddr = "localhost:4317"
	if _, err := f4.DiscoverAddress(ctx, c.a.C); err == nil || f4.Exposed() {
		t.Errorf("an address other clusters cannot use was recorded: %v", err)
	}
	*now = now.Add(time.Minute)
	k4.svcAddr = "198.51.100.9:4317"
	if addr, err := f4.DiscoverAddress(ctx, c.a.C); addr != "198.51.100.9:4317" || err != nil {
		t.Errorf("after the Service shows a good one: %q %v", addr, err)
	}
}

// Opening the FUSION screen is what notices the gateway has no address, and records it without making the page wait.
func TestTheFusionScreenRecordsTheGatewaysAddress(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleAdmin)
	f, k := newFusion(t, a)
	a.a.Fusion = f
	k.svcAddr = "203.0.113.7:4317"
	if r := a.do("GET", "/api/v1/fusion", nil, withCookie(cookie)); r.json(t)["central"].(map[string]any)["exposed"] != false {
		t.Fatalf("a screen for a FUSION that was never enabled recorded something: %s", r.Body.String())
	}
	if k.svcReads != 0 {
		t.Fatal("the Service was read for a gateway that does not exist yet")
	}
	if r := a.do("POST", "/api/v1/fusion/enable", nil, withCookie(cookie)); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		r := a.do("GET", "/api/v1/fusion", nil, withCookie(cookie))
		central, _ := r.json(t)["central"].(map[string]any)
		if central["exposed"] == true {
			if central["endpoint"] != "203.0.113.7:4317" {
				t.Fatalf("central = %v", central)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the address was never recorded: %v", central)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
