package server

import (
	"strings"
	"testing"

	"continuum/internal/store"
)

// A server told to name a chart that Helm would read as an option does not print a command for it, and it finds that before the
// one-time token exists: the token is shown once, with the command, so a token made and then lost to a refused command is worse than
// no token.
func TestAChartTheCommandCannotNameSpendsNoToken(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleAdmin)
	a.a.ChartRef = "--post-renderer=/tmp/x"

	r := a.do("POST", "/api/v1/tokens", map[string]any{"name": "edge", "tier": 1}, withCookie(cookie))
	if r.Code != 500 {
		t.Fatalf("a chart that cannot be named: %d %s", r.Code, r.Body.String())
	}
	if strings.Contains(r.Body.String(), "post-renderer") {
		t.Errorf("the setting is for the log, not for the caller: %s", r.Body.String())
	}
	tokens, err := a.st.ListTokens(a.ctx, "org-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 0 {
		t.Fatalf("a token was made though its command could not be written: %v", tokens)
	}

	a.a.ChartRef = "chart"
	if r := a.do("POST", "/api/v1/tokens", map[string]any{"name": "edge", "tier": 1}, withCookie(cookie)); r.Code != 201 {
		t.Fatalf("with a chart that can be named: %d %s", r.Code, r.Body.String())
	}
}

// The commands themselves say why they cannot be written, rather than printing one that does something else.
func TestTheAgentCommandsAreRefusedForAnOptionAsChart(t *testing.T) {
	a := testAdmin(t)
	a.ChartRef = "--kubeconfig=/x"
	if cmd, err := a.installCommand(ImageConfig{}, "cnt_1", store.Token{AccessTier: 2}); err == nil {
		t.Errorf("install: %s", cmd)
	}
	if cmd, err := a.upgradeCommand(ImageConfig{}, 2, "", ""); err == nil {
		t.Errorf("upgrade: %s", cmd)
	}
}

// A server that cannot write the commands it exists to print does not start.
func TestAServerThatCannotNameItsChartDoesNotStart(t *testing.T) {
	a := testAdmin(t)
	if err := a.CheckInstallConfig(); err != nil {
		t.Fatal(err)
	}
	a.ChartRef = "--kubeconfig=/x"
	if err := a.CheckInstallConfig(); err == nil {
		t.Fatal("started with a chart it cannot name")
	}
}

// The heartbeat secret is shown once with the upgrade that uses it, so an upgrade that cannot be written is found before the secret is
// minted, not after it is lost.
func TestAnOperatorChartTheCommandCannotNameMintsNoHeartbeatSecret(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleAdmin)
	cl := a.approvedCluster(t, fp)
	id := a.createOperatorDoc(t, cookie, extBody("athens", cl))["operator"].(map[string]any)["id"].(string)

	a.a.ChartRef = "--post-renderer=/tmp/x/continuum-agent" // the operator chart is derived from it
	if r := a.do("POST", "/api/v1/operators/"+id+"/heartbeat", nil, withCookie(cookie)); r.Code != 500 {
		t.Fatalf("a chart that cannot be named: %d %s", r.Code, r.Body.String())
	}
	op, err := a.st.GetOperator(a.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if op.HeartbeatEnabledAt != nil {
		t.Fatal("the heartbeat secret was minted though its upgrade could not be written")
	}
}
