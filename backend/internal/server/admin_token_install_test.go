package server

import (
	"encoding/json"
	"strings"
	"testing"

	"continuum/internal/install"
)

// The install command a new token is shown with is written by the server from what the person chose: which per-node collectors to
// switch on and which namespaces the agent may report. The page used to append those flags to the command it was handed.

func (a *adminRig) newToken(t *testing.T, cookie string, body map[string]any) (int, string, string) {
	t.Helper()
	r := a.do("POST", "/api/v1/tokens", body, withCookie(cookie))
	if r.Code != 201 {
		return r.Code, r.Body.String(), ""
	}
	doc := r.json(t)
	return r.Code, doc["install"].(string), doc["token"].(string)
}

func (a *adminRig) tokenCount(t *testing.T, cookie string) int {
	t.Helper()
	r := a.do("GET", "/api/v1/tokens", nil, withCookie(cookie))
	var out []json.RawMessage
	if err := json.Unmarshal(r.Body.Bytes(), &out); err != nil {
		t.Fatalf("not a list: %q", r.Body.String())
	}
	return len(out)
}

func TestATokenIsShownWithTheCollectorsAndScopeChosen(t *testing.T) {
	a := newAdminRig(t)
	_, admin := a.user(t, "ada", RoleAdmin)

	_, plain, plainSecret := a.newToken(t, admin, map[string]any{"name": "plain", "tier": 2})
	body := map[string]any{
		"name": "chosen", "tier": 2,
		"collectors": map[string]any{"nodeProbe": true, "flowObserver": true, "measurements": true},
		"scope":      map[string]any{"namespaces": []string{"shop", "pay"}, "exclude": []string{"pay-test"}, "selector": "continuum.io/scope=yes"},
	}
	code, chosen, chosenSecret := a.newToken(t, admin, body)
	if code != 201 {
		t.Fatalf("%d %s", code, chosen)
	}
	// Apart from the token, it is the plain command with the choices after it, each on a line of its own, in the order the page always printed.
	want := plain + install.Continued([]install.Value{
		install.Set("nodeProbe.enabled", "true"), install.Set("flowObserver.enabled", "true"), install.Set("measurements.enabled", "true"),
		install.Strings("scope.namespaces", []string{"shop", "pay"}), install.Strings("scope.exclude", []string{"pay-test"}), install.SetString("scope.selector", "continuum.io/scope=yes"),
	})
	if got := strings.ReplaceAll(chosen, chosenSecret, plainSecret); got != want {
		t.Fatalf("the command is\n%s\nwant\n%s", got, want)
	}
}

func TestACollectorOrScopeNotChosenIsNotInTheCommand(t *testing.T) {
	a := newAdminRig(t)
	_, admin := a.user(t, "ada", RoleAdmin)
	_, cmd, _ := a.newToken(t, admin, map[string]any{"name": "probe", "tier": 1, "collectors": map[string]any{"nodeProbe": true}})
	if !strings.Contains(cmd, "--set nodeProbe.enabled=true") || strings.Contains(cmd, "flowObserver") || strings.Contains(cmd, "measurements") || strings.Contains(cmd, "scope.") {
		t.Fatalf("only the node probe was chosen:\n%s", cmd)
	}
	// Nothing narrowed is nothing said: an empty scope is the whole cluster, as the chart defaults.
	_, cmd, _ = a.newToken(t, admin, map[string]any{"name": "empty", "tier": 1, "scope": map[string]any{"namespaces": []string{}, "exclude": []string{}, "selector": "  "}})
	if strings.Contains(cmd, "scope.") {
		t.Fatalf("an empty scope reached the command:\n%s", cmd)
	}
}

// A choice that cannot be honoured is refused and spends no token.
func TestACollectorOrScopeThatCannotBeHonouredIsRefusedBeforeAnyTokenExists(t *testing.T) {
	a := newAdminRig(t)
	_, admin := a.user(t, "ada", RoleAdmin)
	for name, body := range map[string]map[string]any{
		"the node probe at tier 0":                    {"name": "x", "tier": 0, "collectors": map[string]any{"nodeProbe": true}},
		"the traffic observer at tier 1":              {"name": "x", "tier": 1, "collectors": map[string]any{"flowObserver": true}},
		"measurements at tier 1":                      {"name": "x", "tier": 1, "collectors": map[string]any{"measurements": true}},
		"a namespace that is not a name":              {"name": "x", "tier": 1, "scope": map[string]any{"namespaces": []string{"Shop"}}},
		"a namespace that is a shell command":         {"name": "x", "tier": 1, "scope": map[string]any{"namespaces": []string{"a; rm -rf /"}}},
		"an exclusion that is not a name":             {"name": "x", "tier": 1, "scope": map[string]any{"exclude": []string{"$(id)"}}},
		"a selector on a label the agent never reads": {"name": "x", "tier": 1, "scope": map[string]any{"selector": "team=a"}},
		"a selector value that is a command":          {"name": "x", "tier": 1, "scope": map[string]any{"selector": "continuum.io/scope=$(id)"}},
		"a selector that is not key=value":            {"name": "x", "tier": 1, "scope": map[string]any{"selector": "continuum.io/scope=a=b"}},
		"a collector nobody knows":                    {"name": "x", "tier": 1, "collectors": map[string]any{"keylogger": true}},
		"a scope field nobody knows":                  {"name": "x", "tier": 1, "scope": map[string]any{"everything": true}},
	} {
		before := a.tokenCount(t, admin)
		if code, out, _ := a.newToken(t, admin, body); code != 400 {
			t.Errorf("%s: %d %s", name, code, out)
		}
		if after := a.tokenCount(t, admin); after != before {
			t.Errorf("%s: a token was created though the request was refused", name)
		}
	}
}
