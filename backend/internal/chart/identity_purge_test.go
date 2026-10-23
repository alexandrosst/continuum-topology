package chart

import (
	"reflect"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	rbacv1 "k8s.io/api/rbac/v1"
)

// identity.purgeOnUninstall: what the post-delete hook is, how little it may do, and that it is not there unless asked for.

const purgeName = "ikhnos-agent-purge-identity"

func docsNamed(docs []doc, name string) map[string]doc {
	out := map[string]doc{}
	for _, d := range docs {
		meta, _ := d["metadata"].(map[string]any)
		if meta != nil && meta["name"] == name {
			out[d["kind"].(string)] = d
		}
	}
	return out
}

func annotationsOf(d doc) map[string]any {
	a, _ := d["metadata"].(map[string]any)["annotations"].(map[string]any)
	return a
}

func TestNothingIsDeletedOnUninstallUnlessAskedFor(t *testing.T) {
	docs := renderDocs(t)
	if got := docsNamed(docs, purgeName); len(got) != 0 {
		t.Fatalf("the purge hook is rendered by default: %v", got)
	}
	// The Secret is still the kept one.
	r := render(t)
	if r.secrets["ikhnos-agent-identity"].Annotations["helm.sh/resource-policy"] != "keep" {
		t.Fatal("the identity Secret lost its keep policy")
	}
}

func TestThePurgeHookIsPostDeleteAndItsRoleCanDeleteOneSecretOnly(t *testing.T) {
	docs := renderDocs(t, "--set", "identity.purgeOnUninstall=true")
	got := docsNamed(docs, purgeName)
	for _, kind := range []string{"ServiceAccount", "Role", "RoleBinding", "Job"} {
		d, ok := got[kind]
		if !ok {
			t.Fatalf("no %s in the purge hook: %v", kind, got)
		}
		a := annotationsOf(d)
		if a["helm.sh/hook"] != "post-delete" {
			t.Errorf("%s: hook = %v, want post-delete (the agent's own ServiceAccount and Role are gone by then, and nothing writes the Secret again)", kind, a["helm.sh/hook"])
		}
		if p, _ := a["helm.sh/hook-delete-policy"].(string); !strings.Contains(p, "hook-succeeded") || !strings.Contains(p, "before-hook-creation") {
			t.Errorf("%s: delete policy = %q: the Role must not outlive a successful purge, and a retry must replace a failed one", kind, p)
		}
	}
	if w := annotationsOf(got["Job"])["helm.sh/hook-weight"]; w != "0" {
		t.Errorf("Job weight = %v", w)
	}
	for _, kind := range []string{"ServiceAccount", "Role", "RoleBinding"} {
		if w := annotationsOf(got[kind])["helm.sh/hook-weight"]; w != "-10" {
			t.Errorf("%s weight = %v: it must exist before the Job starts", kind, w)
		}
	}

	var role rbacv1.Role
	decodeInto(t, got["Role"], &role)
	if len(role.Rules) != 1 {
		t.Fatalf("rules = %+v", role.Rules)
	}
	r := role.Rules[0]
	if !reflect.DeepEqual(r.Resources, []string{"secrets"}) || !reflect.DeepEqual(r.ResourceNames, []string{"ikhnos-agent-identity"}) || !reflect.DeepEqual(r.Verbs, []string{"get", "delete"}) {
		t.Errorf("the purge Role is %+v: it may read and delete the identity Secret by name, and nothing else", r)
	}

	var rb rbacv1.RoleBinding
	decodeInto(t, got["RoleBinding"], &rb)
	if rb.RoleRef.Name != purgeName || len(rb.Subjects) != 1 || rb.Subjects[0].Name != purgeName {
		t.Errorf("binding = %+v", rb)
	}
}

