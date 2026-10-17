package executor

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"
	"unicode/utf8"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/home-operations/kritika/internal/runner"
)

const (
	headSHA = "0123456789abcdef0123456789abcdef01234567"
	baseSHA = "89abcdef0123456789abcdef0123456789abcdef"
	runA    = "aaaaaaaa-0000-0000-0000-000000000000"
	runB    = "bbbbbbbb-0000-0000-0000-000000000000"
)

func spec() Spec {
	return Spec{
		Labels:      map[string]string{"account": "acme", "pr": "42", "kind": "review"},
		Annotations: map[string]string{"head-sha": headSHA},
		Job: runner.Spec{
			Version: runner.SpecVersion, Kind: runner.KindReview, RunID: "0123456789abcdef-run",
			CloneURL: "https://forge.example.com/acme/widgets.git", Head: headSHA, Base: baseSHA,
			Ignore: []string{"vendor/**", "**/*.lock"},
			Agent:  &runner.AgentLimits{}, Model: &runner.ModelEndpoint{GatewayURL: "http://kritika-gateway:8082", Model: "review"},
			Prompt: &runner.Prompt{Repository: "acme/widgets"},
		},
		Secrets:  runner.Secrets{GitToken: "ghs_secret_token", GatewayToken: "krk_run_token", DatabaseToken: "db_run_token"},
		Deadline: 5 * time.Minute,
	}
}

// withImage is a pod template of the runner container alone, running image.
func withImage(image string) PodTemplate {
	return PodTemplate{Spec: PodTemplateSpec{Containers: []TemplateContainer{{Name: runnerRole, Image: image}}}}
}

func TestJobSpec(t *testing.T) {
	k := &Kube{Namespace: "kritika", ServiceAccount: "kritika-runner", DatabaseSecret: "kritika-postgres-runner", DatabaseSecretKey: "uri",
		DatabaseRole: "custom_runner", GatewayURL: "http://kritika-gateway:8082", PodTemplate: PodTemplate{Spec: PodTemplateSpec{
			RuntimeClassName: "gvisor",
			Containers:       []TemplateContainer{{Name: runnerRole, Image: "ttl.sh/x:1h", ImagePullPolicy: corev1.PullIfNotPresent}},
		}}}
	j := k.job(spec())
	if j.Name != "kritika-run-01234567" || j.Namespace != "kritika" {
		t.Fatalf("name/namespace = %s/%s", j.Name, j.Namespace)
	}
	if *j.Spec.ActiveDeadlineSeconds != 300 || *j.Spec.TTLSecondsAfterFinished != 600 || *j.Spec.BackoffLimit != 0 {
		t.Fatalf("deadline/ttl/backoff = %d/%d/%d", *j.Spec.ActiveDeadlineSeconds, *j.Spec.TTLSecondsAfterFinished, *j.Spec.BackoffLimit)
	}
	if j.Labels["kritika.home-operations.com/account"] != "acme" || j.Annotations["kritika.home-operations.com/head-sha"] != headSHA ||
		j.Spec.Template.Labels["kritika.home-operations.com/role"] != "runner" {
		t.Fatalf("labels/annotations = %v %v", j.Labels, j.Annotations)
	}
	pod := j.Spec.Template.Spec
	if pod.ServiceAccountName != "kritika-runner" || *pod.AutomountServiceAccountToken || pod.RestartPolicy != corev1.RestartPolicyNever {
		t.Fatalf("pod spec = %+v", pod)
	}
	if d := pod.DNSConfig; d == nil || len(d.Options) != 1 || d.Options[0].Name != "ndots" || d.Options[0].Value == nil || *d.Options[0].Value != "4" {
		t.Fatalf("dnsConfig = %+v, want ndots 4", d)
	}
	c := pod.Containers[0]
	if c.Image != "ttl.sh/x:1h" || c.ImagePullPolicy != corev1.PullIfNotPresent || len(c.Args) != 1 || c.Args[0] != "run" {
		t.Fatalf("container = %+v", c)
	}
	checkRunnerEnv(t, c.Env)
	checkProxyEnv(t, c.Env, "http://:$(KRITIKA_GATEWAY_TOKEN)@kritika-gateway:8082")
	checkSpecMount(t, pod, c)
	if env := (&Kube{Namespace: "kritika", PodTemplate: withImage("x")}).job(spec()).Spec.Template.Spec.Containers[0].Env; slices.ContainsFunc(env,
		func(e corev1.EnvVar) bool { return e.Name == "HTTPS_PROXY" }) {
		t.Fatal("a Kube without a gateway must hand the runner no proxy")
	}
	if !*c.SecurityContext.ReadOnlyRootFilesystem || !*pod.SecurityContext.RunAsNonRoot {
		t.Fatal("runner pod must be read-only and non-root")
	}
	if pod.RuntimeClassName == nil || *pod.RuntimeClassName != "gvisor" {
		t.Fatalf("runtimeClassName = %v, want gvisor", pod.RuntimeClassName)
	}
}

// TestJobSpecDefaults: templates that name the runner's image alone leave
// the pod's runtime, resources, affinity and pull policy to the cluster,
// and keep a finished Job for the default TTL.
func TestJobSpecDefaults(t *testing.T) {
	j := (&Kube{Namespace: "kritika", PodTemplate: withImage("x")}).job(spec())
	if pod := j.Spec.Template.Spec; pod.RuntimeClassName != nil || pod.Resources != nil || pod.Affinity != nil || pod.Containers[0].ImagePullPolicy != "" {
		t.Fatalf("pod spec = %+v, want the cluster's defaults", pod)
	}
	if *j.Spec.TTLSecondsAfterFinished != int32(defaultTTL/time.Second) {
		t.Fatalf("ttl = %d, want the default", *j.Spec.TTLSecondsAfterFinished)
	}
}

// TestJobSpecPodFailurePolicy: a pod Kubernetes disrupts fails its Job by
// the pod failure policy, which names the failure once the pod is gone.
func TestJobSpecPodFailurePolicy(t *testing.T) {
	j := (&Kube{Namespace: "kritika", PodTemplate: withImage("x")}).job(spec())
	if p := j.Spec.PodFailurePolicy; p == nil || len(p.Rules) != 1 || p.Rules[0].Action != batchv1.PodFailurePolicyActionFailJob ||
		len(p.Rules[0].OnPodConditions) != 1 || p.Rules[0].OnPodConditions[0] != (batchv1.PodFailurePolicyOnPodConditionsPattern{
		Type: corev1.DisruptionTarget, Status: corev1.ConditionTrue}) {
		t.Fatalf("podFailurePolicy = %+v, want a disrupted pod to fail the Job by the policy", p)
	}
}

