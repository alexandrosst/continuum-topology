package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

const ns = "ikhnos-system"

// fakeInstaller is a release store that does what the Installer says and records what it was asked.
type fakeInstaller struct {
	mu       sync.Mutex
	chart    string
	rel      *Release
	applied  []map[string]any
	removed  int
	events   []string
	applyErr error
	rmErr    error
}

func newInstaller() *fakeInstaller { return &fakeInstaller{chart: "0.1.0"} }

func (f *fakeInstaller) ChartVersion() string { return f.chart }

func (f *fakeInstaller) Current(_ context.Context, namespace, release string) (*Release, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if namespace != ns || release != ReleaseName {
		return nil, fmt.Errorf("asked for %s/%s", namespace, release)
	}
	if f.rel == nil {
		return nil, nil
	}
	c := *f.rel
	return &c, nil
}

func (f *fakeInstaller) Apply(_ context.Context, namespace, release string, values map[string]any) (*Release, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if namespace != ns || release != ReleaseName {
		return nil, fmt.Errorf("applied to %s/%s", namespace, release)
	}
	if f.applyErr != nil {
		return nil, f.applyErr
	}
	rev := 1
	if f.rel != nil {
		rev = f.rel.Revision + 1
	}
	// The values come back as JSON would give them, as they do from Helm's release record.
	f.rel = &Release{Revision: rev, ChartVersion: f.chart, Deployed: true, Values: roundTrip(values)}
	f.applied = append(f.applied, values)
	f.events = append(f.events, "apply")
	c := *f.rel
	return &c, nil
}

func (f *fakeInstaller) Uninstall(_ context.Context, namespace, release string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rmErr != nil {
		return f.rmErr
	}
	f.rel = nil
	f.removed++
	f.events = append(f.events, "uninstall")
	return nil
}

func (f *fakeInstaller) lastApplied() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.applied) == 0 {
		return nil
	}
	return f.applied[len(f.applied)-1]
}

func (f *fakeInstaller) applies() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.applied)
}

func roundTrip(m map[string]any) map[string]any {
	// Numbers become float64, as in a stored release.
	out := map[string]any{}
	for k, v := range m {
		out[k] = roundTripValue(v)
	}
	return out
}

func roundTripValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return roundTrip(t)
	case []any:
		o := make([]any, len(t))
		for i, x := range t {
			o[i] = roundTripValue(x)
		}
		return o
	case int64:
		return float64(t)
	}
	return v
}

type rig struct {
	t    *testing.T
	dyn  *dynamicfake.FakeDynamicClient
	kube *kubefake.Clientset
	inst *fakeInstaller
	r    *Reconciler
	nmu  sync.Mutex
	now  time.Time
}

func newRig(t *testing.T) *rig {
	t.Helper()
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{GVR: "IkhnosAgentList"})
	g := &rig{t: t, dyn: dyn, kube: kubefake.NewSimpleClientset(), inst: newInstaller(), now: time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)}
	g.r = &Reconciler{Dyn: dyn, Kube: g.kube, Installer: g.inst, Namespace: ns, Now: g.clock}
	return g
}

// clock is the rig's time; a loop that is running reads it while a test moves it.
func (g *rig) clock() time.Time {
	g.nmu.Lock()
	defer g.nmu.Unlock()
	return g.now
}

func (g *rig) advance(d time.Duration) {
	g.nmu.Lock()
	defer g.nmu.Unlock()
	g.now = g.now.Add(d)
}

func goodSpec() map[string]any {
	return map[string]any{
		"server":     map[string]any{"address": "ikhnos.example.com:8443", "caPin": "sha256/AbCd=="},
		"enrollment": map[string]any{"tokenSecretRef": map[string]any{"name": "enroll"}},
	}
}

func (g *rig) secret(name, key, value string) {
	g.t.Helper()
	_, err := g.kube.CoreV1().Secrets(ns).Create(context.Background(), &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Data: map[string][]byte{key: []byte(value)}}, metav1.CreateOptions{})
	if err != nil {
		g.t.Fatal(err)
	}
}

