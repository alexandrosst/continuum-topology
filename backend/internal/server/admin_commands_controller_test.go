package server

import (
	"encoding/json"
	"strings"
	"testing"

	"ikhnos/internal/accounts"
	"ikhnos/internal/install"
)

// controllerAnswer is the controller block of the endpoint's answer, read back.
type controllerAnswer struct {
	Controller struct {
		Command                  string   `json:"command"`
		Patch                    string   `json:"patch"`
		NeedsTelemetryPermission bool     `json:"needsTelemetryPermission"`
		Problems                 []string `json:"problems"`
	} `json:"controller"`
}

func (a *adminRig) controllerUpgrade(t *testing.T, cookie string, body any) controllerAnswer {
	t.Helper()
	r := a.do("POST", upgradePath, body, withCookie(cookie))
	if r.Code != 200 {
		t.Fatalf("%d: %s", r.Code, r.Body.String())
	}
	if !strings.Contains(r.Body.String(), `"problems":[]`) {
		t.Fatalf("a list of problems is [] and not null, for the page to read: %s", r.Body.String())
	}
	var out controllerAnswer
	if err := json.Unmarshal(r.Body.Bytes(), &out); err != nil {
		t.Fatalf("not JSON: %q", r.Body.String())
	}
	return out
}

func telemetryDraft(extra func(m map[string]any)) map[string]any {
	m := map[string]any{
		"signals":      map[string]any{"resourceUsage": true},
		"destination":  map[string]any{"endpoint": "collector.example.com:4317", "protocol": "grpc"},
		"energy":       map[string]any{"kind": "bundle-kepler"},
		"accelerators": map[string]any{"kind": "bundle-dcgm"},
		"processing":   map[string]any{"tracesSamplingPercent": 100},
	}
	if extra != nil {
		extra(m)
	}
	return m
}

// The same change, for an agent a controller keeps, is a patch of its resource: the tier alone is a merge patch, and the telemetry is the
// resource's words for what the helm block says in values.
func TestAgentUpgradeCommandAlsoWritesThePatchOfTheResource(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "ed", accounts.RoleEditor)

	out := a.controllerUpgrade(t, cookie, map[string]any{"tier": 1})
	want := `kubectl --namespace ikhnos-system patch ikhnosagents.ikhnos.io this-cluster --type merge -p '{"spec":{"accessTier":1}}'`
	if c := out.Controller; c.Patch != want || c.Command != want || c.NeedsTelemetryPermission || len(c.Problems) != 0 {
		t.Fatalf("a tier: %+v", c)
	}

	// Telemetry on: the draft's resource, as the renderer writes it, and the controller must have been given the permissions for it.
	body := map[string]any{"namespace": "ikhnos", "telemetry": telemetryDraft(nil)}
	out = a.controllerUpgrade(t, cookie, body)
	var tel install.Telemetry
	j, _ := json.Marshal(body["telemetry"])
	if err := json.Unmarshal(j, &tel); err != nil {
		t.Fatal(err)
	}
	rt, err := install.ResourceOf(tel)
	if err != nil || rt == nil {
		t.Fatalf("%v %v", rt, err)
	}
	wantPatch, err := install.ResourcePatch{Namespace: "ikhnos", SetTelemetry: true, Telemetry: rt}.Render()
	if err != nil {
		t.Fatal(err)
	}
	if c := out.Controller; c.Patch != wantPatch || !c.NeedsTelemetryPermission || len(c.Problems) != 0 || !strings.Contains(c.Patch, "--type json") {
		t.Fatalf("telemetry on: %+v\nwant %s", c, wantPatch)
	}

	// Telemetry off removes the resource's: nothing needs the controller's permission for that.
	out = a.controllerUpgrade(t, cookie, map[string]any{"telemetry": map[string]any{"hadTelemetry": true}})
	c := out.Controller
	if c.NeedsTelemetryPermission || !strings.Contains(c.Patch, `"telemetry":null`) || !strings.Contains(c.Patch, "--type merge") {
		t.Fatalf("telemetry off: %+v", c)
	}

	// No tier in the request leaves the resource's as it is, and the chart's default (2) is what cluster links are held to.
	out = a.controllerUpgrade(t, cookie, map[string]any{"telemetry": telemetryDraft(func(m map[string]any) { m["signals"] = map[string]any{"clusterLinks": true} })})
	if c := out.Controller; c.Patch == "" || len(c.Problems) != 0 {
		t.Fatalf("cluster links at the default tier: %+v", c)
	}

	// Neither a tier nor telemetry: nothing to patch, and no problem in that.
	out = a.controllerUpgrade(t, cookie, map[string]any{})
	if c := out.Controller; c.Command != "" || c.Patch != "" || len(c.Problems) != 0 {
		t.Fatalf("nothing to change: %+v", c)
	}
}

