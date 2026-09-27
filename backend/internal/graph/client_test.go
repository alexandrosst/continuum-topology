package graph

import (
	"errors"
	"net"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"
)

// realDialError builds the exact error shape Go's own net/http stack produces for a failed dial,
// so the test exercises the real chain (*url.Error -> *net.OpError -> *os.SyscallError -> syscall.Errno)
// rather than a simplified stand-in that might not unwrap the same way.
func realDialError(errno syscall.Errno) error {
	return &url.Error{
		Op:  "Post",
		URL: "http://10.42.0.50:7474/tx/commit",
		Err: &net.OpError{
			Op:   "dial",
			Net:  "tcp",
			Addr: &net.TCPAddr{IP: net.ParseIP("10.42.0.51"), Port: 7474},
			Err:  &os.SyscallError{Syscall: "connect", Err: errno},
		},
	}
}

func TestScrubUnwrapsURLError(t *testing.T) {
	dial := realDialError(syscall.EHOSTUNREACH)
	scrubbed := scrub(dial)

	var ue *url.Error
	if errors.As(scrubbed, &ue) {
		t.Fatalf("scrub left a *url.Error wrapper: %v", scrubbed)
	}
	if !strings.Contains(scrubbed.Error(), "no route to host") {
		t.Fatalf("scrubbed error lost the real message, got %q", scrubbed.Error())
	}
}

func TestHostUnreachableHintFiresOnRealEHOSTUNREACHShape(t *testing.T) {
	scrubbed := scrub(realDialError(syscall.EHOSTUNREACH))

	hint := hostUnreachableHint(scrubbed)
	if hint == "" {
		t.Fatalf("expected a hint for EHOSTUNREACH, got none (message was %q)", scrubbed.Error())
	}
	if !strings.Contains(hint, "firewalld") {
		t.Fatalf("hint should point at the firewalld cause, got %q", hint)
	}
}

func TestHostUnreachableHintStaysSilentOnOtherErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"connection refused", scrub(realDialError(syscall.ECONNREFUSED))},
		{"generic error", errors.New("the database answered 500")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if hint := hostUnreachableHint(c.err); hint != "" {
				t.Fatalf("expected no hint for %q, got %q", c.err, hint)
			}
		})
	}
}

func TestHostUnreachableHintMatchesTheActualErrorSeenInProduction(t *testing.T) {
	// This is the literal string that showed up on the History page during a real debugging
	// session (a host firewall - firewalld - blocking a bundled Neo4j pod's own bridge, with no
	// Kubernetes NetworkPolicy involved at all). Pinning it here means a refactor that breaks the
	// wrapping chain, or changes scrub's behavior, fails loudly instead of just losing the hint.
	scrubbed := scrub(realDialError(syscall.EHOSTUNREACH))
	full := ErrUnavailable.Error() + ": " + scrubbed.Error() + hostUnreachableHint(scrubbed)

	const wantSubstr = "dial tcp 10.42.0.51:7474: connect: no route to host"
	if !strings.Contains(full, wantSubstr) {
		t.Fatalf("expected the assembled message to contain %q, got %q", wantSubstr, full)
	}
	if !strings.Contains(full, "https://alexandrosst.github.io/continuum-topology/troubleshooting/common-errors") {
		t.Fatalf("expected the assembled message to link the troubleshooting doc, got %q", full)
	}
}
