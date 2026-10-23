package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Installer installs, upgrades and removes the agent release. The reconciler decides what is to be done; an Installer does it. The one
// the controller runs with is built on the Helm library (internal/controller/helminstall); the tests use a fake.
type Installer interface {
	// Current is the release as it is installed, or nil when there is none.
	Current(ctx context.Context, namespace, release string) (*Release, error)
	// Apply installs the release, or upgrades it when it is there, with exactly these values (the chart's defaults under them).
	Apply(ctx context.Context, namespace, release string, values map[string]any) (*Release, error)
	// Uninstall removes the release, running its hooks. A release that is not there is not an error.
	Uninstall(ctx context.Context, namespace, release string) error
	// ChartVersion is the version of the chart Apply installs.
	ChartVersion() string
}

// Release is what the reconciler needs to know of an installed release.
type Release struct {
	Revision     int
	ChartVersion string
	// Deployed is whether the last revision completed; a failed or half-done one is not.
	Deployed bool
	// Values are the values the release was installed with (not the chart's defaults).
	Values map[string]any
}

// Hash is a short digest of values, for telling whether the installed ones are the wanted ones. JSON writes a map's keys in order, so
// the same values always give the same digest.
func Hash(values map[string]any) string {
	b, _ := json.Marshal(values)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// Merge is over laid on under: a key in over replaces the one in under, except that two documents are merged key by key. Neither is
// changed.
func Merge(under, over map[string]any) map[string]any {
	out := make(map[string]any, len(under)+len(over))
	for k, v := range under {
		out[k] = clone(v)
	}
	for k, v := range over {
		if o, ok := v.(map[string]any); ok {
			if u, ok := out[k].(map[string]any); ok {
				out[k] = Merge(u, o)
				continue
			}
		}
		out[k] = clone(v)
	}
	return out
}

func clone(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return Merge(nil, t)
	case []any:
		o := make([]any, len(t))
		for i, x := range t {
			o[i] = clone(x)
		}
		return o
	}
	return v
}