func TestJobSpecKeepsImagePath(t *testing.T) {
	pod := (&Kube{Namespace: "kritika", PodTemplate: withImage("x")}).job(spec()).Spec.Template.Spec
	for _, v := range pod.Volumes {
		if v.Image != nil {
			t.Fatalf("unexpected image volume: %s", v.Name)
		}
	}
	if slices.ContainsFunc(pod.Containers[0].Env, func(e corev1.EnvVar) bool { return e.Name == "PATH" }) {
		t.Fatal("the runner must keep the image's own PATH")
	}
}

// checkProxyEnv asserts the runner is pointed at the gateway for both
// schemes, with the run token the kubelet fills in as the password, and
// excepts only loopback and the gateway's own model endpoint.
func checkProxyEnv(t *testing.T, vars []corev1.EnvVar, gateway string) {
	t.Helper()
	env := map[string]string{}
	for _, e := range vars {
		env[e.Name] = e.Value
	}
	if env["HTTPS_PROXY"] != gateway || env["HTTP_PROXY"] != gateway || env["NO_PROXY"] != "localhost,127.0.0.1,kritika-gateway" {
		t.Fatalf("proxy env = %v", env)
	}
}

// checkRunnerEnv asserts the runner gets its job document, its credentials
// only through secret references, and nothing it must never see.
func checkRunnerEnv(t *testing.T, vars []corev1.EnvVar) {
	t.Helper()
	env := map[string]corev1.EnvVar{}
	for _, e := range vars {
		env[e.Name] = e
	}
	if e, ok := env["KRITIKA_RUN_SPEC_FILE"]; !ok || e.Value != "/var/run/kritika/spec.json" {
		t.Fatalf("KRITIKA_RUN_SPEC_FILE = %+v", e)
	}
	if _, ok := env["KRITIKA_RUN_SPEC"]; ok {
		t.Fatal("the job document must not travel as a variable")
	}
	for _, e := range vars {
		if strings.Contains(e.Value, "ghs_secret_token") || strings.Contains(e.Value, "krk_run_token") || strings.Contains(e.Value, "db_run_token") {
			t.Fatalf("%s carries a secret in plain text", e.Name)
		}
	}
	for name, want := range map[string]struct {
		key      string
		optional bool
	}{"KRITIKA_GIT_TOKEN": {"git-token", false}, "KRITIKA_GATEWAY_TOKEN": {"gateway-token", false}, "KRITIKA_DATABASE_TOKEN": {"database-token", false}} {
		ref := env[name].ValueFrom
		if ref == nil || ref.SecretKeyRef == nil || ref.SecretKeyRef.Name != "kritika-run-0123456789abcdef-run" || ref.SecretKeyRef.Key != want.key ||
			(ref.SecretKeyRef.Optional != nil && *ref.SecretKeyRef.Optional) != want.optional {
			t.Fatalf("%s = %+v", name, env[name])
		}
	}
	for _, name := range []string{"KRITIKA_RUN_KIND", "KRITIKA_RUN_ID", "KRITIKA_CLONE_URL", "KRITIKA_HEAD_SHA", "KRITIKA_BASE_SHA", "KRITIKA_IGNORE"} {
		if _, ok := env[name]; ok {
			t.Fatalf("%s is replaced by the job document", name)
		}
	}
	if env["KRITIKA_DATABASE_RUNNER_ROLE"].Value != "custom_runner" {
		t.Fatalf("runner role = %v", env["KRITIKA_DATABASE_RUNNER_ROLE"])
	}
	if ref := env["KRITIKA_DATABASE_URL"].ValueFrom.SecretKeyRef; ref.Name != "kritika-postgres-runner" || ref.Key != "uri" {
		t.Fatalf("db env = %+v", ref)
	}
	for _, name := range []string{"KRITIKA_EMBED_API_KEY", "KRITIKA_DATABASE_OWNER_URL", "OPENROUTER_API_KEY"} {
		if _, leaked := env[name]; leaked {
			t.Fatalf("%s must never reach a runner pod", name)
		}
	}
}

// checkSpecMount asserts the job document is mounted read-only from the
// run's Secret, and only that key of it.
func checkSpecMount(t *testing.T, pod corev1.PodSpec, c corev1.Container) {
	t.Helper()
	var mounted bool
	for _, m := range c.VolumeMounts {
		if m.Name == "spec" {
			mounted = m.MountPath == "/var/run/kritika" && m.ReadOnly
		}
	}
	var vol *corev1.SecretVolumeSource
	for _, v := range pod.Volumes {
		if v.Name == "spec" {
			vol = v.Secret
		}
	}
	if !mounted || vol == nil || vol.SecretName != "kritika-run-0123456789abcdef-run" || len(vol.Items) != 1 ||
		vol.Items[0].Key != "run-spec.json" || vol.Items[0].Path != "spec.json" {
		t.Fatalf("spec mount = %v, volume = %+v", mounted, vol)
	}
}

func TestKubeRunRejectsOversizedSpecBeforeCreatingAnything(t *testing.T) {
	client := fake.NewSimpleClientset()
	s := spec()
	s.Job.RepoFiles = []string{strings.Repeat("<", runner.MaxSpecBytes)}
	res := newKube(t, client).Run(t.Context(), s)
	if res.Err == nil || !strings.Contains(res.Err.Error(), "byte limit") {
		t.Fatalf("err = %v", res.Err)
	}
	if n := len(calls(client)); n != 0 {
		t.Fatalf("%d API calls for a spec that cannot be delivered", n)
	}
}

func TestKubeRunWaitsForCompletion(t *testing.T) {
	client := fake.NewSimpleClientset()
	k := newKube(t, client)
	ctx := t.Context()
	created := reacted(client, "create", "jobs")
	done := make(chan Result, 1)
	go func() { done <- k.Run(ctx, spec()) }()

	// Let the Job get created, then mark it succeeded with a finished pod.
	name := waitJob(t, client, created).Name
	_, _ = client.CoreV1().Pods("kritika").Create(ctx, &corev1.Pod{
		Name: name + "-abcde", Namespace: "kritika", Labels: map[string]string{"job-name": name},
		Spec: corev1.PodSpec{NodeName: "k8s-1"},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{State: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Reason: "Completed"}}}}},
	}, metav1.CreateOptions{})
	j, _ := client.BatchV1().Jobs("kritika").Get(ctx, name, metav1.GetOptions{})
	j.Status.Succeeded = 1
	_, _ = client.BatchV1().Jobs("kritika").UpdateStatus(ctx, j, metav1.UpdateOptions{})

	select {
	case res := <-done:
		if res.Err != nil || res.JobName != name || res.NodeName != "k8s-1" || res.TerminationReason != "Completed" {
			t.Fatalf("result = %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the job succeeded")
	}
}

