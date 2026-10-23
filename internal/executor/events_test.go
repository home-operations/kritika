package executor

import (
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// TestEvents: an Event lands on the run's Job or on this replica's pod,
// named by its UID, as kritika's, with its note cut to what an Event
// holds; with no Job, no pod name, or off Kubernetes, there is none.
func TestEvents(t *testing.T) {
	client := fake.NewSimpleClientset()
	e := NewEvents(client, "kritika", "kritika-7c9f5d6b8-xk2lp", "pod-uid", slog.New(slog.DiscardHandler))
	e.RunEvent(t.Context(), Result{JobName: "kritika-run-01234567", JobUID: "job-uid"}, false, "Superseded", "a newer head superseded the run")
	e.RunEvent(t.Context(), Result{}, true, "NeverStarted", "no Job to name")
	e.PodEvent(t.Context(), true, "ConfigRefused", "ApplyConfiguration", strings.Repeat("x", 2000))
	NewEvents(client, "kritika", "", "", slog.New(slog.DiscardHandler)).PodEvent(t.Context(), false, "LeaderElected", "Lead", "no pod name")
	var off *Events
	off.PodEvent(t.Context(), false, "LeaderElected", "Lead", "off Kubernetes")
	e.Wait(t.Context())

	list, err := client.EventsV1().Events("kritika").List(t.Context(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 2 {
		t.Fatalf("events = %+v, want the Job's and the pod's", list.Items)
	}
	byReason := map[string]int{list.Items[0].Reason: 0, list.Items[1].Reason: 1}
	job := list.Items[byReason["Superseded"]]
	if job.Regarding != (corev1.ObjectReference{APIVersion: "batch/v1", Kind: "Job", Namespace: "kritika", Name: "kritika-run-01234567", UID: "job-uid"}) ||
		job.Type != corev1.EventTypeNormal || job.Action != "EndRun" || job.Note != "a newer head superseded the run" ||
		job.ReportingController != "kritika" || job.ReportingInstance != "kritika-7c9f5d6b8-xk2lp" || job.EventTime.IsZero() ||
		!strings.HasPrefix(job.Name, "kritika-run-01234567.") {
		t.Fatalf("Job event = %+v", job)
	}
	pod := list.Items[byReason["ConfigRefused"]]
	if pod.Regarding != (corev1.ObjectReference{APIVersion: "v1", Kind: "Pod", Namespace: "kritika", Name: "kritika-7c9f5d6b8-xk2lp", UID: "pod-uid"}) ||
		pod.Type != corev1.EventTypeWarning || pod.Action != "ApplyConfiguration" || len(pod.Note) > eventNoteBytes {
		t.Fatalf("pod event = %+v (note %d bytes)", pod, len(pod.Note))
	}
}

// TestEventsLongNames: a pod whose name is over what an Event's reporting
// instance or name may hold still gets its Events, the instance being its
// UID and the name generated.
func TestEventsLongNames(t *testing.T) {
	client := fake.NewSimpleClientset()
	pod := strings.Repeat("p", 250)
	NewEvents(client, "kritika", pod, "0c3a0bde-5c6d-4f6c-9a43-1f2b1e4e8a10", slog.New(slog.DiscardHandler)).
		PodEvent(t.Context(), false, "LeaderElected", "Lead", "this replica took the lead")
	list, err := client.EventsV1().Events("kritika").List(t.Context(), metav1.ListOptions{})
	if err != nil || len(list.Items) != 1 {
		t.Fatalf("events = %+v, %v", list, err)
	}
	e := list.Items[0]
	if e.ReportingInstance != "0c3a0bde-5c6d-4f6c-9a43-1f2b1e4e8a10" || e.Regarding.Name != pod ||
		len(validation.IsDNS1123Subdomain(e.Name)) != 0 {
		t.Fatalf("instance %q, regarding %q, name %q", e.ReportingInstance, e.Regarding.Name, e.Name)
	}
}

// TestPodEventAsync: the Event lands on this replica's pod after the call
// has returned, however long the API takes, and Wait waits for it; off
// Kubernetes or without a pod name there is none.
func TestPodEventAsync(t *testing.T) {
	client := fake.NewSimpleClientset()
	blocked := make(chan struct{})
	release := sync.OnceFunc(func() { close(blocked) })
	t.Cleanup(release)
	client.PrependReactor("create", "events", func(k8stesting.Action) (bool, runtime.Object, error) {
		<-blocked
		return false, nil, nil
	})
	e := NewEvents(client, "kritika", "kritika-7c9f5d6b8-xk2lp", "pod-uid", slog.New(slog.DiscardHandler))
	returned := make(chan struct{})
	go func() {
		e.PodEventAsync(t.Context(), true, "AppUninstalled", "Uninstall", "uninstalled from stranger")
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("PodEventAsync waited for the API to write the Event")
	}
	NewEvents(client, "kritika", "", "", slog.New(slog.DiscardHandler)).PodEventAsync(t.Context(), true, "AppUninstalled", "Uninstall", "no pod name")
	var off *Events
	off.PodEventAsync(t.Context(), true, "AppUninstalled", "Uninstall", "off Kubernetes")
	release()
	e.Wait(t.Context())

	list, err := client.EventsV1().Events("kritika").List(t.Context(), metav1.ListOptions{})
	if err != nil || len(list.Items) != 1 {
		t.Fatalf("events = %+v, %v; want the pod's one", list, err)
	}
	if ev := list.Items[0]; ev.Regarding.Name != "kritika-7c9f5d6b8-xk2lp" || ev.Type != corev1.EventTypeWarning ||
		ev.Reason != "AppUninstalled" || ev.Action != "Uninstall" || ev.Note != "uninstalled from stranger" {
		t.Fatalf("event = %+v", ev)
	}
}
