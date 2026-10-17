package executor

import (
	"reflect"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestDecodePod(t *testing.T) {
	full := map[string]any{
		"labels":       map[string]any{"team": "reviews"},
		"annotations":  map[string]any{"karpenter.sh/do-not-disrupt": "true"},
		"nodeSelector": map[string]any{"node-role.example.com/runner": "true"},
		"tolerations":  []any{map[string]any{"key": "runner", "operator": "Exists", "effect": "NoSchedule"}},
		"affinity": map[string]any{"nodeAffinity": map[string]any{"requiredDuringSchedulingIgnoredDuringExecution": map[string]any{
			"nodeSelectorTerms": []any{map[string]any{"matchExpressions": []any{
				map[string]any{"key": "kubernetes.io/arch", "operator": "In", "values": []any{"amd64"}},
			}}},
		}}},
		"topologySpreadConstraints": []any{map[string]any{
			"maxSkew": 1, "topologyKey": "kubernetes.io/hostname", "whenUnsatisfiable": "ScheduleAnyway",
			"labelSelector": map[string]any{"matchLabels": map[string]any{"kritika.home-operations.com/role": "runner"}},
		}},
		"priorityClassName": "kritika-runner",
		"hostUsers":         false,
	}
	tests := []struct {
		name    string
		pod     map[string]any
		wantErr string
	}{
		{name: "nothing set", pod: nil},
		{name: "every field", pod: full},
		{name: "a field of the executor's", pod: map[string]any{"serviceAccountName": "default"}, wantErr: `unknown field "serviceAccountName"`},
		{name: "a misspelled field", pod: map[string]any{"toleration": []any{}}, wantErr: `unknown field "toleration"`},
		{name: "a field of the wrong type", pod: map[string]any{"tolerations": "runner"}, wantErr: "runner.pod"},
		{name: "a misspelt nested field", pod: map[string]any{"tolerations": []any{map[string]any{"keyy": "runner"}}}, wantErr: `unknown field "keyy"`},
		{name: "kritika's name label", pod: map[string]any{"labels": map[string]any{"app.kubernetes.io/name": "x"}}, wantErr: "label app.kubernetes.io/name"},
		{name: "kritika's component label", pod: map[string]any{"labels": map[string]any{"app.kubernetes.io/component": "x"}},
			wantErr: "label app.kubernetes.io/component"},
		{name: "the legacy controller label", pod: map[string]any{"labels": map[string]any{"controller-uid": "x"}}, wantErr: "label controller-uid"},
		{name: "the runner role label", pod: map[string]any{"labels": map[string]any{"kritika.home-operations.com/role": "other"}},
			wantErr: "label kritika.home-operations.com/role"},
		{name: "the instance label the Services select by", pod: map[string]any{"labels": map[string]any{"app.kubernetes.io/instance": "kritika"}},
			wantErr: "label app.kubernetes.io/instance"},
		{name: "the Job controller's label", pod: map[string]any{"labels": map[string]any{"batch.kubernetes.io/job-name": "x"}},
			wantErr: "label batch.kubernetes.io/job-name"},
		{name: "the legacy Job name label", pod: map[string]any{"labels": map[string]any{"job-name": "x"}}, wantErr: "label job-name"},
		{name: "an AppArmor annotation", pod: map[string]any{"annotations": map[string]any{"container.apparmor.security.beta.kubernetes.io/runner": "unconfined"}},
			wantErr: "AppArmor"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := DecodePod(tt.pod)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("DecodePod = %+v, %v; want an error with %q", p, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tt.pod == nil {
				if !reflect.DeepEqual(p, Pod{}) {
					t.Fatalf("DecodePod(nil) = %+v", p)
				}
				return
			}
			if p.Labels["team"] != "reviews" || p.Annotations["karpenter.sh/do-not-disrupt"] != "true" ||
				p.NodeSelector["node-role.example.com/runner"] != "true" || len(p.Tolerations) != 1 ||
				p.Tolerations[0].Operator != corev1.TolerationOpExists || p.Affinity == nil || p.Affinity.NodeAffinity == nil ||
				len(p.TopologySpreadConstraints) != 1 || p.TopologySpreadConstraints[0].MaxSkew != 1 ||
				p.PriorityClassName != "kritika-runner" || p.HostUsers == nil || *p.HostUsers {
				t.Fatalf("DecodePod = %+v", p)
			}
		})
	}
}

// TestJobSpecPod: the configuration's runner.pod places and marks the
// runner pod beside, never instead of, the executor's own settings.
func TestJobSpecPod(t *testing.T) {
	pod, err := DecodePod(map[string]any{
		"labels":       map[string]any{"team": "reviews"},
		"annotations":  map[string]any{"karpenter.sh/do-not-disrupt": "true"},
		"nodeSelector": map[string]any{"node-role.example.com/runner": "true"},
		"tolerations":  []any{map[string]any{"key": "runner", "operator": "Exists", "effect": "NoSchedule"}},
		"affinity": map[string]any{"nodeAffinity": map[string]any{"requiredDuringSchedulingIgnoredDuringExecution": map[string]any{
			"nodeSelectorTerms": []any{map[string]any{"matchExpressions": []any{
				map[string]any{"key": "kubernetes.io/arch", "operator": "In", "values": []any{"amd64"}},
			}}},
		}}},
		"topologySpreadConstraints": []any{map[string]any{"maxSkew": 1, "topologyKey": "kubernetes.io/hostname", "whenUnsatisfiable": "ScheduleAnyway"}},
		"priorityClassName":         "kritika-runner",
		"hostUsers":                 false,
	})
	if err != nil {
		t.Fatal(err)
	}
	j := mustJob(t, &Kube{Namespace: "kritika", Image: "x", ServiceAccount: "sa", Pod: pod}, spec())
	tpl := j.Spec.Template
	if tpl.Labels["team"] != "reviews" || tpl.Labels["kritika.home-operations.com/role"] != "runner" ||
		tpl.Annotations["karpenter.sh/do-not-disrupt"] != "true" {
		t.Fatalf("pod metadata = %+v", tpl.ObjectMeta)
	}
	if _, ok := j.Labels["team"]; ok {
		t.Fatal("runner.pod's labels are the pod's, not the Job's")
	}
	ps := tpl.Spec
	if ps.NodeSelector["node-role.example.com/runner"] != "true" || len(ps.Tolerations) != 1 || ps.PriorityClassName != "kritika-runner" ||
		ps.HostUsers == nil || *ps.HostUsers || ps.Affinity == nil || ps.Affinity.NodeAffinity == nil ||
		len(ps.TopologySpreadConstraints) != 1 || ps.TopologySpreadConstraints[0].TopologyKey != "kubernetes.io/hostname" {
		t.Fatalf("pod spec = %+v", ps)
	}
	if ps.ServiceAccountName != "sa" || ps.AutomountServiceAccountToken == nil || *ps.AutomountServiceAccountToken ||
		ps.SecurityContext == nil || ps.SecurityContext.RunAsNonRoot == nil || !*ps.SecurityContext.RunAsNonRoot {
		t.Fatalf("the executor's own pod settings changed: %+v", ps)
	}
}

// TestKubeCheckPod: a runner.pod is checked by the API with a runner Job
// created as a dry run; one the API finds invalid stops serve, any other
// refusal does not, and no runner.pod sends nothing.
func TestKubeCheckPod(t *testing.T) {
	invalid := apierrors.NewInvalid(schema.GroupKind{Group: "batch", Kind: "Job"}, "kritika-run-check-x", field.ErrorList{
		field.NotSupported(field.NewPath("spec", "template", "spec", "tolerations").Index(0).Child("operator"), "Exist", []string{"Equal", "Exists"}),
	})
	noDryRun := apierrors.NewBadRequest(`admission webhook "policy.example.com" does not support dry run`)
	tests := []struct {
		name    string
		pod     Pod
		answer  error
		wantErr bool
		sent    bool
	}{
		{name: "no runner.pod", pod: Pod{}},
		{name: "accepted", pod: Pod{PriorityClassName: "kritika-runner"}, sent: true},
		{name: "invalid", pod: Pod{PriorityClassName: "kritika-runner"}, answer: invalid, wantErr: true, sent: true},
		{name: "a webhook that cannot take a dry run", pod: Pod{PriorityClassName: "kritika-runner"}, answer: noDryRun, sent: true},
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
			k := &Kube{Client: client, Namespace: "kritika", Image: "x", Pod: tt.pod}
			err := k.CheckPod(t.Context(), 20*time.Minute, map[string]any{"limits": map[string]any{"memory": "1Gi"}})
			if (err != nil) != tt.wantErr || tt.wantErr && (!apierrors.IsInvalid(err) || !strings.Contains(err.Error(), "runner.pod")) {
				t.Fatalf("CheckPod = %v, want an error %v", err, tt.wantErr)
			}
			if (sent != nil) != tt.sent {
				t.Fatalf("a Job was sent: %v, want %v", sent != nil, tt.sent)
			}
			if sent != nil && (sent.Name != "" || sent.GenerateName == "" || len(opts.DryRun) != 1 || opts.DryRun[0] != metav1.DryRunAll ||
				sent.Spec.Template.Spec.PriorityClassName != "kritika-runner") {
				t.Fatalf("sent %s/%s with %+v and priority class %q, want a dry run of the runner Job",
					sent.Name, sent.GenerateName, opts, sent.Spec.Template.Spec.PriorityClassName)
			}
			// A policy judges the dry run as a run's Job: the same deadline and resources.
			if sent != nil && (sent.Spec.ActiveDeadlineSeconds == nil || *sent.Spec.ActiveDeadlineSeconds != 1200 ||
				sent.Spec.Template.Spec.Containers[0].Resources.Limits.Memory().String() != "1Gi") {
				t.Fatalf("dry run deadline %v and resources %+v, want the configured ones",
					sent.Spec.ActiveDeadlineSeconds, sent.Spec.Template.Spec.Containers[0].Resources)
			}
		})
	}
}
