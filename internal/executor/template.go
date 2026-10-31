package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/home-operations/kritika/internal/runner"
	"github.com/home-operations/kritika/internal/runspec"
)

// JobTemplate is what the configuration's runner.jobTemplate sets on every
// runner Job, as a JobTemplateSpec spells it: labels and annotations, how
// long the Job can run, and how long it is kept once it ends. Its retries,
// its pod failure policy and its pod stay the executor's.
type JobTemplate struct {
	Metadata TemplateMetadata `json:"metadata"`
	Spec     JobTemplateSpec  `json:"spec"`
}

// JobTemplateSpec is the part of a JobSpec a runner Job takes from the
// configuration. ActiveDeadlineSeconds reaches the Job as Spec.Deadline,
// which the configuration reads from it for the work around the run too.
type JobTemplateSpec struct {
	ActiveDeadlineSeconds   *int64 `json:"activeDeadlineSeconds,omitempty"`
	TTLSecondsAfterFinished *int32 `json:"ttlSecondsAfterFinished,omitempty"`
}

// TemplateMetadata is the labels and annotations a template sets.
type TemplateMetadata struct {
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// PodTemplate is what the configuration's runner.podTemplate sets on every
// runner pod, as a PodTemplateSpec spells it. It sets labels and annotations,
// resources for the whole pod, and where the pod is scheduled and at what
// priority. It also sets a runtime class and a user namespace of its own, and
// the runner container's image. What keeps a runner isolated stays the
// executor's: its ServiceAccount, the token it does not mount, its security
// contexts, its volumes, and its command.
type PodTemplate struct {
	Metadata TemplateMetadata `json:"metadata"`
	Spec     PodTemplateSpec  `json:"spec"`
}

// PodTemplateSpec is the part of a PodSpec a runner pod takes from the
// configuration, its Containers the runner container alone.
type PodTemplateSpec struct {
	Resources                 *corev1.ResourceRequirements      `json:"resources,omitempty"`
	RuntimeClassName          string                            `json:"runtimeClassName,omitempty"`
	PriorityClassName         string                            `json:"priorityClassName,omitempty"`
	HostUsers                 *bool                             `json:"hostUsers,omitempty"`
	NodeSelector              map[string]string                 `json:"nodeSelector,omitempty"`
	Tolerations               []corev1.Toleration               `json:"tolerations,omitempty"`
	Affinity                  *corev1.Affinity                  `json:"affinity,omitempty"`
	TopologySpreadConstraints []corev1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`
	Containers                []TemplateContainer               `json:"containers,omitempty"`
}

// TemplateContainer is the runner container as the pod template sets it.
// Env is the admin's variables for the run tool's commands, each a value
// or a Secret's key.
type TemplateContainer struct {
	Name            string            `json:"name"`
	Image           string            `json:"image,omitempty"`
	ImagePullPolicy corev1.PullPolicy `json:"imagePullPolicy,omitempty"`
	Env             []corev1.EnvVar   `json:"env,omitempty"`
}

// commandEnv names the runner container's variables for the run tool's
// commands and marks those that a Secret's key sets.
func (t PodTemplate) commandEnv() []runspec.CommandEnvVar {
	env := make([]runspec.CommandEnvVar, 0, len(t.runner().Env))
	for _, e := range t.runner().Env {
		env = append(env, runspec.CommandEnvVar{Name: e.Name, Secret: e.ValueFrom != nil})
	}
	return env
}

// runner is the pod template's runner container, which DecodePodTemplate
// requires.
func (t PodTemplate) runner() TemplateContainer {
	if len(t.Spec.Containers) == 0 {
		return TemplateContainer{}
	}
	return t.Spec.Containers[0]
}

// appArmorAnnotation prefixes the annotations that set a container's
// AppArmor profile, which Kubernetes still copies into its security context
// until the field replaces them.
const appArmorAnnotation = "container.apparmor.security.beta.kubernetes.io/"

// DecodeJobTemplate decodes the configuration's runner.jobTemplate, which
// it keeps as the YAML written. It refuses a field that JobTemplate does not
// have and does not drop it. It also refuses a label or annotation that
// kritika sets on its Jobs.
func DecodeJobTemplate(m map[string]any) (JobTemplate, error) {
	var t JobTemplate
	if err := decodeTemplate(m, &t); err != nil {
		return JobTemplate{}, fmt.Errorf("executor: runner.jobTemplate: %w", err)
	}
	for _, key := range slices.Sorted(maps.Keys(t.Metadata.Labels)) {
		if takenLabel(key) {
			return JobTemplate{}, fmt.Errorf("executor: runner.jobTemplate: label %s is kritika's or the Job controller's", key)
		}
	}
	for _, key := range slices.Sorted(maps.Keys(t.Metadata.Annotations)) {
		if strings.HasPrefix(key, "kritika.home-operations.com/") {
			return JobTemplate{}, fmt.Errorf("executor: runner.jobTemplate: annotation %s is kritika's", key)
		}
	}
	// A Job removed the moment it finishes takes its pod's log with it
	// before the executor reads the log's tail.
	if ttl := t.Spec.TTLSecondsAfterFinished; ttl != nil && *ttl < 1 {
		return JobTemplate{}, fmt.Errorf("executor: runner.jobTemplate: spec.ttlSecondsAfterFinished %d is not a positive number of seconds",
			*ttl)
	}
	return t, nil
}

// DecodePodTemplate decodes the configuration's runner.podTemplate likewise.
// It refuses a label or annotation that a runner's isolation rests on. It
// requires the one container, named runner, with its image. It also requires
// that no variable of that container has a name that the runner keeps for
// the commands.
func DecodePodTemplate(m map[string]any) (PodTemplate, error) {
	var t PodTemplate
	if err := decodeTemplate(m, &t); err != nil {
		return PodTemplate{}, fmt.Errorf("executor: runner.podTemplate: %w", err)
	}
	for _, key := range slices.Sorted(maps.Keys(t.Metadata.Labels)) {
		if takenLabel(key) {
			return PodTemplate{}, fmt.Errorf("executor: runner.podTemplate: label %s is kritika's or the Job controller's", key)
		}
	}
	for _, key := range slices.Sorted(maps.Keys(t.Metadata.Annotations)) {
		if strings.HasPrefix(key, appArmorAnnotation) {
			return PodTemplate{}, fmt.Errorf("executor: runner.podTemplate: annotation %s would set the runner's AppArmor profile", key)
		}
	}
	if len(t.Spec.Containers) != 1 || t.Spec.Containers[0].Name != runnerRole {
		return PodTemplate{}, errors.New("executor: runner.podTemplate: spec.containers must hold one container, named runner")
	}
	switch c := t.runner(); {
	case c.Image == "":
		return PodTemplate{}, errors.New("executor: runner.podTemplate: the runner container needs an image")
	case !slices.Contains([]corev1.PullPolicy{"", corev1.PullAlways, corev1.PullIfNotPresent, corev1.PullNever}, c.ImagePullPolicy):
		return PodTemplate{}, fmt.Errorf(
			"executor: runner.podTemplate: the runner container's imagePullPolicy %q is not Always, IfNotPresent or Never", c.ImagePullPolicy)
	}
	seen := map[string]bool{}
	for _, e := range t.runner().Env {
		switch {
		case runner.ReservedEnvName(e.Name):
			return PodTemplate{}, fmt.Errorf("executor: runner.podTemplate: env %q is a name the runner keeps for the commands", e.Name)
		case seen[e.Name]:
			return PodTemplate{}, fmt.Errorf("executor: runner.podTemplate: env %q is set twice", e.Name)
		case e.ValueFrom != nil && e.ValueFrom.SecretKeyRef == nil,
			e.ValueFrom != nil && *e.ValueFrom != (corev1.EnvVarSource{SecretKeyRef: e.ValueFrom.SecretKeyRef}):
			return PodTemplate{}, fmt.Errorf("executor: runner.podTemplate: env %q takes a value or a Secret's key alone", e.Name)
		}
		seen[e.Name] = true
	}
	return t, nil
}

// decodeTemplate decodes m into v and refuses a field that v does not have.
func decodeTemplate(m map[string]any, v any) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// CheckJob has the API make sure that a runner Job is valid. The Job is as
// the executor builds it from the configuration's templates, and it is
// created as a dry run. This covers what decoding the templates cannot: an
// enum, a required field, a label's syntax, a cluster's admission policy,
// the chart's runner admission among them. The Job is a run's, its name and
// deadline included, so a policy judges it as it judges theirs.
//
// A Job that the API finds invalid stops serve at startup, rather than
// failing every run. Any other refusal is only logged. For example, a webhook
// that cannot take a dry run, a quota, or a run's Job of the same name.
func (k *Kube) CheckJob(ctx context.Context, deadline time.Duration) error {
	job := k.job(Spec{Job: runspec.Spec{RunID: "00000000-0000-0000-0000-000000000000"}, Deadline: deadline})
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	_, err := k.Client.BatchV1().Jobs(k.Namespace).Create(cctx, job, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
	switch {
	case apierrors.IsInvalid(err):
		return fmt.Errorf("executor: the API refuses a runner Job: %w", err)
	case err != nil:
		k.logger().Warn("runner Job not checked by the API", "error", err)
	}
	return nil
}

// takenLabel reports whether key is a label that the configuration cannot
// set on runner Jobs or pods. One kind is kritika's own, by which its watch
// and NetworkPolicies select them. Another is the instance label, by which
// kritika's Services select the serve pods alone. The last is one that the
// Job controller sets, which the API refuses a Job's pods to carry otherwise.
func takenLabel(key string) bool {
	switch key {
	case "app.kubernetes.io/name", "app.kubernetes.io/component", "app.kubernetes.io/instance", jobNameLabel, "controller-uid":
		return true
	}
	return strings.HasPrefix(key, "kritika.home-operations.com/") || strings.HasPrefix(key, "batch.kubernetes.io/")
}
