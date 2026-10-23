package controller

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"ikhnos/internal/install"
)

// Phases an IkhnosAgent is in.
const (
	PhasePending    = "Pending"    // waiting for something the person has to provide (the token Secret)
	PhaseInstalling = "Installing" // the release is being installed or changed
	PhaseReady      = "Ready"      // the release is installed as the spec asks
	PhaseFailed     = "Failed"     // the installer failed; it is tried again
	PhaseInvalid    = "Invalid"    // the spec cannot be installed from
	PhaseBlocked    = "Blocked"    // another IkhnosAgent in this namespace owns the release
	PhaseDeleting   = "Deleting"   // the release is being uninstalled
)

// Status is what the controller writes back.
type Status struct {
	Phase              string      `json:"phase,omitempty"`
	ObservedGeneration int64       `json:"observedGeneration,omitempty"`
	Release            *ReleaseRef `json:"release,omitempty"`
	// ValuesHash is the digest of the values the release was last installed with (see Hash).
	ValuesHash string      `json:"valuesHash,omitempty"`
	Conditions []Condition `json:"conditions,omitempty"`
}

// ReleaseRef is the installed release.
type ReleaseRef struct {
	Name         string `json:"name"`
	Revision     int    `json:"revision"`
	ChartVersion string `json:"chartVersion"`
}

// Condition follows Kubernetes' own: Ready is true when the release is installed as the spec asks.
type Condition struct {
	Type               string `json:"type"`
	Status             string `json:"status"`
	Reason             string `json:"reason,omitempty"`
	Message            string `json:"message,omitempty"`
	LastTransitionTime string `json:"lastTransitionTime,omitempty"`
	ObservedGeneration int64  `json:"observedGeneration,omitempty"`
}

// Reconciler brings one namespace's IkhnosAgent and release into agreement.
type Reconciler struct {
	Dyn       dynamic.Interface
	Kube      kubernetes.Interface
	Installer Installer
	Namespace string
	// Defaults are Helm values every release gets under whatever the resource says: where this build's images are, for one. They are
	// part of what the release is compared with, so a change in them upgrades it like a change in the resource does.
	Defaults map[string]any
	// MaxTier is the highest access tier this controller installs an agent at (nil: 2, the highest there is). It is the ceiling of the
	// permissions the controller was given: to create the agent's ClusterRoles it must hold what they grant (Kubernetes lets nobody
	// hand out what they do not have), so a controller that is to install only tier 0 or 1 agents is given no more than those need.
	MaxTier *int
	Log     *slog.Logger
	Now     func() time.Time
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Reconciler) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

// WaitingForSecret is how long to wait before looking again for a token Secret that is not there yet.
const WaitingForSecret = 30 * time.Second

// Reconcile looks at the IkhnosAgent called name and does what it needs. A non-zero duration asks to be called again after it; an error
// is retried with backoff.
func (r *Reconciler) Reconcile(ctx context.Context, name string) (time.Duration, error) {
	res := r.Dyn.Resource(GVR).Namespace(r.Namespace)
	obj, err := res.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	list, err := res.List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, err
	}
	if owner := ownerOf(list.Items); owner != obj.GetName() {
		return 0, r.blocked(ctx, obj, owner)
	}
	if obj.GetDeletionTimestamp() != nil {
		return 0, r.finish(ctx, obj)
	}
	return r.apply(ctx, obj)
}

// ownerOf is the resource that owns the release when there are several: the oldest, then the first by name. A release is one thing, so
// the others wait; they must not fight over it, and deleting one of them must not uninstall what is not theirs.
func ownerOf(items []unstructured.Unstructured) string {
	if len(items) == 0 {
		return ""
	}
	sorted := append([]unstructured.Unstructured(nil), items...)
	sort.Slice(sorted, func(i, j int) bool {
		a, b := sorted[i].GetCreationTimestamp(), sorted[j].GetCreationTimestamp()
		if !a.Equal(&b) {
			return a.Before(&b)
		}
		return sorted[i].GetName() < sorted[j].GetName()
	})
	return sorted[0].GetName()
}

func (r *Reconciler) blocked(ctx context.Context, obj *unstructured.Unstructured, owner string) error {
	// It never held the finalizer, so deleting it is free; a finalizer left from an earlier ownership is let go.
	if hasFinalizer(obj) && obj.GetDeletionTimestamp() != nil {
		if err := r.setFinalizer(ctx, obj, false); err != nil {
			return err
		}
		return nil
	}
	return r.setStatus(ctx, obj, Status{Phase: PhaseBlocked, ObservedGeneration: obj.GetGeneration()},
		r.cond(obj, "False", "Blocked", fmt.Sprintf("%s owns the agent release in this namespace; there is one agent per namespace", owner)))
}

