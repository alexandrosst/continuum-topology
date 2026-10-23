package link

import (
	"bytes"
	"log/slog"
	"os"
	"strings"
	"testing"

	ikhnosv1 "ikhnos/gen/ikhnosv1"
)

func capWithIssuedCredentials(t *testing.T, f *fakeIperf, creds *ikhnosv1.LinkIperfClient, mut func(*Capacity)) (*Capacity, *bytes.Buffer) {
	t.Helper()
	log := &bytes.Buffer{}
	c, _ := capFor(t, f, func(c *Capacity) {
		c.Log = slog.New(slog.NewTextHandler(log, nil))
		c.KeyDir = t.TempDir()
		if mut != nil {
			mut(c)
		}
	})
	lp := peer("cl-a", "A", "10.0.0.1")
	lp.Throughput, lp.IperfClient = true, creds
	c.Meter.Peers.Set("cl-me", []*ikhnosv1.LinkPeer{lp})
	return c, log
}

func TestTheIssuedCredentialsAuthenticateTheTestsAndThePasswordStaysOutOfTheArguments(t *testing.T) {
	f := &fakeIperf{}
	c, log := capWithIssuedCredentials(t, f, clientProto(t), func(c *Capacity) { c.AutoAuth = true })
	settle(c)
	if f.count() == 0 {
		t.Fatal("no test ran")
	}
	_, pub := testKeys(t)
	for i := 0; i < f.count(); i++ {
		args := f.argsOf(i)
		if !strings.Contains(args, "--username "+testUser) || !strings.Contains(args, "--rsa-public-key-path ") {
			t.Fatalf("test %d is not authenticated: %s", i, args)
		}
		mustNotContain(t, "the iperf3 arguments", args)
		if got := strings.Join(f.envs[i], " "); !strings.Contains(got, "IPERF3_PASSWORD="+testPassword) {
			t.Errorf("the password does not reach iperf3 in its environment: %q", got)
		}
	}
	// The public key is in the file the argument names, and nothing else is.
	args := strings.Fields(f.argsOf(0))
	for i, a := range args {
		if a == "--rsa-public-key-path" {
			b, err := os.ReadFile(args[i+1])
			if err != nil || string(b) != pub {
				t.Errorf("key file: %v %q", err, b)
			}
			if st, _ := os.Stat(args[i+1]); st.Mode().Perm() != 0o600 {
				t.Errorf("mode %v", st.Mode().Perm())
			}
		}
	}
	mustNotContain(t, "the log", log.String())
}

// When the install lets Ikhnos issue the credentials, a peer it has issued none for is never tested without authentication.
func TestWithoutIssuedCredentialsNothingIsTestedUnauthenticatedAndTheFailureIsAuth(t *testing.T) {
	f := &fakeIperf{}
	c, log := capWithIssuedCredentials(t, f, nil, func(c *Capacity) { c.AutoAuth = true })
	for range 3 {
		c.step(t.Context())
	}
	if f.count() != 0 {
		t.Fatalf("iperf3 was run %d times without credentials: %s", f.count(), f.argsOf(0))
	}
	out := render(c.Meter)
	n := 0
	for k, v := range out {
		if strings.Contains(k, "throughput_test_failures_total") && strings.Contains(k, `reason="auth"`) && v != "0" {
			n++
		}
	}
	if n == 0 {
		t.Errorf("no auth failure was counted:\n%v", out)
	}
	if got := strings.Count(log.String(), "wait for iperf3 credentials"); got != 1 {
		t.Errorf("the wait was logged %d times, want once:\n%s", got, log.String())
	}
	// Not in auto mode (an install with auth.automatic off): as before, no authentication.
	f2 := &fakeIperf{}
	c2, _ := capWithIssuedCredentials(t, f2, nil, nil)
	settle(c2)
	if f2.count() == 0 || strings.Contains(f2.argsOf(0), "--username") {
		t.Errorf("without auto mode the tests must run as before: %d runs", f2.count())
	}
}

func TestACredentialOfTheInstallsOwnWinsOverTheIssuedOne(t *testing.T) {
	dir := t.TempDir()
	pw := dir + "/password"
	os.WriteFile(pw, []byte("manual-password-0123456789\n"), 0o600)
	f := &fakeIperf{}
	c, _ := capWithIssuedCredentials(t, f, clientProto(t), func(c *Capacity) {
		c.AutoAuth = true
		c.User, c.KeyFile, c.PasswordFile = "manual", dir+"/public.pem", pw
	})
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	settle(c)
	if !strings.Contains(f.argsOf(0), "--username manual --rsa-public-key-path "+dir+"/public.pem") {
		t.Errorf("the install's own credentials were not used: %s", f.argsOf(0))
	}
	if !strings.Contains(strings.Join(f.envs[0], " "), "IPERF3_PASSWORD=manual-password-0123456789") {
		t.Error("the manual password was not used")
	}
}