func (g *rig) agent(name string, spec map[string]any, edit ...func(*unstructured.Unstructured)) {
	g.t.Helper()
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": Group + "/" + Version, "kind": Kind,
		"metadata": map[string]any{"name": name, "namespace": ns, "generation": int64(1)},
		"spec":     spec,
	}}
	u.SetCreationTimestamp(metav1.NewTime(g.clock()))
	for _, e := range edit {
		e(u)
	}
	if _, err := g.dyn.Resource(GVR).Namespace(ns).Create(context.Background(), u, metav1.CreateOptions{}); err != nil {
		g.t.Fatal(err)
	}
}

func (g *rig) get(name string) *unstructured.Unstructured {
	g.t.Helper()
	u, err := g.dyn.Resource(GVR).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		g.t.Fatal(err)
	}
	return u
}

func (g *rig) setSpec(name string, spec map[string]any) {
	g.t.Helper()
	u := g.get(name)
	u.Object["spec"] = spec
	u.SetGeneration(u.GetGeneration() + 1)
	if _, err := g.dyn.Resource(GVR).Namespace(ns).Update(context.Background(), u, metav1.UpdateOptions{}); err != nil {
		g.t.Fatal(err)
	}
}

func (g *rig) reconcile(name string) (time.Duration, error) {
	g.t.Helper()
	return g.r.Reconcile(context.Background(), name)
}

func (g *rig) must(name string) time.Duration {
	g.t.Helper()
	d, err := g.reconcile(name)
	if err != nil {
		g.t.Fatalf("reconcile %s: %v", name, err)
	}
	return d
}

func (g *rig) status(name string) Status {
	g.t.Helper()
	var s Status
	raw, _ := g.get(name).Object["status"].(map[string]any)
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &s); err != nil {
		g.t.Fatal(err)
	}
	return s
}

func (g *rig) statusWrites() int {
	n := 0
	for _, a := range g.dyn.Actions() {
		if a.GetVerb() == "update" && a.GetSubresource() == "status" {
			n++
		}
	}
	return n
}

func (g *rig) delete(name string) {
	g.t.Helper()
	u := g.get(name)
	now := metav1.NewTime(g.clock())
	u.SetDeletionTimestamp(&now)
	if _, err := g.dyn.Resource(GVR).Namespace(ns).Update(context.Background(), u, metav1.UpdateOptions{}); err != nil {
		g.t.Fatal(err)
	}
}

func finalizers(u *unstructured.Unstructured) []string { return u.GetFinalizers() }

func TestAnAgentIsInstalledOnceWithTheValuesOfItsSpecAndItsToken(t *testing.T) {
	g := newRig(t)
	g.secret("enroll", "token", " ikh_abc\n")
	spec := goodSpec()
	spec["accessTier"] = int64(1)
	spec["collectors"] = map[string]any{"nodeProbe": true}
	spec["scope"] = map[string]any{"namespaces": []any{"shop"}}
	g.agent("a", spec)

	g.must("a")
	if got := finalizers(g.get("a")); len(got) != 1 || got[0] != Finalizer {
		t.Fatalf("finalizers %v", got)
	}
	v := g.inst.lastApplied()
	want := map[string]any{
		"server":     map[string]any{"address": "ikhnos.example.com:8443", "caPin": "sha256/AbCd=="},
		"enrollment": map[string]any{"token": "ikh_abc"},
		"access":     map[string]any{"tier": int64(1)},
		"nodeProbe":  map[string]any{"enabled": true},
		"scope":      map[string]any{"namespaces": []any{"shop"}},
	}
	if fmt.Sprint(v) != fmt.Sprint(want) {
		t.Fatalf("installed with\n%v\nwant\n%v", v, want)
	}
	st := g.status("a")
	if st.Phase != PhaseReady || st.Release == nil || st.Release.Revision != 1 || st.Release.ChartVersion != "0.1.0" || st.ObservedGeneration != 1 || st.ValuesHash != Hash(v) {
		t.Fatalf("status %+v", st)
	}
	if len(st.Conditions) != 1 || st.Conditions[0].Type != "Ready" || st.Conditions[0].Status != "True" {
		t.Fatalf("conditions %+v", st.Conditions)
	}

	// Looked at again with nothing changed: nothing is installed and nothing is written.
	writes := g.statusWrites()
	g.must("a")
	g.must("a")
	if g.inst.applies() != 1 {
		t.Errorf("applied %d times", g.inst.applies())
	}
	if g.statusWrites() != writes {
		t.Errorf("the status was written %d more times with nothing changed", g.statusWrites()-writes)
	}
}

