package executor

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/home-operations/kritika/internal/runner"
)

// runnerRole names a runner pod's container, and its component and role
// labels, which the runner network policy selects.
const runnerRole = "runner"

// jobKind is a Job's kind, as its Secret's owner reference and its Events
// name it.
const jobKind = "Job"

// noProxy is what a runner pod with a gateway reaches directly, besides
// the gateway itself, whose model endpoint is not a proxy request.
const noProxy = "localhost,127.0.0.1"

// Kube runs each spec as a Kubernetes Job in the worker's own namespace.
type Kube struct {
	Client    kubernetes.Interface
	Namespace string
	// Image is the kritika image the Job runs, normally the release's -tools
	// image.
	Image string
	// ImagePullPolicy, when set, is the runner container's imagePullPolicy.
	ImagePullPolicy string
	// ServiceAccount is the permissionless runner service account.
	ServiceAccount string
	// DatabaseSecret and DatabaseSecretKey reference the Secret holding the
	// runner role's DSN, injected as KRITIKA_DATABASE_URL.
	DatabaseSecret, DatabaseSecretKey string
	// DatabaseRole is the database role the runner login must inherit.
	DatabaseRole string
	// GatewayURL is the egress gateway the pod is handed as its HTTPS_PROXY
	// and HTTP_PROXY, with its run token as the password the gateway admits
	// a proxy request by: with the runner network policy allowing nothing
	// else, every byte the runner sends out passes the gateway's host
	// allowlist.
	// Every review needs it, so serve refuses to start without one; the
	// check here only keeps a test's spec honest.
	GatewayURL string
	// RuntimeClass, when set, is the RuntimeClass the pod runs under.
	RuntimeClass string
	// TTL is ttlSecondsAfterFinished; the run row outlives the Job.
	TTL time.Duration
	// Poll is how often the Job is checked.
	Poll time.Duration
	// StartGrace is how long a pod may stay unable to start before its run
	// is given up; startGrace unless set.
	StartGrace time.Duration
	// Events, when set, records the Events on runner Jobs.
	Events *Events
	Logger *slog.Logger
}

// RunEvent implements EventRecorder.
func (k *Kube) RunEvent(ctx context.Context, res Result, warning bool, reason, note string) {
	k.Events.RunEvent(ctx, res, warning, reason, note)
}

// NewKubeInCluster returns a Kubernetes client from the pod's service
// account, and the pod's namespace, for a Kube executor.
func NewKubeInCluster() (kubernetes.Interface, string, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, "", fmt.Errorf("executor: in-cluster config: %w", err)
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, "", fmt.Errorf("executor: kubernetes client: %w", err)
	}
	ns, err := namespaceFromServiceAccount()
	if err != nil {
		return nil, "", err
	}
	return client, ns, nil
}

