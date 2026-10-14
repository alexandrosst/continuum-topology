package link

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeProc stands in for iperf3 -s: it records what it was started with and what the files held at that moment.
type fakeProc struct {
	args    []string
	key     string
	users   string
	done    chan struct{}
	stopped atomic.Bool
	once    sync.Once
}

func (p *fakeProc) Stop()                 { p.stopped.Store(true); p.once.Do(func() { close(p.done) }) }
func (p *fakeProc) Done() <-chan struct{} { return p.done }
func (p *fakeProc) die()                  { p.once.Do(func() { close(p.done) }) }

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

type runnerRig struct {
	r      *IperfServerRunner
	dir    string
	log    *lockedBuf
	mu     sync.Mutex
	procs  []*fakeProc
	answer func() (IperfServer, error)
	gen    chan struct{}
	asked  chan struct{}
}

func newRunnerRig(t *testing.T) *runnerRig {
	t.Helper()
	dir := t.TempDir()
	rr := &runnerRig{dir: dir, log: &lockedBuf{}, gen: make(chan struct{}), asked: make(chan struct{}, 100)}
	rr.r = &IperfServerRunner{Dir: dir, Bin: "/x/iperf3", Port: 8862, IdleTimeout: 60, BitrateLimit: "10G", Every: time.Minute, RestartAfter: time.Millisecond}
	rr.r.Log = slog.New(slog.NewTextHandler(rr.log, nil))
	rr.r.Fetch = func(context.Context) (IperfServer, error) {
		rr.asked <- struct{}{}
		rr.mu.Lock()
		f := rr.answer
		rr.mu.Unlock()
		return f()
	}
	rr.r.Start = func(_ context.Context, bin string, args []string) (IperfProc, error) {
		k, _ := os.ReadFile(filepath.Join(dir, "private.pem"))
		u, _ := os.ReadFile(filepath.Join(dir, "users.csv"))
		p := &fakeProc{args: append([]string{bin}, args...), key: string(k), users: string(u), done: make(chan struct{})}
		rr.mu.Lock()
		rr.procs = append(rr.procs, p)
		rr.mu.Unlock()
		return p, nil
	}
	rr.r.After = func(d time.Duration) <-chan time.Time {
		rr.mu.Lock()
		g := rr.gen
		rr.mu.Unlock()
		c := make(chan time.Time, 1)
		go func() {
			select {
			case <-g:
				c <- time.Now()
			case <-time.After(10 * time.Second):
			}
		}()
		return c
	}
	return rr
}

func (rr *runnerRig) set(s IperfServer, err error) {
	rr.mu.Lock()
	rr.answer = func() (IperfServer, error) { return s, err }
	rr.mu.Unlock()
}

func (rr *runnerRig) start(t *testing.T) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { rr.r.Run(ctx); close(done) }()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("the runner did not stop")
		}
	}
}

// turn waits for the next question to the agent and then lets the runner finish applying the answer.
func (rr *runnerRig) asks(t *testing.T) {
	t.Helper()
	select {
	case <-rr.asked:
	case <-time.After(5 * time.Second):
		t.Fatal("the runner never asked the agent")
	}
}

// tick ends every wait the runner is in.
func (rr *runnerRig) tick() {
	rr.mu.Lock()
	close(rr.gen)
	rr.gen = make(chan struct{})
	rr.mu.Unlock()
}

func (rr *runnerRig) procCount() int {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	return len(rr.procs)
}

