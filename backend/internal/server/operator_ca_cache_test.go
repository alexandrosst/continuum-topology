package server

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"continuum/internal/pki"
)

// countOpens counts how many times an operator CA is really opened (each is an Argon2id run) for the rest of the test.
func countOpens(t *testing.T) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	old := openOperatorCA
	openOperatorCA = func(c *Core, certPEM, keyPEM []byte) (*pki.CA, error) {
		n.Add(1)
		return old(c, certPEM, keyPEM)
	}
	t.Cleanup(func() { openOperatorCA = old })
	return &n
}

// Every certificate issued for an operator used to open its CA again; now the first does and the rest reuse it.
func TestOperatorCAIsOpenedOnceNotOncePerCertificate(t *testing.T) {
	opens := countOpens(t)
	e := newEnv(t)
	cl := e.approvedCluster(t, fp)
	op, _, _, err := e.core.CreateOperator(e.ctx, "alex", "athens", []string{cl}, extDest("c:4317"), nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if _, _, _, err := e.core.IssueOperatorClientCert(e.ctx, "alex", op.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := e.core.ReissueOperatorInstall(e.ctx, "alex", op.ID); err != nil {
		t.Fatal(err)
	}
	if got := opens.Load(); got != 1 {
		t.Fatalf("the CA was opened %d times for 5 issues", got)
	}
}

// A revoked or deleted operator's key does not stay in memory.
func TestRevokingOrDeletingAnOperatorDropsItsOpenedCA(t *testing.T) {
	e := newEnv(t)
	cl := e.approvedCluster(t, fp)
	mk := func(name string) string {
		op, _, _, err := e.core.CreateOperator(e.ctx, "alex", name, []string{cl}, extDest("c:4317"), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := e.core.IssueOperatorClientCert(e.ctx, "alex", op.ID); err != nil {
			t.Fatal(err)
		}
		return op.ID
	}
	cached := func(id string) bool {
		e.base.opCAs.mu.Lock()
		defer e.base.opCAs.mu.Unlock()
		_, ok := e.base.opCAs.opened[id]
		return ok
	}
	a, b := mk("a"), mk("b")
	if !cached(a) || !cached(b) {
		t.Fatal("test setup: nothing cached")
	}
	if err := e.core.RevokeOperator(e.ctx, "alex", a, "gone"); err != nil {
		t.Fatal(err)
	}
	if cached(a) || !cached(b) {
		t.Fatalf("after revoking a: a cached %v, b cached %v", cached(a), cached(b))
	}
	if err := e.core.DeleteOperator(e.ctx, "alex", b); err != nil {
		t.Fatal(err)
	}
	if cached(b) {
		t.Fatal("a deleted operator's CA is still held")
	}
}

// What was opened from one sealed key is never served for another: a key that changed in the database is opened afresh.
func TestOpenedCAIsKeyedByTheSealedKey(t *testing.T) {
	var opens int
	old := openOperatorCA
	openOperatorCA = func(*Core, []byte, []byte) (*pki.CA, error) { opens++; return &pki.CA{}, nil }
	t.Cleanup(func() { openOperatorCA = old })
	o := newOperatorCAs()
	ctx := context.Background()
	first, _ := o.open(ctx, nil, "op-1", []byte("cert"), []byte("key-1"))
	again, _ := o.open(ctx, nil, "op-1", []byte("cert"), []byte("key-1"))
	other, _ := o.open(ctx, nil, "op-1", []byte("cert"), []byte("key-2"))
	if first != again || first == other || opens != 2 {
		t.Fatalf("opens = %d, same key reused = %v, changed key reused = %v", opens, first == again, first == other)
	}
}

// At most two Argon2 opens run at once, whatever the number of operators asking, and a caller that gives up while
// waiting for a slot is let go.
func TestAtMostTwoOperatorCAsAreOpenedAtOnce(t *testing.T) {
	var running, peak atomic.Int32
	release := make(chan struct{})
	old := openOperatorCA
	openOperatorCA = func(*Core, []byte, []byte) (*pki.CA, error) {
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		<-release
		running.Add(-1)
		return &pki.CA{}, nil
	}
	t.Cleanup(func() { openOperatorCA = old })
	o := newOperatorCAs()
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = o.open(context.Background(), nil, string(rune('a'+i)), []byte("c"), []byte("k"))
		}(i)
	}
	deadline := time.Now().Add(5 * time.Second)
	for running.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // time for a third to (wrongly) start
	if got := running.Load(); got != 2 {
		t.Fatalf("%d opens running at once, want 2", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := o.open(ctx, nil, "late", []byte("c"), []byte("k")); err == nil {
		t.Fatal("a caller waiting for a slot was not let go when its context ended")
	}
	close(release)
	wg.Wait()
	if peak.Load() != 2 {
		t.Fatalf("peak concurrency = %d", peak.Load())
	}
}

// Callers that arrive together for one operator share one open: the ones that waited for a slot find what the first opened.
func TestConcurrentCallersForOneOperatorOpenItsCAOnce(t *testing.T) {
	var opens atomic.Int32
	old := openOperatorCA
	openOperatorCA = func(*Core, []byte, []byte) (*pki.CA, error) {
		opens.Add(1)
		time.Sleep(30 * time.Millisecond)
		return &pki.CA{}, nil
	}
	t.Cleanup(func() { openOperatorCA = old })
	o := newOperatorCAs()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := o.open(context.Background(), nil, "op-1", []byte("cert"), []byte("key")); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := opens.Load(); got != 1 {
		t.Fatalf("8 simultaneous callers opened the CA %d times", got)
	}
}

// An opened CA that nobody has used for a while is not kept: a decrypted private key does not outlive its use by months.
func TestAnIdleOpenedCAIsDropped(t *testing.T) {
	var opens int
	old := openOperatorCA
	openOperatorCA = func(*Core, []byte, []byte) (*pki.CA, error) { opens++; return &pki.CA{}, nil }
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	oldNow := caCacheNow
	caCacheNow = func() time.Time { return now }
	t.Cleanup(func() { openOperatorCA, caCacheNow = old, oldNow })
	o := newOperatorCAs()
	ctx := context.Background()
	o.open(ctx, nil, "op-1", []byte("c"), []byte("k"))
	o.open(ctx, nil, "op-2", []byte("c"), []byte("k"))
	now = now.Add(caCacheLifetime - time.Minute)
	o.open(ctx, nil, "op-1", []byte("c"), []byte("k")) // used again: its clock starts over
	if opens != 2 {
		t.Fatalf("opens = %d inside the lifetime", opens)
	}
	now = now.Add(2 * time.Minute) // op-2 has been idle for longer than the lifetime, op-1 has not
	o.open(ctx, nil, "op-1", []byte("c"), []byte("k"))
	o.mu.Lock()
	_, kept2 := o.opened["op-2"]
	o.mu.Unlock()
	if kept2 || opens != 2 {
		t.Fatalf("idle entry kept = %v, opens = %d", kept2, opens)
	}
	now = now.Add(caCacheLifetime + time.Minute)
	o.open(ctx, nil, "op-1", []byte("c"), []byte("k"))
	if opens != 3 {
		t.Fatalf("an entry unused past the lifetime was served: opens = %d", opens)
	}
}

// An operator revoked while its CA is being opened does not get the result cached afterwards.
func TestACAOpenedWhileTheOperatorIsRevokedIsNotKept(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	old := openOperatorCA
	openOperatorCA = func(*Core, []byte, []byte) (*pki.CA, error) { close(started); <-release; return &pki.CA{}, nil }
	t.Cleanup(func() { openOperatorCA = old })
	o := newOperatorCAs()
	done := make(chan struct{})
	go func() {
		defer close(done)
		o.open(context.Background(), nil, "op-1", []byte("c"), []byte("k"))
	}()
	<-started
	o.forget("op-1") // revoked meanwhile
	close(release)
	<-done
	o.mu.Lock()
	_, kept := o.opened["op-1"]
	o.mu.Unlock()
	if kept {
		t.Fatal("the CA of an operator revoked during the open was cached")
	}
}