// Run implements Executor: create the run's Secret and Job, wait for the
// Job, capture the pod's log tail, and report. A finished Job is left to its
// TTL, and its Secret goes with it. When ctx ends first the Job is deleted,
// pod included, and the result's error is ctx's cause.
func (k *Kube) Run(ctx context.Context, spec Spec) Result {
	if err := spec.Job.Validate(); err != nil {
		return Result{Err: fmt.Errorf("executor: %w", err)}
	}
	name := jobName(spec.Job.RunID)
	runSpec, err := runner.EncodeSpec(spec.Job)
	if err != nil {
		return Result{Err: fmt.Errorf("executor: %w", err)}
	}
	job, err := k.job(spec)
	if err != nil {
		return Result{Err: err}
	}
	// The API refusing the Secret, the Job or the Secret's owner leaves a
	// run whose container never ran, which the worker retries.
	secrets := k.Client.CoreV1().Secrets(k.Namespace)
	if _, err := secrets.Create(ctx, k.secret(spec, runSpec), metav1.CreateOptions{}); err != nil {
		return Result{NeverStarted: true, Err: setupError(ctx, fmt.Errorf("executor: create secret: %w", err))}
	}
	created, err := k.Client.BatchV1().Jobs(k.Namespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		err = setupError(ctx, fmt.Errorf("executor: create job: %w", err))
		if ctx.Err() != nil {
			// The server may have persisted the Job before the call gave up.
			k.deleteJob(ctx, name)
		}
		k.deleteSecret(ctx, name)
		return Result{NeverStarted: true, Err: err}
	}
	res := Result{JobName: created.Name, JobUID: string(created.UID)}
	if err := k.own(ctx, created); err != nil {
		res.NeverStarted, res.Err = true, setupError(ctx, err)
		k.deleteJob(ctx, created.Name)
		k.deleteSecret(ctx, name)
		return res
	}
	poll := cmp.Or(k.Poll, 3*time.Second)
	t := time.NewTicker(poll)
	defer t.Stop()
	unread := 0
	started := false
	// stuckSince is when the pod became unable to start, or the Job went
	// without one, and podless whether it was the latter: a pod appearing
	// starts its own grace.
	var stuckSince time.Time
	var podless bool
	for {
		select {
		case <-ctx.Done():
			k.cancel(ctx, &res, spec.Secrets)
			return res
		case <-t.C:
		}
		j, err := k.Client.BatchV1().Jobs(k.Namespace).Get(ctx, created.Name, metav1.GetOptions{})
		if err != nil {
			if ctx.Err() != nil {
				k.cancel(ctx, &res, spec.Secrets)
				return res
			}
			// The API server answering badly says nothing about the run: the
			// Job is checked again next tick. One that stays unreadable is
			// deleted rather than left to run beside the retry the worker
			// will start.
			if unread++; unread < maxUnreadStatus {
				k.logger().Warn("runner job status not read, retrying", "job", created.Name, "error", err)
				continue
			}
			k.deleteJob(ctx, created.Name)
			k.finish(ctx, &res, spec.Secrets)
			res.Abandoned, res.Err = true, fmt.Errorf("executor: get job: %w", err)
			return res
		}
		unread = 0
		if j.Status.Succeeded > 0 || j.Status.Failed > 0 || jobFinished(j) {
			k.finish(ctx, &res, spec.Secrets)
			k.ended(ctx, &res, j, started)
			return res
		}
		if started {
			continue
		}
		var stuck string
		if started, stuck = k.podStart(ctx, created.Name); stuck == "" {
			stuckSince = time.Time{}
			continue
		}
		if stuckSince.IsZero() || podless != (stuck == noPod) {
			stuckSince, podless = time.Now(), stuck == noPod
		}
		if time.Since(stuckSince) >= cmp.Or(k.StartGrace, startGrace) {
			if stuck == noPod {
				why := k.whyNoPod(ctx, created.Name)
				// A pod created while that was read is watched as any other.
				if started, stuck = k.podStart(ctx, created.Name); stuck != noPod {
					stuckSince = time.Time{}
					continue
				}
				stuck = why
			}
			k.deleteJob(ctx, created.Name)
			k.finish(ctx, &res, spec.Secrets)
			res.TerminationReason, res.NeverStarted = stuck, true
			res.Err = fmt.Errorf("executor: job %s never started: %s", created.Name, stuck)
			return res
		}
	}
}

// ended says how a Job that finished went, in res, which finish has filled
// from its pod; started is whether one of its containers was seen running.
func (k *Kube) ended(ctx context.Context, res *Result, j *batchv1.Job, started bool) {
	switch {
	case j.Status.Succeeded > 0:
	// A Job that failed with no failed pod, none of whose containers was
	// seen running, no pod for finish and none now, never had one created,
	// as when the namespace refuses it on admission and its deadline passes
	// first.
	case j.Status.Failed == 0 && !started && res.PodName == "" && k.podless(ctx, j.Name):
		res.NeverStarted, res.TerminationReason = true, k.whyNoPod(ctx, j.Name)+"; then "+jobFailure(j)
		res.Err = fmt.Errorf("executor: job %s never started: %s", j.Name, res.TerminationReason)
	// The Job's pod failure policy fails it for a disrupted pod alone, which
	// may be gone by the time finish looks for it.
	case res.Disruption != "" || failedBy(j, batchv1.JobReasonPodFailurePolicy):
		res.Disruption = cmp.Or(res.Disruption, jobFailure(j))
		res.Err = fmt.Errorf("executor: job %s's pod was disrupted: %s", j.Name, res.Disruption)
	default:
		res.Err = fmt.Errorf("executor: job %s failed: %s", j.Name, res.TerminationReason)
	}
}

// setupError is the error of a step setting the run up, with the run's
// cancellation cause beside it when the step failed because ctx ended: the
// cause says why. It is taken as the step fails, before any cleanup, so a
// cancellation landing during the cleanup is not taken for the failure's.
func setupError(ctx context.Context, err error) error {
	if ctx.Err() == nil || !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("%w (%w)", err, context.Cause(ctx))
}

