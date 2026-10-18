package executor

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
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
	// JobTemplate and PodTemplate are the configuration's runner.jobTemplate
	// and runner.podTemplate, set on every runner Job and its pod, the
	// runner container's image among them.
	JobTemplate JobTemplate
	PodTemplate PodTemplate
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
	// Poll is how often a run reads its Job and pod from the watch again,
	// besides on each change to them, for the time a pod's start is given.
	Poll time.Duration
	// StartGrace is how long a pod may stay unable to start before its run
	// is given up; startGrace unless set.
	StartGrace time.Duration
	// Events, when set, records the Events on runner Jobs.
	Events *Events
	Logger *slog.Logger
	// watch, which Start sets and Run needs, holds runner Jobs and pods.
	watch *watches
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

// Run implements Executor: create the run's Job and its Secret, wait for
// the Job, capture the pod's log tail, and report. A finished Job is left to
// its TTL, and its Secret goes with it. When ctx ends first the Job is
// deleted, pod included, and the result's error is ctx's cause.
func (k *Kube) Run(ctx context.Context, spec Spec) Result {
	if err := spec.Job.Validate(); err != nil {
		return Result{Err: fmt.Errorf("executor: %w", err)}
	}
	if a := spec.Job.Agent; a != nil {
		agent := *a
		agent.Env = k.PodTemplate.commandEnv()
		spec.Job.Agent = &agent
	}
	name := jobName(spec.Job.RunID)
	runSpec, err := runner.EncodeSpec(spec.Job)
	if err != nil {
		return Result{Err: fmt.Errorf("executor: %w", err)}
	}
	job := k.job(spec)
	// The API refusing the Job or the Secret leaves a run whose container
	// never ran, which the worker retries.
	created, err := k.Client.BatchV1().Jobs(k.Namespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		err = setupError(ctx, fmt.Errorf("executor: create job: %w", err))
		if ctx.Err() != nil {
			// The server may have persisted the Job before the call gave up.
			k.deleteJob(ctx, name, "")
		}
		return Result{NeverStarted: true, Err: err}
	}
	res := Result{JobName: created.Name, JobUID: string(created.UID)}
	// The Secret is the Job's from the start, so garbage collection deletes
	// it with the Job whatever becomes of this process; until it exists the
	// kubelet keeps retrying the Job's pod, which mounts it.
	if unknown, err := k.createSecret(ctx, k.secret(spec, runSpec, created)); err != nil {
		// A Secret that may have been stored may have started the pod: the
		// run surely never started only when the API refused it.
		res.NeverStarted, res.Err = !unknown, setupError(ctx, err)
		k.deleteJob(ctx, created.Name, created.UID)
		return res
	}
	wake, done := k.watch.wait(created.Name)
	defer done()
	// The tick reads the watch again, not the API: for the grace a stuck
	// pod or a podless Job is given, and a new Job's way to the watch.
	poll := cmp.Or(k.Poll, 3*time.Second)
	t := time.NewTicker(poll)
	defer t.Stop()
	missing := 0
	started := false
	// stuckSince is when the pod became unable to start, or the Job went
	// without one, and podless whether it was the latter: a pod appearing
	// starts its own grace.
	var stuckSince time.Time
	var podless bool
	for {
		tick := false
		select {
		case <-ctx.Done():
			k.cancel(ctx, &res, spec.Secrets)
			return res
		case <-wake:
		case <-t.C:
			tick = true
		}
		j, err := k.watch.jobs.Jobs(k.Namespace).Get(created.Name)
		if err == nil && j.UID != created.UID {
			err = fmt.Errorf("another Job of that name, uid %s", j.UID)
		}
		if err != nil {
			// A Job not in the watch is on its way there, or was deleted by
			// someone else; one of its name with another UID is an earlier
			// one the watch has yet to drop. One still missing after
			// maxMissing ticks is deleted rather than left to run beside the
			// retry the worker will start.
			if tick {
				missing++
			}
			if missing < maxMissing {
				continue
			}
			k.deleteJob(ctx, created.Name, created.UID)
			k.finish(ctx, &res, spec.Secrets)
			res.Abandoned, res.Err = true, fmt.Errorf("executor: job %s: %w", created.Name, err)
			return res
		}
		missing = 0
		if j.Status.Succeeded > 0 || j.Status.Failed > 0 || jobFinished(j) {
			k.finish(ctx, &res, spec.Secrets)
			k.ended(ctx, &res, j, started)
			return res
		}
		if started {
			continue
		}
		var stuck string
		if started, stuck = podStart(k.watchedPods(created)); stuck == "" {
			stuckSince = time.Time{}
			continue
		}
		if stuckSince.IsZero() || podless != (stuck == noPod) {
			stuckSince, podless = time.Now(), stuck == noPod
		}
		if time.Since(stuckSince) >= cmp.Or(k.StartGrace, startGrace) {
			var why string
			if stuck == noPod {
				why = k.whyNoPod(ctx, created.Name)
			}
			// The watch may hold a pod's status from before it recovered, or
			// no pod created meanwhile: the API is asked before the Job is
			// given up, and a pod list that fails, or one that no longer
			// shows what the watch did, gives nothing up.
			pods, err := k.listedPods(ctx, created)
			var fresh string
			if err == nil {
				started, fresh = podStart(pods)
			}
			if err != nil || fresh == "" || (fresh == noPod) != (stuck == noPod) {
				stuckSince = time.Time{}
				continue
			}
			stuck = cmp.Or(why, fresh)
			k.deleteJob(ctx, created.Name, created.UID)
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
	case j.Status.Failed == 0 && !started && res.PodName == "" && k.podless(ctx, j):
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

// podStart reports whether a Job's pod, the latest of pods, has started its
// container, and otherwise why it cannot, empty while it is only on its
// way, pulling its image; noPod while there is none.
func podStart(pods []*corev1.Pod) (started bool, stuck string) {
	if len(pods) == 0 {
		return false, noPod
	}
	pod := slices.MaxFunc(pods, func(a, b *corev1.Pod) int { return a.CreationTimestamp.Compare(b.CreationTimestamp.Time) })
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

// jobNameLabel names, on a Job's pods, the Job, as the Job controller
// labels them.
const jobNameLabel = "job-name"

// ownPods drops, from pods labelled with a Job's name, those another Job
// controls: one of its name, gone before it.
func ownPods(pods []*corev1.Pod, job *batchv1.Job) []*corev1.Pod {
	return slices.DeleteFunc(pods, func(p *corev1.Pod) bool {
		ref := metav1.GetControllerOfNoCopy(p)
		return ref != nil && ref.UID != job.UID
	})
}

// listedPods are the Job's pods as the API lists them now.
func (k *Kube) listedPods(ctx context.Context, job *batchv1.Job) ([]*corev1.Pod, error) {
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	list, err := k.Client.CoreV1().Pods(k.Namespace).List(lctx, metav1.ListOptions{LabelSelector: jobNameLabel + "=" + job.Name})
	if err != nil {
		return nil, err
	}
	pods := make([]*corev1.Pod, len(list.Items))
	for i := range list.Items {
		pods[i] = &list.Items[i]
	}
	return ownPods(pods, job), nil
}

// podless reports whether the Job has no pod, false when its pods cannot
// be read.
func (k *Kube) podless(ctx context.Context, job *batchv1.Job) bool {
	pods, err := k.listedPods(ctx, job)
	return err == nil && len(pods) == 0
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

// maxMissing is how many ticks in a row a Job may be missing from the watch
// before it is given up on: with the default poll, half a minute.
const maxMissing = 10

// createSecret creates the run's Secret. A create that fails without the
// API refusing it may have stored the Secret all the same, the answer lost:
// it is sent again, and the API answering that a Secret of its name exists
// says it was, the name being the run's alone. unknown is whether a failure
// leaves open that it was stored, and the Job's pod started with it.
func (k *Kube) createSecret(ctx context.Context, secret *corev1.Secret) (unknown bool, err error) {
	secrets := k.Client.CoreV1().Secrets(k.Namespace)
	_, err = secrets.Create(ctx, secret, metav1.CreateOptions{})
	if err != nil && !refused(err) {
		unknown = true
		if ctx.Err() == nil {
			_, err = secrets.Create(ctx, secret, metav1.CreateOptions{})
		}
	}
	if err == nil || apierrors.IsAlreadyExists(err) {
		return false, nil
	}
	return unknown, fmt.Errorf("executor: create secret: %w", err)
}

// refused reports whether err is the API's answer that it would not carry
// out a request, which it then did not, rather than a failure that leaves
// that unknown.
func refused(err error) bool {
	status, ok := errors.AsType[*apierrors.StatusError](err)
	return ok && status.ErrStatus.Code >= 400 && status.ErrStatus.Code < 500 && status.ErrStatus.Code != http.StatusRequestTimeout
}

// cancel deletes the Job after ctx ended, then records what the pod got to.
func (k *Kube) cancel(ctx context.Context, res *Result, secrets runner.Secrets) {
	res.Err = context.Cause(ctx)
	k.deleteJob(ctx, res.JobName, types.UID(res.JobUID))
	k.finish(ctx, res, secrets)
}

// deleteJob removes a Job and, with foreground propagation, its pod: the
// Job of uid alone, unless uid is empty, so another Job of its name is left
// alone. It runs without ctx's cancellation because ctx has usually ended.
func (k *Kube) deleteJob(ctx context.Context, name string, uid types.UID) {
	if err := k.removeJob(ctx, name, uid); err != nil {
		k.logger().Warn("delete runner job", "job", name, "error", err)
	}
}

// DeleteRun deletes the Job of run runID, pod and Secret with it, for a run
// whose worker is gone. A Job already gone counts as deleted.
func (k *Kube) DeleteRun(ctx context.Context, runID string) error {
	return k.removeJob(ctx, jobName(runID), "")
}

func (k *Kube) removeJob(ctx context.Context, name string, uid types.UID) error {
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	opts := metav1.DeleteOptions{PropagationPolicy: new(metav1.DeletePropagationForeground)}
	if uid != "" {
		opts.Preconditions = &metav1.Preconditions{UID: &uid}
	}
	if err := k.Client.BatchV1().Jobs(k.Namespace).Delete(dctx, name, opts); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("executor: delete runner job %s: %w", name, err)
	}
	return nil
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
	pods, err := k.listedPods(fctx, &batchv1.Job{Name: res.JobName, UID: types.UID(res.JobUID)})
	if err != nil || len(pods) == 0 {
		return
	}
	pod := pods[len(pods)-1]
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

// secretName names a run's Secret by the whole run ID, so a Secret of its
// name can only be the one the run created.
func secretName(runID string) string { return "kritika-run-" + runID }

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
// encoded job document, owned by job, which is not its controller. The
// Job's foreground deletion waits for the Secret's, so no new Job of its
// name can take the Secret up while it is there.
func (k *Kube) secret(spec Spec, runSpec []byte, job *batchv1.Job) *corev1.Secret {
	return &corev1.Secret{
		Name: secretName(spec.Job.RunID), Namespace: k.Namespace, Labels: runnerLabels(spec),
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: "batch/v1", Kind: jobKind, Name: job.Name, UID: job.UID,
			Controller: new(false), BlockOwnerDeletion: new(true),
		}},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			secretKeyGitToken: []byte(spec.Secrets.GitToken), secretKeyGatewayToken: []byte(spec.Secrets.GatewayToken),
			secretKeyDatabaseToken: []byte(spec.Secrets.DatabaseToken), secretKeyRunSpec: runSpec,
		},
	}
}

// defaultTTL is how long a finished Job stays for kubectl when the
// jobTemplate sets no ttlSecondsAfterFinished; the run row keeps everything
// the Job knew.
const defaultTTL = 10 * time.Minute

// job builds the Job for a spec. The runner gets its job document as a
// read-only file and its credentials as variables, both from the run's
// Secret, and the runner role's DSN from the database Secret; nothing else.
func (k *Kube) job(spec Spec) *batchv1.Job {
	name := jobName(spec.Job.RunID)
	deadline := int64(spec.Deadline / time.Second)
	ttl := int32(defaultTTL / time.Second)
	if t := k.JobTemplate.Spec.TTLSecondsAfterFinished; t != nil {
		ttl = *t
	}
	labels := runnerLabels(spec)
	jobLabels, podLabels := maps.Clone(labels), maps.Clone(labels)
	maps.Copy(jobLabels, k.JobTemplate.Metadata.Labels)
	maps.Copy(podLabels, k.PodTemplate.Metadata.Labels)
	annotations := map[string]string{}
	maps.Copy(annotations, k.JobTemplate.Metadata.Annotations)
	for key, v := range spec.Annotations {
		annotations["kritika.home-operations.com/"+key] = v
	}
	var backoff int32
	secret := secretName(spec.Job.RunID)
	secretRef := func(key string) *corev1.EnvVarSource {
		return &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			Name: secret, Key: key, Optional: new(false)}}
	}
	// The admin's variables are the commands', named for the runner to
	// hand on alone, and come first: the kubelet expands a $(NAME) in a
	// value to a variable defined before it alone, so none of theirs can
	// take in a credential of the run's.
	var env []corev1.EnvVar
	for _, e := range k.PodTemplate.runner().Env {
		e.Name = runner.CommandEnvPrefix + e.Name
		env = append(env, e)
	}
	env = append(env, []corev1.EnvVar{
		{Name: "KRITIKA_RUN_SPEC_FILE", Value: specDir + "/" + specFile},
		{Name: "KRITIKA_GIT_TOKEN", ValueFrom: secretRef(secretKeyGitToken)},
		{Name: "KRITIKA_GATEWAY_TOKEN", ValueFrom: secretRef(secretKeyGatewayToken)},
		{Name: "KRITIKA_DATABASE_TOKEN", ValueFrom: secretRef(secretKeyDatabaseToken)},
		{Name: "KRITIKA_DATABASE_RUNNER_ROLE", Value: cmp.Or(k.DatabaseRole, "kritika_runner")},
		{Name: "KRITIKA_LOG_FORMAT", Value: "json"},
		{Name: "KRITIKA_DATABASE_URL", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			Name: k.DatabaseSecret, Key: k.DatabaseSecretKey}}},
	}...)
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
		Image:           k.PodTemplate.runner().Image,
		ImagePullPolicy: k.PodTemplate.runner().ImagePullPolicy,
		Args:            []string{"run"},
		Env:             env,
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
			SecretName: secret, Items: []corev1.KeyToPath{{Key: secretKeyRunSpec, Path: specFile}},
		}},
	}
	pod := k.PodTemplate.Spec
	var runtimeClass *string
	if pod.RuntimeClassName != "" {
		runtimeClass = new(pod.RuntimeClassName)
	}
	var resources *corev1.ResourceRequirements
	if r := pod.Resources; r != nil && (len(r.Requests) > 0 || len(r.Limits) > 0) {
		resources = r
	}
	// A pod Kubernetes disrupts fails the Job, as with no policy, but for
	// that reason, PodFailurePolicy, which names it once the pod is gone:
	// the worker runs such a run again.
	policy := &batchv1.PodFailurePolicy{Rules: []batchv1.PodFailurePolicyRule{{
		Action:          batchv1.PodFailurePolicyActionFailJob,
		OnPodConditions: []batchv1.PodFailurePolicyOnPodConditionsPattern{{Type: corev1.DisruptionTarget, Status: corev1.ConditionTrue}},
	}}}
	return &batchv1.Job{
		Name: name, Namespace: k.Namespace, Labels: jobLabels, Annotations: annotations,
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			ActiveDeadlineSeconds:   &deadline,
			TTLSecondsAfterFinished: &ttl,
			PodFailurePolicy:        policy,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: podLabels, Annotations: k.PodTemplate.Metadata.Annotations},
				Spec: corev1.PodSpec{
					ServiceAccountName:           k.ServiceAccount,
					AutomountServiceAccountToken: new(false),
					RestartPolicy:                corev1.RestartPolicyNever,
					Resources:                    resources,
					RuntimeClassName:             runtimeClass,
					NodeSelector:                 pod.NodeSelector,
					Tolerations:                  pod.Tolerations,
					Affinity:                     pod.Affinity,
					TopologySpreadConstraints:    pod.TopologySpreadConstraints,
					PriorityClassName:            pod.PriorityClassName,
					HostUsers:                    pod.HostUsers,
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
	}
}
