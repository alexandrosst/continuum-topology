package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/kernel"
	"continuum/internal/pki"

	"google.golang.org/grpc/codes"
)

func TestLoginFailuresBackOffPerAccountWhateverTheAddress(t *testing.T) {
	a := newAdminRig(t)
	a.user(t, "alex", RoleAdmin)
	login := func(user, pw string, n int) resp {
		// every attempt from a different network, so no per-address limit can be what stops it
		return a.do("POST", "/api/v1/auth/login", map[string]string{"username": user, "password": pw}, fromIP(fmt.Sprintf("198.51.%d.%d", n/200, n%200+1)))
	}
	for _, user := range []string{"alex", "nobody-by-this-name"} {
		for i := 0; i < kernel.FailFree; i++ {
			if r := login(user, "wrong password here", i); r.Code != 401 {
				t.Fatalf("%s failure %d: %d", user, i, r.Code)
			}
		}
	}
	real, ghost := login("alex", "wrong password here", 50), login("nobody-by-this-name", "wrong password here", 51)
	if real.Code != 401 || ghost.Code != 401 {
		t.Fatalf("the fifth failure is still an ordinary one: %d %d", real.Code, ghost.Code)
	}
	// From now on both names wait, even with the right password and from a fresh address; the answers are the same
	// for an account that exists and one that does not.
	r1, r2 := login("alex", goodPW, 60), login("nobody-by-this-name", goodPW, 61)
	if r1.Code != 429 || r2.Code != 429 || r1.Body.String() != r2.Body.String() {
		t.Fatalf("backoff: %d %q vs %d %q", r1.Code, r1.Body.String(), r2.Code, r2.Body.String())
	}
	if !strings.Contains(r1.Body.String(), "1 second") || strings.Contains(strings.ToLower(r1.Body.String()), "exist") {
		t.Fatalf("message: %s", r1.Body.String())
	}
	// A different account is unaffected.
	a.user(t, "carol", RoleViewer)
	if r := login("carol", goodPW, 70); r.Code != 200 {
		t.Fatalf("another account: %d", r.Code)
	}
	// The wait doubles with each further failure (each one is refused, and only real attempts count).
	*a.now = a.now.Add(1100 * time.Millisecond)
	if r := login("alex", "wrong again please", 80); r.Code != 401 {
		t.Fatalf("after the first wait: %d", r.Code)
	}
	if r := login("alex", goodPW, 81); r.Code != 429 || !strings.Contains(r.Body.String(), "2 seconds") {
		t.Fatalf("second backoff: %d %s", r.Code, r.Body.String())
	}
	*a.now = a.now.Add(2100 * time.Millisecond)
	if r := login("alex", goodPW, 82); r.Code != 200 {
		t.Fatalf("the right password after the wait: %d %s", r.Code, r.Body.String())
	}
	// A success starts over.
	if r := login("alex", "wrong password here", 83); r.Code != 401 {
		t.Fatalf("counter not reset: %d", r.Code)
	}
	if r := login("alex", goodPW, 84); r.Code != 200 {
		t.Fatalf("one failure must not cause a wait: %d", r.Code)
	}
}

func TestLoginLimitsGroupAnIPv6NetworkTogether(t *testing.T) {
	a := newAdminRig(t)
	a.user(t, "alex", RoleAdmin)
	limited := 0
	for i := 0; i < 60; i++ {
		// 60 different addresses of one /64, each asking about a different name
		r := a.do("POST", "/api/v1/auth/login", map[string]string{"username": fmt.Sprintf("guess%d", i), "password": "whatever it is"}, func(r *http.Request) { r.RemoteAddr = fmt.Sprintf("[2001:db8:5:5::%x]:4000", i+1) })
		if r.Code == 429 {
			limited++
		}
	}
	if limited < 40 {
		t.Fatalf("only %d of 60 attempts from one /64 were limited: every address in it must share the allowance", limited)
	}
	// another /64 is separate
	r := a.do("POST", "/api/v1/auth/login", map[string]string{"username": "alex", "password": goodPW}, func(r *http.Request) { r.RemoteAddr = "[2001:db8:6:6::1]:4000" })
	if r.Code != 200 {
		t.Fatalf("a different network was limited: %d", r.Code)
	}
}