func (r *Reconciler) apply(ctx context.Context, obj *unstructured.Unstructured) (time.Duration, error) {
	if !hasFinalizer(obj) {
		if err := r.setFinalizer(ctx, obj, true); err != nil {
			return 0, err
		}
	}
	spec, err := parseSpec(obj)
	if err != nil {
		return 0, r.setStatus(ctx, obj, Status{Phase: PhaseInvalid, ObservedGeneration: obj.GetGeneration()},
			r.cond(obj, "False", "InvalidSpec", err.Error()))
	}
	token, wait, err := r.token(ctx, spec)
	if err != nil {
		return 0, err
	}
	cur, err := r.Installer.Current(ctx, r.Namespace, ReleaseName)
	if err != nil {
		return 0, err
	}
	if wait != "" && cur == nil {
		return WaitingForSecret, r.setStatus(ctx, obj, Status{Phase: PhasePending, ObservedGeneration: obj.GetGeneration()},
			r.cond(obj, "False", "WaitingForToken", wait))
	}
	values, err := r.values(spec, token)
	if err != nil {
		return 0, r.setStatus(ctx, obj, Status{Phase: PhaseInvalid, ObservedGeneration: obj.GetGeneration()},
			r.cond(obj, "False", "InvalidSpec", err.Error()))
	}
	want := Hash(values)
	chartVersion := r.Installer.ChartVersion()
	if cur != nil && cur.Deployed && cur.ChartVersion == chartVersion && Hash(cur.Values) == want {
		return 0, r.setStatus(ctx, obj, r.ready(obj, cur, want), r.cond(obj, "True", "Installed", "the release is installed as the spec asks"))
	}
	if err := r.setStatus(ctx, obj, Status{Phase: PhaseInstalling, ObservedGeneration: obj.GetGeneration(), ValuesHash: want},
		r.cond(obj, "False", "Installing", "the release is being installed or changed")); err != nil {
		return 0, err
	}
	rel, err := r.Installer.Apply(ctx, r.Namespace, ReleaseName, values)
	if err != nil {
		_ = r.setStatus(ctx, obj, Status{Phase: PhaseFailed, ObservedGeneration: obj.GetGeneration(), ValuesHash: want},
			r.cond(obj, "False", "InstallFailed", err.Error()))
		return 0, fmt.Errorf("installing the agent release: %w", err)
	}
	r.log().Info("agent release applied", "release", ReleaseName, "revision", rel.Revision, "chart", rel.ChartVersion)
	return 0, r.setStatus(ctx, obj, r.ready(obj, rel, want), r.cond(obj, "True", "Installed", "the release is installed as the spec asks"))
}

// ceiling is the highest tier this controller may install.
func (r *Reconciler) ceiling() int {
	if r.MaxTier != nil {
		return *r.MaxTier
	}
	return DefaultTier
}

// values are the spec's Helm values over the defaults. A spec that does not name a tier gets the highest this controller may give
// (the chart's own default, 2, when it has no lower ceiling); one that names a tier above it is refused, not trimmed.
func (r *Reconciler) values(spec Spec, token string) (map[string]any, error) {
	if spec.AccessTier == nil && r.ceiling() < DefaultTier {
		t := r.ceiling()
		spec.AccessTier = &t
	}
	if spec.AccessTier != nil && *spec.AccessTier > r.ceiling() {
		return nil, &install.ProblemsError{Problems: []string{fmt.Sprintf("spec.accessTier %d is above the %d this controller was given the permissions for (maxAccessTier in its chart)", *spec.AccessTier, r.ceiling())}}
	}
	v, err := spec.Values(token)
	if err != nil {
		return nil, err
	}
	return Merge(r.Defaults, v), nil
}

func (r *Reconciler) ready(obj *unstructured.Unstructured, rel *Release, hash string) Status {
	return Status{Phase: PhaseReady, ObservedGeneration: obj.GetGeneration(), ValuesHash: hash,
		Release: &ReleaseRef{Name: ReleaseName, Revision: rel.Revision, ChartVersion: rel.ChartVersion}}
}

// token reads the enrolment token from its Secret. The second result says what is missing when there is no token to give yet.
func (r *Reconciler) token(ctx context.Context, spec Spec) (token, missing string, err error) {
	ref := spec.Enrollment.TokenSecretRef
	if ref == nil {
		return "", "spec.enrollment.tokenSecretRef is not set", nil
	}
	sec, err := r.Kube.CoreV1().Secrets(r.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", fmt.Sprintf("the Secret %q does not exist in this namespace", ref.Name), nil
	}
	if err != nil {
		return "", "", err
	}
	return tokenOf(sec, ref)
}

func tokenOf(sec *corev1.Secret, ref *SecretKeyRef) (string, string, error) {
	b, ok := sec.Data[ref.TokenKey()]
	if !ok {
		return "", fmt.Sprintf("the Secret %q has no key %q", ref.Name, ref.TokenKey()), nil
	}
	return strings.TrimSpace(string(b)), "", nil
}

