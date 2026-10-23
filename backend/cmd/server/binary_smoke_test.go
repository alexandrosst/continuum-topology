package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"ikhnos/internal/store"
)

// A characterization test of the real binary: it builds cmd/server, starts it on loopback with an empty data
// directory, waits until it answers its probes, stops it with SIGTERM and checks what it leaves behind. It
// pins the contract the chart and every deployment rely on (the flags, the probes, the first-start password,
// a clean exit on SIGTERM, a healthy database) which no in-process test reaches, since main and run are not
// callable from one. It needs the Go toolchain and a few seconds, so -short skips it.

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

type runningServer struct {
	cmd    *exec.Cmd
	out    *syncBuffer
	admin  string
	done   chan error
	exited bool
}

type syncBuffer struct {
	mu chan struct{}
	b  bytes.Buffer
}

func newSyncBuffer() *syncBuffer { return &syncBuffer{mu: make(chan struct{}, 1)} }
func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu <- struct{}{}
	defer func() { <-s.mu }()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string {
	s.mu <- struct{}{}
	defer func() { <-s.mu }()
	return s.b.String()
}

func startServer(t *testing.T, bin, dataDir string) *runningServer {
	t.Helper()
	agent, admin := freePort(t), freePort(t)
	cmd := exec.Command(bin, "--data-dir", dataDir, "--agent-address", agent, "--agent-listen", agent, "--admin-listen", admin)
	out := newSyncBuffer()
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	s := &runningServer{cmd: cmd, out: out, admin: admin, done: make(chan error, 1)}
	go func() { s.done <- cmd.Wait() }()
	t.Cleanup(func() {
		if !s.exited {
			_ = cmd.Process.Kill()
			<-s.done
		}
	})
	return s
}

func (s *runningServer) get(path string) (int, string, error) {
	resp, err := http.Get("http://" + s.admin + path)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), nil
}

func (s *runningServer) waitReady(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-s.done:
			s.exited = true
			t.Fatalf("the server exited before it was ready: %v\n%s", err, s.out.String())
		default:
		}
		if code, _, err := s.get("/readyz"); err == nil && code == 200 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("the server was not ready in 30s:\n%s", s.out.String())
}

func (s *runningServer) stop(t *testing.T) {
	t.Helper()
	if err := s.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-s.done:
		s.exited = true
		if err != nil {
			t.Fatalf("SIGTERM must end the server with exit status 0, got %v\n%s", err, s.out.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("the server did not stop within 20s of SIGTERM:\n%s", s.out.String())
	}
}

func TestServerBinaryStartsAnswersProbesAndStopsCleanlyOnSIGTERM(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the real binary")
	}
	bin := filepath.Join(t.TempDir(), "server")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	dataDir := filepath.Join(t.TempDir(), "data")

	// First start: empty data directory.
	s := startServer(t, bin, dataDir)
	s.waitReady(t)
	if code, body, err := s.get("/healthz"); err != nil || code != 200 || !strings.Contains(body, "ok") {
		t.Fatalf("/healthz: %d %q %v", code, body, err)
	}
	logs := s.out.String()
	m := regexp.MustCompile(`one-time password\s+(\S+)`).FindStringSubmatch(logs)
	if m == nil {
		t.Fatalf("the first start must print the one-time admin password:\n%s", logs)
	}
	if !strings.Contains(logs, "ikhnos server started") {
		t.Fatalf("no start-up line:\n%s", logs)
	}
	s.stop(t)
	if !strings.Contains(s.out.String(), "shutting down") {
		t.Fatalf("SIGTERM should be logged as a shutdown:\n%s", s.out.String())
	}

	// What it leaves behind: a healthy database at the current schema, a CA, private permissions.
	db := filepath.Join(dataDir, "ikhnos.db")
	v, err := store.CheckDatabase(context.Background(), db)
	if err != nil || v != store.SchemaVersion {
		t.Fatalf("the database after a clean stop: version %d, %v", v, err)
	}
	for _, p := range []string{dataDir, db} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s is accessible to group or others: %v", p, fi.Mode().Perm())
		}
	}
	if matches, _ := filepath.Glob(filepath.Join(dataDir, "pki", "*")); len(matches) == 0 {
		t.Errorf("no CA material was written under %s/pki", dataDir)
	}

	// Second start on the same data directory: it must not make a second first-account password, and the
	// account the first start made is still there.
	s2 := startServer(t, bin, dataDir)
	s2.waitReady(t)
	if strings.Contains(s2.out.String(), "one-time password") {
		t.Fatalf("a restart must not create the first account again:\n%s", s2.out.String())
	}
	s2.stop(t)
	st, err := store.OpenSQLite(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if n, err := st.CountUsers(context.Background()); err != nil || n != 1 {
		t.Fatalf("users after two starts: %d %v, want 1", n, err)
	}
	if _, err := st.GetUserByName(context.Background(), "admin"); err != nil {
		t.Fatalf("the first account is called admin: %v", err)
	}
}

func TestServerBinaryRefusesToStartWithoutAnAgentAddress(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the real binary")
	}
	bin := filepath.Join(t.TempDir(), "server")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	cmd := exec.Command(bin, "--data-dir", filepath.Join(t.TempDir(), "d"))
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected a refusal, got success:\n%s", out)
	}
	if !strings.Contains(string(out), "--agent-address is required") {
		t.Fatalf("the refusal should say what is missing:\n%s", out)
	}
}