// TestKubeRunDeletesTheJobWhenCancelled checks the Job is actually gone
// after a plain cancellation, not only that a delete was sent.
func TestKubeRunDeletesTheJobWhenCancelled(t *testing.T) {
	client := fake.NewSimpleClientset()
	k := newKube(t, client)
	ctx, cancel := context.WithCancel(t.Context())
	created := reacted(client, "create", "jobs")
	done := make(chan Result, 1)
	go func() { done <- k.Run(ctx, spec()) }()
	waitJob(t, client, created)
	cancel()
	select {
	case res := <-done:
		if !errors.Is(res.Err, context.Canceled) {
			t.Fatalf("result = %+v; want the cancellation surfaced", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
	jobs, _ := client.BatchV1().Jobs("kritika").List(t.Context(), metav1.ListOptions{})
	if len(jobs.Items) != 0 {
		t.Fatalf("the orphaned Job must be deleted, %d left", len(jobs.Items))
	}
}

func TestKubeRunReportsFailure(t *testing.T) {
	client := fake.NewSimpleClientset()
	k := newKube(t, client)
	ctx := t.Context()
	created := reacted(client, "create", "jobs")
	done := make(chan Result, 1)
	go func() { done <- k.Run(ctx, spec()) }()
	j := waitJob(t, client, created)
	j.Status.Failed = 1
	j.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "DeadlineExceeded"}}
	_, _ = client.BatchV1().Jobs("kritika").UpdateStatus(ctx, j, metav1.UpdateOptions{})
	res := <-done
	if res.Err == nil || res.NeverStarted {
		t.Fatalf("res = %+v, want the error of a job that ran and failed", res)
	}
}

// TestKubeRunGivesUpAJobWithNoPod: a Job the namespace never lets create
// its pod is given up after StartGrace as a run that never started, with
// the Job controller's latest FailedCreate refusal as the reason, and
// deleted.
func TestKubeRunGivesUpAJobWithNoPod(t *testing.T) {
	client := fake.NewSimpleClientset()
	k := newKube(t, client)
	k.StartGrace = 200 * time.Millisecond
	created := reacted(client, "create", "jobs")
	done := make(chan Result, 1)
	go func() { done <- k.Run(t.Context(), spec()) }()
	j := waitJob(t, client, created)
	now := time.Now()
	for _, e := range []eventsv1.Event{
		{Name: "a", Reason: "FailedCreate", Note: `pods "a" is forbidden: exceeded quota: no-pods`, EventTime: metav1.NewMicroTime(now)},
		{Name: "b", Reason: "FailedCreate", Note: `pods "b" is forbidden: exceeded quota: no-pods, used: pods=0`,
			EventTime: metav1.NewMicroTime(now.Add(time.Second))},
		{Name: "c", Reason: "SuccessfulDelete", Note: "not why", EventTime: metav1.NewMicroTime(now.Add(2 * time.Second))},
	} {
		e.Namespace, e.Regarding = "kritika", corev1.ObjectReference{Kind: "Job", Name: j.Name}
		if _, err := client.EventsV1().Events("kritika").Create(t.Context(), &e, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	var res Result
	select {
	case res = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not give up a Job with no pod")
	}
	want := noPod + `: pods "b" is forbidden: exceeded quota: no-pods, used: pods=0`
	if !res.NeverStarted || res.TerminationReason != want || res.Err == nil {
		t.Fatalf("res = %+v, want a run that never started, %q", res, want)
	}
	if jobs, _ := client.BatchV1().Jobs("kritika").List(t.Context(), metav1.ListOptions{}); len(jobs.Items) != 0 {
		t.Fatalf("the Job must be deleted, %d left", len(jobs.Items))
	}
}

// TestKubeRunJobFailedWithNoPod: a Job that fails, at its deadline, before
// any pod was created for it is a run that never started.
func TestKubeRunJobFailedWithNoPod(t *testing.T) {
	client := fake.NewSimpleClientset()
	k := newKube(t, client)
	k.StartGrace = time.Hour
	created := reacted(client, "create", "jobs")
	done := make(chan Result, 1)
	go func() { done <- k.Run(t.Context(), spec()) }()
	j := waitJob(t, client, created)
	j.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "DeadlineExceeded",
		Message: "Job was active longer than specified deadline"}}
	if _, err := client.BatchV1().Jobs("kritika").UpdateStatus(t.Context(), j, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	res := <-done
	want := noPod + "; then the Job failed: DeadlineExceeded: Job was active longer than specified deadline"
	if !res.NeverStarted || res.TerminationReason != want || res.Err == nil || !strings.Contains(res.Err.Error(), "never started") {
		t.Fatalf("res = %+v, want a run that never started, %q", res, want)
	}
}

// runningStatus is a pod whose container has started.
var runningStatus = corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
	State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
}}}

// TestKubeRunLetsAPodCreatedLateRun: a pod the Job controller creates while
// the executor reads why there was none runs on, and the run ends with it.
func TestKubeRunLetsAPodCreatedLateRun(t *testing.T) {
	client := fake.NewSimpleClientset()
	k := newKube(t, client)
	k.StartGrace = 50 * time.Millisecond
	job := jobName(spec().Job.RunID)
	read := make(chan struct{}, 1)
	client.PrependReactor("list", "events", func(k8stesting.Action) (bool, runtime.Object, error) {
		// The tracker has its own lock, which the Fake's reactors do not hold.
		if err := client.Tracker().Add(runnerPod(job, runningStatus)); err != nil {
			t.Error(err)
		}
		select {
		case read <- struct{}{}:
		default:
		}
		return false, nil, nil
	})
	created := reacted(client, "create", "jobs")
	done := make(chan Result, 1)
	go func() { done <- k.Run(t.Context(), spec()) }()
	j := waitJob(t, client, created)
	select {
	case <-read:
	case <-time.After(5 * time.Second):
		t.Fatal("the executor never read why the Job had no pod")
	}
	j.Status.Succeeded = 1
	if _, err := client.BatchV1().Jobs("kritika").UpdateStatus(t.Context(), j, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if res := <-done; res.Err != nil || res.NeverStarted {
		t.Fatalf("res = %+v, want the run that went on in its pod", res)
	}
}

// TestEnded: a Job that failed with none of its pods counted failed is a
// run that never started only when nothing says it ran: no container seen
// running, no pod finish found, and a pod list that succeeds and is empty.
func TestEnded(t *testing.T) {
	job := &batchv1.Job{Name: "job", Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{
		Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "DeadlineExceeded",
	}}}}
	for _, tt := range []struct {
		name        string
		started     bool
		podName     string
		pod         bool
		unreadable  bool
		wantStarted bool
	}{
		{name: "nothing ran", wantStarted: false},
		{name: "a container was seen running", started: true, wantStarted: true},
		{name: "finish found a pod since gone", podName: "job-abcde", wantStarted: true},
		{name: "a pod is there", pod: true, wantStarted: true},
		{name: "the pods cannot be read", unreadable: true, wantStarted: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			if tt.pod {
				if _, err := client.CoreV1().Pods("kritika").Create(t.Context(), runnerPod("job", corev1.PodStatus{}), metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			if tt.unreadable {
				client.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, errors.New("the server is currently unable to handle the request")
				})
			}
			res := Result{JobName: "job", PodName: tt.podName}
			(&Kube{Client: client, Namespace: "kritika"}).ended(t.Context(), &res, job, tt.started)
			if res.Err == nil || res.NeverStarted == tt.wantStarted {
				t.Fatalf("res = %+v, want started %v", res, tt.wantStarted)
			}
		})
	}
}

