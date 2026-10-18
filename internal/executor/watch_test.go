package executor

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
)

// TestKubeStartNeedsTheWatch: an executor the API lets list runner Jobs and
// pods but not watch them does not start.
func TestKubeStartNeedsTheWatch(t *testing.T) {
	for _, resource := range []schema.GroupResource{batchv1.Resource("jobs"), corev1.Resource("pods")} {
		t.Run(resource.Resource, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			client.PrependWatchReactor(resource.Resource, func(k8stesting.Action) (bool, watch.Interface, error) {
				return true, nil, apierrors.NewForbidden(resource, "", errors.New("no watch"))
			})
			k := &Kube{Client: client, Namespace: "kritika"}
			t.Cleanup(k.Stop)
			if err := k.Start(t.Context()); !apierrors.IsForbidden(err) {
				t.Fatalf("Start = %v, want the watch refused", err)
			}
		})
	}
}

// TestKubeStartBoundsAStalledWatch: a trial watch the API never answers
// fails Start once watchSync has passed, though ctx has no deadline.
func TestKubeStartBoundsAStalledWatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client, err := kubernetes.NewForConfig(&rest.Config{Host: "https://kube.invalid", Transport: stalled{}})
		if err != nil {
			t.Fatal(err)
		}
		k := &Kube{Client: client, Namespace: "kritika"}
		t.Cleanup(k.Stop)
		start := time.Now()
		if err := k.Start(t.Context()); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Start = %v, want the watch given up", err)
		}
		if waited := time.Since(start); waited != watchSync {
			t.Fatalf("Start waited %s, want %s", waited, watchSync)
		}
	})
}

// stalled is an API server that never answers.
type stalled struct{}

func (stalled) RoundTrip(r *http.Request) (*http.Response, error) {
	<-r.Context().Done()
	return nil, r.Context().Err()
}

// TestKubeStartBoundsTheFirstLists: lists the API never answers fail Start
// once watchSync has passed, with the wait's cause and every kind not
// listed.
func TestKubeStartBoundsTheFirstLists(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client, err := kubernetes.NewForConfig(&rest.Config{Host: "https://kube.invalid", Transport: unlisted{}})
		if err != nil {
			t.Fatal(err)
		}
		k := &Kube{Client: client, Namespace: "kritika"}
		t.Cleanup(k.Stop)
		err = k.Start(t.Context())
		if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "*v1.Job, *v1.Pod") {
			t.Fatalf("Start = %v, want both kinds not listed", err)
		}
	})
}

// unlisted is an API server that answers Start's trial watches and
// nothing else.
type unlisted struct{}

func (unlisted) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.Contains(r.URL.Query().Get("fieldSelector"), watchCheck) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: http.NoBody, Request: r}, nil
	}
	<-r.Context().Done()
	return nil, r.Context().Err()
}

// TestKubeStartWatchesRunnersAlone: every list and watch Start makes, its
// check's and its informers', selects runner Jobs and pods by their role
// label: the fake does not filter a watch by it, the API server does.
func TestKubeStartWatchesRunnersAlone(t *testing.T) {
	client := fake.NewSimpleClientset()
	k := newKube(t, client)
	k.Stop()
	resources := map[string]bool{}
	for _, a := range client.Actions() {
		var sel string
		switch a := a.(type) {
		case k8stesting.ListAction:
			sel = a.GetListRestrictions().Labels.String()
		case k8stesting.WatchAction:
			sel = a.GetWatchRestrictions().Labels.String()
		default:
			t.Fatalf("Start sent %s %s", a.GetVerb(), a.GetResource().Resource)
		}
		if sel != runnerSelector {
			t.Errorf("%s %s selects %q, want %q", a.GetVerb(), a.GetResource().Resource, sel, runnerSelector)
		}
		resources[a.GetVerb()+" "+a.GetResource().Resource] = true
	}
	for _, want := range []string{"list jobs", "watch jobs", "list pods", "watch pods"} {
		if !resources[want] {
			t.Errorf("Start did not %s", want)
		}
	}
}

// TestKubeWatchOutlastsStart: the watch goes on after Start's ctx, which a
// signal ends while runs still drain, has ended, until Stop.
func TestKubeWatchOutlastsStart(t *testing.T) {
	client := fake.NewSimpleClientset()
	k := &Kube{Client: client, Namespace: "kritika", PodTemplate: withImage("img"), ServiceAccount: "sa", DatabaseSecret: "s", DatabaseSecretKey: "uri", Poll: 10 * time.Millisecond}
	t.Cleanup(k.Stop)
	ctx, cancel := context.WithCancel(t.Context())
	if err := k.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	created := reacted(client, "create", "jobs")
	done := make(chan Result, 1)
	go func() { done <- k.Run(t.Context(), spec()) }()
	j := waitJob(t, client, created)
	j.Status.Succeeded = 1
	if _, err := client.BatchV1().Jobs("kritika").UpdateStatus(t.Context(), j, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case res := <-done:
		if res.Err != nil {
			t.Fatalf("res = %+v, want the Job's success", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not see the Job succeed")
	}
}

// TestWaitLeavesALaterWaitAlone: a wait on a Job ending does not keep a
// later wait, on another Job of its name, from being woken.
func TestWaitLeavesALaterWaitAlone(t *testing.T) {
	w := &watches{woken: map[string]chan struct{}{}}
	_, done := w.wait("job")
	wake, _ := w.wait("job")
	done()
	w.changed(&batchv1.Job{Name: "job"})
	select {
	case <-wake:
	default:
		t.Fatal("the later wait was not woken")
	}
}