// startGrace is how long a runner pod may stay unable to start, uncreated,
// unscheduled or its container refused, before the run is given up rather
// than holding its review's slot until the Job's deadline. It outlasts what
// clears on its own: a registry's hiccup, a node on its way from an
// autoscaler.
const startGrace = 3 * time.Minute

// noPod is why a Job's pod cannot start while none exists: on its way for
// the moment the Job controller takes to create it, stuck once startGrace
// passes, as when the namespace refuses it on admission, by a ResourceQuota,
// Pod Security or a webhook, and the controller keeps trying.
const noPod = "no pod was created for the Job"

// stuckReasons are the container waiting reasons of a pod that will not
// start as it is: an image that cannot be pulled, or a container the
// kubelet cannot build, as from a Secret or key that does not exist.
var stuckReasons = []string{"ErrImagePull", "ImagePullBackOff", "InvalidImageName", "CreateContainerConfigError", "CreateContainerError"}

// podStart reports whether the Job's pod has started its container, and
// otherwise why it cannot, empty while it is only on its way, pulling its
// image; noPod while there is none. A pod that cannot be read is on its
// way.
func (k *Kube) podStart(ctx context.Context, job string) (started bool, stuck string) {
	pods, err := k.Client.CoreV1().Pods(k.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "job-name=" + job})
	if err != nil {
		return false, ""
	}
	if len(pods.Items) == 0 {
		return false, noPod
	}
	pod := pods.Items[len(pods.Items)-1]
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Running != nil || cs.State.Terminated != nil {
			return true, ""
		}
		if w := cs.State.Waiting; w != nil && slices.Contains(stuckReasons, w.Reason) {
			return false, strings.TrimSuffix(w.Reason+": "+w.Message, ": ")
		}
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Reason == corev1.PodReasonUnschedulable {
			return false, strings.TrimSuffix(c.Reason+": "+c.Message, ": ")
		}
	}
	return false, ""
}

// podless reports whether the Job has no pod, false when its pods cannot
// be read.
func (k *Kube) podless(ctx context.Context, job string) bool {
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	pods, err := k.Client.CoreV1().Pods(k.Namespace).List(lctx, metav1.ListOptions{LabelSelector: "job-name=" + job})
	return err == nil && len(pods.Items) == 0
}

// whyNoPod is noPod with the latest reason the Job controller recorded for
// not creating the Job's pod, its FailedCreate Event, when one can be read.
func (k *Kube) whyNoPod(ctx context.Context, job string) string {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	list, err := k.Client.EventsV1().Events(k.Namespace).List(rctx, metav1.ListOptions{
		FieldSelector: fields.Set{"regarding.kind": jobKind, "regarding.name": job, "reason": "FailedCreate"}.String(),
	})
	if err != nil {
		return noPod
	}
	var note string
	var last time.Time
	for _, e := range list.Items {
		if e.Reason != "FailedCreate" || e.Regarding.Name != job {
			continue
		}
		if seen := eventSeen(e); note == "" || seen.After(last) {
			note, last = e.Note, seen
		}
	}
	if note == "" {
		return noPod
	}
	return noPod + ": " + note
}

// eventSeen is when an Event was last seen, as the Events API or, for one
// a controller recorded through the core API, its deprecated fields say.
func eventSeen(e eventsv1.Event) time.Time {
	switch {
	case e.Series != nil:
		return e.Series.LastObservedTime.Time
	case !e.EventTime.IsZero():
		return e.EventTime.Time
	}
	return e.DeprecatedLastTimestamp.Time
}

// failure is the Job's failed condition, or its FailureTarget, which the
// Job controller sets first and may count the failed pod under before it
// adds the other; ok is false when the Job has neither.
func failure(j *batchv1.Job) (c batchv1.JobCondition, ok bool) {
	for _, t := range []batchv1.JobConditionType{batchv1.JobFailed, batchv1.JobFailureTarget} {
		if i := slices.IndexFunc(j.Status.Conditions, func(c batchv1.JobCondition) bool {
			return c.Type == t && c.Status == corev1.ConditionTrue
		}); i >= 0 {
			return j.Status.Conditions[i], true
		}
	}
	return batchv1.JobCondition{}, false
}

// failedBy reports whether the Job failed, or is failing, for reason.
func failedBy(j *batchv1.Job, reason string) bool {
	c, ok := failure(j)
	return ok && c.Reason == reason
}

// jobFailure is the Job's failure, its reason and message.
func jobFailure(j *batchv1.Job) string {
	if c, ok := failure(j); ok {
		return strings.TrimSuffix("the Job failed: "+c.Reason+": "+c.Message, ": ")
	}
	return "the Job failed"
}

