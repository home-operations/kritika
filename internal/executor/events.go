package executor

import (
	"cmp"
	"context"
	"log/slog"
	"os"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	recordutil "k8s.io/client-go/tools/record/util"

	"github.com/home-operations/kritika/internal/textcut"
)

// Events records Kubernetes Events, which kubectl describe and kubectl
// events show and event collectors gather: on a runner Job, why kritika
// ended its run; on this replica's own pod, what its leader did. It writes
// each Event itself rather than through client-go's broadcaster, which
// aggregates the floods a controller of many objects emits; kritika emits a
// few. Recording is best effort: a refused Event is logged, never an error.
type Events struct {
	client    kubernetes.Interface
	namespace string
	// pod is this replica's pod, its Name empty outside one; instance names
	// the replica as the Events' reporting instance.
	pod      corev1.ObjectReference
	instance string
	logger   *slog.Logger
	// pending counts the Events still being written that RunEvent and
	// PodEventAsync returned before.
	pending sync.WaitGroup
}

// Fields of every Event kritika records: the controller it names as
// reporting it, the longest reporting instance and note one holds, and how
// long its write may take.
const (
	eventController    = "kritika"
	eventInstanceBytes = 128
	eventNoteBytes     = 1024
	eventTimeout       = 10 * time.Second
)

// NewEvents records Events in namespace as the replica of pod podName,
// whose UID kubectl describe matches a pod's Events by. Without a pod name
// the replica records none on its pod.
func NewEvents(client kubernetes.Interface, namespace, podName, podUID string, logger *slog.Logger) *Events {
	host, _ := os.Hostname()
	instance := cmp.Or(podName, host, eventController)
	if len(instance) > eventInstanceBytes {
		instance = cmp.Or(podUID, instance[:eventInstanceBytes])
	}
	e := &Events{client: client, namespace: namespace, instance: instance, logger: logger}
	if podName != "" {
		e.pod = corev1.ObjectReference{APIVersion: "v1", Kind: "Pod", Namespace: namespace, Name: podName, UID: types.UID(podUID)}
	}
	return e
}

// RunEvent records an Event on the Job of res's run, none when no Job was
// created. It returns at once: a run's end waits for nothing, and Wait
// waits for what is still being written.
func (e *Events) RunEvent(ctx context.Context, res Result, warning bool, reason, note string) {
	if e == nil || res.JobName == "" {
		return
	}
	job := corev1.ObjectReference{APIVersion: "batch/v1", Kind: jobKind, Namespace: e.namespace, Name: res.JobName, UID: types.UID(res.JobUID)}
	e.pending.Go(func() { e.record(ctx, job, warning, reason, "EndRun", note) })
}

// Wait waits for the Events RunEvent and PodEventAsync are still writing,
// or until ctx ends.
func (e *Events) Wait(ctx context.Context) {
	if e == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		e.pending.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// PodEvent records an Event on this replica's pod.
func (e *Events) PodEvent(ctx context.Context, warning bool, reason, action, note string) {
	if e == nil || e.pod.Name == "" {
		return
	}
	e.record(ctx, e.pod, warning, reason, action, note)
}

// PodEventAsync records an Event on this replica's pod as PodEvent does,
// but returns at once, as RunEvent does: a webhook's answer must not wait
// on the Kubernetes API.
func (e *Events) PodEventAsync(ctx context.Context, warning bool, reason, action, note string) {
	if e == nil || e.pod.Name == "" {
		return
	}
	e.pending.Go(func() { e.record(ctx, e.pod, warning, reason, action, note) })
}

// record writes one Event. It often says why a run ended, which is when
// its ctx has: the write does not end with ctx.
func (e *Events) record(ctx context.Context, regarding corev1.ObjectReference, warning bool, reason, action, note string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), eventTimeout)
	defer cancel()
	eventType := corev1.EventTypeNormal
	if warning {
		eventType = corev1.EventTypeWarning
	}
	now := time.Now()
	event := &eventsv1.Event{
		Name: recordutil.GenerateEventName(regarding.Name, now.UnixNano()), Namespace: e.namespace,
		EventTime: metav1.NewMicroTime(now), ReportingController: eventController, ReportingInstance: e.instance,
		Action: action, Reason: reason, Regarding: regarding, Note: textcut.Truncate(note, eventNoteBytes), Type: eventType,
	}
	if _, err := e.client.EventsV1().Events(e.namespace).Create(ctx, event, metav1.CreateOptions{}); err != nil {
		e.logger.Warn("event not recorded", "object", regarding.Kind+"/"+regarding.Name, "reason", reason, "error", err)
	}
}