// TestKubeRunDisruptedPod: a pod Kubernetes disrupted is reported as such,
// with its DisruptionTarget condition, or, once it is gone, with the
// failure the Job's pod failure policy gave the Job.
func TestKubeRunDisruptedPod(t *testing.T) {
	for _, tt := range []struct {
		name      string
		pod       bool
		reason    string
		condition batchv1.JobConditionType
		want      string
	}{
		{name: "the pod is there", pod: true, reason: batchv1.JobReasonBackoffLimitExceeded, condition: batchv1.JobFailed,
			want: "EvictionByEvictionAPI: Eviction API: evicting"},
		{name: "the pod is gone", reason: batchv1.JobReasonPodFailurePolicy, condition: batchv1.JobFailed,
			want: "the Job failed: PodFailurePolicy: Pod kritika/kritika-run-01234567-abcde has condition DisruptionTarget matching FailJob rule at index 0"},
		{name: "the pod is gone, the Job counted it before it failed", reason: batchv1.JobReasonPodFailurePolicy, condition: batchv1.JobFailureTarget,
			want: "the Job failed: PodFailurePolicy: Pod kritika/kritika-run-01234567-abcde has condition DisruptionTarget matching FailJob rule at index 0"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			k := newKube(t, client)
			created := reacted(client, "create", "jobs")
			done := make(chan Result, 1)
			go func() { done <- k.Run(t.Context(), spec()) }()
			j := waitJob(t, client, created)
			if tt.pod {
				evicted := corev1.PodStatus{Phase: corev1.PodFailed, Conditions: []corev1.PodCondition{{
					Type: corev1.DisruptionTarget, Status: corev1.ConditionTrue, Reason: "EvictionByEvictionAPI", Message: "Eviction API: evicting",
				}}}
				if _, err := client.CoreV1().Pods("kritika").Create(t.Context(), runnerPod(j.Name, evicted), metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			j.Status.Failed = 1
			j.Status.Conditions = []batchv1.JobCondition{{Type: tt.condition, Status: corev1.ConditionTrue, Reason: tt.reason,
				Message: "Pod kritika/" + j.Name + "-abcde has condition DisruptionTarget matching FailJob rule at index 0"}}
			if _, err := client.BatchV1().Jobs("kritika").UpdateStatus(t.Context(), j, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			res := <-done
			if res.Disruption != tt.want || res.Err == nil || !strings.Contains(res.Err.Error(), "pod was disrupted") || res.NeverStarted {
				t.Fatalf("res = %+v, want a disrupted run, %q", res, tt.want)
			}
		})
	}
}

// TestWhyNoPod: the latest FailedCreate Event of the Job names why it has
// no pod, however it says when it was seen: as a series, an Event's time,
// or the core API's last timestamp; with none to read, noPod alone.
func TestWhyNoPod(t *testing.T) {
	now := time.Now()
	client := fake.NewSimpleClientset()
	for _, e := range []eventsv1.Event{
		{Name: "core", Note: "core, newest", DeprecatedLastTimestamp: metav1.NewTime(now.Add(3 * time.Second))},
		{Name: "series", Note: "series, older", EventTime: metav1.NewMicroTime(now),
			Series: &eventsv1.EventSeries{Count: 2, LastObservedTime: metav1.NewMicroTime(now.Add(2 * time.Second))}},
		{Name: "event", Note: "event, oldest", EventTime: metav1.NewMicroTime(now.Add(time.Second))},
		{Name: "other", Note: "another Job's", EventTime: metav1.NewMicroTime(now.Add(time.Hour)), Regarding: corev1.ObjectReference{Kind: "Job", Name: "other"}},
	} {
		e.Namespace, e.Reason = "kritika", "FailedCreate"
		if e.Regarding.Name == "" {
			e.Regarding = corev1.ObjectReference{Kind: "Job", Name: "job"}
		}
		if _, err := client.EventsV1().Events("kritika").Create(t.Context(), &e, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if got := newKube(t, client).whyNoPod(t.Context(), "job"); got != noPod+": core, newest" {
		t.Fatalf("whyNoPod = %q", got)
	}
	refusing := fake.NewSimpleClientset()
	refusing.PrependReactor("list", "events", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("events is forbidden")
	})
	if got := newKube(t, refusing).whyNoPod(t.Context(), "job"); got != noPod {
		t.Fatalf("whyNoPod without its Events = %q", got)
	}
}

// TestKubeRunGivesUpAMissingJob: a Job someone else deleted, gone from the
// watch, is given up after maxMissing ticks, deleted in case it is still
// there, so it does not run on beside the worker's retry.
func TestKubeRunGivesUpAMissingJob(t *testing.T) {
	client := fake.NewSimpleClientset()
	k := newKube(t, client)
	created := reacted(client, "create", "jobs")
	done := make(chan Result, 1)
	go func() { done <- k.Run(t.Context(), spec()) }()
	j := waitJob(t, client, created)
	if err := client.BatchV1().Jobs("kritika").Delete(t.Context(), j.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	var res Result
	select {
	case res = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not give up a Job that went missing")
	}
	if !res.Abandoned || res.Err == nil || !strings.Contains(res.Err.Error(), "not found") {
		t.Fatalf("res = %+v, want the Job abandoned as not found", res)
	}
}

// TestKubeRunTakesNoOtherJobForItsOwn: a Job of the run's name the watch
// still holds, not the one the run created by its UID, does not end the
// run: the run gives its own up as missing, deleting that one alone.
func TestKubeRunTakesNoOtherJobForItsOwn(t *testing.T) {
	client := fake.NewSimpleClientset(&batchv1.Job{
		Name: jobName(spec().Job.RunID), Namespace: "kritika", UID: "earlier",
		Labels: map[string]string{"kritika.home-operations.com/role": "runner"}, Status: batchv1.JobStatus{Succeeded: 1},
	})
	k := newKube(t, client)
	client.PrependReactor("create", "jobs", func(a k8stesting.Action) (bool, runtime.Object, error) {
		j := a.(k8stesting.CreateAction).GetObject().(*batchv1.Job).DeepCopy()
		j.UID = "this-run"
		return true, j, nil
	})
	deleted := reacted(client, "delete", "jobs")
	res := k.Run(t.Context(), spec())
	if !res.Abandoned || res.Err == nil || !strings.Contains(res.Err.Error(), "uid earlier") {
		t.Fatalf("res = %+v, want the run's Job given up as missing", res)
	}
	p := awaitAction(t, deleted, "the Job's deletion").(k8stesting.DeleteAction).GetDeleteOptions().Preconditions
	if p == nil || p.UID == nil || *p.UID != "this-run" {
		t.Fatalf("delete preconditions = %+v, want the run's Job's UID", p)
	}
}

// TestKubeRunTakesNoOtherJobsPod: a pod of an earlier Job of the run's
// name, by its controller, which the watch still holds as it ran, is not
// the run's: the run's Job, which never had a pod, is given up as never
// started.
func TestKubeRunTakesNoOtherJobsPod(t *testing.T) {
	job := jobName(spec().Job.RunID)
	earlier := runnerPod(job, runningStatus)
	earlier.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: job, UID: "earlier", Controller: new(true)}}
	client := fake.NewSimpleClientset(earlier)
	k := newKube(t, client)
	k.StartGrace = 30 * time.Millisecond
	client.PrependReactor("create", "jobs", func(a k8stesting.Action) (bool, runtime.Object, error) {
		a.(k8stesting.CreateAction).GetObject().(*batchv1.Job).UID = "this-run"
		return false, nil, nil
	})
	done := make(chan Result, 1)
	go func() { done <- k.Run(t.Context(), spec()) }()
	select {
	case res := <-done:
		if !res.NeverStarted || res.TerminationReason != noPod {
			t.Fatalf("res = %+v, want a run that never started, %q", res, noPod)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run took the earlier Job's pod for its own")
	}
}

// TestKubeRunRemembersAStartedPod: a run whose container was seen running
// started, though its pod is gone by the time its Job fails without
// counting it.
func TestKubeRunRemembersAStartedPod(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := fake.NewSimpleClientset()
		k := newKube(t, client)
		k.StartGrace = time.Hour
		created := reacted(client, "create", "jobs")
		done := make(chan Result, 1)
		go func() { done <- k.Run(t.Context(), spec()) }()
		j := waitJob(t, client, created)
		// Run waits on its Job, then has seen its pod running.
		synctest.Wait()
		pod := runnerPod(j.Name, runningStatus)
		if _, err := client.CoreV1().Pods("kritika").Create(t.Context(), pod, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if err := client.CoreV1().Pods("kritika").Delete(t.Context(), pod.Name, metav1.DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
		j.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "DeadlineExceeded"}}
		if _, err := client.BatchV1().Jobs("kritika").UpdateStatus(t.Context(), j, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		if res := <-done; res.NeverStarted || res.Err == nil {
			t.Fatalf("res = %+v, want a run that started and failed", res)
		}
	})
}

// newKube is an executor over client whose watch has started.
func newKube(t *testing.T, client *fake.Clientset) *Kube {
	t.Helper()
	k := &Kube{Client: client, Namespace: "kritika", PodTemplate: withImage("img"), ServiceAccount: "sa", DatabaseSecret: "s", DatabaseSecretKey: "uri", Poll: 10 * time.Millisecond}
	t.Cleanup(k.Stop)
	if err := k.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	return k
}

// calls are the actions Run sent the fake, without its watch's.
func calls(client *fake.Clientset) []k8stesting.Action {
	return slices.DeleteFunc(client.Actions(), func(a k8stesting.Action) bool {
		return a.GetVerb() == "list" && (a.GetResource().Resource == "jobs" || a.GetResource().Resource == "pods") || a.GetVerb() == "watch"
	})
}

// reacted hands over each action with verb on resource that Run sends the
// fake. The Fake holds its lock from the reactor through storing the
// object, so a read made after a receive sees the stored state.
func reacted(client *fake.Clientset, verb, resource string) <-chan k8stesting.Action {
	ch := make(chan k8stesting.Action, 8)
	client.PrependReactor(verb, resource, func(a k8stesting.Action) (bool, runtime.Object, error) {
		ch <- a
		return false, nil, nil
	})
	return ch
}

// awaitAction returns the next action on ch, or fails the test.
func awaitAction(t *testing.T, ch <-chan k8stesting.Action, what string) k8stesting.Action {
	t.Helper()
	select {
	case a := <-ch:
		return a
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not happen", what)
		return nil
	}
}

// waitJob returns the Job once Run has created it, as the fake holds it.
func waitJob(t *testing.T, client *fake.Clientset, created <-chan k8stesting.Action) *batchv1.Job {
	t.Helper()
	name := awaitAction(t, created, "job creation").(k8stesting.CreateAction).GetObject().(*batchv1.Job).Name
	jobs, err := client.BatchV1().Jobs("kritika").List(t.Context(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.IndexFunc(jobs.Items, func(j batchv1.Job) bool { return j.Name == name }); i >= 0 {
		return &jobs.Items[i]
	}
	t.Fatalf("job %s is not held by the fake", name)
	return nil
}

func TestDeleteRun(t *testing.T) {
	runJob := func(runID string) *batchv1.Job {
		return &batchv1.Job{Name: jobName(runID), Namespace: "kritika"}
	}
	tests := []struct {
		name    string
		objects []runtime.Object
		failure error
		wantErr bool
	}{
		{name: "present", objects: []runtime.Object{runJob(runA), runJob(runB)}},
		{name: "already gone", objects: []runtime.Object{runJob(runB)}},
		{name: "api failure", objects: []runtime.Object{runJob(runA)}, failure: apierrors.NewForbidden(batchv1.Resource("jobs"), "x", errors.New("no")), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewClientset(tt.objects...)
			if tt.failure != nil {
				client.PrependReactor("delete", "jobs", func(k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, tt.failure
				})
			}
			k := &Kube{Client: client, Namespace: "kritika"}
			err := k.DeleteRun(t.Context(), runA)
			if (err != nil) != tt.wantErr {
				t.Fatalf("DeleteRun() error = %v, wantErr %v", err, tt.wantErr)
			}
			list, err := client.BatchV1().Jobs("kritika").List(t.Context(), metav1.ListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			for _, j := range list.Items {
				if j.Name == jobName(runA) && !tt.wantErr {
					t.Fatalf("job %s still present", j.Name)
				}
			}
			if len(tt.objects) > 1 && len(list.Items) == 0 {
				t.Fatal("the other run's Job was deleted too")
			}
		})
	}
}

// TestKubeRunSecretLifecycle: the run's Secret, named by the whole run ID,
// is created after its Job, with the Job as its owner from the start, and
// never changed.
func TestKubeRunSecretLifecycle(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("create", "jobs", func(a k8stesting.Action) (bool, runtime.Object, error) {
		a.(k8stesting.CreateAction).GetObject().(*batchv1.Job).UID = "this-run"
		return false, nil, nil
	})
	k := newKube(t, client)
	k.PodTemplate.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "KONFLATE_URL", Value: "https://konflate.internal.example"},
		{Name: "API_TOKEN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{Key: "token"}}}}
	ctx, cancel := context.WithCancel(t.Context())
	created := reacted(client, "create", "secrets")
	done := make(chan Result, 1)
	s := spec()
	go func() { done <- k.Run(ctx, s) }()
	name := awaitAction(t, created, "the Secret's creation").(k8stesting.CreateAction).GetObject().(*corev1.Secret).Name
	if name != "kritika-run-0123456789abcdef-run" {
		t.Fatalf("secret name = %q, want the whole run ID in it", name)
	}
	sec, err := client.CoreV1().Secrets("kritika").Get(t.Context(), name, metav1.GetOptions{})
	if err != nil || len(sec.OwnerReferences) != 1 {
		t.Fatalf("secret = %+v, %v; want one owner reference", sec, err)
	}
	if string(sec.Data["git-token"]) != "ghs_secret_token" || string(sec.Data["gateway-token"]) != "krk_run_token" ||
		string(sec.Data["database-token"]) != "db_run_token" {
		t.Fatalf("secret data = %v", sec.Data)
	}
	got, err := runner.DecodeSpec(sec.Data["run-spec.json"])
	if err != nil || got.Head != headSHA || got.Base != baseSHA || strings.Join(got.Ignore, ",") != "vendor/**,**/*.lock" {
		t.Fatalf("run spec = %+v, %v", got, err)
	}
	// The run's commands get the admin's variables by name, the Secret's
	// masked; the worker's own spec is left as it was.
	if !slices.Equal(got.Agent.Env, []runner.CommandEnvVar{{Name: "KONFLATE_URL"}, {Name: "API_TOKEN", Secret: true}}) || s.Job.Agent.Env != nil {
		t.Fatalf("run spec env = %+v, worker's = %+v", got.Agent.Env, s.Job.Agent.Env)
	}
	if sec.Labels["kritika.home-operations.com/role"] != "runner" || sec.Labels["kritika.home-operations.com/account"] != "acme" {
		t.Fatalf("secret labels = %v", sec.Labels)
	}
	ref := sec.OwnerReferences[0]
	if ref.Kind != "Job" || ref.APIVersion != "batch/v1" || ref.Name != jobName(spec().Job.RunID) || ref.UID != "this-run" ||
		ref.Controller == nil || *ref.Controller || ref.BlockOwnerDeletion == nil || !*ref.BlockOwnerDeletion {
		t.Fatalf("owner reference = %+v", ref)
	}

	var order []string
	for _, a := range calls(client) {
		if a.GetVerb() != "get" {
			order = append(order, a.GetVerb()+" "+a.GetResource().Resource)
		}
	}
	if strings.Join(order, ",") != "create jobs,create secrets" {
		t.Fatalf("order = %v", order)
	}
	cancel()
	<-done
}

// TestKubeRunNeverStartsWhenSecretCreateFails: a Secret the API refuses
// leaves a run that never started, its Job deleted rather than left waiting
// for the Secret; a refusal is not sent again.
func TestKubeRunNeverStartsWhenSecretCreateFails(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("create", "jobs", func(a k8stesting.Action) (bool, runtime.Object, error) {
		a.(k8stesting.CreateAction).GetObject().(*batchv1.Job).UID = "this-run"
		return false, nil, nil
	})
	sent := 0
	client.PrependReactor("create", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		sent++
		return true, nil, apierrors.NewForbidden(corev1.Resource("secrets"), "", errors.New("denied"))
	})
	deleted := reacted(client, "delete", "jobs")
	res := newKube(t, client).Run(t.Context(), spec())
	if !apierrors.IsForbidden(res.Err) || !res.NeverStarted || sent != 1 {
		t.Fatalf("res = %+v after %d creates, want the refusal of a run that never started after 1", res, sent)
	}
	jobs, _ := client.BatchV1().Jobs("kritika").List(t.Context(), metav1.ListOptions{})
	if len(jobs.Items) != 0 {
		t.Fatalf("a Job was left without its Secret: %v", jobs.Items)
	}
	p := awaitAction(t, deleted, "the Job's deletion").(k8stesting.DeleteAction).GetDeleteOptions().Preconditions
	if p == nil || p.UID == nil || *p.UID != "this-run" {
		t.Fatalf("delete preconditions = %+v, want the run's Job's UID", p)
	}
}

// TestKubeRunCannotSayASecretWasNotStored: a Secret whose creates both
// failed without the API refusing them, the first stored all the same, may
// have started the pod: the run, its Job deleted, is not one that never
// started.
func TestKubeRunCannotSayASecretWasNotStored(t *testing.T) {
	client := fake.NewSimpleClientset()
	sent := 0
	client.PrependReactor("create", "secrets", func(a k8stesting.Action) (bool, runtime.Object, error) {
		// The tracker has its own lock, which the Fake's reactors do not hold.
		if sent++; sent == 1 {
			if err := client.Tracker().Add(a.(k8stesting.CreateAction).GetObject()); err != nil {
				t.Error(err)
			}
		}
		return true, nil, errors.New("http2: client connection lost")
	})
	res := newKube(t, client).Run(t.Context(), spec())
	if res.NeverStarted || res.Err == nil || !strings.Contains(res.Err.Error(), "connection lost") || sent != 2 {
		t.Fatalf("res = %+v after %d creates, want a failure not called unstarted after 2", res, sent)
	}
	if jobs, _ := client.BatchV1().Jobs("kritika").List(t.Context(), metav1.ListOptions{}); len(jobs.Items) != 0 {
		t.Fatalf("the Job must be deleted, %d left", len(jobs.Items))
	}
}

// TestKubeRunKeepsASecretStoredThoughItsCreateFailed: a Secret the API
// stored though its create failed, the answer lost, is found by sending it
// again, and the run goes on in the pod that may be using it already.
func TestKubeRunKeepsASecretStoredThoughItsCreateFailed(t *testing.T) {
	client := fake.NewSimpleClientset()
	sent := 0
	client.PrependReactor("create", "secrets", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if sent++; sent > 1 {
			return false, nil, nil
		}
		// The tracker has its own lock, which the Fake's reactors do not hold.
		if err := client.Tracker().Add(a.(k8stesting.CreateAction).GetObject()); err != nil {
			t.Error(err)
		}
		return true, nil, errors.New("http2: client connection lost")
	})
	k := newKube(t, client)
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
		if res.Err != nil || res.NeverStarted || sent != 2 {
			t.Fatalf("res = %+v after %d creates, want the run's success after 2", res, sent)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not go on with its stored Secret")
	}
}

// TestKubeRunCreatesNoSecretWhenJobCreateFails: a Job the API refuses
// leaves no Secret to clean up, as none is created without its owner.
func TestKubeRunCreatesNoSecretWhenJobCreateFails(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("create", "jobs", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("quota exceeded")
	})
	res := newKube(t, client).Run(t.Context(), spec())
	if res.Err == nil || !strings.Contains(res.Err.Error(), "quota exceeded") || !res.NeverStarted {
		t.Fatalf("res = %+v, want the create error of a run that never started", res)
	}
	for _, a := range client.Actions() {
		if a.GetResource().Resource == "secrets" {
			t.Fatalf("%s secrets without the Job", a.GetVerb())
		}
	}
}

func TestKubeRunCancelDeletesJobInForeground(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("get", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.GetSubresource() == "log" {
			return true, &runtime.Unknown{Raw: []byte("cloning with ghs_secret_token and krk_run_token\n")}, nil
		}
		return false, nil, nil
	})
	k := newKube(t, client)
	cause := errors.New("superseded")
	ctx, cancel := context.WithCancelCause(t.Context())
	created := reacted(client, "create", "jobs")
	done := make(chan Result, 1)
	go func() { done <- k.Run(ctx, spec()) }()
	j := waitJob(t, client, created)
	_, _ = client.CoreV1().Pods("kritika").Create(t.Context(), &corev1.Pod{
		Name: j.Name + "-abcde", Namespace: "kritika", Labels: map[string]string{"job-name": j.Name},
	}, metav1.CreateOptions{})
	cancel(cause)

	var res Result
	select {
	case res = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
	if !errors.Is(res.Err, cause) {
		t.Fatalf("err = %v, want the cancellation cause", res.Err)
	}
	var deleted bool
	for _, a := range client.Actions() {
		if d, ok := a.(k8stesting.DeleteAction); ok && a.GetResource().Resource == "jobs" && d.GetName() == j.Name {
			p := d.GetDeleteOptions().PropagationPolicy
			deleted = p != nil && *p == metav1.DeletePropagationForeground
		}
	}
	if !deleted {
		t.Fatal("the Job must be deleted with foreground propagation")
	}
	if strings.Contains(res.LogTail, "ghs_secret_token") || strings.Contains(res.LogTail, "krk_run_token") || !strings.Contains(res.LogTail, "***") {
		t.Fatalf("log tail not masked: %q", res.LogTail)
	}
}

func TestKubeRunRejectsInvalidSpecBeforeCreatingAnything(t *testing.T) {
	client := fake.NewSimpleClientset()
	s := spec()
	s.Job.Head = "not-a-sha"
	res := newKube(t, client).Run(t.Context(), s)
	if res.Err == nil || !strings.Contains(res.Err.Error(), "head") {
		t.Fatalf("err = %v", res.Err)
	}
	if actions := calls(client); len(actions) != 0 {
		t.Fatalf("an invalid spec must create nothing, got %v", actions)
	}
}

func TestKubeRunDeletesJobWhenCreateFailsOnCancel(t *testing.T) {
	client := fake.NewSimpleClientset()
	ctx, cancel := context.WithCancel(t.Context())
	client.PrependReactor("create", "jobs", func(k8stesting.Action) (bool, runtime.Object, error) {
		// The server may have persisted the Job before the caller gave up.
		cancel()
		return true, nil, context.Canceled
	})
	res := newKube(t, client).Run(ctx, spec())
	if res.Err == nil {
		t.Fatal("a failed create must produce an error")
	}
	var jobDeleted bool
	for _, a := range client.Actions() {
		if d, ok := a.(k8stesting.DeleteAction); ok && d.GetName() == "kritika-run-01234567" && a.GetResource().Resource == "jobs" {
			jobDeleted = true
		}
		if a.GetResource().Resource == "secrets" {
			t.Fatalf("%s secrets for a Job that may not exist", a.GetVerb())
		}
	}
	if !jobDeleted {
		t.Fatal("the Job the server may have persisted must be deleted")
	}
}

// TestKubeRunCancelledInSetupSaysWhy: a run cancelled while it is set up
// fails the step under way, and its error carries the cancellation's
// cause beside the step's, so the run's Event says why it ended. A Secret
// whose create was cut off may have been stored, so the run is not one
// that surely never started.
func TestKubeRunCancelledInSetupSaysWhy(t *testing.T) {
	cause := errors.New("cancelled from the dashboard")
	ctx, cancel := context.WithCancelCause(t.Context())
	client := fake.NewSimpleClientset()
	client.PrependReactor("create", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		cancel(cause)
		return true, nil, context.Canceled
	})
	res := newKube(t, client).Run(ctx, spec())
	if res.NeverStarted || !errors.Is(res.Err, cause) || !strings.Contains(res.Err.Error(), "create secret") {
		t.Fatalf("res = %+v, want the Secret's error with the cancellation's cause", res)
	}
}

