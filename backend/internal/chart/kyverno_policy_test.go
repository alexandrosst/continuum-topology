package chart

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// kyvernoMatch is Kyverno's image-reference wildcard: * stands for any run of characters.
func kyvernoMatch(pattern, ref string) bool {
	re := "^" + strings.ReplaceAll(regexp.QuoteMeta(pattern), `\*`, ".*") + "$"
	return regexp.MustCompile(re).MatchString(ref)
}

// Every image Ikhnos publishes (the agent, the server, the controller and the cluster-link prober) is verified in each form a pod can
// name it. A reference by digest, which is what the install commands print when a digest is given, matched nothing, so the pods that
// were most carefully pinned were admitted unverified.
func TestTheSignaturePolicyCoversEveryImageInEveryForm(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "policy", "kyverno-verify-images.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var policy struct {
		Spec struct {
			Rules []struct {
				VerifyImages []struct {
					ImageReferences []string `json:"imageReferences"`
				} `json:"verifyImages"`
			} `json:"rules"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(raw, &policy); err != nil {
		t.Fatal(err)
	}
	var patterns []string
	for _, r := range policy.Spec.Rules {
		for _, v := range r.VerifyImages {
			patterns = append(patterns, v.ImageReferences...)
		}
	}
	if len(patterns) == 0 {
		t.Fatal("the policy names no image")
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, name := range []string{"ikhnos", "server", "controller", "link"} {
		for form, ref := range map[string]string{
			"bare": "registry.example/acme/" + name, "tag": "registry.example/acme/" + name + ":1.4.0", "digest": "registry.example/acme/" + name + "@" + digest,
			"tag and digest": "registry.example/acme/" + name + ":1.4.0@" + digest,
		} {
			covered := false
			for _, p := range patterns {
				covered = covered || kyvernoMatch(p, ref)
			}
			if !covered {
				t.Errorf("the %s image by %s (%s) is not verified by the policy", name, form, ref)
			}
		}
	}
}
