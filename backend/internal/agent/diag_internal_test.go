package agent

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestSplitSyncKeepsSmallPicturesWholeAndChunksLargeOnes(t *testing.T) {
	small := &continuumv1.Sync{Seq: 3, Full: true, Cluster: &continuumv1.ClusterFacts{Uid: "u"}, Workloads: []*continuumv1.WorkloadFacts{{Key: "a"}}}
	if got := splitSync(small, 1<<20); len(got) != 1 || got[0] != small || got[0].ChunkTotal != 0 || got[0].SyncId != "" {
		t.Fatalf("a small picture must be sent as it is: %+v", got)
	}

	big := &continuumv1.Sync{Seq: 7, Full: true, Cluster: &continuumv1.ClusterFacts{Uid: "u", Version: "v1.30"}, Modules: []*continuumv1.ModuleStatus{{Name: "m"}}}
	for i := 0; i < 20; i++ {
		big.Nodes = append(big.Nodes, &continuumv1.NodeFacts{Key: fmt.Sprint("n", i), Name: fmt.Sprint("n", i)})
	}
	for i := 0; i < 60000; i++ {
		big.Workloads = append(big.Workloads, &continuumv1.WorkloadFacts{Key: fmt.Sprintf("ns%d/Deployment/w%d", i%50, i), Name: fmt.Sprint("w", i), Namespace: fmt.Sprint("ns", i%50), Kind: "Deployment",
			Labels: map[string]string{"app": fmt.Sprint("w", i), "tier": "backend"}})
	}
	for i := 0; i < 500; i++ {
		big.DeletedWorkloads = append(big.DeletedWorkloads, fmt.Sprint("gone", i))
	}
	chunks := splitSync(big, ChunkBytes)
	if len(chunks) < 3 || len(chunks) > maxChunks {
		t.Fatalf("%d chunks", len(chunks))
	}
	var nodes, workloads, deleted int
	for i, c := range chunks {
		if int(c.ChunkIndex) != i || int(c.ChunkTotal) != len(chunks) || c.SyncId == "" || c.SyncId != chunks[0].SyncId || c.Seq != 7 || !c.Full {
			t.Fatalf("chunk %d: index %d total %d id %q seq %d full %v", i, c.ChunkIndex, c.ChunkTotal, c.SyncId, c.Seq, c.Full)
		}
		if (i == 0) != (c.Cluster != nil) || (i > 0 && len(c.Modules) > 0) {
			t.Fatalf("chunk %d: cluster facts and modules belong in the first chunk only", i)
		}
		if n := proto.Size(c); n > ChunkBytes+64<<10 {
			t.Fatalf("chunk %d is %d bytes for a limit of %d", i, n, ChunkBytes)
		}
		nodes, workloads, deleted = nodes+len(c.Nodes), workloads+len(c.Workloads), deleted+len(c.DeletedWorkloads)
	}
	if nodes != 20 || workloads != 60000 || deleted != 500 {
		t.Fatalf("the chunks carry %d nodes, %d workloads, %d deletions", nodes, workloads, deleted)
	}
	if len(splitSync(big, 0)) != 1 {
		t.Fatal("a zero limit means no chunking")
	}
	// A second picture gets a different id, so the server cannot mix the two.
	if again := splitSync(big, ChunkBytes); again[0].SyncId == chunks[0].SyncId {
		t.Fatal("two pictures share an id")
	}
}

func TestOverridesCanOnlyNarrow(t *testing.T) {
	for installed := 0; installed <= 2; installed++ {
		for approved := 0; approved <= 4; approved++ {
			o := resolveOverrides(&continuumv1.Config{ApprovedAccessTier: uint32(approved)}, installed)
			if o.tier > installed || o.tier > approved {
				t.Fatalf("installed %d, approved %d: collects at %d", installed, approved, o.tier)
			}
			if (approved > installed) != (len(o.ignored) > 0) {
				t.Fatalf("installed %d, approved %d: ignored %v", installed, approved, o.ignored)
			}
		}
	}
	o := resolveOverrides(&continuumv1.Config{ApprovedAccessTier: 2,
		PausedCollectors:   []string{"flow", "cameras", "flow", "measure"},
		ExcludedNamespaces: []string{"shop", "Bad_Name", "kube-system", "batch", "shop"}}, 2)
	if o.tier != 2 || len(o.paused) != 2 || !o.paused["flow"] || !o.paused["measure"] || strings.Join(o.excluded, ",") != "batch,shop" {
		t.Fatalf("resolved: %+v", o)
	}
	if len(o.ignored) != 3 { // cameras, Bad_Name, kube-system
		t.Fatalf("ignored: %v", o.ignored)
	}
	// Nothing pushed means nothing paused and nothing excluded: an empty Config restores what the install allows.
	if o := resolveOverrides(&continuumv1.Config{ApprovedAccessTier: 1}, 2); len(o.paused) != 0 || len(o.excluded) != 0 || len(o.ignored) != 0 {
		t.Fatalf("empty overrides: %+v", o)
	}
}

