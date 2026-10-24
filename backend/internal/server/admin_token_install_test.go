package server

import (
	"encoding/json"
	"strings"
	"testing"

	"ikhnos/internal/accounts"
	"ikhnos/internal/install"
	"ikhnos/internal/installcfg"
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
	_, admin := a.user(t, "ada", accounts.RoleAdmin)

	_, plain, plainSecret := a.newToken(t, admin, map[string]any{"name": "plain", "tier": 2})
	body := map[string]any{
		"name": "chosen", "tier": 2,
		"collectors": map[string]any{"nodeProbe": true, "flowObserver": true, "measurements": true},
		"scope":      map[string]any{"namespaces": []string{"shop", "pay"}, "exclude": []string{"pay-test"}, "selector": "ikhnos.io/scope=yes"},
	}
	code, chosen, chosenSecret := a.newToken(t, admin, body)
	if code != 201 {
		t.Fatalf("%d %s", code, chosen)
	}
	// Apart from the token, it is the plain command with the choices after it, each on a line of its own, in the order the page always printed.
	want := plain + install.Continued([]install.Value{
		install.Set("nodeProbe.enabled", "true"), install.Set("flowObserver.enabled", "true"), install.Set("measurements.enabled", "true"),
		install.Strings("scope.namespaces", []string{"shop", "pay"}), install.Strings("scope.exclude", []string{"pay-test"}), install.SetString("scope.selector", "ikhnos.io/scope=yes"),
	})
	if got := strings.ReplaceAll(chosen, chosenSecret, plainSecret); got != want {
		t.Fatalf("the command is\n%s\nwant\n%s", got, want)
	}
}

