package executor

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/home-operations/kritika/internal/runspec"
)

func TestDecodeJobTemplate(t *testing.T) {
	tests := []struct {
		name     string
		template map[string]any
		wantErr  string
	}{
		{name: "nothing set", template: nil},
		{name: "every field", template: map[string]any{
			"metadata": map[string]any{"labels": map[string]any{"team": "reviews"}, "annotations": map[string]any{"example.com/owner": "reviews"}},
			"spec":     map[string]any{"activeDeadlineSeconds": 1200, "ttlSecondsAfterFinished": 300},
		}},
		{name: "the Job's pod", template: map[string]any{"spec": map[string]any{"template": map[string]any{}}}, wantErr: `unknown field "template"`},
		{name: "the Job's retries", template: map[string]any{"spec": map[string]any{"backoffLimit": 3}}, wantErr: `unknown field "backoffLimit"`},
		{name: "a field of the wrong type", template: map[string]any{"spec": map[string]any{"ttlSecondsAfterFinished": "10m"}}, wantErr: "runner.jobTemplate"},
		{name: "a TTL of 0", template: map[string]any{"spec": map[string]any{"ttlSecondsAfterFinished": 0}}, wantErr: "not a positive number of seconds"},
		{name: "a TTL past int32", template: map[string]any{"spec": map[string]any{"ttlSecondsAfterFinished": int64(1) << 31}}, wantErr: "runner.jobTemplate"},
		{name: "kritika's label", template: map[string]any{"metadata": map[string]any{"labels": map[string]any{"kritika.home-operations.com/role": "x"}}},
			wantErr: "label kritika.home-operations.com/role"},
		{name: "the Job controller's label", template: map[string]any{"metadata": map[string]any{"labels": map[string]any{"controller-uid": "x"}}},
			wantErr: "label controller-uid"},
		{name: "kritika's annotation", template: map[string]any{"metadata": map[string]any{"annotations": map[string]any{"kritika.home-operations.com/head-sha": "x"}}},
			wantErr: "annotation kritika.home-operations.com/head-sha"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j, err := DecodeJobTemplate(tt.template)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("DecodeJobTemplate = %+v, %v; want an error with %q", j, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tt.template == nil {
				if !reflect.DeepEqual(j, JobTemplate{}) {
					t.Fatalf("DecodeJobTemplate(nil) = %+v", j)
				}
				return
			}
			if j.Metadata.Labels["team"] != "reviews" || j.Metadata.Annotations["example.com/owner"] != "reviews" ||
				j.Spec.ActiveDeadlineSeconds == nil || *j.Spec.ActiveDeadlineSeconds != 1200 ||
				j.Spec.TTLSecondsAfterFinished == nil || *j.Spec.TTLSecondsAfterFinished != 300 {
				t.Fatalf("DecodeJobTemplate = %+v", j)
			}
		})
	}
}

// fullPodTemplate sets every field a runner pod takes from its template.
func fullPodTemplate() map[string]any {
	return map[string]any{
		"metadata": map[string]any{
			"labels":      map[string]any{"pool": "reviews"},
			"annotations": map[string]any{"karpenter.sh/do-not-disrupt": "true"},
		},
		"spec": map[string]any{
			"resources": map[string]any{
				"requests": map[string]any{"cpu": "500m", "memory": "1Gi"},
				"limits":   map[string]any{"memory": "2Gi"},
			},
			"runtimeClassName":  "gvisor",
			"priorityClassName": "kritika-runner",
			"hostUsers":         false,
			"nodeSelector":      map[string]any{"node-role.example.com/runner": "true"},
			"tolerations":       []any{map[string]any{"key": "runner", "operator": "Exists", "effect": "NoSchedule"}},
			"affinity": map[string]any{"nodeAffinity": map[string]any{"requiredDuringSchedulingIgnoredDuringExecution": map[string]any{
				"nodeSelectorTerms": []any{map[string]any{"matchExpressions": []any{
					map[string]any{"key": "kubernetes.io/arch", "operator": "In", "values": []any{"amd64"}},
				}}},
			}}},
			"topologySpreadConstraints": []any{map[string]any{
				"maxSkew": 1, "topologyKey": "kubernetes.io/hostname", "whenUnsatisfiable": "ScheduleAnyway",
				"labelSelector": map[string]any{"matchLabels": map[string]any{"kritika.home-operations.com/role": "runner"}},
			}},
			"containers": []any{map[string]any{"name": "runner", "image": "registry.example/tools:1", "imagePullPolicy": "Always",
				"env": []any{
					map[string]any{"name": "KONFLATE_URL", "value": "https://konflate.internal.example"},
					map[string]any{"name": "API_TOKEN", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "tools", "key": "token"}}},
				},
			}},
		},
	}
}