func newTestRunner() *runner {
	return &runner{log: slog.Default(), probs: newProblemSet(), dg: newDiagState(), started: time.Now()}
}

func codesOf(r *runner) map[string]*continuumv1.Problem {
	out := map[string]*continuumv1.Problem{}
	for _, p := range r.probs.list() {
		out[p.Code] = p
	}
	return out
}

func TestOverridesIgnoredAreReportedAndClearedWhenPutRight(t *testing.T) {
	r := newTestRunner()
	r.enforce(resolveOverrides(&continuumv1.Config{ApprovedAccessTier: 2}, 1))
	p := codesOf(r)[CodeOverrideIgnored]
	if p == nil || !strings.Contains(p.Message, "helm upgrade --reuse-values") || p.Severity != continuumv1.Problem_INFO {
		t.Fatalf("problem = %v", p)
	}
	r.enforce(resolveOverrides(&continuumv1.Config{ApprovedAccessTier: 1}, 1))
	if codesOf(r)[CodeOverrideIgnored] != nil {
		t.Fatal("the problem stayed after the server pushed something the agent can honour")
	}
}

func TestStreamErrorsBecomeTypedProblems(t *testing.T) {
	cases := []struct {
		err  error
		code string
	}{
		{status.Error(codes.ResourceExhausted, "this cluster's facts would exceed the server's limit for workloads (60000, at most 50000)"), CodeSyncTooLarge},
		{status.Error(codes.InvalidArgument, "a single sync may carry at most 5000 nodes"), CodeSyncTooLarge},
		{status.Error(codes.ResourceExhausted, "sending too fast"), CodeServerLimitsRefused},
		{status.Error(codes.InvalidArgument, "workload facts are malformed"), CodeServerLimitsRefused},
	}
	for _, c := range cases {
		r := newTestRunner()
		r.noteStreamError(c.err)
		if p := codesOf(r)[c.code]; p == nil || p.Severity != continuumv1.Problem_ERROR {
			t.Errorf("%v: %v", c.err, codesOf(r))
		}
	}
	r := newTestRunner()
	r.noteStreamError(status.Error(codes.Unavailable, "connection refused"))
	r.noteStreamError(fmt.Errorf("plain error"))
	if len(r.probs.list()) != 0 {
		t.Fatal("an ordinary connection failure is not a server refusal")
	}
}

func TestPanicsAreRecoveredCountedAndReported(t *testing.T) {
	r := newTestRunner()
	r.safely("worker", func() { panic("kaboom") })
	if r.panics.Load() != 1 {
		t.Fatal("not counted")
	}
	p := codesOf(r)[CodeInternalError]
	if p == nil || !strings.Contains(p.Message, "worker") || !strings.Contains(p.Message, "kaboom") || p.Severity != continuumv1.Problem_ERROR {
		t.Fatalf("problem = %v", p)
	}
}

