package helminstall

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/kube"
	kubefake "helm.sh/helm/v3/pkg/kube/fake"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage"
	"helm.sh/helm/v3/pkg/storage/driver"

	"ikhnos/internal/chart"
	"ikhnos/internal/controller"
)

const ns = "ikhnos-system"

// recorder is a cluster that takes what it is given and remembers every manifest Helm asked it to build: that is where a hook shows.
type recorder struct {
	kubefake.PrintingKubeClient
	mu    sync.Mutex
	built []string
}

func (r *recorder) Build(reader io.Reader, validate bool) (kube.ResourceList, error) {
	b, _ := io.ReadAll(reader)
	r.mu.Lock()
	r.built = append(r.built, string(b))
	r.mu.Unlock()
	return r.PrintingKubeClient.Build(strings.NewReader(string(b)), validate)
}

func (r *recorder) hooksBuilt(needle string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, m := range r.built {
		if strings.Contains(m, needle) {
			n++
		}
	}
	return n
}

type rig struct {
	t    *testing.T
	cfg  *action.Configuration
	kube *recorder
	in   *Installer
}

func newRig(t *testing.T) *rig {
	t.Helper()
	k := &recorder{PrintingKubeClient: kubefake.PrintingKubeClient{Out: io.Discard}}
	cfg := &action.Configuration{
		Releases:     storage.Init(driver.NewMemory()),
		KubeClient:   k,
		Capabilities: chartutil.DefaultCapabilities,
		Log:          func(string, ...any) {},
	}
	return &rig{t: t, cfg: cfg, kube: k, in: &Installer{Chart: chart.Agent, Configure: func(string) (*action.Configuration, error) { return cfg, nil }}}
}

func values(extra map[string]any) map[string]any {
	v := map[string]any{
		"server":     map[string]any{"address": "ikhnos.example.com:8443", "caPin": "sha256/AbCd=="},
		"enrollment": map[string]any{"token": "tok3n"},
	}
	for k, x := range extra {
		v[k] = x
	}
	return v
}

func (g *rig) manifest() string {
	g.t.Helper()
	r, err := g.cfg.Releases.Last("ikhnos-agent")
	if err != nil {
		g.t.Fatal(err)
	}
	return r.Manifest
}

func TestInstallingTheEmbeddedChartMakesAReleaseWithTheValuesItWasGiven(t *testing.T) {
	g := newRig(t)
	ctx := context.Background()
	if cur, err := g.in.Current(ctx, ns, controller.ReleaseName); cur != nil || err != nil {
		t.Fatalf("a release before any: %v %v", cur, err)
	}
	rel, err := g.in.Apply(ctx, ns, controller.ReleaseName, values(nil))
	if err != nil {
		t.Fatal(err)
	}
	if rel.Revision != 1 || !rel.Deployed || rel.ChartVersion != chart.Agent.Version() {
		t.Fatalf("release %+v", rel)
	}
	cur, err := g.in.Current(ctx, ns, controller.ReleaseName)
	if err != nil || cur == nil {
		t.Fatalf("current %v %v", cur, err)
	}
	if !cur.Deployed || cur.Revision != 1 || controller.Hash(cur.Values) != controller.Hash(values(nil)) {
		t.Fatalf("current %+v", cur)
	}
	m := g.manifest()
	for _, want := range []string{"kind: Deployment", "kind: ClusterRole", "name: ikhnos-agent"} {
		if !strings.Contains(m, want) {
			t.Errorf("the release lacks %q", want)
		}
	}
	if strings.Contains(m, "kind: DaemonSet") {
		t.Error("a collector is on that nobody asked for")
	}
}

func TestAnUpgradeIsExactlyTheNewValuesNotTheOldOnesWithMoreOnTop(t *testing.T) {
	g := newRig(t)
	ctx := context.Background()
	on := values(map[string]any{"nodeProbe": map[string]any{"enabled": true}})
	if _, err := g.in.Apply(ctx, ns, controller.ReleaseName, on); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(g.manifest(), "kind: DaemonSet") {
		t.Fatal("the node probe was asked for and is not in the release")
	}
	rel, err := g.in.Apply(ctx, ns, controller.ReleaseName, values(nil))
	if err != nil {
		t.Fatal(err)
	}
	if rel.Revision != 2 {
		t.Fatalf("revision %d", rel.Revision)
	}
	if strings.Contains(g.manifest(), "kind: DaemonSet") {
		t.Error("the node probe stayed after a revision that does not ask for it")
	}
	cur, _ := g.in.Current(ctx, ns, controller.ReleaseName)
	if controller.Hash(cur.Values) != controller.Hash(values(nil)) {
		t.Errorf("the stored values are %v", cur.Values)
	}
}

