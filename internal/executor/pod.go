package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/home-operations/kritika/internal/runner"
)

// Pod is what the configuration's runner.pod sets on runner pods: where
// they are scheduled, at what priority, the labels and annotations a
// cluster's tooling reads, and a user namespace of their own. What keeps a
// runner isolated, its ServiceAccount, the token it does not mount, its
// security context and its volumes, stays the executor's.
type Pod struct {
	Labels                    map[string]string                 `json:"labels,omitempty"`
	Annotations               map[string]string                 `json:"annotations,omitempty"`
	NodeSelector              map[string]string                 `json:"nodeSelector,omitempty"`
	Tolerations               []corev1.Toleration               `json:"tolerations,omitempty"`
	Affinity                  *corev1.Affinity                  `json:"affinity,omitempty"`
	TopologySpreadConstraints []corev1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`
	PriorityClassName         string                            `json:"priorityClassName,omitempty"`
	HostUsers                 *bool                             `json:"hostUsers,omitempty"`
}

// appArmorAnnotation prefixes the annotations that set a container's
// AppArmor profile, which Kubernetes still copies into its security context
// until the field replaces them.
const appArmorAnnotation = "container.apparmor.security.beta.kubernetes.io/"

// DecodePod decodes the configuration's runner.pod, which it keeps as the
// YAML written, refusing a field Pod does not have rather than dropping it,
// and a label or annotation a runner's isolation rests on.
func DecodePod(m map[string]any) (Pod, error) {
	var p Pod
	if m == nil {
		return p, nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return p, fmt.Errorf("executor: runner.pod: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return Pod{}, fmt.Errorf("executor: runner.pod: %w", err)
	}
	for _, key := range slices.Sorted(maps.Keys(p.Labels)) {
		if takenLabel(key) {
			return Pod{}, fmt.Errorf("executor: runner.pod: label %s is kritika's or the Job controller's", key)
		}
	}
	for _, key := range slices.Sorted(maps.Keys(p.Annotations)) {
		if strings.HasPrefix(key, appArmorAnnotation) {
			return Pod{}, fmt.Errorf("executor: runner.pod: annotation %s would set the runner's AppArmor profile", key)
		}
	}
	return p, nil
}

// CheckPod has the API validate a runner Job carrying the configuration's
// runner.pod, created as a dry run, for what decoding it cannot check: an
// enum, a required field, a label's syntax. The Job has the deadline and
// resources every run's has, so a cluster's policy judges it as it would
// theirs. One the API finds invalid stops serve at startup rather than
// failing every run; any other refusal, such as a webhook that cannot take
// a dry run, is only logged.
func (k *Kube) CheckPod(ctx context.Context, deadline time.Duration, resources map[string]any) error {
	if reflect.DeepEqual(k.Pod, Pod{}) {
		return nil
	}
	job, err := k.job(Spec{Job: runner.Spec{RunID: "00000000-0000-0000-0000-000000000000"}, Deadline: deadline, Resources: resources})
	if err != nil {
		return err
	}
	job.Name, job.GenerateName = "", "kritika-run-check-"
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	_, err = k.Client.BatchV1().Jobs(k.Namespace).Create(cctx, job, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
	switch {
	case apierrors.IsInvalid(err):
		return fmt.Errorf("executor: runner.pod: the API refuses a runner Job with it: %w", err)
	case err != nil:
		k.logger().Warn("runner.pod not checked by the API", "error", err)
	}
	return nil
}

// takenLabel reports whether key is a label the configuration may not set
// on runner pods: kritika's own, by which its watch and NetworkPolicies
// select runner pods; the instance label, by which kritika's Services
// select the serve pods alone; or one the Job controller sets, which the
// API refuses a Job's pods to carry otherwise.
func takenLabel(key string) bool {
	switch key {
	case "app.kubernetes.io/name", "app.kubernetes.io/component", "app.kubernetes.io/instance", jobNameLabel, "controller-uid":
		return true
	}
	return strings.HasPrefix(key, "kritika.home-operations.com/") || strings.HasPrefix(key, "batch.kubernetes.io/")
}