// adminEnv is the variables fullPodTemplate gives the runner container.
var adminEnv = []corev1.EnvVar{
	{Name: "KONFLATE_URL", Value: "https://konflate.internal.example"},
	{Name: "API_TOKEN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{Name: "tools", Key: "token"}}},
}

func TestDecodePodTemplate(t *testing.T) {
	// podSpec is a template of spec alone, with the runner container
	// unless spec names containers.
	podSpec := func(spec map[string]any) map[string]any {
		if _, ok := spec["containers"]; !ok {
			spec["containers"] = []any{map[string]any{"name": "runner", "image": "x"}}
		}
		return map[string]any{"spec": spec}
	}
	runnerEnv := func(env ...any) map[string]any {
		return podSpec(map[string]any{"containers": []any{map[string]any{"name": "runner", "image": "x", "env": env}}})
	}
	labels := func(labels map[string]any) map[string]any {
		return map[string]any{"metadata": map[string]any{"labels": labels}, "spec": podSpec(map[string]any{})["spec"]}
	}
	tests := []struct {
		name     string
		template map[string]any
		wantErr  string
	}{
		{name: "every field", template: fullPodTemplate()},
		{name: "the runner container alone", template: podSpec(map[string]any{})},
		{name: "no runner container", template: nil, wantErr: "one container, named runner"},
		{name: "another container", template: podSpec(map[string]any{"containers": []any{map[string]any{"name": "sidecar", "image": "x"}}}),
			wantErr: "one container, named runner"},
		{name: "a second container", template: podSpec(map[string]any{"containers": []any{
			map[string]any{"name": "runner", "image": "x"}, map[string]any{"name": "sidecar", "image": "x"},
		}}), wantErr: "one container, named runner"},
		{name: "no image", template: podSpec(map[string]any{"containers": []any{map[string]any{"name": "runner"}}}), wantErr: "needs an image"},
		{name: "an unknown pull policy", template: podSpec(map[string]any{"containers": []any{
			map[string]any{"name": "runner", "image": "x", "imagePullPolicy": "Sometimes"},
		}}), wantErr: `imagePullPolicy "Sometimes"`},
		{name: "a field of the executor's", template: podSpec(map[string]any{"serviceAccountName": "default"}), wantErr: `unknown field "serviceAccountName"`},
		{name: "a container field of the executor's", template: podSpec(map[string]any{"containers": []any{
			map[string]any{"name": "runner", "image": "x", "command": []any{"sh"}},
		}}), wantErr: `unknown field "command"`},
		{name: "a misspelled field", template: podSpec(map[string]any{"toleration": []any{}}), wantErr: `unknown field "toleration"`},
		{name: "a field of the wrong type", template: podSpec(map[string]any{"tolerations": "runner"}), wantErr: "runner.podTemplate"},
		{name: "a misspelt nested field", template: podSpec(map[string]any{"tolerations": []any{map[string]any{"keyy": "runner"}}}),
			wantErr: `unknown field "keyy"`},
		{name: "a quantity that is none", template: podSpec(map[string]any{"resources": map[string]any{"limits": map[string]any{"memory": "lots"}}}),
			wantErr: "runner.podTemplate"},
		{name: "a variable of the commands' own", template: runnerEnv(map[string]any{"name": "HTTPS_PROXY", "value": "http://elsewhere"}),
			wantErr: `env "HTTPS_PROXY" is a name the runner keeps`},
		{name: "a credential's variable, in lower case", template: runnerEnv(map[string]any{"name": "gh_token", "value": "x"}),
			wantErr: `env "gh_token" is a name the runner keeps`},
		{name: "a variable set twice", template: runnerEnv(map[string]any{"name": "X", "value": "1"}, map[string]any{"name": "X", "value": "2"}),
			wantErr: `env "X" is set twice`},
		{name: "a variable from a ConfigMap", template: runnerEnv(map[string]any{"name": "X",
			"valueFrom": map[string]any{"configMapKeyRef": map[string]any{"name": "c", "key": "k"}}}), wantErr: `env "X" takes a value or a Secret's key alone`},
		{name: "a variable from a field", template: runnerEnv(map[string]any{"name": "X",
			"valueFrom": map[string]any{"fieldRef": map[string]any{"fieldPath": "spec.nodeName"}}}), wantErr: `env "X" takes a value or a Secret's key alone`},
		{name: "a variable from a Secret and a ConfigMap", template: runnerEnv(map[string]any{"name": "X", "valueFrom": map[string]any{
			"secretKeyRef": map[string]any{"name": "s", "key": "k"}, "configMapKeyRef": map[string]any{"name": "c", "key": "k"}}}),
			wantErr: `env "X" takes a value or a Secret's key alone`},
		{name: "a misspelt Secret key field", template: runnerEnv(map[string]any{"name": "X",
			"valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "s", "keys": "k"}}}), wantErr: `unknown field "keys"`},
		{name: "kritika's name label", template: labels(map[string]any{"app.kubernetes.io/name": "x"}), wantErr: "label app.kubernetes.io/name"},
		{name: "kritika's component label", template: labels(map[string]any{"app.kubernetes.io/component": "x"}),
			wantErr: "label app.kubernetes.io/component"},
		{name: "the legacy controller label", template: labels(map[string]any{"controller-uid": "x"}), wantErr: "label controller-uid"},
		{name: "the runner role label", template: labels(map[string]any{"kritika.home-operations.com/role": "other"}),
			wantErr: "label kritika.home-operations.com/role"},
		{name: "the instance label the Services select by", template: labels(map[string]any{"app.kubernetes.io/instance": "kritika"}),
			wantErr: "label app.kubernetes.io/instance"},
		{name: "the Job controller's label", template: labels(map[string]any{"batch.kubernetes.io/job-name": "x"}),
			wantErr: "label batch.kubernetes.io/job-name"},
		{name: "the legacy Job name label", template: labels(map[string]any{"job-name": "x"}), wantErr: "label job-name"},
		{name: "an AppArmor annotation", template: map[string]any{
			"metadata": map[string]any{"annotations": map[string]any{"container.apparmor.security.beta.kubernetes.io/runner": "unconfined"}},
			"spec":     podSpec(map[string]any{})["spec"],
		}, wantErr: "AppArmor"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := DecodePodTemplate(tt.template)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("DecodePodTemplate = %+v, %v; want an error with %q", p, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tt.name != "every field" {
				return
			}
			s := p.Spec
			if p.Metadata.Labels["pool"] != "reviews" || p.Metadata.Annotations["karpenter.sh/do-not-disrupt"] != "true" ||
				s.Resources == nil || s.Resources.Limits.Memory().String() != "2Gi" || s.Resources.Requests.Cpu().String() != "500m" ||
				s.RuntimeClassName != "gvisor" || s.PriorityClassName != "kritika-runner" || s.HostUsers == nil || *s.HostUsers ||
				s.NodeSelector["node-role.example.com/runner"] != "true" || len(s.Tolerations) != 1 ||
				s.Tolerations[0].Operator != corev1.TolerationOpExists || s.Affinity == nil || s.Affinity.NodeAffinity == nil ||
				len(s.TopologySpreadConstraints) != 1 || s.TopologySpreadConstraints[0].MaxSkew != 1 ||
				!reflect.DeepEqual(p.runner(), TemplateContainer{Name: "runner", Image: "registry.example/tools:1", ImagePullPolicy: corev1.PullAlways, Env: adminEnv}) {
				t.Fatalf("DecodePodTemplate = %+v", p)
			}
		})
	}
}

// templatedJob is a run's Job built from templates that set every field.
func templatedJob(t *testing.T) *batchv1.Job {
	t.Helper()
	jobTemplate, err := DecodeJobTemplate(map[string]any{
		"metadata": map[string]any{"labels": map[string]any{"team": "reviews"}, "annotations": map[string]any{"example.com/owner": "reviews"}},
		"spec":     map[string]any{"activeDeadlineSeconds": 1200, "ttlSecondsAfterFinished": 300},
	})
	if err != nil {
		t.Fatal(err)
	}
	podTemplate, err := DecodePodTemplate(fullPodTemplate())
	if err != nil {
		t.Fatal(err)
	}
	return (&Kube{Namespace: "kritika", ServiceAccount: "sa", JobTemplate: jobTemplate, PodTemplate: podTemplate}).job(spec())
}

// TestJobSpecTemplateMetadata: the Job template marks the runner Job and
// the pod template its pod, each beside kritika's own labels and
// annotations, and the Job keeps the run's deadline.
func TestJobSpecTemplateMetadata(t *testing.T) {
	j := templatedJob(t)
	if j.Labels["team"] != "reviews" || j.Labels["kritika.home-operations.com/role"] != "runner" ||
		j.Annotations["example.com/owner"] != "reviews" || j.Annotations["kritika.home-operations.com/head-sha"] != headSHA {
		t.Fatalf("Job metadata = %+v", j.ObjectMeta)
	}
	// The Job's deadline is the run's, which the configuration reads from
	// the template.
	if *j.Spec.TTLSecondsAfterFinished != 300 || *j.Spec.ActiveDeadlineSeconds != 300 || *j.Spec.BackoffLimit != 0 {
		t.Fatalf("ttl/deadline/backoff = %d/%d/%d", *j.Spec.TTLSecondsAfterFinished, *j.Spec.ActiveDeadlineSeconds, *j.Spec.BackoffLimit)
	}
	tpl := j.Spec.Template
	if tpl.Labels["pool"] != "reviews" || tpl.Labels["kritika.home-operations.com/role"] != "runner" ||
		tpl.Annotations["karpenter.sh/do-not-disrupt"] != "true" {
		t.Fatalf("pod metadata = %+v", tpl.ObjectMeta)
	}
	if _, ok := j.Labels["pool"]; ok {
		t.Fatal("the pod template's labels are the pod's, not the Job's")
	}
	if _, ok := tpl.Labels["team"]; ok {
		t.Fatal("the Job template's labels are the Job's, not the pod's")
	}
}

// TestJobSpecPodTemplate: the pod template places the runner pod and sets
// its resources and image beside, never instead of, the executor's own
// settings.
func TestJobSpecPodTemplate(t *testing.T) {
	ps := templatedJob(t).Spec.Template.Spec
	if ps.Resources == nil || ps.Resources.Limits.Memory().String() != "2Gi" || ps.RuntimeClassName == nil || *ps.RuntimeClassName != "gvisor" ||
		ps.PriorityClassName != "kritika-runner" || ps.HostUsers == nil || *ps.HostUsers {
		t.Fatalf("pod spec = %+v", ps)
	}
	if ps.NodeSelector["node-role.example.com/runner"] != "true" || len(ps.Tolerations) != 1 || ps.Affinity == nil || ps.Affinity.NodeAffinity == nil ||
		len(ps.TopologySpreadConstraints) != 1 || ps.TopologySpreadConstraints[0].TopologyKey != "kubernetes.io/hostname" {
		t.Fatalf("pod placement = %+v", ps)
	}
	if c := ps.Containers[0]; c.Image != "registry.example/tools:1" || c.ImagePullPolicy != corev1.PullAlways ||
		len(c.Resources.Requests) != 0 || len(c.Resources.Limits) != 0 {
		t.Fatalf("runner container = %+v, want the template's image and pull policy, and the pod's resources alone", c)
	}
	// The admin's variables come before kritika's, whose values a $(NAME)
	// in theirs then cannot take in, and under the prefix the runner hands
	// on, so none of them configures the runner itself.
	want := slices.Clone(adminEnv)
	for i := range want {
		want[i].Name = runspec.CommandEnvPrefix + want[i].Name
	}
	if env := ps.Containers[0].Env; len(env) <= len(want) || !reflect.DeepEqual(env[:len(want)], want) ||
		env[len(want)].Name != "KRITIKA_RUN_SPEC_FILE" {
		t.Fatalf("runner env = %+v, want the admin's variables first, prefixed", env)
	}
	if ps.ServiceAccountName != "sa" || ps.AutomountServiceAccountToken == nil || *ps.AutomountServiceAccountToken ||
		ps.SecurityContext == nil || ps.SecurityContext.RunAsNonRoot == nil || !*ps.SecurityContext.RunAsNonRoot {
		t.Fatalf("the executor's own pod settings changed: %+v", ps)
	}
}

// TestKubeCheckJob: a runner Job built from the templates is checked by the
// API, created as a dry run; one the API finds invalid stops serve, any
// other refusal does not.
func TestKubeCheckJob(t *testing.T) {
	invalid := apierrors.NewInvalid(schema.GroupKind{Group: "batch", Kind: "Job"}, "kritika-run-00000000", field.ErrorList{
		field.NotSupported(field.NewPath("spec", "template", "spec", "tolerations").Index(0).Child("operator"), "Exist", []string{"Equal", "Exists"}),
	})
	noDryRun := apierrors.NewBadRequest(`admission webhook "policy.example.com" does not support dry run`)
	taken := apierrors.NewAlreadyExists(schema.GroupResource{Group: "batch", Resource: "jobs"}, "kritika-run-00000000")
	tests := []struct {
		name    string
		answer  error
		wantErr bool
	}{
		{name: "accepted"},
		{name: "invalid", answer: invalid, wantErr: true},
		{name: "a webhook that cannot take a dry run", answer: noDryRun},
		{name: "a run's Job of the same name", answer: taken},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			var sent *batchv1.Job
			var opts metav1.CreateOptions
			client.PrependReactor("create", "jobs", func(a k8stesting.Action) (bool, runtime.Object, error) {
				c := a.(k8stesting.CreateActionImpl)
				sent, opts = c.GetObject().(*batchv1.Job), c.GetCreateOptions()
				return true, sent, tt.answer
			})
			pod := withImage("x")
			pod.Spec.PriorityClassName = "kritika-runner"
			pod.Spec.Resources = &corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")}}
			k := &Kube{Client: client, Namespace: "kritika", PodTemplate: pod}
			err := k.CheckJob(t.Context(), 20*time.Minute)
			if (err != nil) != tt.wantErr || tt.wantErr && (!apierrors.IsInvalid(err) || !strings.Contains(err.Error(), "refuses a runner Job")) {
				t.Fatalf("CheckJob = %v, want an error %v", err, tt.wantErr)
			}
			if sent == nil || sent.Name != "kritika-run-00000000" || len(opts.DryRun) != 1 || opts.DryRun[0] != metav1.DryRunAll ||
				sent.Spec.Template.Spec.PriorityClassName != "kritika-runner" {
				t.Fatalf("sent %+v with %+v, want a dry run of the runner Job", sent, opts)
			}
			// A policy judges the dry run as a run's Job: the same deadline and resources.
			if sent.Spec.ActiveDeadlineSeconds == nil || *sent.Spec.ActiveDeadlineSeconds != 1200 ||
				sent.Spec.Template.Spec.Resources == nil || sent.Spec.Template.Spec.Resources.Limits.Memory().String() != "1Gi" {
				t.Fatalf("dry run deadline %v and resources %+v, want the configured ones",
					sent.Spec.ActiveDeadlineSeconds, sent.Spec.Template.Spec.Resources)
			}
		})
	}
}