func TestTheVersionsAReleaseBuildGivesAreTheOnesTheReleaseIsInstalledAt(t *testing.T) {
	g := newRig(t)
	g.in.Version, g.in.AppVersion = "0.0.0-edge.gabc1234", "edge"
	if g.in.ChartVersion() != "0.0.0-edge.gabc1234" {
		t.Fatal(g.in.ChartVersion())
	}
	rel, err := g.in.Apply(context.Background(), ns, controller.ReleaseName, values(nil))
	if err != nil {
		t.Fatal(err)
	}
	if rel.ChartVersion != "0.0.0-edge.gabc1234" {
		t.Fatalf("installed at %s", rel.ChartVersion)
	}
	// The agent's image is the chart's appVersion when no tag is chosen, so the appVersion has to be the release's.
	if !strings.Contains(g.manifest(), ":edge") {
		t.Errorf("the image does not carry the app version:\n%s", imageLines(g.manifest()))
	}
}

func imageLines(m string) string {
	var out []string
	for _, l := range strings.Split(m, "\n") {
		if strings.Contains(l, "image:") {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

func TestAnInstallThatFailedIsTriedAgainAsARevisionAndGetsThere(t *testing.T) {
	g := newRig(t)
	ctx := context.Background()
	// What Helm leaves when the cluster refused the first install: revision 1, failed. (The fake cluster builds no objects, so it
	// cannot be made to refuse one; the record is made as Helm makes it.)
	if _, err := g.in.Apply(ctx, ns, controller.ReleaseName, values(nil)); err != nil {
		t.Fatal(err)
	}
	last, _ := g.cfg.Releases.Last(controller.ReleaseName)
	last.SetStatus(release.StatusFailed, "the cluster said no")
	if err := g.cfg.Releases.Update(last); err != nil {
		t.Fatal(err)
	}
	cur, err := g.in.Current(ctx, ns, controller.ReleaseName)
	if err != nil || cur == nil || cur.Deployed {
		t.Fatalf("a failed install is a release that is not deployed: %+v %v", cur, err)
	}
	rel, err := g.in.Apply(ctx, ns, controller.ReleaseName, values(nil))
	if err != nil {
		t.Fatalf("a failed release was not tried again: %v", err)
	}
	if !rel.Deployed || rel.Revision != 2 {
		t.Fatalf("%+v", rel)
	}
}

func TestARevisionNobodyFinishedDoesNotBlockTheNext(t *testing.T) {
	g := newRig(t)
	ctx := context.Background()
	if _, err := g.in.Apply(ctx, ns, controller.ReleaseName, values(nil)); err != nil {
		t.Fatal(err)
	}
	last, _ := g.cfg.Releases.Last(controller.ReleaseName)
	last.SetStatus(release.StatusPendingUpgrade, "")
	if err := g.cfg.Releases.Update(last); err != nil {
		t.Fatal(err)
	}
	if cur, _ := g.in.Current(ctx, ns, controller.ReleaseName); cur.Deployed {
		t.Fatal("a pending revision counts as deployed")
	}
	rel, err := g.in.Apply(ctx, ns, controller.ReleaseName, values(map[string]any{"nodeProbe": map[string]any{"enabled": true}}))
	if err != nil {
		t.Fatalf("the unfinished revision blocked the next: %v", err)
	}
	if !rel.Deployed || rel.Revision != 2 {
		t.Fatalf("%+v", rel)
	}
}

func TestUninstallRunsTheIdentityHookOnlyWhenTheReleaseWasInstalledToHaveIt(t *testing.T) {
	for _, purge := range []bool{false, true} {
		g := newRig(t)
		ctx := context.Background()
		v := values(nil)
		if purge {
			v["identity"] = map[string]any{"purgeOnUninstall": true}
		}
		if _, err := g.in.Apply(ctx, ns, controller.ReleaseName, v); err != nil {
			t.Fatal(err)
		}
		before := g.kube.hooksBuilt("purge-identity")
		if err := g.in.Uninstall(ctx, ns, controller.ReleaseName); err != nil {
			t.Fatal(err)
		}
		ran := g.kube.hooksBuilt("purge-identity") - before
		if (ran > 0) != purge {
			t.Errorf("purge=%v: the hook was built %d times on uninstall", purge, ran)
		}
		if cur, err := g.in.Current(ctx, ns, controller.ReleaseName); cur != nil || err != nil {
			t.Errorf("the release is still there: %v %v", cur, err)
		}
		// Uninstalling what is not there is done.
		if err := g.in.Uninstall(ctx, ns, controller.ReleaseName); err != nil {
			t.Errorf("a second uninstall: %v", err)
		}
	}
}
