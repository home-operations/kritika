package executor

import (
	"context"
	"fmt"
	"sync"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8slabels "k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	batchv1listers "k8s.io/client-go/listers/batch/v1"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
)

// runnerSelector selects runner Jobs and their pods, by the role label a
// NetworkPolicy selects them by too.
const runnerSelector = "kritika.home-operations.com/role=" + runnerRole

// watchSync bounds how long Start waits for its trial watches and its
// first lists.
const watchSync = time.Minute

// watchCheck names the object Start's trial watches select, which no
// runner Job or pod is.
const watchCheck = "kritika-watch-check"

// watches is the executor's watch of runner Jobs and their pods, one list
// and watch of each per replica however many runs are in flight: Run reads
// its Job and pod from it, woken by every change to them.
type watches struct {
	jobs batchv1listers.JobLister
	pods corev1listers.PodLister
	stop context.CancelFunc
	mu   sync.Mutex
	// woken is the channel of the run waiting on each Job, by its name.
	woken map[string]chan struct{}
}

// Start watches runner Jobs and their pods, which Run needs first, until
// Stop. The API server sends each change as it happens, rather than the
// replica asking after every run in flight. Start returns once the API lets
// it watch both and both are listed, and it waits at most watchSync. ctx
// bounds that wait alone: the watch outlasts ctx, which a signal ends while
// runs still drain. It watches the role label's Jobs and pods alone, never a
// Secret.
func (k *Kube) Start(ctx context.Context) error {
	sctx, cancel := context.WithTimeout(ctx, watchSync)
	defer cancel()
	// A refused watch will leave the informers relisting, and the success of
	// their first lists does not show that. So each watch is tried once, for no
	// object.
	none := metav1.ListOptions{LabelSelector: runnerSelector, FieldSelector: "metadata.name=" + watchCheck}
	jw, err := k.Client.BatchV1().Jobs(k.Namespace).Watch(sctx, none)
	if err != nil {
		return fmt.Errorf("executor: watch runner jobs: %w", err)
	}
	jw.Stop()
	pw, err := k.Client.CoreV1().Pods(k.Namespace).Watch(sctx, none)
	if err != nil {
		return fmt.Errorf("executor: watch runner pods: %w", err)
	}
	pw.Stop()
	wctx, stop := context.WithCancel(context.WithoutCancel(ctx))
	watching := false
	defer func() {
		if !watching {
			stop()
		}
	}()
	factory := informers.NewSharedInformerFactoryWithOptions(k.Client, 0, informers.WithNamespace(k.Namespace),
		informers.WithTweakListOptions(func(o *metav1.ListOptions) { o.LabelSelector = runnerSelector }))
	jobs, pods := factory.Batch().V1().Jobs(), factory.Core().V1().Pods()
	w := &watches{jobs: jobs.Lister(), pods: pods.Lister(), stop: stop, woken: map[string]chan struct{}{}}
	handler := cache.ResourceEventHandlerFuncs{AddFunc: w.changed, UpdateFunc: func(_, obj any) { w.changed(obj) }, DeleteFunc: w.changed}
	var handled []cache.InformerSynced
	for _, i := range []cache.SharedIndexInformer{jobs.Informer(), pods.Informer()} {
		reg, err := i.AddEventHandler(handler)
		if err != nil {
			return fmt.Errorf("executor: watch runner jobs: %w", err)
		}
		handled = append(handled, reg.HasSynced)
	}
	factory.StartWithContext(wctx)
	if err := factory.WaitForCacheSyncWithContext(sctx).AsError(); err != nil {
		return fmt.Errorf("executor: watch runner jobs: not listed within %s: %w", watchSync, err)
	}
	// The lists are handed to the handler before any run registers. So an
	// event of a listed Job never stands in for the cache read that a
	// registration asks for.
	if !cache.WaitForCacheSync(sctx.Done(), handled...) {
		return fmt.Errorf("executor: watch runner jobs: not handled within %s", watchSync)
	}
	k.watch, watching = w, true
	return nil
}

// Stop ends the watch Start began, once no run reads it: after the queues
// stop.
func (k *Kube) Stop() {
	if k.watch != nil {
		k.watch.stop()
	}
}

// changed wakes the run waiting on the Job obj is, or whose pod it is.
func (w *watches) changed(obj any) {
	if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = d.Obj
	}
	var job string
	switch o := obj.(type) {
	case *batchv1.Job:
		job = o.Name
	case *corev1.Pod:
		job = o.Labels[jobNameLabel]
	}
	w.mu.Lock()
	ch := w.woken[job]
	w.mu.Unlock()
	if ch != nil {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// wait requests an initial cache read and wakes again on job's changes until
// done. done leaves alone a later wait on a Job of the same name.
func (w *watches) wait(job string) (wake <-chan struct{}, done func()) {
	ch := make(chan struct{}, 1)
	// Creation and Secret delivery can outlast the Job's watch events.
	ch <- struct{}{}
	w.mu.Lock()
	w.woken[job] = ch
	w.mu.Unlock()
	return ch, func() {
		w.mu.Lock()
		if w.woken[job] == ch {
			delete(w.woken, job)
		}
		w.mu.Unlock()
	}
}

// watchedPods are the Job's pods as the watch holds them.
func (k *Kube) watchedPods(job *batchv1.Job) []*corev1.Pod {
	// A selector built from a set always parses.
	pods, _ := k.watch.pods.Pods(k.Namespace).List(k8slabels.SelectorFromSet(k8slabels.Set{jobNameLabel: job.Name}))
	return ownPods(pods, job)
}