// maxUnreadStatus is how many status reads in a row may fail before the
// Job is given up on: with the default poll, half a minute of the API
// server not answering.
const maxUnreadStatus = 10

// own makes the Job the Secret's owner so garbage collection deletes the
// Secret with the Job. It is not the controller and must not block the
// Job's deletion.
func (k *Kube) own(ctx context.Context, job *batchv1.Job) error {
	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"ownerReferences": []metav1.OwnerReference{{
		APIVersion: "batch/v1", Kind: jobKind, Name: job.Name, UID: job.UID,
		Controller: new(false), BlockOwnerDeletion: new(false),
	}}}})
	if err != nil {
		return fmt.Errorf("executor: encode owner reference: %w", err)
	}
	if _, err := k.Client.CoreV1().Secrets(k.Namespace).Patch(ctx, job.Name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("executor: own secret: %w", err)
	}
	return nil
}

// cancel deletes the Job after ctx ended, then records what the pod got to.
func (k *Kube) cancel(ctx context.Context, res *Result, secrets runner.Secrets) {
	res.Err = context.Cause(ctx)
	k.deleteJob(ctx, res.JobName)
	k.finish(ctx, res, secrets)
}

// deleteJob removes a Job and, with foreground propagation, its pod. It
// runs without ctx's cancellation because ctx has usually ended.
func (k *Kube) deleteJob(ctx context.Context, name string) {
	if err := k.removeJob(ctx, name); err != nil {
		k.logger().Warn("delete runner job", "job", name, "error", err)
	}
}

// DeleteRun deletes the Job of run runID, pod and Secret with it, for a run
// whose worker is gone. A Job already gone counts as deleted.
func (k *Kube) DeleteRun(ctx context.Context, runID string) error {
	return k.removeJob(ctx, jobName(runID))
}

func (k *Kube) removeJob(ctx context.Context, name string) error {
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	opts := metav1.DeleteOptions{PropagationPolicy: new(metav1.DeletePropagationForeground)}
	if err := k.Client.BatchV1().Jobs(k.Namespace).Delete(dctx, name, opts); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("executor: delete runner job %s: %w", name, err)
	}
	return nil
}

// deleteSecret removes a Secret no Job owns yet.
func (k *Kube) deleteSecret(ctx context.Context, name string) {
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	if err := k.Client.CoreV1().Secrets(k.Namespace).Delete(dctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		k.logger().Warn("delete runner secret", "secret", name, "error", err)
	}
}

func (k *Kube) logger() *slog.Logger {
	return cmp.Or(k.Logger, slog.Default())
}

func jobFinished(j *batchv1.Job) bool {
	return slices.ContainsFunc(j.Status.Conditions, func(c batchv1.JobCondition) bool {
		return (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) && c.Status == corev1.ConditionTrue
	})
}

// finish fills the pod-level fields of a result from the Job's pod, with
// the run's secrets masked out of the log tail. Best effort: a missing pod
// leaves the fields empty rather than failing the run.
func (k *Kube) finish(ctx context.Context, res *Result, secrets runner.Secrets) {
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	pods, err := k.Client.CoreV1().Pods(k.Namespace).List(fctx, metav1.ListOptions{LabelSelector: "job-name=" + res.JobName})
	if err != nil || len(pods.Items) == 0 {
		return
	}
	pod := pods.Items[len(pods.Items)-1]
	res.PodName, res.NodeName = pod.Name, pod.Spec.NodeName
	for _, c := range pod.Status.Conditions {
		switch {
		case c.Type == corev1.PodScheduled && c.Status == corev1.ConditionTrue:
			res.ScheduledAt = c.LastTransitionTime.Time
		case c.Type == corev1.DisruptionTarget && c.Status == corev1.ConditionTrue:
			res.Disruption = strings.TrimSuffix(c.Reason+": "+c.Message, ": ")
		}
	}
	if pod.Status.StartTime != nil {
		res.StartedAt = pod.Status.StartTime.Time
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Terminated != nil {
			res.ExitCode = int(cs.State.Terminated.ExitCode)
			res.TerminationReason = cs.State.Terminated.Reason
			res.DeadlineExceeded = cs.State.Terminated.Reason == "DeadlineExceeded"
		}
	}
	// LimitBytes keeps the first bytes of what it is given, so the tail is
	// asked for by lines, generously, with LimitBytes only as a ceiling;
	// the kept tail is cut from the end of what came back.
	lines, limit := int64(logTailLines), int64(logReadBytes)
	stream, err := k.Client.CoreV1().Pods(k.Namespace).GetLogs(pod.Name,
		&corev1.PodLogOptions{TailLines: &lines, LimitBytes: &limit}).Stream(fctx)
	if err != nil {
		return
	}
	defer func() { _ = stream.Close() }()
	b, _ := io.ReadAll(io.LimitReader(stream, limit))
	res.LogTail = logTail(string(b), int64(len(b)) >= limit, secrets)
}