func TestACollectorOrScopeNotChosenIsNotInTheCommand(t *testing.T) {
	a := newAdminRig(t)
	_, admin := a.user(t, "ada", accounts.RoleAdmin)
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
	_, admin := a.user(t, "ada", accounts.RoleAdmin)
	for name, body := range map[string]map[string]any{
		"the node probe at tier 0":                    {"name": "x", "tier": 0, "collectors": map[string]any{"nodeProbe": true}},
		"the traffic observer at tier 1":              {"name": "x", "tier": 1, "collectors": map[string]any{"flowObserver": true}},
		"measurements at tier 1":                      {"name": "x", "tier": 1, "collectors": map[string]any{"measurements": true}},
		"a namespace that is not a name":              {"name": "x", "tier": 1, "scope": map[string]any{"namespaces": []string{"Shop"}}},
		"a namespace that is a shell command":         {"name": "x", "tier": 1, "scope": map[string]any{"namespaces": []string{"a; rm -rf /"}}},
		"an exclusion that is not a name":             {"name": "x", "tier": 1, "scope": map[string]any{"exclude": []string{"$(id)"}}},
		"a selector on a label the agent never reads": {"name": "x", "tier": 1, "scope": map[string]any{"selector": "team=a"}},
		"a selector value that is a command":          {"name": "x", "tier": 1, "scope": map[string]any{"selector": "ikhnos.io/scope=$(id)"}},
		"a selector that is not key=value":            {"name": "x", "tier": 1, "scope": map[string]any{"selector": "ikhnos.io/scope=a=b"}},
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

// Whether uninstalling also deletes the identity Secret is chosen when the agent is installed, because `helm uninstall` takes no values:
// the command says so only when it was asked for, and never otherwise.
func TestAnInstallThatAsksForItsIdentityToBePurgedSaysSoInTheCommand(t *testing.T) {
	a := newAdminRig(t)
	_, admin := a.user(t, "ada", accounts.RoleAdmin)

	_, plain, plainSecret := a.newToken(t, admin, map[string]any{"name": "plain", "tier": 1})
	if strings.Contains(plain, "purgeOnUninstall") {
		t.Fatalf("a plain install carries the purge option:\n%s", plain)
	}
	_, off, _ := a.newToken(t, admin, map[string]any{"name": "off", "tier": 1, "purgeIdentity": false})
	if strings.Contains(off, "purgeOnUninstall") {
		t.Fatalf("an explicit no reached the command:\n%s", off)
	}
	code, on, onSecret := a.newToken(t, admin, map[string]any{"name": "on", "tier": 1, "purgeIdentity": true})
	if code != 201 {
		t.Fatalf("%d %s", code, on)
	}
	want := plain + install.Continued([]install.Value{install.Set("identity.purgeOnUninstall", "true")})
	if got := strings.ReplaceAll(on, onSecret, plainSecret); got != want {
		t.Fatalf("the command is\n%s\nwant\n%s", got, want)
	}
	// It composes with the other choices, after them.
	_, both, _ := a.newToken(t, admin, map[string]any{"name": "both", "tier": 1, "purgeIdentity": true, "collectors": map[string]any{"nodeProbe": true}})
	if i, j := strings.Index(both, "nodeProbe.enabled"), strings.Index(both, "identity.purgeOnUninstall"); i < 0 || j < i {
		t.Fatalf("the purge option should follow the collectors:\n%s", both)
	}
}

// The same choices, written as an IkhnosAgent for the optional controller: what to run, in order.

func adminRigWith(t *testing.T, s installcfg.Settings) *adminRig {
	t.Helper()
	e := newEnv(t)
	a := &Admin{P: NewPlatform(e.base, nil), C: e.base, Settings: s}
	return &adminRig{env: e, h: a.Handler(), a: a}
}

func (a *adminRig) controllerOf(t *testing.T, cookie string, body map[string]any) (doc map[string]any, token, helm string) {
	t.Helper()
	r := a.do("POST", "/api/v1/tokens", body, withCookie(cookie))
	if r.Code != 201 {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	all := r.json(t)
	return all["controller"].(map[string]any), all["token"].(string), all["install"].(string)
}

func TestAControllerViewIsOfferedWhenTheControllersChartCanBeNamed(t *testing.T) {
	a := adminRigWith(t, installcfg.Settings{AgentAddr: "ik.example.com:8443", ImageRegistry: "reg.example/team", ControllerChartVersion: "0.0.0-edge.abc"})
	_, admin := a.user(t, "ada", accounts.RoleAdmin)
	c, token, helm := a.controllerOf(t, admin, map[string]any{"name": "c1", "tier": 1,
		"collectors": map[string]any{"nodeProbe": true}, "scope": map[string]any{"namespaces": []string{"shop"}}, "purgeIdentity": true})
	if c["available"] != true || c["reason"] != "" {
		t.Fatalf("%v", c)
	}
	str := func(k string) string { return c[k].(string) }
	for _, want := range []string{"helm install ikhnos-controller oci://reg.example/team/ikhnos-controller", "--version 0.0.0-edge.abc", "--set maxAccessTier=1",
		"--set image.repository=reg.example/team/controller", "--set agent.image.repository=reg.example/team/ikhnos"} {
		if !strings.Contains(str("install"), want) {
			t.Errorf("%q is not in the controller's install:\n%s", want, str("install"))
		}
	}
	// The cluster is asked first, so a controller that is there is left alone; and a refused ceiling has its own command.
	if !strings.HasPrefix(str("install"), "if helm status ikhnos-controller --namespace ikhnos-system ") || !strings.HasSuffix(str("install"), "\nfi") {
		t.Errorf("the install does not ask the cluster first:\n%s", str("install"))
	}
	if want := "helm upgrade ikhnos-controller oci://reg.example/team/ikhnos-controller --version 0.0.0-edge.abc --namespace ikhnos-system --reset-then-reuse-values --set maxAccessTier=1"; str("raise") != want {
		t.Errorf("the command that raises the ceiling:\n%s", str("raise"))
	}
	for _, want := range []string{"kind: IkhnosAgent", `address: "ik.example.com:8443"`, "accessTier: 1", "nodeProbe: true", `namespaces: ["shop"]`, "purgeIdentityOnDelete: true",
		`repository: "reg.example/team/ikhnos"`, "name: ikhnos-enrollment"} {
		if !strings.Contains(str("manifest"), want) {
			t.Errorf("%q is not in the resource:\n%s", want, str("manifest"))
		}
	}
	if str("manifestCommand") != "kubectl apply -f - <<'YAML'\n"+str("manifest")+"YAML" {
		t.Errorf("the command that applies the resource is not the resource:\n%s", str("manifestCommand"))
	}
	// The token is in the one command that makes the Secret, and in nothing the person might keep or share.
	if !strings.Contains(str("secret"), token) || !strings.Contains(str("secret"), "name: ikhnos-enrollment") {
		t.Errorf("the Secret's command lacks the token:\n%s", str("secret"))
	}
	for _, k := range []string{"install", "manifest", "manifestCommand"} {
		if strings.Contains(str(k), token) {
			t.Errorf("the token is in the controller's %s", k)
		}
	}
	// The pin the helm command carries is the pin the resource carries.
	pin := strings.TrimSuffix(strings.SplitN(strings.SplitN(helm, "enrollment.key=", 2)[1], token, 2)[0], ".")
	if pin == "" || !strings.Contains(str("manifest"), pin) {
		t.Errorf("the resource does not carry the pin %q the helm command does:\n%s", pin, str("manifest"))
	}
	if c["maxTier"] != float64(1) || c["namespace"] != "ikhnos-system" {
		t.Errorf("%v", c)
	}
}

func TestAControllerViewIsNotOfferedWithoutAChartToInstallItFrom(t *testing.T) {
	for name, s := range map[string]installcfg.Settings{
		"no registry":                   {AgentAddr: "x:1"},
		"the chart is a local file":     {AgentAddr: "x:1", ImageRegistry: "reg.example", ChartRef: "local"},
		"an agent chart that is a .tgz": {AgentAddr: "x:1", ChartRef: "./ikhnos-agent-0.1.0.tgz"},
	} {
		a := adminRigWith(t, s)
		_, admin := a.user(t, "ada", accounts.RoleAdmin)
		c, token, helm := a.controllerOf(t, admin, map[string]any{"name": "c", "tier": 2})
		if c["available"] != false || !strings.Contains(c["reason"].(string), "registry") {
			t.Errorf("%s: %v", name, c)
		}
		for _, k := range []string{"install", "raise", "secret", "manifest", "manifestCommand"} {
			if c[k] != "" {
				t.Errorf("%s: %s = %q", name, k, c[k])
			}
		}
		// The helm command, which is the way to install without it, is whole.
		if !strings.Contains(helm, token) {
			t.Errorf("%s: the helm command lost the token", name)
		}
	}
}
