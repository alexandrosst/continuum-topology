package controller

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

// Resync is how often every resource is looked at again with nothing having changed: it is what notices a release that was removed by
// hand, and a token Secret that appeared. (The controller does not watch Secrets or the release's objects.)
const Resync = 10 * time.Minute

// Run watches the namespace's IkhnosAgents and reconciles them until ctx ends. One worker: there is one release, and its changes must
// not overlap.
func (r *Reconciler) Run(ctx context.Context) error {
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]())
	defer queue.ShutDown()

	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(r.Dyn, Resync, r.Namespace, nil)
	inf := factory.ForResource(GVR).Informer()
	enqueue := func(o any) {
		if u, ok := o.(*unstructured.Unstructured); ok {
			queue.Add(u.GetName())
		}
	}
	// A resource that goes may have been the owner of the release, and the ones waiting for it then have to be looked at.
	enqueueAll := func() {
		for _, o := range inf.GetStore().List() {
			enqueue(o)
		}
	}
	if _, err := inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    enqueue,
		UpdateFunc: func(_, n any) { enqueue(n) },
		DeleteFunc: func(o any) {
			if t, ok := o.(cache.DeletedFinalStateUnknown); ok {
				o = t.Obj
			}
			enqueue(o)
			enqueueAll()
		},
	}); err != nil {
		return err
	}
	factory.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), inf.HasSynced) {
		return fmt.Errorf("the cache of IkhnosAgents did not sync")
	}
	r.log().Info("controller running", "namespace", r.Namespace, "chart", r.Installer.ChartVersion())

	go func() {
		<-ctx.Done()
		queue.ShutDown()
	}()
	for {
		name, shutdown := queue.Get()
		if shutdown {
			return nil
		}
		again, err := r.Reconcile(ctx, name)
		switch {
		case err != nil:
			r.log().Error("reconcile failed", "name", name, "err", err)
			queue.AddRateLimited(name)
		case again > 0:
			queue.Forget(name)
			queue.AddAfter(name, again)
		default:
			queue.Forget(name)
		}
		queue.Done(name)
	}
}