// The log read for a run's tail: enough lines to fill LogTailBytes many
// times over, and a byte ceiling well above that.
const (
	logTailLines = 5000
	logReadBytes = 4 << 20
)

// logTail is the last LogTailBytes of a pod's log, secrets masked before
// the cut so none straddles it. A read that hit the byte ceiling ends
// mid-line, where a secret could be cut short of its mask, so that partial
// line is dropped.
func logTail(log string, hitCeiling bool, secrets runner.Secrets) string {
	if hitCeiling {
		if i := strings.LastIndexByte(log, '\n'); i >= 0 {
			log = log[:i+1]
		}
	}
	return tail(secrets.Mask(log), LogTailBytes)
}

// Secret keys of a run's job-scoped Secret.
const (
	secretKeyGitToken      = "git-token"
	secretKeyGatewayToken  = "gateway-token"
	secretKeyDatabaseToken = "database-token"
	secretKeyRunSpec       = "run-spec.json"
)

// The run's job document is mounted read-only from its Secret at
// specDir/specFile.
const (
	specDir  = "/var/run/kritika"
	specFile = "spec.json"
)

func jobName(runID string) string { return "kritika-run-" + runID[:8] }

// gatewayHost is the host of a gateway URL config has already checked.
func gatewayHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// proxyEnv is the gateway URL as the pod's proxy variables carry it, with
// the run token as its password. The kubelet expands the reference from
// the variable the Secret fills before it in the container's environment,
// so the token travels in the Secret alone.
func proxyEnv(gatewayURL string) string {
	return strings.Replace(gatewayURL, "://", "://:$(KRITIKA_GATEWAY_TOKEN)@", 1)
}

// runnerLabels are shared by a run's Job, pod and Secret. The role label is what
// a NetworkPolicy selects runner pods by. A label value may not hold a
// slash, which an owner/repo name does.
func runnerLabels(spec Spec) map[string]string {
	l := map[string]string{
		"app.kubernetes.io/name": "kritika", "app.kubernetes.io/component": runnerRole, "kritika.home-operations.com/role": runnerRole,
	}
	for key, v := range spec.Labels {
		l["kritika.home-operations.com/"+key] = strings.ReplaceAll(v, "/", "_")
	}
	return l
}

// secret builds the run's job-scoped Secret: the credentials and the
// encoded job document.
func (k *Kube) secret(spec Spec, runSpec []byte) *corev1.Secret {
	return &corev1.Secret{
		Name: jobName(spec.Job.RunID), Namespace: k.Namespace, Labels: runnerLabels(spec),
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			secretKeyGitToken: []byte(spec.Secrets.GitToken), secretKeyGatewayToken: []byte(spec.Secrets.GatewayToken),
			secretKeyDatabaseToken: []byte(spec.Secrets.DatabaseToken), secretKeyRunSpec: runSpec,
		},
	}
}