// TestKubeRunRefusedInSetupStaysRefused: a setup step the API refuses
// keeps its refusal as the run's error though the run is cancelled while
// the executor cleans up after it.
func TestKubeRunRefusedInSetupStaysRefused(t *testing.T) {
	cause := errors.New("cancelled from the dashboard")
	ctx, cancel := context.WithCancelCause(t.Context())
	client := fake.NewSimpleClientset()
	client.PrependReactor("create", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(corev1.Resource("secrets"), "", errors.New("denied"))
	})
	client.PrependReactor("delete", "jobs", func(k8stesting.Action) (bool, runtime.Object, error) {
		cancel(cause)
		return false, nil, nil
	})
	res := newKube(t, client).Run(ctx, spec())
	if !res.NeverStarted || errors.Is(res.Err, cause) || !strings.Contains(res.Err.Error(), "forbidden") {
		t.Fatalf("res = %+v, want the refusal alone", res)
	}
}

func TestFinishKeepsTheTailOfTheLog(t *testing.T) {
	const token = "ghs_secret_token"
	secrets := runner.Secrets{GitToken: token, GatewayToken: "krk_run_token"}
	// A secret straddles the point LogTailBytes from the end.
	straddling := "HEAD " + strings.Repeat("x", 1000) + token + strings.Repeat("y", LogTailBytes-8) + " END\n"
	tests := []struct {
		name  string
		logs  string
		check func(t *testing.T, tail string)
	}{
		{name: "the end is kept and a straddling secret masked", logs: straddling, check: func(t *testing.T, tail string) {
			if len(tail) > LogTailBytes || !strings.HasSuffix(tail, " END\n") || strings.Contains(tail, "HEAD") ||
				strings.Contains(tail, "ghs_") || strings.Contains(tail, "_token") {
				t.Fatalf("tail len %d starts %q ends %q", len(tail), tail[:20], tail[len(tail)-20:])
			}
		}},
		{name: "a short log is kept whole", logs: "fetched\ndone\n", check: func(t *testing.T, tail string) {
			if tail != "fetched\ndone\n" {
				t.Fatalf("tail = %q", tail)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			var opts *corev1.PodLogOptions
			client.PrependReactor("get", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
				if a.GetSubresource() != "log" {
					return false, nil, nil
				}
				opts, _ = a.(k8stesting.GenericAction).GetValue().(*corev1.PodLogOptions)
				return true, &runtime.Unknown{Raw: []byte(tt.logs)}, nil
			})
			_, _ = client.CoreV1().Pods("kritika").Create(t.Context(), &corev1.Pod{
				Name: "kritika-run-01234567-abcde", Namespace: "kritika", Labels: map[string]string{"job-name": "kritika-run-01234567"},
			}, metav1.CreateOptions{})
			res := Result{JobName: "kritika-run-01234567"}
			newKube(t, client).finish(t.Context(), &res, secrets)
			if opts == nil || opts.TailLines == nil || *opts.TailLines != logTailLines || opts.LimitBytes == nil || *opts.LimitBytes != logReadBytes {
				t.Fatalf("log options = %+v", opts)
			}
			tt.check(t, res.LogTail)
		})
	}
}