func TestAChangedSpecOrChartUpgradesTheReleaseAndNothingElseDoes(t *testing.T) {
	g := newRig(t)
	g.secret("enroll", "token", "ikh_abc")
	g.agent("a", goodSpec())
	g.must("a")

	spec := goodSpec()
	spec["collectors"] = map[string]any{"nodeProbe": true}
	spec["accessTier"] = int64(2)
	g.setSpec("a", spec)
	g.must("a")
	if g.inst.applies() != 2 || g.status("a").Release.Revision != 2 {
		t.Fatalf("a changed spec: %d applies, status %+v", g.inst.applies(), g.status("a"))
	}

	g.inst.chart = "0.2.0"
	g.must("a")
	if g.inst.applies() != 3 || g.status("a").Release.ChartVersion != "0.2.0" {
		t.Fatalf("a new chart: %d applies, status %+v", g.inst.applies(), g.status("a"))
	}

	// A release that did not complete is tried again.
	g.inst.mu.Lock()
	g.inst.rel.Deployed = false
	g.inst.mu.Unlock()
	g.must("a")
	if g.inst.applies() != 4 {
		t.Fatalf("a release that is not deployed was left: %d applies", g.inst.applies())
	}
}

func TestWithoutATokenSecretNothingIsInstalledYetAndItIsLookedForAgain(t *testing.T) {
	g := newRig(t)
	g.agent("a", goodSpec()) // the Secret it names does not exist
	d := g.must("a")
	if d != WaitingForSecret || g.inst.applies() != 0 {
		t.Fatalf("waited %v, applied %d", d, g.inst.applies())
	}
	st := g.status("a")
	if st.Phase != PhasePending || st.Conditions[0].Reason != "WaitingForToken" || st.Conditions[0].Status != "False" {
		t.Fatalf("status %+v", st)
	}

	// A Secret without the key is told apart.
	g.secret("enroll", "other", "x")
	g.must("a")
	if m := g.status("a").Conditions[0].Message; m != `the Secret "enroll" has no key "token"` {
		t.Errorf("message %q", m)
	}

	if err := g.kube.CoreV1().Secrets(ns).Delete(context.Background(), "enroll", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	g.secret("enroll", "token", "ikh_abc")
	if d := g.must("a"); d != 0 || g.inst.applies() != 1 || g.status("a").Phase != PhaseReady {
		t.Fatalf("after the Secret came: %v, %d applies, %+v", d, g.inst.applies(), g.status("a"))
	}
}

func TestOnceEnrolledTheTokenMayGoAndTheReleaseLosesItAndStaysInstalled(t *testing.T) {
	g := newRig(t)
	g.secret("enroll", "token", "ikh_abc")
	g.agent("a", goodSpec())
	g.must("a")
	if err := g.kube.CoreV1().Secrets(ns).Delete(context.Background(), "enroll", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	g.must("a")
	if _, has := g.inst.lastApplied()["enrollment"]; has || g.status("a").Phase != PhaseReady || g.inst.removed != 0 {
		t.Fatalf("values %v, status %+v, removed %d", g.inst.lastApplied(), g.status("a"), g.inst.removed)
	}
}

func TestASpecThatCannotBeInstalledFromIsSaidAndNothingIsInstalled(t *testing.T) {
	g := newRig(t)
	g.secret("enroll", "token", "ikh_abc")
	for name, edit := range map[string]func(map[string]any){
		"no port": func(s map[string]any) {
			s["server"] = map[string]any{"address": "ikhnos.example.com", "caPin": "sha256/x"}
		},
		"no pin":             func(s map[string]any) { s["server"] = map[string]any{"address": "a.example:1", "caPin": ""} },
		"a tier that is not": func(s map[string]any) { s["accessTier"] = int64(3) },
		"a probe at tier 0": func(s map[string]any) {
			s["accessTier"] = int64(0)
			s["collectors"] = map[string]any{"nodeProbe": true}
		},
		"a namespace not one":    func(s map[string]any) { s["scope"] = map[string]any{"namespaces": []any{"Shop"}} },
		"a spec of another kind": func(s map[string]any) { s["collectors"] = "all" },
	} {
		s := goodSpec()
		edit(s)
		n := "x"
		g.agent(n, s)
		g.must(n)
		st := g.status(n)
		if st.Phase != PhaseInvalid || st.Conditions[0].Reason != "InvalidSpec" || st.Conditions[0].Message == "" || g.inst.applies() != 0 {
			t.Errorf("%s: status %+v, %d applies", name, st, g.inst.applies())
		}
		if err := g.dyn.Resource(GVR).Namespace(ns).Delete(context.Background(), n, metav1.DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	// The same resource, corrected, installs.
	g.agent("a", func() map[string]any { s := goodSpec(); s["accessTier"] = int64(9); return s }())
	g.must("a")
	g.setSpec("a", goodSpec())
	g.must("a")
	if g.status("a").Phase != PhaseReady || g.inst.applies() != 1 {
		t.Fatalf("corrected: %+v, %d applies", g.status("a"), g.inst.applies())
	}
}

func TestAnInstallThatFailsIsReportedAndRetriedAndNothingIsHiddenInTheStatus(t *testing.T) {
	g := newRig(t)
	g.secret("enroll", "token", "ikh_abc")
	g.agent("a", goodSpec())
	g.inst.applyErr = errors.New("rendering the chart: boom")
	if _, err := g.reconcile("a"); err == nil {
		t.Fatal("a failed install was not returned for a retry")
	}
	st := g.status("a")
	if st.Phase != PhaseFailed || st.Conditions[0].Reason != "InstallFailed" || st.Conditions[0].Message != "rendering the chart: boom" {
		t.Fatalf("status %+v", st)
	}
	g.inst.applyErr = nil
	g.must("a")
	if g.status("a").Phase != PhaseReady {
		t.Fatalf("after the retry: %+v", g.status("a"))
	}
}

func TestDeletingAnAgentUninstallsItsReleaseAndOnlyThenLetsItGo(t *testing.T) {
	g := newRig(t)
	g.secret("enroll", "token", "ikh_abc")
	g.agent("a", goodSpec())
	g.must("a")

	g.inst.rmErr = errors.New("cluster unreachable")
	g.delete("a")
	if _, err := g.reconcile("a"); err == nil {
		t.Fatal("an uninstall that failed was not returned for a retry")
	}
	if len(finalizers(g.get("a"))) != 1 || g.inst.rel == nil {
		t.Fatal("it was let go with the release still installed")
	}
	g.inst.rmErr = nil
	g.must("a")
	if g.inst.removed != 1 || g.inst.rel != nil || len(finalizers(g.get("a"))) != 0 {
		t.Fatalf("removed %d, finalizers %v", g.inst.removed, finalizers(g.get("a")))
	}
	if g.status("a").Phase != PhaseDeleting {
		t.Errorf("phase %q", g.status("a").Phase)
	}
}

func TestAskingForTheIdentityToGoIsAppliedBeforeTheUninstallThatRunsItsHook(t *testing.T) {
	g := newRig(t)
	g.secret("enroll", "token", "ikh_abc")
	g.agent("a", goodSpec())
	g.must("a") // installed without the purge
	spec := goodSpec()
	spec["purgeIdentityOnDelete"] = true
	g.setSpec("a", spec)
	g.delete("a") // deleted before the change was reconciled
	g.must("a")
	if fmt.Sprint(g.inst.events) != "[apply apply uninstall]" {
		t.Fatalf("events %v", g.inst.events)
	}
	if id, _ := g.inst.lastApplied()["identity"].(map[string]any); id["purgeOnUninstall"] != true {
		t.Fatalf("the release was uninstalled without the purge: %v", g.inst.lastApplied())
	}

	// Already agreeing: nothing extra is applied.
	h := newRig(t)
	h.secret("enroll", "token", "ikh_abc")
	h.agent("a", spec)
	h.must("a")
	h.delete("a")
	h.must("a")
	if fmt.Sprint(h.inst.events) != "[apply uninstall]" {
		t.Fatalf("events %v", h.inst.events)
	}
}

func TestAResourceCanBeLetGoWithoutTouchingTheReleaseWhenItIsToldTo(t *testing.T) {
	g := newRig(t)
	g.secret("enroll", "token", "ikh_abc")
	g.agent("a", goodSpec())
	g.must("a")
	u := g.get("a")
	u.SetAnnotations(map[string]string{SkipUninstall: "true"})
	if _, err := g.dyn.Resource(GVR).Namespace(ns).Update(context.Background(), u, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	g.delete("a")
	g.inst.rmErr = errors.New("would fail")
	g.must("a")
	if g.inst.removed != 0 || g.inst.rel == nil || len(finalizers(g.get("a"))) != 0 {
		t.Fatalf("removed %d, release %v, finalizers %v", g.inst.removed, g.inst.rel, finalizers(g.get("a")))
	}
}

func TestOnlyTheOldestAgentOwnsTheReleaseAndTheOthersWaitWithoutTouchingIt(t *testing.T) {
	g := newRig(t)
	g.secret("enroll", "token", "ikh_abc")
	g.agent("first", goodSpec())
	g.advance(time.Minute)
	g.agent("second", goodSpec())
	g.must("first")
	g.must("second")
	if g.inst.applies() != 1 {
		t.Fatalf("applies %d", g.inst.applies())
	}
	st := g.status("second")
	if st.Phase != PhaseBlocked || st.Conditions[0].Reason != "Blocked" || len(finalizers(g.get("second"))) != 0 {
		t.Fatalf("second: %+v, finalizers %v", st, finalizers(g.get("second")))
	}

	// The one that waits goes: the release is not its to uninstall.
	g.delete("second")
	g.must("second")
	if g.inst.removed != 0 || g.inst.rel == nil {
		t.Fatal("a resource that did not own the release uninstalled it")
	}

	// The owner goes: the release is uninstalled, and the other takes over.
	g2 := newRig(t)
	g2.secret("enroll", "token", "ikh_abc")
	g2.agent("first", goodSpec())
	g2.now = g2.now.Add(time.Minute)
	g2.agent("second", goodSpec())
	g2.must("first")
	g2.must("second")
	g2.delete("first")
	g2.must("first")
	if err := g2.dyn.Resource(GVR).Namespace(ns).Delete(context.Background(), "first", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	g2.must("second")
	if g2.inst.removed != 1 || g2.inst.applies() != 2 || g2.status("second").Phase != PhaseReady || len(finalizers(g2.get("second"))) != 1 {
		t.Fatalf("removed %d, applies %d, second %+v", g2.inst.removed, g2.inst.applies(), g2.status("second"))
	}
}

func TestOnlyOneNamespaceIsLookedAt(t *testing.T) {
	g := newRig(t)
	g.secret("enroll", "token", "ikh_abc")
	other := &unstructured.Unstructured{Object: map[string]any{"apiVersion": Group + "/" + Version, "kind": Kind,
		"metadata": map[string]any{"name": "a", "namespace": "elsewhere"}, "spec": goodSpec()}}
	if _, err := g.dyn.Resource(GVR).Namespace("elsewhere").Create(context.Background(), other, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if d, err := g.reconcile("a"); d != 0 || err != nil || g.inst.applies() != 0 {
		t.Fatalf("%v %v %d", d, err, g.inst.applies())
	}
}

func TestTheStatusOfAnAgentThatDisappearedIsNotWrittenBackToLifeAndNoneIsMissed(t *testing.T) {
	g := newRig(t)
	if d, err := g.reconcile("nothing"); d != 0 || err != nil {
		t.Fatalf("%v %v", d, err)
	}
	for _, a := range g.dyn.Actions() {
		if a.GetVerb() != "get" && a.GetVerb() != "list" {
			t.Errorf("an action on nothing: %v", a)
		}
	}
}

func TestTwoAgentsMadeAtTheSameInstantHaveTheSameOwnerWhicheverIsLookedAtFirst(t *testing.T) {
	g := newRig(t)
	g.secret("enroll", "token", "ikh_abc")
	g.agent("b", goodSpec())
	g.agent("a", goodSpec())
	g.must("b")
	g.must("a")
	if g.status("a").Phase != PhaseReady || g.status("b").Phase != PhaseBlocked {
		t.Fatalf("a: %s, b: %s", g.status("a").Phase, g.status("b").Phase)
	}
}

func TestATokenWithTheNewlineAnEditorLeftIsTheTokenWithoutIt(t *testing.T) {
	g := newRig(t)
	g.secret("enroll", "token", "ikh_abc\n")
	g.agent("a", goodSpec())
	g.must("a")
	got := g.inst.lastApplied()["enrollment"].(map[string]any)["token"]
	if got != "ikh_abc" {
		t.Fatalf("token %q", got)
	}
}

func TestWhatThisBuildDefaultsIsUnderTheSpecAndIsPartOfWhatTheReleaseIsComparedWith(t *testing.T) {
	g := newRig(t)
	g.r.Defaults = map[string]any{"image": map[string]any{"repository": "ghcr.io/acme/ikhnos", "tag": "1.2.3"}, "telemetry": map[string]any{"x": "y"}}
	g.secret("enroll", "token", "ikh_abc")
	spec := goodSpec()
	g.agent("a", spec)
	g.must("a")
	img := g.inst.lastApplied()["image"].(map[string]any)
	if img["repository"] != "ghcr.io/acme/ikhnos" || img["tag"] != "1.2.3" {
		t.Fatalf("defaults not applied: %v", img)
	}
	// Looked at again: the release has the defaults in it and is the same thing, not a change.
	g.must("a")
	if g.inst.applies() != 1 {
		t.Fatalf("the defaults made a release look changed: %d applies", g.inst.applies())
	}

	// The resource's own image wins key by key, and the key it does not name keeps the default.
	spec = goodSpec()
	spec["image"] = map[string]any{"repository": "registry.example/mine"}
	g.setSpec("a", spec)
	g.must("a")
	img = g.inst.lastApplied()["image"].(map[string]any)
	if img["repository"] != "registry.example/mine" || img["tag"] != "1.2.3" {
		t.Fatalf("image %v", img)
	}

	// A different default (a new controller version) is an upgrade.
	g.r.Defaults = map[string]any{"image": map[string]any{"repository": "ghcr.io/acme/ikhnos", "tag": "1.2.4"}}
	g.must("a")
	if tag := g.inst.lastApplied()["image"].(map[string]any)["tag"]; g.inst.applies() != 3 || tag != "1.2.4" {
		t.Fatalf("applies %d, image %v", g.inst.applies(), g.inst.lastApplied()["image"])
	}
}

func TestMergeKeepsBothSidesAndChangesNeither(t *testing.T) {
	under := map[string]any{"a": map[string]any{"x": 1, "y": 2}, "l": []any{"p"}, "s": "keep"}
	over := map[string]any{"a": map[string]any{"y": 3}, "l": []any{"q"}, "n": "new"}
	got := Merge(under, over)
	want := map[string]any{"a": map[string]any{"x": 1, "y": 3}, "l": []any{"q"}, "s": "keep", "n": "new"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%v", got)
	}
	got["a"].(map[string]any)["x"] = 99
	if under["a"].(map[string]any)["x"] != 1 || over["a"].(map[string]any)["y"] != 3 {
		t.Fatal("a merge changed its inputs")
	}
}

func TestAControllerGivenLessThanTheTopTierInstallsNothingAboveIt(t *testing.T) {
	one := 1
	g := newRig(t)
	g.r.MaxTier = &one
	g.secret("enroll", "token", "ikh_abc")

	// No tier named: the highest it may give, not the chart's 2.
	g.agent("a", goodSpec())
	g.must("a")
	if tier := g.inst.lastApplied()["access"].(map[string]any)["tier"]; tier != int64(1) {
		t.Fatalf("installed at tier %v", tier)
	}

	// A tier above it is refused and said, and the release stays as it was.
	spec := goodSpec()
	spec["accessTier"] = int64(2)
	g.setSpec("a", spec)
	g.must("a")
	st := g.status("a")
	if st.Phase != PhaseInvalid || !strings.Contains(st.Conditions[0].Message, "above the 1") || g.inst.applies() != 1 {
		t.Fatalf("status %+v, applies %d", st, g.inst.applies())
	}

	// A tier at or below it is fine.
	spec["accessTier"] = int64(0)
	g.setSpec("a", spec)
	g.must("a")
	if g.status("a").Phase != PhaseReady || g.inst.lastApplied()["access"].(map[string]any)["tier"] != int64(0) {
		t.Fatalf("status %+v", g.status("a"))
	}
}
