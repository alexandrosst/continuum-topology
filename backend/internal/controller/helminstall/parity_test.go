package helminstall

import (
	"reflect"
	"testing"

	"helm.sh/helm/v3/pkg/strvals"
	"k8s.io/client-go/rest"

	"ikhnos/internal/controller"
	"ikhnos/internal/install"
)

// helmReads is what Helm itself makes of the values when they are given as the flags the Connect page prints.
func helmReads(t *testing.T, vs []install.Value) map[string]any {
	t.Helper()
	out := map[string]any{}
	for _, v := range vs {
		var err error
		switch v.Kind {
		case install.Plain:
			err = strvals.ParseInto(v.Key+"="+v.Value, out)
		case install.String:
			err = strvals.ParseIntoString(v.Key+"="+v.Value, out)
		case install.JSON:
			err = strvals.ParseJSON(v.Key+"="+v.Value, out)
		case install.List:
			list := "{"
			for i, it := range v.Items {
				if i > 0 {
					list += ","
				}
				list += it
			}
			err = strvals.ParseInto(v.Key+"="+list+"}", out)
		}
		if err != nil {
			t.Fatalf("Helm cannot read %+v: %v", v, err)
		}
	}
	return out
}

// The controller installs from install.HelmValues; the page prints flags. They are one thing only if Helm reads the flags into what
// HelmValues makes, which this holds to Helm's own parser instead of to a reading of its documentation.
func TestWhatTheControllerInstallsWithIsWhatHelmMakesOfTheFlagsThePagePrints(t *testing.T) {
	cases := map[string][]install.Value{
		"every kind": {
			install.Set("a.flag", "true"), install.Set("a.off", "false"), install.Set("a.n", "2"), install.Set("a.neg", "-7"),
			install.Set("a.word", "hello"), install.SetString("a.numeric", "12345"), install.SetString("a.bool", "true"),
			install.SetString("a.empty", ""), install.SetJSON("a.doc", `{"x":[1,2],"y":{"z":"w"}}`),
			install.Strings("a.names", []string{"one", "two"}), install.Strings("a.none", nil),
		},
		"numbers that are not numbers": {
			install.Set("k.leadingZero", "012"), install.Set("k.zero", "0"), install.Set("k.exp", "1e3"), install.Set("k.dotted", "1.5"),
			install.Set("k.plus", "+3"), install.Set("k.nul", "null"), install.Set("k.upper", "TRUE"), install.Set("k.mixed", "Null"),
			install.Set("k.negzero", "-0"), install.Set("k.big", "99999999999999999999"), install.Set("k.empty", ""), install.Set("k.space", " 4"),
		},
		"later wins, siblings stay": {
			install.Set("s.a", "1"), install.Set("s.b", "2"), install.Set("s.a", "3"), install.SetString("t.x", "1"), install.SetString("t.x", "2"),
		},
	}
	// And the values the choices really make.
	choices, err := install.AgentChoices(2, install.Collectors{NodeProbe: true, FlowObserver: true, Measurements: true},
		install.Scope{Namespaces: []string{"shop", "pay"}, Exclude: []string{"kube-public"}, Selector: "app=web"}, true)
	if err != nil {
		t.Fatal(err)
	}
	cases["the page's own choices"] = append([]install.Value{
		install.Set("server.address", "ikhnos.example.com:8443"), install.SetString("server.caPin", "sha256/AbCd=="),
		install.SetString("enrollment.token", "12345678"), install.Set("access.tier", "2"),
	}, choices...)

	for name, vs := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := install.HelmValues(vs)
			if err != nil {
				t.Fatal(err)
			}
			if want := helmReads(t, vs); !reflect.DeepEqual(got, want) {
				t.Fatalf("HelmValues\n  %#v\nHelm\n  %#v", got, want)
			}
		})
	}
}

func TestTheRealConfigurationNeedsNoClusterToBeMadeAndKeepsReleasesAsSecrets(t *testing.T) {
	cfg, err := Config(&rest.Config{Host: "https://127.0.0.1:1"}, nil)(controller.ReleaseName)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Releases == nil || cfg.KubeClient == nil {
		t.Fatalf("%+v", cfg)
	}
	if d := cfg.Releases.Driver.Name(); d != "Secret" {
		t.Fatalf("releases are kept in %q", d)
	}
}