func TestLogTailDropsALineCutAtTheCeiling(t *testing.T) {
	const token = "ghs_secret_token"
	// The read stopped inside the secret on its last line.
	log := "first\nsecond\nclone with ghs_secr"
	got := logTail(log, true, runner.Secrets{GitToken: token})
	if got != "first\nsecond\n" {
		t.Fatalf("tail = %q", got)
	}
	if got := logTail(log, false, runner.Secrets{GitToken: token}); got != log {
		t.Fatalf("a read under the ceiling is kept whole, got %q", got)
	}
}

func TestTailKeepsValidUTF8(t *testing.T) {
	got := tail("ab€cd", 4)
	if got != "cd" || !utf8.ValidString(got) {
		t.Fatalf("tail = %q", got)
	}
}

// runnerPod is a Job's pod as the fake holds it, with status.
func runnerPod(job string, status corev1.PodStatus) *corev1.Pod {
	return &corev1.Pod{
		Name: job + "-abcde", Namespace: "kritika", Labels: map[string]string{"job-name": job, "kritika.home-operations.com/role": "runner"},
		Status: status,
	}
}

func waiting(reason, message string) corev1.PodStatus {
	return corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason, Message: message}},
	}}}
}

func TestPodStart(t *testing.T) {
	unschedulable := corev1.PodStatus{Conditions: []corev1.PodCondition{{
		Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable, Message: "0/3 nodes are available",
	}}}
	running := corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}}
	tests := []struct {
		name        string
		status      *corev1.PodStatus
		wantStarted bool
		wantStuck   string
	}{
		{name: "no pod yet", wantStuck: noPod},
		{name: "pulling its image", status: new(waiting("ContainerCreating", ""))},
		{name: "running", status: &running, wantStarted: true},
		{name: "an image that cannot be pulled", status: new(waiting("ImagePullBackOff", `Back-off pulling image "img"`)), wantStuck: `ImagePullBackOff: Back-off pulling image "img"`},
		{name: "a missing Secret", status: new(waiting("CreateContainerConfigError", `secret "s" not found`)), wantStuck: `CreateContainerConfigError: secret "s" not found`},
		{name: "a reason without a message", status: new(waiting("ErrImagePull", "")), wantStuck: "ErrImagePull"},
		{name: "unschedulable", status: &unschedulable, wantStuck: "Unschedulable: 0/3 nodes are available"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var pods []*corev1.Pod
			if tt.status != nil {
				pods = append(pods, runnerPod("job", *tt.status))
			}
			started, stuck := podStart(pods)
			if started != tt.wantStarted || stuck != tt.wantStuck {
				t.Fatalf("podStart = %v, %q; want %v, %q", started, stuck, tt.wantStarted, tt.wantStuck)
			}
		})
	}
}

