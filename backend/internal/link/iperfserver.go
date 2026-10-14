package link

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// IperfServerRunner is the iperf3 server of one node when the Ikhnos server issues the credentials. It replaces running iperf3
// directly in the responder pod: it asks the agent for this cluster's server credentials (FetchIperfServer), keeps them in a
// private in-memory directory, and runs iperf3 -s with them.
//
// It fails closed. iperf3 is started only with a private key and a users file, so there is never an unauthenticated server:
// with no credentials (the agent has none for this cluster, or the answer is refused, or they cannot be written) the server is not
// running and the prober of the other cluster reports an `auth` or `unreachable` failure, as it would for any server that is down.
// When the credentials change (a rotation, a pair added or removed) iperf3 is restarted with the new files; when they do not,
// it is left alone. When the agent cannot be reached, or says it has not had its Config yet (ErrIperfNotReady, an agent that was
// just restarted), it keeps serving with what it has: a server that is only briefly cut off from its agent should not drop tests,
// and what it has was issued for exactly this purpose. Only an answer that says "none" stops the server.
type IperfServerRunner struct {
	// Dir is where the key and the users file are written: a memory-backed directory private to this pod.
	Dir string
	// Bin, Port, IdleTimeout (seconds) and BitrateLimit are iperf3's own settings.
	Bin          string
	Port         int
	IdleTimeout  int
	BitrateLimit string
	// Every is how often the agent is asked (default one minute); after a failure it asks again sooner, backing off to Every.
	Every time.Duration
	Log   *slog.Logger

	// RestartAfter is the pause before iperf3 is started again after it exited by itself (default 2 seconds).
	RestartAfter time.Duration

	// Fetch asks the agent. Start runs iperf3. Both are replaced in tests.
	Fetch func(ctx context.Context) (IperfServer, error)
	Start func(ctx context.Context, bin string, args []string) (IperfProc, error)
	After func(d time.Duration) <-chan time.Time

	cur  [sha256.Size]byte
	proc IperfProc
	// state is what was last logged, so a steady state is said once and not every minute.
	state string
}

// IperfProc is a running iperf3.
type IperfProc interface {
	// Stop ends it and waits for it to be gone.
	Stop()
	// Done is closed when it has exited.
	Done() <-chan struct{}
}

// NewIperfServerRunner is a runner that asks the agent at url with the responder secret and runs the real iperf3.
func NewIperfServerRunner(url string, secret []byte, hc *http.Client) *IperfServerRunner {
	return &IperfServerRunner{
		Fetch: func(ctx context.Context) (IperfServer, error) {
			return FetchIperfServer(ctx, hc, url, secret, time.Now())
		},
		Start: startIperf,
	}
}

const (
	privateKeyFile = "private.pem"
	usersFile      = "users.csv"
)

// Run asks, applies and repeats until ctx ends, and then stops iperf3 and removes the files.
func (r *IperfServerRunner) Run(ctx context.Context) {
	if r.Every <= 0 {
		r.Every = time.Minute
	}
	if r.After == nil {
		r.After = time.After
	}
	if r.RestartAfter <= 0 {
		r.RestartAfter = 2 * time.Second
	}
	if r.Log == nil {
		r.Log = slog.Default()
	}
	defer r.shutdown()
	r.clearFiles() // an earlier container of this pod may have left some in the volume
	wait := 5 * time.Second
	for {
		creds, err := r.Fetch(ctx)
		next := r.Every
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, ErrIperfNotReady) {
			// Not a withdrawal: keep what is running and ask again soon.
			r.say("agent-not-ready", "the agent has not received its configuration yet; the iperf3 server keeps its current state", "serving", r.proc != nil, "retry_in", wait)
			next = wait
			wait = min(wait*2, r.Every)
		} else if err != nil {
			// The error is about the transport or a refused document; it never holds a credential.
			r.say("agent-unreachable", "could not get the iperf3 credentials from the agent", "err", err.Error(), "serving", r.proc != nil, "retry_in", wait)
			next = wait
			wait = min(wait*2, r.Every)
		} else {
			wait = 5 * time.Second
			r.apply(ctx, creds)
		}
		var exited <-chan struct{}
		if r.proc != nil {
			exited = r.proc.Done()
		}
		select {
		case <-ctx.Done():
			return
		case <-r.After(next):
		case <-exited:
			// iperf3 stopped by itself: say so, and start it again with the same files at the next turn.
			r.Log.Warn("the iperf3 server exited; starting it again")
			r.proc, r.cur = nil, [sha256.Size]byte{}
			select {
			case <-ctx.Done():
				return
			case <-time.After(r.RestartAfter):
			}
		}
	}
}