// What a resource cannot say is told, and the helm block stays: a regional operator, extra processors, a group left as installed, a
// signal that needs a tier the change does not give.
func TestAgentUpgradeCommandSaysWhatTheResourceCannotState(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "ed", accounts.RoleEditor)
	for name, c := range map[string]struct {
		body map[string]any
		want string
	}{
		"a regional operator": {map[string]any{"telemetry": telemetryDraft(func(m map[string]any) {
			m["destination"] = map[string]any{"endpoint": "collector.example.com:4317", "operator": "eu-1", "protocol": "grpc"}
		})}, "regional operator"},
		"extra processors": {map[string]any{"telemetry": telemetryDraft(func(m map[string]any) {
			m["processing"] = map[string]any{"tracesSamplingPercent": 100, "extra": []any{map[string]any{"kind": "transform", "name": "tidy", "raw": "{}"}}}
		})}, "extra processors"},
		"a group left as installed": {map[string]any{"telemetry": telemetryDraft(func(m map[string]any) {
			m["kept"] = []string{"tags"}
		})}, "left as installed"},
		"cluster links below tier 2": {map[string]any{"tier": 1, "telemetry": telemetryDraft(func(m map[string]any) {
			m["signals"] = map[string]any{"clusterLinks": true}
		})}, "access tier 2"},
	} {
		// The helm block is written for each: it is what the page offers instead.
		if _, h := a.upgrade(t, cookie, c.body); h.Command == "" || len(h.Problems) != 0 {
			t.Fatalf("%s: the helm block: %+v", name, h)
		}
		out := a.controllerUpgrade(t, cookie, c.body)
		if got := out.Controller; got.Patch != "" || got.Command != "" || got.NeedsTelemetryPermission || !strings.Contains(strings.Join(got.Problems, ";"), c.want) {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

// The credential a destination needs is a Secret, made by the same command as the helm block's; the patch names it, and carries no
// credential.
func TestAgentUpgradeCommandPatchesWithTheSecretsOfTheHelmBlock(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "ed", accounts.RoleEditor)
	body := map[string]any{"telemetry": telemetryDraft(func(m map[string]any) {
		m["destination"] = map[string]any{"endpoint": "collector.example.com:4317", "protocol": "grpc", "authSecret": "otlp-token"}
	})}
	code, helm := a.upgrade(t, cookie, body)
	if code != 200 || len(helm.Secrets) != 1 || helm.SecretCommand == "" {
		t.Fatalf("%d %+v", code, helm)
	}
	out := a.controllerUpgrade(t, cookie, body)
	c := out.Controller
	if c.Patch == "" || !strings.HasPrefix(c.Command, helm.SecretCommand) || !strings.HasSuffix(c.Command, c.Patch) || !strings.Contains(c.Patch, `"authSecret":"otlp-token"`) {
		t.Fatalf("%+v", c)
	}
}

// The namespace in the patch is the one the helm block uses: a name that is not a plain one is not pasted.
func TestAgentUpgradeCommandPatchNeverPastesAHostileNamespace(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "ed", accounts.RoleEditor)
	for _, hostile := range []string{"x; rm -rf /", "$(id)", "a b", "`id`", "A", "--set x=1", "x\ny"} {
		out := a.controllerUpgrade(t, cookie, map[string]any{"tier": 1, "namespace": hostile})
		if p := out.Controller.Patch; strings.Contains(p, hostile) || !strings.HasPrefix(p, "kubectl --namespace ikhnos-system patch ") {
			t.Errorf("%q reached the patch: %q", hostile, p)
		}
	}
}