// TestKubeRunGivesUpAPodThatNeverStarts: a pod stuck past the start grace
// ends the run with why, its Job deleted, instead of holding it until the
// Job's deadline.
func TestKubeRunGivesUpAPodThatNeverStarts(t *testing.T) {
	client := fake.NewSimpleClientset()
	k := newKube(t, client)
	k.StartGrace = 30 * time.Millisecond
	created := reacted(client, "create", "jobs")
	done := make(chan Result, 1)
	go func() { done <- k.Run(t.Context(), spec()) }()
	j := waitJob(t, client, created)
	if _, err := client.CoreV1().Pods("kritika").Create(t.Context(), runnerPod(j.Name, waiting("ImagePullBackOff", "no such tag")), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case res := <-done:
		if res.Err == nil || !strings.Contains(res.Err.Error(), "never started: ImagePullBackOff: no such tag") || res.TerminationReason == "" || !res.NeverStarted {
			t.Fatalf("res = %+v, want a run that never started", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not give the stuck pod up")
	}
	jobs, err := client.BatchV1().Jobs("kritika").List(t.Context(), metav1.ListOptions{})
	if err != nil || len(jobs.Items) != 0 {
		t.Fatalf("the stuck Job must be deleted, %d left (%v)", len(jobs.Items), err)
	}
}

// TestKubeRunAsksTheAPIBeforeGivingUpAPod: a pod the watch holds as stuck,
// which the API lists as running, as when the watch is behind, is not given
// up.
func TestKubeRunAsksTheAPIBeforeGivingUpAPod(t *testing.T) {
	job := jobName(spec().Job.RunID)
	client := fake.NewSimpleClientset(runnerPod(job, waiting("ImagePullBackOff", "no such tag")))
	k := newKube(t, client)
	k.StartGrace = 30 * time.Millisecond
	listed := make(chan struct{}, 1)
	client.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		select {
		case listed <- struct{}{}:
		default:
		}
		return true, &corev1.PodList{Items: []corev1.Pod{*runnerPod(job, runningStatus)}}, nil
	})
	created := reacted(client, "create", "jobs")
	done := make(chan Result, 1)
	go func() { done <- k.Run(t.Context(), spec()) }()
	j := waitJob(t, client, created)
	select {
	case <-listed:
	case <-time.After(5 * time.Second):
		t.Fatal("the executor never asked the API for the pod")
	}
	j.Status.Succeeded = 1
	if _, err := client.BatchV1().Jobs("kritika").UpdateStatus(t.Context(), j, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if res := <-done; res.Err != nil || res.NeverStarted {
		t.Fatalf("res = %+v, want the run that went on in its pod", res)
	}
}
