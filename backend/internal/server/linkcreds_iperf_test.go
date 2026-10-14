package server

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/link"
)

// newOtherKey is the private key of some other cluster.
func newOtherKey(t *testing.T) string {
	t.Helper()
	id, err := newClusterIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return id.Private.Reveal()
}

// iperfRun runs the real iperf3 client the way the prober does (password only in the environment) and returns what it measured, or
// the error iperf3 reported.
func iperfRun(t *testing.T, bin string, port int, user, password, pubFile string) (link.IperfResult, error) {
	t.Helper()
	args := []string{"-c", "127.0.0.1", "-p", strconv.Itoa(port), "-t", "1", "-J"}
	if user != "" {
		args = append(args, "--username", user)
	}
	if pubFile != "" {
		args = append(args, "--rsa-public-key-path", pubFile)
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
	if password != "" {
		cmd.Env = append(cmd.Env, "IPERF3_PASSWORD="+password)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		return link.IperfResult{}, io.ErrNoProgress
	}
	return link.ParseIperf(&out, link.Forward)
}

// The one real end-to-end run: credentials issued by the server for a pair, delivered in the Configs of the two clusters, the server's
// side through the agent's signed endpoint to the responder supervisor running the real iperf3 -s, the client's side used by the real
// iperf3 -c on localhost. A test with the issued credentials works; a wrong password, a wrong user, no credentials, and another
// cluster's key are refused; after a rotation the old password stops working and the new one works.
func TestRealIperf3AuthenticatedRunWithIssuedCredentials(t *testing.T) {
	bin, err := exec.LookPath("iperf3")
	if err != nil {
		t.Skip("iperf3 is not installed")
	}
	cr := newCredRig(t)
	links := []LinkSetting{cr.pair(true)}
	cr.save(t, links...)

	meter := link.NewMeter(link.NewPeers())
	secret := []byte("responder-secret-secret-secret-1")
	meter.ServeIperf(secret)
	if why := meter.SetIperfServer(cr.cfg(cr.second).LinkIperfServer); why != "" {
		t.Fatalf("the agent refused the server credentials: %s", why)
	}
	srv := httptest.NewServer(meter.Handler())
	defer srv.Close()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	runner := link.NewIperfServerRunner(srv.URL, secret, srv.Client())
	runner.Dir = filepath.Join(t.TempDir(), "creds")
	runner.Bin, runner.Port, runner.Every = bin, port, 200*time.Millisecond
	runner.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { runner.Run(ctx); close(stopped) }()
	defer func() { cancel(); <-stopped }()

	listening := func() bool {
		c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
		}
		return err == nil
	}
	waitUntil := func(what string, ok func() bool) {
		t.Helper()
		for end := time.Now().Add(20 * time.Second); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
			if ok() {
				return
			}
		}
		t.Fatalf("timed out waiting for %s", what)
	}
	waitUntil("the authenticated iperf3 server", listening)

	files := t.TempDir()
	pub := func(name, pem string) string {
		p := filepath.Join(files, name)
		if err := os.WriteFile(p, []byte(pem), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	client := func() *continuumv1.LinkIperfClient { return cr.cfg(cr.first).LinkPeers[0].IperfClient }
	c := client()

	// The issued credentials work.
	res, err := iperfRun(t, bin, port, c.Username, c.Password, pub("good.pem", c.PublicKeyPem))
	if err != nil {
		t.Fatalf("a test with the issued credentials failed: %v", err)
	}
	if f, ok := res.Flow(link.Forward); !ok || f.Bits <= 0 {
		t.Fatalf("no throughput measured: %+v", res)
	}

	// Everything else is refused, and a refused attempt does not stop the server for the next one.
	if _, err := iperfRun(t, bin, port, c.Username, c.Password+"x", pub("good.pem", c.PublicKeyPem)); err == nil {
		t.Error("a wrong password was accepted")
	}
	if _, err := iperfRun(t, bin, port, "somebody-else", c.Password, pub("good.pem", c.PublicKeyPem)); err == nil {
		t.Error("a wrong user was accepted")
	}
	if _, err := iperfRun(t, bin, port, "", "", ""); err == nil {
		t.Error("a client with no credentials was accepted: the server is open")
	}
	if _, err := iperfRun(t, bin, port, c.Username, c.Password, pub("wrong.pem", publicOf(t, newOtherKey(t)))); err == nil {
		t.Error("a test encrypted to another cluster's key was accepted")
	}
	if _, err := iperfRun(t, bin, port, c.Username, c.Password, pub("good.pem", c.PublicKeyPem)); err != nil {
		t.Fatalf("the server did not survive refused attempts: %v", err)
	}

	// A rotation: the server hands the agent new credentials, the responder restarts iperf3 with them, the old password is refused.
	if err := cr.core.RotateLinkCredentials(cr.ctx, links, "", cr.first.ClusterID, cr.second.ClusterID); err != nil {
		t.Fatal(err)
	}
	if why := meter.SetIperfServer(cr.cfg(cr.second).LinkIperfServer); why != "" {
		t.Fatalf("the agent refused the rotated credentials: %s", why)
	}
	n := client()
	if n.Password == c.Password {
		t.Fatal("rotation kept the password")
	}
	waitUntil("the new password to work", func() bool {
		_, err := iperfRun(t, bin, port, n.Username, n.Password, pub("good.pem", n.PublicKeyPem))
		return err == nil
	})
	if _, err := iperfRun(t, bin, port, c.Username, c.Password, pub("good.pem", c.PublicKeyPem)); err == nil {
		t.Error("the old password still works after the rotation")
	}

	// Withdrawing the credentials stops the server: it never falls back to an open one.
	meter.SetIperfServer(nil)
	waitUntil("the server to stop when it has no credentials", func() bool { return !listening() })
}
