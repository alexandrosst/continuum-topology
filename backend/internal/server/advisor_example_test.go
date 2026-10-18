package server

import (
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"continuum/internal/accounts"
	"continuum/internal/store"
)

// The example advisor in docs/examples is run against the real handlers, so the script and the API cannot drift apart unseen.
func TestTheExampleAdvisorProposesThroughTheRealAPIAndOnlyProposes(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	script := "../../docs/examples/advisor.py"
	if _, err := os.Stat(script); err != nil {
		t.Fatal(err)
	}
	a := newAdminRig(t)
	_, editor := a.user(t, "edith", accounts.RoleEditor)
	id, _, _ := a.approvedAgent(t, fp) // approved at tier 2
	secret, _ := a.do("POST", "/api/v1/auth/tokens", map[string]string{"name": "advisor"}, withCookie(editor)).json(t)["token"].(string)
	if secret == "" {
		t.Fatal("no API token")
	}

	// The agent's own process has been using about half a core for the last four minutes.
	h := a.hub()
	start := *a.now
	var cpu float64
	h.mu.Lock()
	v := h.viewFor(id)
	for i := 0; i <= 8; i++ {
		at := start.Add(time.Duration(i) * 30 * time.Second)
		cpu += 0.5 * 30
		v.Self.Add(SelfStatsSample{At: at, RSSBytes: 60 << 20, CPUSeconds: cpu, LinkBytesCumulative: uint64(i) * 30000}, at)
	}
	h.mu.Unlock()

	srv := httptest.NewServer(a.h)
	defer srv.Close()
	run := func(args ...string) (string, error) {
		cmd := exec.Command(py, append([]string{"-I", script}, args...)...)
		cmd.Env = []string{"IKHNOS_URL=" + srv.URL, "IKHNOS_ORG=org-1", "IKHNOS_TOKEN=" + secret}
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		return out.String(), err
	}
	pending := func() []store.Proposal {
		ps, _ := a.st.ListProposals(context.Background(), "org-1", store.ProposalQuery{Status: store.ProposalPending})
		return ps
	}

	// a dry run sends nothing
	out, err := run("run", "--dry-run")
	if err != nil || !strings.Contains(out, "would propose") || len(pending()) != 0 {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	// a threshold above the load proposes nothing
	if out, err = run("run", "--threshold", "90"); err != nil || !strings.Contains(out, "nothing to propose") || len(pending()) != 0 {
		t.Fatalf("high threshold: %v\n%s", err, out)
	}

	// the real run proposes once, and changes nothing about the agent
	out, err = run("run")
	if err != nil || !strings.Contains(out, "proposed pr-") {
		t.Fatalf("run: %v\n%s", err, out)
	}
	ps := pending()
	if len(ps) != 1 || ps[0].Advisor != "cpu-guard-example" || ps[0].AgentID != id || ps[0].Widens || ps[0].Confidence != "declared" ||
		!strings.Contains(string(ps[0].Change), `"pause":["flow"]`) || !strings.Contains(string(ps[0].Predicted), "agent_cpu_cores") {
		t.Fatalf("stored: %+v", ps)
	}
	if c := h.consentOf(id); len(c.Paused) != 0 {
		t.Fatalf("the advisor paused something: %+v", c)
	}
	// with one waiting it proposes nothing more
	if out, err = run("run"); err != nil || !strings.Contains(out, "nothing to propose") || len(pending()) != 1 {
		t.Fatalf("second run: %v\n%s", err, out)
	}
	// list and show work
	if out, err = run("list", "--status", "pending"); err != nil || !strings.Contains(out, ps[0].ID) {
		t.Fatalf("list: %v\n%s", err, out)
	}
	if out, err = run("show", ps[0].ID); err != nil || !strings.Contains(out, `"status": "pending"`) {
		t.Fatalf("show: %v\n%s", err, out)
	}
	// once a person accepts it, the collector is paused and the advisor has nothing more to say
	if _, err := h.C.Loop.AcceptProposal(asRole(accounts.RoleEditor), "edith", ps[0].ID, ""); err != nil {
		t.Fatal(err)
	}
	if c := h.consentOf(id); len(c.Paused) != 1 || c.Paused[0] != "flow" {
		t.Fatalf("after accepting: %+v", c)
	}
	if out, err = run("run"); err != nil || !strings.Contains(out, "nothing to propose") {
		t.Fatalf("after accepting: %v\n%s", err, out)
	}
	// a wrong token is a clear failure, not a traceback
	bad := exec.Command(py, "-I", script, "list")
	bad.Env = []string{"IKHNOS_URL=" + srv.URL, "IKHNOS_ORG=org-1", "IKHNOS_TOKEN=nope"}
	var bo bytes.Buffer
	bad.Stdout, bad.Stderr = &bo, &bo
	if err := bad.Run(); err == nil || !strings.Contains(bo.String(), "error: HTTP 401") || strings.Contains(bo.String(), "Traceback") {
		t.Fatalf("bad token: %v\n%s", err, bo.String())
	}
}