// finish uninstalls the release of a resource that is being deleted, and then lets it go.
func (r *Reconciler) finish(ctx context.Context, obj *unstructured.Unstructured) error {
	if !hasFinalizer(obj) {
		return nil
	}
	if obj.GetAnnotations()[SkipUninstall] == "true" {
		r.log().Warn("letting an IkhnosAgent go without uninstalling its release", "name", obj.GetName())
		return r.setFinalizer(ctx, obj, false)
	}
	_ = r.setStatus(ctx, obj, Status{Phase: PhaseDeleting, ObservedGeneration: obj.GetGeneration()},
		r.cond(obj, "False", "Deleting", "the release is being uninstalled"))
	cur, err := r.Installer.Current(ctx, r.Namespace, ReleaseName)
	if err != nil {
		return err
	}
	if cur != nil {
		if err := r.matchPurge(ctx, obj, cur); err != nil {
			return err
		}
		if err := r.Installer.Uninstall(ctx, r.Namespace, ReleaseName); err != nil {
			return fmt.Errorf("uninstalling the agent release: %w", err)
		}
	}
	return r.setFinalizer(ctx, obj, false)
}

// matchPurge makes the installed release agree with the spec on deleting the identity before it is uninstalled: the hook that does it
// is part of the release as installed, so a spec that asks for it after the install has to be applied first.
func (r *Reconciler) matchPurge(ctx context.Context, obj *unstructured.Unstructured, cur *Release) error {
	spec, err := parseSpec(obj)
	if err != nil {
		return nil // a spec that cannot be read says nothing about it: the release goes as it is
	}
	if purgeOf(cur.Values) == spec.PurgeIdentityOnDelete {
		return nil
	}
	token, _, err := r.token(ctx, spec)
	if err != nil {
		return err
	}
	values, err := r.values(spec, token)
	if err != nil {
		return nil
	}
	if _, err := r.Installer.Apply(ctx, r.Namespace, ReleaseName, values); err != nil {
		return fmt.Errorf("applying the identity setting before uninstalling: %w", err)
	}
	return nil
}

func purgeOf(values map[string]any) bool {
	id, _ := values["identity"].(map[string]any)
	b, _ := id["purgeOnUninstall"].(bool)
	return b
}

func hasFinalizer(obj *unstructured.Unstructured) bool {
	for _, f := range obj.GetFinalizers() {
		if f == Finalizer {
			return true
		}
	}
	return false
}

func (r *Reconciler) setFinalizer(ctx context.Context, obj *unstructured.Unstructured, on bool) error {
	res := r.Dyn.Resource(GVR).Namespace(r.Namespace)
	fresh, err := res.Get(ctx, obj.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var fs []string
	for _, f := range fresh.GetFinalizers() {
		if f != Finalizer {
			fs = append(fs, f)
		}
	}
	if on {
		fs = append(fs, Finalizer)
	}
	fresh.SetFinalizers(fs)
	_, err = res.Update(ctx, fresh, metav1.UpdateOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func (r *Reconciler) cond(obj *unstructured.Unstructured, status, reason, message string) Condition {
	return Condition{Type: "Ready", Status: status, Reason: reason, Message: message, ObservedGeneration: obj.GetGeneration()}
}

// setStatus writes the status when it differs from what is there. A condition keeps its time of transition while its status does not
// change, so an unchanged resource is not written to again and again.
func (r *Reconciler) setStatus(ctx context.Context, obj *unstructured.Unstructured, st Status, c Condition) error {
	res := r.Dyn.Resource(GVR).Namespace(r.Namespace)
	fresh, err := res.Get(ctx, obj.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var old Status
	if raw, ok := fresh.Object["status"].(map[string]any); ok {
		_ = runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &old)
	}
	c.LastTransitionTime = r.now().UTC().Format(time.RFC3339)
	for _, o := range old.Conditions {
		if o.Type == c.Type && o.Status == c.Status {
			c.LastTransitionTime = o.LastTransitionTime
		}
	}
	st.Conditions = []Condition{c}
	if equalStatus(old, st) {
		return nil
	}
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&st)
	if err != nil {
		return err
	}
	fresh.Object["status"] = m
	_, err = res.UpdateStatus(ctx, fresh, metav1.UpdateOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func equalStatus(a, b Status) bool {
	ma, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(&a)
	mb, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(&b)
	return fmt.Sprint(ma) == fmt.Sprint(mb)
}

// parseSpec reads the spec of a resource. A spec that does not parse, or that has problems, is told in full.
func parseSpec(obj *unstructured.Unstructured) (Spec, error) {
	var s Spec
	raw, _ := obj.Object["spec"].(map[string]any)
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &s); err != nil {
		return Spec{}, fmt.Errorf("the spec cannot be read: %w", err)
	}
	if p := s.Problems(); len(p) > 0 {
		return Spec{}, &install.ProblemsError{Problems: p}
	}
	return s, nil
}
