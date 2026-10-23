package controller

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"
)

// likeAnAPIServer makes the fake do what a real one does and the fake does not: a resource that is being deleted goes when its last
// finalizer is taken off.
func (g *rig) likeAnAPIServer() {
	g.dyn.PrependReactor("update", Resource, func(a k8stesting.Action) (bool, runtime.Object, error) {
		ua, ok := a.(k8stesting.UpdateAction)
		if !ok || a.GetSubresource() != "" {
			return false, nil, nil
		}
		u, ok := ua.GetObject().(*unstructured.Unstructured)
		if !ok || u.GetDeletionTimestamp() == nil || len(u.GetFinalizers()) > 0 {
			return false, nil, nil
		}
		if err := g.dyn.Tracker().Delete(GVR, a.GetNamespace(), u.GetName()); err != nil {
			return true, nil, err
		}
		return true, u, nil
	})
}

// eventually waits for a thing the loop does in its own time.
func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestTheLoopInstallsWhatIsCreatedAndUninstallsWhatIsDeleted(t *testing.T) {
	g := newRig(t)
	g.likeAnAPIServer()
	g.secret("enroll", "token", "tok3n")

	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- g.r.Run(ctx) }()
	defer func() {
		stop()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run returned %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Run did not return after its context ended")
		}
	}()

	g.agent("edge-a", goodSpec())
	eventually(t, "the agent to be installed", func() bool { return g.inst.applies() == 1 })
	eventually(t, "the status to say Ready", func() bool {
		u, err := g.dyn.Resource(GVR).Namespace(ns).Get(context.Background(), "edge-a", metav1.GetOptions{})
		if err != nil {
			return false
		}
		st, _ := u.Object["status"].(map[string]any)
		return st["phase"] == string(PhaseReady)
	})

	// A change to the resource is picked up without anyone asking.
	spec := goodSpec()
	spec["collectors"] = map[string]any{"nodeProbe": true}
	g.setSpec("edge-a", spec)
	eventually(t, "the change to be applied", func() bool { return g.inst.applies() == 2 })

	// Deleting it uninstalls, and the resource goes only after.
	g.delete("edge-a")
	eventually(t, "the uninstall", func() bool {
		g.inst.mu.Lock()
		defer g.inst.mu.Unlock()
		return g.inst.removed == 1
	})
	eventually(t, "the resource to be gone", func() bool {
		_, err := g.dyn.Resource(GVR).Namespace(ns).Get(context.Background(), "edge-a", metav1.GetOptions{})
		return err != nil
	})
}

func TestTheNextAgentTakesOverWhenTheOwnerGoesWithoutAnyoneTouchingIt(t *testing.T) {
	g := newRig(t)
	g.likeAnAPIServer()
	g.secret("enroll", "token", "tok3n")
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- g.r.Run(ctx) }()
	defer func() { stop(); <-done }()

	g.agent("first", goodSpec())
	eventually(t, "the first to install", func() bool { return g.inst.applies() == 1 })
	g.advance(time.Minute)
	g.agent("second", goodSpec())
	eventually(t, "the second to be called blocked", func() bool {
		u, err := g.dyn.Resource(GVR).Namespace(ns).Get(context.Background(), "second", metav1.GetOptions{})
		if err != nil {
			return false
		}
		st, _ := u.Object["status"].(map[string]any)
		return st["phase"] == string(PhaseBlocked)
	})
	g.delete("first")
	eventually(t, "the second to become the owner and be Ready", func() bool {
		u, err := g.dyn.Resource(GVR).Namespace(ns).Get(context.Background(), "second", metav1.GetOptions{})
		if err != nil {
			return false
		}
		st, _ := u.Object["status"].(map[string]any)
		return st["phase"] == string(PhaseReady)
	})
}
