package controller

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"ikhnos/internal/install"
	"ikhnos/internal/installcfg"
	"ikhnos/internal/kernel"
)

// The Connect page writes an IkhnosAgent for the controller from the same choices as the `helm install` it prints. These tests hold the
// two to each other and the resource to this package: the page's words are the API's, and the agent that results is the one the helm
// command would have made.

type choice struct {
	name       string
	tier       int
	collectors install.Collectors
	scope      install.Scope
	purge      bool
	img        kernel.ImageConfig
}

func choices() []choice {
	digest := "sha256:" + strings.Repeat("c", 64)
	return []choice{
		{name: "nothing chosen", tier: 2},
		{name: "tier 0", tier: 0},
		{name: "tier 1 with the node probe", tier: 1, collectors: install.Collectors{NodeProbe: true}},
		{name: "everything on", tier: 2, collectors: install.Collectors{NodeProbe: true, FlowObserver: true, Measurements: true}},
		{name: "a narrowed scope", tier: 2, scope: install.Scope{Namespaces: []string{"shop", "pay"}, Exclude: []string{"pay-test"}, Selector: "ikhnos.io/scope=yes"}},
		{name: "identity purged", tier: 1, purge: true},
		{name: "a registry", tier: 2, img: kernel.ImageConfig{Registry: "reg.example/team"}},
		{name: "a registry, tag and digest", tier: 1, img: kernel.ImageConfig{Registry: "reg.example/team", Tag: "v1.2.3", Digest: digest}},
		{name: "all of it", tier: 2, collectors: install.Collectors{NodeProbe: true, FlowObserver: true, Measurements: true},
			scope: install.Scope{Namespaces: []string{"a"}, Exclude: []string{"b"}, Selector: "app=x"}, purge: true,
			img: kernel.ImageConfig{Registry: "reg.example", Tag: "edge"}},
	}
}

const (
	testPin   = "sha256/AbCdEf+/0123456789="
	testToken = "ikh_0123456789abcdef"
	testAddr  = "ik.example.com:8443"
)

func manifestFor(t *testing.T, c choice) (spec Spec, doc map[string]any, text string) {
	t.Helper()
	res := install.AgentResource{Server: testAddr, CAPin: testPin, Tier: c.tier, Collectors: c.collectors, Scope: c.scope, PurgeIdentity: c.purge}
	if c.img.Configured() {
		res.Image = &install.ResourceImage{Repository: c.img.Registry + "/ikhnos", Tag: c.img.Tag, Digest: c.img.Digest}
	}
	text, err := res.Manifest(install.DefaultNamespace)
	if err != nil {
		t.Fatalf("%s: %v", c.name, err)
	}
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatalf("%s: not YAML: %v\n%s", c.name, err, text)
	}
	// Strictly: a field this package does not have is an error, not something dropped.
	var whole struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct{ Name, Namespace string }
		Spec       Spec `json:"spec"`
	}
	if err := yaml.UnmarshalStrict([]byte(text), &whole); err != nil {
		t.Fatalf("%s: the controller cannot read what the page writes: %v\n%s", c.name, err, text)
	}
	if whole.APIVersion != Group+"/"+Version || whole.Kind != Kind {
		t.Errorf("%s: %s %s, the resource is %s/%s %s", c.name, whole.APIVersion, whole.Kind, Group, Version, Kind)
	}
	if whole.Metadata.Namespace != install.DefaultNamespace {
		t.Errorf("%s: namespace %q", c.name, whole.Metadata.Namespace)
	}
	return whole.Spec, doc, text
}

