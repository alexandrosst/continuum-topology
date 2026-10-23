package controller

import (
	"reflect"
	"strings"
	"testing"

	"ikhnos/internal/install"
)

func goodTyped() Spec {
	return Spec{Server: ServerSpec{Address: "ikhnos.example.com:8443", CAPin: "sha256/AbCd=="}}
}

func TestASpecMakesTheValuesTheConnectPageWouldHaveMade(t *testing.T) {
	tier := 1
	s := goodTyped()
	s.AccessTier = &tier
	s.Collectors = install.Collectors{NodeProbe: true}
	s.Scope = install.Scope{Namespaces: []string{"shop", "payments"}, Exclude: []string{"payments-test"}, Selector: "app=web"}
	s.PurgeIdentityOnDelete = true
	s.Image = &ImageSpec{Repository: "registry.example/ikhnos/agent", Tag: "1234"}

	got, err := s.Values("tok3n")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"server":     map[string]any{"address": "ikhnos.example.com:8443", "caPin": "sha256/AbCd=="},
		"enrollment": map[string]any{"token": "tok3n"},
		"access":     map[string]any{"tier": int64(1)},
		"image":      map[string]any{"repository": "registry.example/ikhnos/agent", "tag": "1234"},
		"nodeProbe":  map[string]any{"enabled": true},
		"scope": map[string]any{
			"namespaces": []any{"shop", "payments"},
			"exclude":    []any{"payments-test"},
			"selector":   "app=web",
		},
		"identity": map[string]any{"purgeOnUninstall": true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("values\n got %#v\nwant %#v", got, want)
	}

	// The same choices through the shared helper are the same values: the controller adds nothing of its own to them.
	choices, err := install.AgentChoices(1, s.Collectors, s.Scope, true)
	if err != nil {
		t.Fatal(err)
	}
	via, err := install.HelmValues(choices)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"nodeProbe", "scope", "identity"} {
		if !reflect.DeepEqual(via[k], got[k]) {
			t.Errorf("%s differs from what the shared choices make: %#v vs %#v", k, via[k], got[k])
		}
	}
}

func TestATokenThatLooksLikeANumberOrABooleanStaysAString(t *testing.T) {
	s := goodTyped()
	s.Server.CAPin = "sha256/true"
	for _, tok := range []string{"12345678", "true", "null", "1e3"} {
		v, err := s.Values(tok)
		if err != nil {
			t.Fatal(err)
		}
		if got := v["enrollment"].(map[string]any)["token"]; got != tok {
			t.Errorf("token %q came out as %#v", tok, got)
		}
	}
	v, _ := s.Values("")
	if _, has := v["enrollment"]; has {
		t.Errorf("no token was given and the values still have one: %#v", v["enrollment"])
	}
	if got := v["server"].(map[string]any)["caPin"]; got != "sha256/true" {
		t.Errorf("the pin came out as %#v", got)
	}
}

func TestWhatCannotBeInstalledFromIsSaidInTheResourcesOwnWords(t *testing.T) {
	two, nine := 2, 9
	for _, c := range []struct {
		name string
		edit func(*Spec)
		want string
	}{
		{"no address", func(s *Spec) { s.Server.Address = "" }, "spec.server.address must be host:port"},
		{"no port", func(s *Spec) { s.Server.Address = "ikhnos.example.com" }, "spec.server.address must be host:port"},
		{"port zero", func(s *Spec) { s.Server.Address = "h:0" }, "between 1 and 65535"},
		{"port too big", func(s *Spec) { s.Server.Address = "h:70000" }, "between 1 and 65535"},
		{"no pin", func(s *Spec) { s.Server.CAPin = "" }, "spec.server.caPin"},
		{"pin with a dot", func(s *Spec) { s.Server.CAPin = "sha256/a.b" }, "spec.server.caPin"},
		{"tier nine", func(s *Spec) { s.AccessTier = &nine }, "spec.accessTier must be 0, 1 or 2"},
		{"secret without name", func(s *Spec) { s.Enrollment.TokenSecretRef = &SecretKeyRef{} }, "tokenSecretRef.name"},
		{"observer under tier 1", func(s *Spec) { one := 1; s.AccessTier = &one; s.Collectors.FlowObserver = true }, "needs access tier 2"},
		{"observer fine at 2", func(s *Spec) { s.AccessTier = &two; s.Collectors.FlowObserver = true }, ""},
		{"bad namespace", func(s *Spec) { s.Scope.Namespaces = []string{"Not A Namespace"} }, "namespace"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := goodTyped()
			c.edit(&s)
			all := strings.Join(s.Problems(), "\n")
			if c.want == "" {
				if all != "" {
					t.Fatalf("expected no problems, got %q", all)
				}
				return
			}
			if !strings.Contains(all, c.want) {
				t.Fatalf("problems %q do not say %q", all, c.want)
			}
			if _, err := s.Values("tok"); err == nil {
				t.Fatal("values were made from a spec with a problem")
			}
		})
	}
}

func TestATokenTheChartCouldNotSplitIsRefused(t *testing.T) {
	for _, tok := range []string{"a.b", "a b", "a\nb", "a\x00b", " "} {
		if _, err := goodTyped().Values(tok); err == nil {
			t.Errorf("token %q was accepted", tok)
		}
	}
	if err := ValidToken("abc123_-"); err != nil {
		t.Fatal(err)
	}
}

func TestTheTokenKeyDefaultsToToken(t *testing.T) {
	if (SecretKeyRef{Name: "x"}).TokenKey() != "token" || (SecretKeyRef{Name: "x", Key: "t"}).TokenKey() != "t" {
		t.Fatal("token key")
	}
}
