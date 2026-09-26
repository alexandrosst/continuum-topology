package main

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"continuum/internal/server"
	"continuum/internal/store"

	_ "modernc.org/sqlite"
)

func TestRegistrationDefaultFollowsTheListener(t *testing.T) {
	cases := []struct {
		explicit, listen, want string
	}{
		{"", "127.0.0.1:8080", server.RegOpen},
		{"", "localhost:8080", server.RegOpen},
		{"", "[::1]:8080", server.RegOpen},
		{"", "0.0.0.0:8080", server.RegInvite},
		{"", ":8080", server.RegInvite},
		{"", "[::]:8080", server.RegInvite},
		{"", "10.1.2.3:8080", server.RegInvite},
		{"", "admin.example.com:8080", server.RegInvite},
		// an explicit value always wins, in both directions
		{server.RegOpen, "0.0.0.0:8080", server.RegOpen},
		{server.RegClosed, "127.0.0.1:8080", server.RegClosed},
		{server.RegInvite, "127.0.0.1:8080", server.RegInvite},
	}
	for _, c := range cases {
		got, why := resolveRegistration(c.explicit, c.listen)
		if got != c.want || why == "" {
			t.Errorf("registration(%q, %q) = %q (%s), want %q", c.explicit, c.listen, got, why, c.want)
		}
	}
	if _, why := resolveRegistration("", "0.0.0.0:1"); !strings.Contains(why, "not loopback") || !strings.Contains(why, "--registration open") {
		t.Errorf("the log line should say why and how to change it: %q", why)
	}
}

func TestVerifyAuditReportsTheFirstBrokenLink(t *testing.T) {
	dir := t.TempDir()
	st, err := store.OpenSQLite(filepath.Join(dir, "continuum.db"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if err := st.AddAudit(context.Background(), store.AuditEvent{At: time.Now(), OrgID: "o", Actor: "a", Action: "x", TargetKind: "k", TargetID: "t", Detail: strings.Repeat("d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()

	var out, errw bytes.Buffer
	if code := verifyAudit([]string{"--data-dir", dir}, &out, &errw); code != 0 || !strings.Contains(out.String(), "OK") || !strings.Contains(out.String(), "chain head:") {
		t.Fatalf("intact: %d %s %s", code, out.String(), errw.String())
	}

	// tamper directly in the file, as an attacker with the database would
	raw, err := sql.Open("sqlite", filepath.Join(dir, "continuum.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`UPDATE audit SET detail='forged' WHERE id=3`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	out.Reset()
	if code := verifyAudit([]string{"--data-dir", dir}, &out, &errw); code != 1 || !strings.Contains(out.String(), "BROKEN at audit row 3") {
		t.Fatalf("tampered: %d %s", code, out.String())
	}
	if code := verifyAudit([]string{"--data-dir", filepath.Join(dir, "missing")}, &out, &errw); code != 2 {
		t.Fatalf("no database: %d", code)
	}
}

func TestSSOHeaderRequiresTheProxyFlag(t *testing.T) {
	if err := checkSSOHeader("", false); err != nil {
		t.Errorf("SSO off: %v", err)
	}
	if err := checkSSOHeader("", true); err != nil {
		t.Errorf("SSO off, behind a proxy anyway: %v", err)
	}
	if err := checkSSOHeader("X-Remote-User", true); err != nil {
		t.Errorf("SSO on, behind a proxy: %v", err)
	}
	err := checkSSOHeader("X-Remote-User", false)
	if err == nil {
		t.Fatal("SSO on without --admin-behind-tls-proxy should be refused: a header could be forged directly")
	}
	if !strings.Contains(err.Error(), "--admin-behind-tls-proxy") {
		t.Errorf("the refusal should name the flag: %v", err)
	}
}

func TestNeo4jCredentialsOverPlainHTTPNeedAnExplicitFlagOffLoopback(t *testing.T) {
	log, _ := testLog()
	for _, c := range []struct {
		url   string
		allow bool
		ok    bool
	}{
		{"http://127.0.0.1:7474", false, true},
		{"http://localhost:7474", false, true},
		{"http://localhost", false, true},
		{"http://[::1]:7474", false, true},
		{"https://neo4j.example.com:7473", false, true},
		{"http://neo4j.example.com:7474", false, false},
		{"http://neo4j:7474", false, false},
		{"http://10.0.0.5:7474", false, false},
		{"http://localhost.example.com:7474", false, false},
		{"http://neo4j.example.com:7474", true, true},
		{"http://10.0.0.5:7474", true, true},
	} {
		err := checkNeo4jTransport(log, c.url, c.allow)
		if (err == nil) != c.ok {
			t.Errorf("%s allow=%v: err=%v, want ok=%v", c.url, c.allow, err, c.ok)
		}
		if err != nil && !strings.Contains(err.Error(), "--neo4j-allow-insecure-http") {
			t.Errorf("the refusal should name the flag: %v", err)
		}
	}
}