// safelyReconnect exists because plain safely (above) was once used for stream()'s receive loop, certificate
// renewal, and connection-timing goroutines: a panic there was recovered and logged, same as here, but the
// goroutine then simply ended - leaving the agent looking healthy while quietly deaf to the server (receive
// loop), never renewing an expiring certificate again (certificate renewal), or stuck forever refusing to run
// another measurement round (connection timing, fixed separately in stream.go by unsticking measureBusy rather
// than reconnecting - a panic there doesn't need the whole connection torn down). This test is the regression
// guard for the mechanism itself: a panic must both raise the usual problem AND push an error other code can act
// on, not just be logged and forgotten.
func TestSafelyReconnectPushesAnErrorInsteadOfJustLoggingIt(t *testing.T) {
	r := newTestRunner()
	errs := make(chan error, 1)
	r.safelyReconnect("receive loop", func() { panic("kaboom") }, errs)

	if r.panics.Load() != 1 {
		t.Fatal("not counted")
	}
	p := codesOf(r)[CodeInternalError]
	if p == nil || !strings.Contains(p.Message, "receive loop") || !strings.Contains(p.Message, "kaboom") || p.Severity != continuumv1.Problem_ERROR {
		t.Fatalf("problem = %v", p)
	}
	select {
	case err := <-errs:
		if err == nil || !strings.Contains(err.Error(), "receive loop") || !strings.Contains(err.Error(), "kaboom") {
			t.Fatalf("error = %v", err)
		}
	default:
		t.Fatal("a panic must push an error for the caller to reconnect on, not just log one")
	}

	// A second panic while the channel from the first still hasn't been drained must not block forever: the
	// goroutine's own defer would hang, and nothing would ever drain it from outside a real connection either.
	done := make(chan struct{})
	go func() {
		r.safelyReconnect("receive loop", func() { panic("again") }, errs)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("safelyReconnect must not block when errs is already full")
	}
}

func TestProblemsKeepTheirSinceAndExpire(t *testing.T) {
	ps := newProblemSet()
	ps.raise("k", "x", continuumv1.Problem_WARN, "first", 0)
	first := ps.list()[0].Since.AsTime()
	time.Sleep(10 * time.Millisecond)
	ps.raise("k", "x", continuumv1.Problem_WARN, "again", 0)
	if got := ps.list(); len(got) != 1 || !got[0].Since.AsTime().Equal(first) || got[0].Message != "again" {
		t.Fatalf("raising again must keep since: %v", got)
	}
	ps.raise("t", "y", continuumv1.Problem_INFO, "brief", 20*time.Millisecond)
	time.Sleep(40 * time.Millisecond)
	if len(ps.list()) != 1 {
		t.Fatal("an expired problem is still listed")
	}
	ps.replaceDerived([]*prob{{code: "d", sev: continuumv1.Problem_ERROR, msg: "m", since: time.Now()}})
	before := ps.list()[0]
	if before.Code != "d" { // worst first
		t.Fatalf("order: %v", ps.list())
	}
	since := before.Since.AsTime()
	time.Sleep(5 * time.Millisecond)
	ps.replaceDerived([]*prob{{code: "d", sev: continuumv1.Problem_ERROR, msg: "m", since: time.Now()}})
	if !ps.list()[0].Since.AsTime().Equal(since) {
		t.Fatal("a derived problem that still holds lost its since")
	}
	ps.replaceDerived(nil)
	if len(ps.list()) != 1 {
		t.Fatalf("a derived problem that stopped holding is still listed: %v", ps.list())
	}
}

func TestDiagnosticsAreSentWhenTheyChangeAndAtLeastEveryFiveMinutes(t *testing.T) {
	r := newTestRunner()
	if r.diagToSend(false) == nil {
		t.Fatal("the first account must be sent")
	}
	if r.diagToSend(false) != nil {
		t.Fatal("an unchanged account was sent again")
	}
	r.probs.raise("x", "internal_error", continuumv1.Problem_ERROR, "changed", 0)
	if d := r.diagToSend(false); d == nil || len(d.Problems) != 1 {
		t.Fatal("a changed account was not sent")
	}
	if r.diagToSend(false) != nil {
		t.Fatal("sent twice")
	}
	if r.diagToSend(true) == nil {
		t.Fatal("force did not send")
	}
	r.dg.mu.Lock()
	r.dg.sentAt = time.Now().Add(-diagRefreshEvery - time.Second)
	r.dg.mu.Unlock()
	if r.diagToSend(false) == nil {
		t.Fatal("an unchanged account must still be refreshed after five minutes")
	}
	// Numbers that always change do not count as a change.
	r.started = r.started.Add(-time.Hour)
	if r.diagToSend(false) != nil {
		t.Fatal("uptime alone made the account count as changed")
	}
}
