// Package helminstall is the controller's Installer on the Helm library: the same chart `helm install` would be given, installed,
// upgraded and uninstalled as a Helm release (stored as Secrets in the release's namespace, so `helm list` and `helm get values` see
// it like any other). The chart is the one embedded in this binary, so what the controller installs is what the server hands out.
package helminstall

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"helm.sh/helm/v3/pkg/action"
	helmchart "helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage/driver"

	"ikhnos/internal/chart"
	"ikhnos/internal/controller"
)

// DefaultTimeout is how long an install, an upgrade or an uninstall (with its hooks) may take.
const DefaultTimeout = 5 * time.Minute

// Installer installs the agent chart as a Helm release.
type Installer struct {
	// Chart is the chart to install: chart.Agent.
	Chart *chart.Chart
	// Version and AppVersion, when set, replace the version and appVersion the chart carries. A release build sets them to what the
	// published chart was packaged with (`helm package --version --app-version`), because the files in the binary say what the
	// source tree said; an upgrade is noticed by the version changing, and the agent image defaults to the appVersion.
	Version, AppVersion string
	// Configure gives the Helm configuration for a namespace: where releases are stored and how objects reach the cluster.
	// Config(...) makes the real one.
	Configure func(namespace string) (*action.Configuration, error)
	// Timeout bounds one operation; zero is DefaultTimeout.
	Timeout time.Duration
	Log     *slog.Logger
}

var _ controller.Installer = (*Installer)(nil)

func (i *Installer) timeout() time.Duration {
	if i.Timeout > 0 {
		return i.Timeout
	}
	return DefaultTimeout
}

func (i *Installer) log() *slog.Logger {
	if i.Log != nil {
		return i.Log
	}
	return slog.Default()
}

// load reads the chart afresh each time: Helm fills a chart's values in as it renders, and a chart that is used again is not the
// chart that was loaded.
func (i *Installer) load() (*helmchart.Chart, error) {
	pkg, err := i.Chart.Package()
	if err != nil {
		return nil, err
	}
	ch, err := loader.LoadArchive(bytes.NewReader(pkg))
	if err != nil {
		return nil, fmt.Errorf("the embedded chart cannot be loaded: %w", err)
	}
	if i.Version != "" {
		ch.Metadata.Version = i.Version
	}
	if i.AppVersion != "" {
		ch.Metadata.AppVersion = i.AppVersion
	}
	return ch, nil
}

// ChartVersion is the version releases are installed at.
func (i *Installer) ChartVersion() string {
	if i.Version != "" {
		return i.Version
	}
	return i.Chart.Version()
}

func (i *Installer) cfg(namespace string) (*action.Configuration, error) {
	if i.Configure == nil {
		return nil, errors.New("helminstall: no Configure")
	}
	return i.Configure(namespace)
}

// Current is the latest revision of the release, or nil when there is none.
func (i *Installer) Current(_ context.Context, namespace, name string) (*controller.Release, error) {
	cfg, err := i.cfg(namespace)
	if err != nil {
		return nil, err
	}
	rel, err := action.NewGet(cfg).Run(name)
	if errors.Is(err, driver.ErrReleaseNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return describe(rel), nil
}

func describe(rel *release.Release) *controller.Release {
	r := &controller.Release{Revision: rel.Version, Deployed: rel.Info != nil && rel.Info.Status == release.StatusDeployed, Values: rel.Config}
	if rel.Chart != nil && rel.Chart.Metadata != nil {
		r.ChartVersion = rel.Chart.Metadata.Version
	}
	return r
}

// Apply installs the release, or upgrades it to these values. The values are the whole of what the release is given: whatever an
// earlier revision had and these do not name goes back to the chart's default.
func (i *Installer) Apply(ctx context.Context, namespace, name string, values map[string]any) (*controller.Release, error) {
	cfg, err := i.cfg(namespace)
	if err != nil {
		return nil, err
	}
	ch, err := i.load()
	if err != nil {
		return nil, err
	}
	last, err := action.NewGet(cfg).Run(name)
	if err != nil && !errors.Is(err, driver.ErrReleaseNotFound) {
		return nil, err
	}
	var rel *release.Release
	if last == nil {
		in := action.NewInstall(cfg)
		in.ReleaseName, in.Namespace, in.Timeout = name, namespace, i.timeout()
		rel, err = in.RunWithContext(ctx, ch, values)
	} else {
		// A revision left half done (the controller stopped in the middle of one) would make Helm refuse "another operation is in
		// progress" for ever. There is one controller, so nothing else is working on it: it is a failed revision.
		if last.Info != nil && last.Info.Status.IsPending() {
			i.log().Warn("an earlier revision of the agent release was never finished; marking it failed", "revision", last.Version, "status", string(last.Info.Status))
			last.SetStatus(release.StatusFailed, "the controller stopped before this revision was finished")
			if err := cfg.Releases.Update(last); err != nil {
				return nil, fmt.Errorf("marking the unfinished revision failed: %w", err)
			}
		}
		up := action.NewUpgrade(cfg)
		up.Namespace, up.Timeout, up.ResetValues, up.MaxHistory = namespace, i.timeout(), true, 5
		rel, err = up.RunWithContext(ctx, name, ch, values)
	}
	if err != nil {
		return nil, err
	}
	return describe(rel), nil
}

// Uninstall removes the release with its hooks (the chart's identity purge is a post-delete hook). A release that is not there is
// already uninstalled.
func (i *Installer) Uninstall(_ context.Context, namespace, name string) error {
	cfg, err := i.cfg(namespace)
	if err != nil {
		return err
	}
	un := action.NewUninstall(cfg)
	un.Timeout = i.timeout()
	if _, err := un.Run(name); err != nil && !errors.Is(err, driver.ErrReleaseNotFound) {
		return err
	}
	return nil
}