func waitUntil(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func goodServer(t *testing.T) IperfServer {
	priv, _ := testKeys(t)
	return IperfServer{PrivateKeyPEM: Secret(priv), UsersCSV: Secret(usersCSV(testUser, testPassword))}
}

func TestWithoutCredentialsNoServerRunsAndNeverAnOpenOne(t *testing.T) {
	for name, tc := range map[string]struct {
		s   IperfServer
		err error
	}{
		"the agent has none":       {IperfServer{}, nil},
		"the agent is unreachable": {IperfServer{}, errors.New("connection refused")},
		"only a key":               {IperfServer{PrivateKeyPEM: "k"}, nil},
	} {
		rr := newRunnerRig(t)
		rr.set(tc.s, tc.err)
		stop := rr.start(t)
		rr.asks(t)
		time.Sleep(50 * time.Millisecond)
		if rr.procCount() != 0 {
			t.Errorf("%s: iperf3 was started without credentials", name)
		}
		for _, f := range []string{"private.pem", "users.csv"} {
			if _, err := os.Stat(filepath.Join(rr.dir, f)); err == nil {
				t.Errorf("%s: %s exists", name, f)
			}
		}
		stop()
		mustNotContain(t, name+" log", rr.log.String())
	}
}

func TestTheServerStartsAuthenticatedWithPrivateFilesAndRestartsOnlyWhenTheyChange(t *testing.T) {
	rr := newRunnerRig(t)
	creds := goodServer(t)
	rr.set(creds, nil)
	stop := rr.start(t)
	defer stop()
	rr.asks(t)
	waitUntil(t, "the first start", func() bool { return rr.procCount() == 1 })

	p := rr.procs[0]
	line := strings.Join(p.args, " ")
	for _, want := range []string{"/x/iperf3 -s -p 8862", "--rsa-private-key-path " + filepath.Join(rr.dir, "private.pem"), "--authorized-users-path " + filepath.Join(rr.dir, "users.csv"), "--idle-timeout 60", "--server-bitrate-limit 10G"} {
		if !strings.Contains(line, want) {
			t.Errorf("args %q lack %q", line, want)
		}
	}
	if p.key != creds.PrivateKeyPEM.Reveal() || p.users != creds.UsersCSV.Reveal() {
		t.Error("the files held something else than the credentials when the server started")
	}
	for _, f := range []string{"private.pem", "users.csv"} {
		st, err := os.Stat(filepath.Join(rr.dir, f))
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v, want mode 0600", f, st, err)
		}
	}
	mustNotContain(t, "the process arguments", line)

	// The same credentials again: left alone.
	rr.tick()
	rr.asks(t)
	time.Sleep(50 * time.Millisecond)
	if rr.procCount() != 1 || p.stopped.Load() {
		t.Errorf("an unchanged answer restarted the server (%d starts, stopped %v)", rr.procCount(), p.stopped.Load())
	}

	// A rotation: new users line. The server restarts with the new files and the old process is stopped first.
	next := creds
	next.UsersCSV = Secret(usersCSV("ik-test0002", "another-password-0123456789abcdef-xx"))
	rr.set(next, nil)
	rr.tick()
	rr.asks(t)
	waitUntil(t, "the restart", func() bool { return rr.procCount() == 2 })
	if !p.stopped.Load() {
		t.Error("the old server was not stopped")
	}
	if rr.procs[1].users != next.UsersCSV.Reveal() {
		t.Error("the new server started with the old users")
	}

	// The credentials are withdrawn (the pair was removed): the server stops and the files go.
	rr.set(IperfServer{}, nil)
	rr.tick()
	rr.asks(t)
	waitUntil(t, "the stop", func() bool { return rr.procs[1].stopped.Load() })
	waitUntil(t, "the files to go", func() bool {
		_, e1 := os.Stat(filepath.Join(rr.dir, "private.pem"))
		_, e2 := os.Stat(filepath.Join(rr.dir, "users.csv"))
		return e1 != nil && e2 != nil
	})
	if rr.procCount() != 2 {
		t.Error("a server was started with nothing to serve with")
	}
	mustNotContain(t, "the log", rr.log.String())
}

func TestAnAgentThatCannotBeReachedDoesNotStopAServerThatHasItsCredentials(t *testing.T) {
	rr := newRunnerRig(t)
	rr.set(goodServer(t), nil)
	stop := rr.start(t)
	defer stop()
	rr.asks(t)
	waitUntil(t, "the start", func() bool { return rr.procCount() == 1 })
	rr.set(IperfServer{}, errors.New("agent answered 503"))
	rr.tick()
	rr.asks(t)
	time.Sleep(50 * time.Millisecond)
	if rr.procs[0].stopped.Load() || rr.procCount() != 1 {
		t.Error("the server was stopped because the agent could not be reached")
	}
	if !strings.Contains(rr.log.String(), "could not get the iperf3 credentials") {
		t.Errorf("the failure was not logged:\n%s", rr.log.String())
	}
}

func TestAServerThatDiesIsStartedAgainWithTheSameFiles(t *testing.T) {
	rr := newRunnerRig(t)
	rr.set(goodServer(t), nil)
	stop := rr.start(t)
	defer stop()
	rr.asks(t)
	waitUntil(t, "the start", func() bool { return rr.procCount() == 1 })
	rr.procs[0].die()
	rr.asks(t)
	waitUntil(t, "the second start", func() bool { return rr.procCount() == 2 })
}

func TestCredentialsThatCannotBeWrittenLeaveNoServerRunning(t *testing.T) {
	rr := newRunnerRig(t)
	rr.set(goodServer(t), nil)
	rr.r.Dir = filepath.Join(rr.dir, "missing", "deeper")
	if err := os.WriteFile(filepath.Join(rr.dir, "missing"), []byte("a file, not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	stop := rr.start(t)
	rr.asks(t)
	time.Sleep(50 * time.Millisecond)
	if rr.procCount() != 0 {
		t.Error("iperf3 was started although its files could not be written")
	}
	stop()
	mustNotContain(t, "the log", rr.log.String())
}

func TestStoppingTheRunnerStopsIperfAndRemovesTheFiles(t *testing.T) {
	rr := newRunnerRig(t)
	rr.set(goodServer(t), nil)
	stop := rr.start(t)
	rr.asks(t)
	waitUntil(t, "the start", func() bool { return rr.procCount() == 1 })
	stop()
	if !rr.procs[0].stopped.Load() {
		t.Error("iperf3 kept running after the supervisor stopped")
	}
	if _, err := os.Stat(filepath.Join(rr.dir, "private.pem")); err == nil {
		t.Error("the private key stayed on disk")
	}
}

// A file a previous container of the pod left behind is not served from: the runner starts from nothing.
func TestLeftoverFilesAreClearedAtStart(t *testing.T) {
	rr := newRunnerRig(t)
	for _, f := range []string{"private.pem", "users.csv", "private.pem.new"} {
		os.WriteFile(filepath.Join(rr.dir, f), []byte("old"), 0o600)
	}
	rr.set(IperfServer{}, nil)
	stop := rr.start(t)
	rr.asks(t)
	stop()
	for _, f := range []string{"private.pem", "users.csv", "private.pem.new"} {
		if _, err := os.Stat(filepath.Join(rr.dir, f)); err == nil {
			t.Errorf("%s survived", f)
		}
	}
}