// apply puts creds in force: nothing to serve with stops the server; new credentials restart it; the same ones leave it alone.
func (r *IperfServerRunner) apply(ctx context.Context, creds IperfServer) {
	if creds.Empty() {
		if r.proc != nil {
			r.stop()
		}
		r.clearFiles()
		r.say("no-credentials", "no iperf3 credentials are issued for this cluster, so the iperf3 server is not running (no pair asks this cluster to serve throughput tests, or the Ikhnos server has not issued them yet)")
		return
	}
	sum := sha256.Sum256([]byte(creds.PrivateKeyPEM.Reveal() + "\x00" + creds.UsersCSV.Reveal()))
	if r.proc != nil && sum == r.cur {
		return
	}
	if err := r.writeFiles(creds); err != nil {
		// Without the files there is nothing to authenticate with: do not leave an older server running on credentials the
		// Ikhnos server has replaced.
		r.stop()
		r.clearFiles()
		r.say("write-failed", "could not write the iperf3 credentials; the iperf3 server is not running", "err", err.Error())
		return
	}
	r.stop()
	args := []string{"-s", "-p", strconv.Itoa(r.Port), "--rsa-private-key-path", filepath.Join(r.Dir, privateKeyFile), "--authorized-users-path", filepath.Join(r.Dir, usersFile)}
	if r.IdleTimeout > 0 {
		args = append(args, "--idle-timeout", strconv.Itoa(r.IdleTimeout))
	}
	if r.BitrateLimit != "" {
		args = append(args, "--server-bitrate-limit", r.BitrateLimit)
	}
	p, err := r.Start(ctx, r.Bin, args)
	if err != nil {
		r.clearFiles()
		r.say("start-failed", "could not start the iperf3 server", "err", err.Error())
		return
	}
	r.proc, r.cur = p, sum
	r.say("serving", "the iperf3 server is running with credentials issued by Ikhnos (authenticated mode)")
}

// say logs a message when the state changes, not on every pass.
func (r *IperfServerRunner) say(state, msg string, kv ...any) {
	if state == r.state && state != "agent-unreachable" {
		return
	}
	r.state = state
	if state == "serving" {
		r.Log.Info(msg, kv...)
		return
	}
	r.Log.Warn(msg, kv...)
}

func (r *IperfServerRunner) stop() {
	if r.proc != nil {
		r.proc.Stop()
	}
	r.proc, r.cur = nil, [sha256.Size]byte{}
}

func (r *IperfServerRunner) shutdown() {
	r.stop()
	r.clearFiles()
}

func (r *IperfServerRunner) writeFiles(c IperfServer) error {
	if err := os.MkdirAll(r.Dir, 0o700); err != nil {
		return errors.New("the credentials directory is not usable")
	}
	for name, data := range map[string]string{privateKeyFile: c.PrivateKeyPEM.Reveal(), usersFile: c.UsersCSV.Reveal()} {
		if err := writePrivate(filepath.Join(r.Dir, name), []byte(data)); err != nil {
			return fmt.Errorf("writing %s failed", name)
		}
	}
	return nil
}

// writePrivate writes a file nobody else can read, replacing any older one in one step.
func writePrivate(path string, data []byte) error {
	tmp := path + ".new"
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func (r *IperfServerRunner) clearFiles() {
	for _, n := range []string{privateKeyFile, usersFile, privateKeyFile + ".new", usersFile + ".new"} {
		_ = os.Remove(filepath.Join(r.Dir, n))
	}
}

// ---- the real process ----

type osProc struct {
	cmd  *exec.Cmd
	done chan struct{}
}

func startIperf(ctx context.Context, bin string, args []string) (IperfProc, error) {
	cmd := exec.Command(bin, args...)
	// A cleared environment: iperf3 needs nothing from this process, least of all anything about the agent.
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, errors.New("iperf3 did not start")
	}
	p := &osProc{cmd: cmd, done: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(p.done) }()
	return p, nil
}

func (p *osProc) Done() <-chan struct{} { return p.done }

func (p *osProc) Stop() {
	select {
	case <-p.done:
		return
	default:
	}
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.done
	}
}