// job builds the Job for a spec. The runner gets its job document as a
// read-only file and its credentials as variables, both from the run's
// Secret, and the runner role's DSN from the database Secret; nothing else.
func (k *Kube) job(spec Spec) (*batchv1.Job, error) {
	resources, err := containerResources(spec.Resources)
	if err != nil {
		return nil, err
	}
	name := jobName(spec.Job.RunID)
	deadline := int64(spec.Deadline / time.Second)
	ttl := int32(k.TTL / time.Second)
	labels := runnerLabels(spec)
	annotations := map[string]string{}
	for key, v := range spec.Annotations {
		annotations["kritika.home-operations.com/"+key] = v
	}
	var backoff int32
	secretRef := func(key string) *corev1.EnvVarSource {
		return &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			Name: name, Key: key, Optional: new(false)}}
	}
	env := []corev1.EnvVar{
		{Name: "KRITIKA_RUN_SPEC_FILE", Value: specDir + "/" + specFile},
		{Name: "KRITIKA_GIT_TOKEN", ValueFrom: secretRef(secretKeyGitToken)},
		{Name: "KRITIKA_GATEWAY_TOKEN", ValueFrom: secretRef(secretKeyGatewayToken)},
		{Name: "KRITIKA_DATABASE_TOKEN", ValueFrom: secretRef(secretKeyDatabaseToken)},
		{Name: "KRITIKA_DATABASE_RUNNER_ROLE", Value: cmp.Or(k.DatabaseRole, "kritika_runner")},
		{Name: "KRITIKA_LOG_FORMAT", Value: "json"},
		{Name: "KRITIKA_DATABASE_URL", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			Name: k.DatabaseSecret, Key: k.DatabaseSecretKey}}},
	}
	if k.GatewayURL != "" {
		proxy := proxyEnv(k.GatewayURL)
		env = append(env,
			corev1.EnvVar{Name: "HTTPS_PROXY", Value: proxy},
			corev1.EnvVar{Name: "HTTP_PROXY", Value: proxy},
			// Postgres is not HTTP, and the one Service the runner talks
			// to is the gateway's model endpoint.
			corev1.EnvVar{Name: "NO_PROXY", Value: noProxy + "," + gatewayHost(k.GatewayURL)},
		)
	}
	container := corev1.Container{
		Name:            runnerRole,
		Image:           k.Image,
		ImagePullPolicy: corev1.PullPolicy(k.ImagePullPolicy),
		Args:            []string{"run"},
		Env:             env,
		Resources:       resources,
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: new(false),
			ReadOnlyRootFilesystem:   new(true),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "scratch", MountPath: "/tmp"},
			{Name: "spec", MountPath: specDir, ReadOnly: true},
		},
	}
	volumes := []corev1.Volume{
		{Name: "scratch", EmptyDir: &corev1.EmptyDirVolumeSource{}},
		{Name: "spec", Secret: &corev1.SecretVolumeSource{
			SecretName: name, Items: []corev1.KeyToPath{{Key: secretKeyRunSpec, Path: specFile}},
		}},
	}
	var runtimeClass *string
	if k.RuntimeClass != "" {
		runtimeClass = new(k.RuntimeClass)
	}
	// A pod Kubernetes disrupts fails the Job, as with no policy, but for
	// that reason, PodFailurePolicy, which names it once the pod is gone:
	// the worker runs such a run again.
	policy := &batchv1.PodFailurePolicy{Rules: []batchv1.PodFailurePolicyRule{{
		Action:          batchv1.PodFailurePolicyActionFailJob,
		OnPodConditions: []batchv1.PodFailurePolicyOnPodConditionsPattern{{Type: corev1.DisruptionTarget, Status: corev1.ConditionTrue}},
	}}}
	return &batchv1.Job{
		Name: name, Namespace: k.Namespace, Labels: labels, Annotations: annotations,
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			ActiveDeadlineSeconds:   &deadline,
			TTLSecondsAfterFinished: &ttl,
			PodFailurePolicy:        policy,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					ServiceAccountName:           k.ServiceAccount,
					AutomountServiceAccountToken: new(false),
					RestartPolicy:                corev1.RestartPolicyNever,
					RuntimeClassName:             runtimeClass,
					// The chart's gateway name, <service>.<namespace>.svc.cluster.local,
					// has four dots: with ndots at 4 it is looked up as written
					// rather than through the search list first, whose expansions
					// of it the chart's Cilium DNS rule refuses and musl's
					// resolver, mise's, gives up at.
					DNSConfig: &corev1.PodDNSConfig{Options: []corev1.PodDNSConfigOption{{Name: "ndots", Value: new("4")}}},
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: new(true), RunAsUser: new(int64(65532)), RunAsGroup: new(int64(65532)),
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{container},
					Volumes:    volumes,
				},
			},
		},
	}, nil
}

// CheckResources reports whether the configuration's runner resources
// decode as a container's, so a value that would refuse every Job is
// refused at startup instead.
func CheckResources(m map[string]any) error {
	_, err := containerResources(m)
	return err
}

// containerResources decodes an account's runner.resources, which the
// configuration keeps as the YAML written, refusing a field the Kubernetes
// type does not have rather than dropping it.
func containerResources(m map[string]any) (corev1.ResourceRequirements, error) {
	var r corev1.ResourceRequirements
	if m == nil {
		return r, nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return r, fmt.Errorf("executor: runner.resources: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return r, fmt.Errorf("executor: runner.resources: %w", err)
	}
	return r, nil
}