func TestThePurgeJobRunsTheAgentImageAsNonRootWithNothingExtra(t *testing.T) {
	docs := renderDocs(t, "--set", "identity.purgeOnUninstall=true")
	var job batchv1.Job
	decodeInto(t, docsNamed(docs, purgeName)["Job"], &job)
	pod := job.Spec.Template.Spec
	if pod.ServiceAccountName != purgeName {
		t.Errorf("service account = %q", pod.ServiceAccountName)
	}
	if len(pod.Containers) != 1 {
		t.Fatalf("containers = %d", len(pod.Containers))
	}
	c := pod.Containers[0]
	if !reflect.DeepEqual(c.Args, []string{"purge-identity"}) {
		t.Errorf("args = %v", c.Args)
	}
	env := map[string]string{}
	for _, e := range c.Env {
		env[e.Name] = e.Value
	}
	if env["IKHNOS_IDENTITY_SECRET"] != "ikhnos-agent-identity" {
		t.Errorf("secret name = %q", env["IKHNOS_IDENTITY_SECRET"])
	}
	// The same image as the agent's.
	r := render(t, "--set", "identity.purgeOnUninstall=true")
	if want := r.deployments["ikhnos-agent"].Spec.Template.Spec.Containers[0].Image; c.Image != want {
		t.Errorf("image = %q, want the agent's %q", c.Image, want)
	}
	sc := c.SecurityContext
	if sc == nil || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation || sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
		t.Errorf("container security context = %+v", sc)
	}
	if ps := pod.SecurityContext; ps == nil || ps.RunAsNonRoot == nil || !*ps.RunAsNonRoot {
		t.Errorf("pod security context = %+v", ps)
	}
	if pod.RestartPolicy != "OnFailure" || job.Spec.BackoffLimit == nil || job.Spec.ActiveDeadlineSeconds == nil {
		t.Errorf("a stuck purge must fail the uninstall, not hang it: restart %q backoff %v deadline %v", pod.RestartPolicy, job.Spec.BackoffLimit, job.Spec.ActiveDeadlineSeconds)
	}
	// A service mesh sidecar would keep a Job from ever completing.
	if job.Spec.Template.Labels["sidecar.istio.io/inject"] != "false" || job.Spec.Template.Annotations["linkerd.io/inject"] != "disabled" {
		t.Errorf("the Job's pod is open to mesh injection: %v %v", job.Spec.Template.Labels, job.Spec.Template.Annotations)
	}
}

func TestTheAgentsOwnPermissionsAreNotWidenedByThePurgeOption(t *testing.T) {
	off := render(t)
	on := render(t, "--set", "identity.purgeOnUninstall=true")
	for _, name := range []string{"ikhnos-agent-identity"} {
		if !reflect.DeepEqual(off.roles[name], on.roles[name]) {
			t.Errorf("Role %s changed with the option: %+v -> %+v", name, off.roles[name], on.roles[name])
		}
	}
	for name, role := range on.roles {
		if name == purgeName {
			continue
		}
		for _, r := range role.Rules {
			for _, v := range r.Verbs {
				if v == "delete" || v == "*" {
					t.Errorf("Role %s can %s on %v: only the hook's Role may delete", name, v, r.Resources)
				}
			}
		}
	}
}

func TestThePurgeOptionTakesOnlyABoolean(t *testing.T) {
	for _, args := range [][]string{{"--set", "identity.purgeOnUninstall=yes"}, {"--set", "identity.purge=true"}, {"--set", "identity.purgeOnUninstall=1"}} {
		if out, err := helmTemplate(t, args...); err == nil {
			t.Errorf("%v rendered: %.200s", args, out)
		}
	}
}

func TestThePurgeOptionWorksWithNamespacedRBACToo(t *testing.T) {
	docs := renderDocs(t, "--set", "identity.purgeOnUninstall=true", "--set", "rbac.mode=namespaced", "--set", "scope.namespaces={default}", "--set", "access.tier=2")
	if got := docsNamed(docs, purgeName); len(got) != 4 {
		t.Fatalf("hook objects = %d", len(got))
	}
}