func TestPasswordHashWorkIsBoundedInNumberAndQueue(t *testing.T) {
	e := newEnv(t)
	c := e.base
	c.authSt().hashSem = make(chan struct{}, 2)
	c.authSt().maxQueue = 2
	hash, _ := HashPassword("correct horse battery")
	// occupy both slots
	c.authSt().hashSem <- struct{}{}
	c.authSt().hashSem <- struct{}{}

	results := make(chan error, 4)
	var started sync.WaitGroup
	for i := 0; i < 2; i++ { // two may wait
		started.Add(1)
		go func() {
			started.Done()
			_, err := c.verifyPassword(context.Background(), "correct horse battery", hash)
			results <- err
		}()
	}
	started.Wait()
	for c.authSt().hashQueue.Load() < 2 {
		time.Sleep(time.Millisecond)
	}
	// the third finds the queue full and is refused at once, with a clear error, without waiting
	start := time.Now()
	_, err := c.verifyPassword(context.Background(), "x", hash)
	if kindOf(err) != kernel.KindRateLimited || !strings.Contains(err.Error(), "busy") || time.Since(start) > time.Second {
		t.Fatalf("the request beyond the queue: %v after %v", err, time.Since(start))
	}
	if _, err := c.hashPassword(context.Background(), "x"); kindOf(err) != kernel.KindRateLimited {
		t.Fatalf("hashing beyond the queue: %v", err)
	}
	// free the slots: the two waiters finish
	<-c.authSt().hashSem
	<-c.authSt().hashSem
	for i := 0; i < 2; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("a queued check failed: %v", err)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("a queued check never ran")
		}
	}
	if c.authSt().hashQueue.Load() != 0 || len(c.authSt().hashSem) != 0 {
		t.Fatalf("leaked: queue %d, slots %d", c.authSt().hashQueue.Load(), len(c.authSt().hashSem))
	}
	// a waiter gives up when its request is cancelled
	c.authSt().hashSem <- struct{}{}
	c.authSt().hashSem <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.verifyPassword(ctx, "x", hash); err == nil {
		t.Fatal("a cancelled waiter ran")
	}
	if c.authSt().hashQueue.Load() != 0 {
		t.Fatal("a cancelled waiter stayed in the queue")
	}
}

func TestUnauthenticatedEnrollmentCallsHaveASmallReceiveLimit(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pin := r.core.CA.Pin()
	enr := continuumv1.NewEnrollmentClient(r.dial(t, pki.ClientTLS(pin, "127.0.0.1", nil)))
	d, _ := csr(t)
	// an ordinary request reaches the application (a bad token is Unauthenticated)
	if _, err := enr.Enroll(ctx, &continuumv1.EnrollRequest{Token: "cnt_x", CsrDer: d, ClusterFingerprint: fp}); code(err) != codes.Unauthenticated {
		t.Fatalf("control: %v", err)
	}
	// 100 KiB is refused from the message header, before it is read
	big := &continuumv1.EnrollRequest{Token: "cnt_x", CsrDer: make([]byte, 100<<10), ClusterFingerprint: fp}
	if _, err := enr.Enroll(ctx, big); code(err) != codes.ResourceExhausted {
		t.Fatalf("100 KiB enrollment: %v", err)
	}
	if _, err := enr.PollEnrollment(ctx, &continuumv1.PollRequest{AgentId: strings.Repeat("a", 100<<10), PollSecret: "s"}); code(err) != codes.ResourceExhausted {
		t.Fatalf("100 KiB poll: %v", err)
	}
	if _, err := enr.Rejoin(ctx, &continuumv1.RejoinRequest{ExpiredLeafDer: make([]byte, 100<<10)}); code(err) != codes.ResourceExhausted {
		t.Fatalf("100 KiB rejoin: %v", err)
	}
}

func TestAuthenticatedStreamsKeepTheLargeReceiveLimit(t *testing.T) {
	r := newHubRig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id, key, leaf := r.approvedAgent(t, fp)
	s := r.agentStream(t, ctx, id, key, leaf)
	var ws []*continuumv1.WorkloadFacts
	for i := 0; i < 3000; i++ { // several hundred KiB in one message
		ws = append(ws, &continuumv1.WorkloadFacts{Key: fmt.Sprintf("ns/Deployment/%d", i), Name: fmt.Sprintf("workload-%d", i), Labels: map[string]string{"app": strings.Repeat("a", 60)}})
	}
	m := &continuumv1.Sync{Seq: 1, Full: true, Cluster: &continuumv1.ClusterFacts{Uid: fp}, Workloads: ws}
	if err := s.Send(syncMsg(m)); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Recv(); err != nil || got.GetAck().GetSeq() != 1 {
		t.Fatalf("a large authenticated message: %v %v", got, err)
	}
}

func TestGRPCServerStopsCleanly(t *testing.T) {
	e := newEnv(t)
	srv := e.base.NewGRPC(pki.NewServerCerts(e.core.CA, []string{"127.0.0.1"}), &BaseAgentService{C: e.base})
	l := listenLocal(t)
	done := make(chan error, 1)
	go func() { done <- srv.Serve(l) }()
	time.Sleep(50 * time.Millisecond)
	go srv.GracefulStop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve after a stop returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("GracefulStop did not end Serve")
	}
}

func listenLocal(t *testing.T) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return l
}