func TestTheResourceThePageWritesIsTheAgentTheHelmCommandWouldMake(t *testing.T) {
	for _, c := range choices() {
		t.Run(c.name, func(t *testing.T) {
			spec, _, text := manifestFor(t, c)
			if p := spec.Problems(); len(p) > 0 {
				t.Fatalf("the controller would refuse it: %v\n%s", p, text)
			}
			s := installcfg.Settings{AgentAddr: testAddr}
			extra, err := install.AgentChoices(c.tier, c.collectors, c.scope, c.purge)
			if err != nil {
				t.Fatal(err)
			}
			fromHelm, err := install.HelmValues(s.AgentValues(c.img, testPin, testToken, c.tier, extra...))
			if err != nil {
				t.Fatal(err)
			}
			fromSpec, err := spec.Values(testToken)
			if err != nil {
				t.Fatal(err)
			}
			// The two say where the server is and who may enrol in different words (enrollment.key packs the pin and the token; the
			// resource keeps the pin as its own value and reads the token from a Secret). Everything else must be the same.
			if got := fromHelm["enrollment"].(map[string]any)["key"]; got != testPin+"."+testToken {
				t.Fatalf("the helm command's key is %v", got)
			}
			delete(fromHelm, "enrollment")
			delete(fromSpec, "enrollment")
			srvH, srvS := fromHelm["server"].(map[string]any), fromSpec["server"].(map[string]any)
			if srvS["caPin"] != testPin || srvS["address"] != srvH["address"] || srvH["address"] != testAddr {
				t.Fatalf("the server: helm %v, resource %v", srvH, srvS)
			}
			delete(srvS, "caPin")
			if !reflect.DeepEqual(fromHelm, fromSpec) {
				t.Fatalf("the helm command and the resource install different agents\nhelm:     %v\nresource: %v\n%s", fromHelm, fromSpec, text)
			}
		})
	}
}

// The resource names the Secret the page's command creates, and the key in it, as defaults the controller reads.
func TestTheSecretThePageCreatesIsTheOneTheResourceNames(t *testing.T) {
	spec, _, _ := manifestFor(t, choices()[0])
	ref := spec.Enrollment.TokenSecretRef
	if ref == nil || ref.Name != install.ControllerTokenSecret || ref.TokenKey() != install.ControllerTokenKey {
		t.Fatalf("the resource reads %+v, the page creates %s/%s", ref, install.ControllerTokenSecret, install.ControllerTokenKey)
	}
	if ReleaseName != "ikhnos-agent" || install.ControllerRelease != "ikhnos-controller" {
		t.Errorf("release names %s, %s", ReleaseName, install.ControllerRelease)
	}
	if install.ResourceAPIVersion != Group+"/"+Version || install.ResourceKind != Kind {
		t.Errorf("the page writes %s %s, the resource is %s/%s %s", install.ResourceAPIVersion, install.ResourceKind, Group, Version, Kind)
	}
}

// A field the page writes that the definition does not have would be pruned by the API server, and the choice lost without a word.
func TestEveryFieldThePageWritesIsInTheDefinition(t *testing.T) {
	specSchema, _, _, _ := loadCRD(t)
	for _, c := range choices() {
		_, doc, _ := manifestFor(t, c)
		var missing []string
		var check func(path string, v any, schema map[string]any)
		check = func(path string, v any, schema map[string]any) {
			obj, ok := v.(map[string]any)
			if !ok {
				return
			}
			props, _ := schema["properties"].(map[string]any)
			for k, child := range obj {
				p, ok := props[k].(map[string]any)
				if !ok {
					missing = append(missing, path+"."+k)
					continue
				}
				check(path+"."+k, child, p)
			}
		}
		check("spec", doc["spec"], specSchema)
		sort.Strings(missing)
		if len(missing) > 0 {
			t.Errorf("%s: not in the definition: %v", c.name, missing)
		}
		if req, _ := specSchema["required"].([]any); len(req) > 0 {
			for _, r := range req {
				if _, ok := doc["spec"].(map[string]any)[fmt.Sprint(r)]; !ok {
					t.Errorf("%s: the definition requires spec.%v and the page does not write it", c.name, r)
				}
			}
		}
	}
}
