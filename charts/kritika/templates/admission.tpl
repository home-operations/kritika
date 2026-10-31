{{- if .Values.runner.admissionPolicy.enabled }}
{{- /* Namespace names hold no dots, so the pair is unambiguous. */}}
{{- $name := printf "%s.%s-runner" .Release.Namespace (include "kritika.fullname" .) }}
{{- $kritika := printf "system:serviceaccount:%s:%s" .Release.Namespace (include "kritika.serviceAccountName" .) }}
{{- $podTemplate := include "kritika.runnerPodTemplate" . | fromYaml }}
{{- $runtimeClass := dig "spec" "runtimeClassName" "" $podTemplate }}
{{- $podLabels := list }}
{{- range $k, $v := (dig "metadata" "labels" dict $podTemplate) }}
{{- $podLabels = append $podLabels (printf "'%s': '%s'" $k $v) }}
{{- end }}
{{- $image := "" }}
{{- $envSecrets := list }}
{{- range (dig "spec" "containers" list $podTemplate) }}
{{- if eq .name "runner" }}
{{- $image = .image }}
{{- range (.env | default list) }}
{{- with (dig "valueFrom" "secretKeyRef" "name" "" .) }}
{{- $envSecrets = append $envSecrets (printf "'%s'" .) }}
{{- end }}
{{- end }}
{{- end }}
{{- end }}
{{- /* The image's repository: what precedes its tag, whose colon follows the last slash, and its digest. */}}
{{- $repository := regexReplaceAll "^([^@]+?)(:[^:/@]+)?(@.+)?$" $image "${1}" }}
# The Role lets kritika serve create Jobs and Secrets in its namespace, and
# the cluster does not compare a Job's pod with its creator's permissions. The
# cluster therefore refuses, from kritika serve's ServiceAccount, any Job that
# is not a runner Job as the executor builds it. It also refuses any Secret
# that is not a run's.
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata:
  name: {{ $name }}-jobs
  labels:
    {{- include "kritika.labels" . | nindent 4 }}
spec:
  failurePolicy: Fail
  matchConstraints:
    resourceRules:
      - apiGroups: ["batch"]
        apiVersions: ["v1"]
        operations: ["CREATE"]
        resources: ["jobs"]
  matchConditions:
    - name: kritika
      expression: {{ printf "request.userInfo.username == '%s'" $kritika | quote }}
  variables:
    - name: pod
      expression: object.spec.template.spec
    - name: labels
      expression: "has(object.spec.template.metadata.labels) ? object.spec.template.metadata.labels : {}"
    # A run's Secret is named after its Job, which is named after its run:
    # the Job's name, then the rest of the run's ID.
    - name: runSecret
      expression: "'^' + object.metadata.name + '(-[0-9a-f]{4}){3}-[0-9a-f]{12}$'"
    - name: podLabels
      expression: {{ printf "{%s}" (join ", " $podLabels) | quote }}
    - name: envSecrets
      expression: {{ printf "[%s]" (join ", " ($envSecrets | uniq)) | quote }}
  validations:
    - expression: "object.metadata.name.matches('^kritika-run-[0-9a-f]{8}$')"
      message: a runner Job is named kritika-run- and the first eight characters of its run's ID
    - expression: >-
        'kritika.home-operations.com/role' in variables.labels &&
        variables.labels['kritika.home-operations.com/role'] == 'runner'
      message: a runner pod carries the runner role label, by which the runner NetworkPolicy selects it
    - expression: >-
        variables.labels.all(k,
          (k == 'app.kubernetes.io/name' && variables.labels[k] == 'kritika') ||
          (k == 'app.kubernetes.io/component' && variables.labels[k] == 'runner') ||
          k.startsWith('kritika.home-operations.com/') ||
          k == 'job-name' || k == 'controller-uid' || k.startsWith('batch.kubernetes.io/') ||
          (k in variables.podLabels && variables.podLabels[k] == variables.labels[k]))
      message: a runner pod carries kritika's labels, the Job controller's and runner.podTemplate's alone, none another policy or Service selects other pods by
    - expression: >-
        has(variables.pod.serviceAccountName) && variables.pod.serviceAccountName == '{{ include "kritika.runnerServiceAccountName" . }}' &&
        has(variables.pod.automountServiceAccountToken) && !variables.pod.automountServiceAccountToken &&
        !has(variables.pod.imagePullSecrets)
      message: a runner pod runs as the runner ServiceAccount, its token not mounted, pulling with its image pull Secrets alone
    {{- if $runtimeClass }}
    - expression: "has(variables.pod.runtimeClassName) && variables.pod.runtimeClassName == '{{ $runtimeClass }}'"
      message: a runner pod runs under the runner RuntimeClass
    {{- else }}
    - expression: "!has(variables.pod.runtimeClassName)"
      message: a runner pod runs under the cluster's default runtime
    {{- end }}
    - expression: >-
        !(has(variables.pod.hostNetwork) && variables.pod.hostNetwork) &&
        !(has(variables.pod.hostPID) && variables.pod.hostPID) &&
        !(has(variables.pod.hostIPC) && variables.pod.hostIPC)
      message: a runner pod shares none of the node's namespaces
    - expression: >-
        has(variables.pod.securityContext) &&
        has(variables.pod.securityContext.runAsNonRoot) && variables.pod.securityContext.runAsNonRoot &&
        has(variables.pod.securityContext.seccompProfile) && variables.pod.securityContext.seccompProfile.type == 'RuntimeDefault' &&
        !has(variables.pod.securityContext.appArmorProfile) && !has(variables.pod.securityContext.seLinuxOptions) &&
        !has(variables.pod.securityContext.windowsOptions) && !has(variables.pod.securityContext.sysctls) &&
        !(has(object.spec.template.metadata.annotations) &&
          object.spec.template.metadata.annotations.exists(k, k.startsWith('container.apparmor.security.beta.kubernetes.io/')))
      message: a runner pod runs as non-root, confined by the runtime's default profiles
    - expression: >-
        variables.pod.containers.size() == 1 && !has(variables.pod.initContainers) && !has(variables.pod.ephemeralContainers)
      message: a runner pod has the runner container alone
    - expression: >-
        variables.pod.containers.all(c, c.image.matches(r'^{{ regexQuoteMeta $repository }}(:[^:/@]+)?(@[^@]+)?$'))
      message: a runner pod runs an image of the runner repository
    - expression: >-
        variables.pod.containers.all(c, has(c.securityContext) &&
          has(c.securityContext.allowPrivilegeEscalation) && !c.securityContext.allowPrivilegeEscalation &&
          has(c.securityContext.readOnlyRootFilesystem) && c.securityContext.readOnlyRootFilesystem &&
          !(has(c.securityContext.privileged) && c.securityContext.privileged) &&
          has(c.securityContext.capabilities) && has(c.securityContext.capabilities.drop) &&
          'ALL' in c.securityContext.capabilities.drop && !has(c.securityContext.capabilities.add) &&
          !(has(c.securityContext.runAsNonRoot) && !c.securityContext.runAsNonRoot) &&
          !(has(c.securityContext.runAsUser) && c.securityContext.runAsUser == 0) &&
          !(has(c.securityContext.seccompProfile) && c.securityContext.seccompProfile.type != 'RuntimeDefault') &&
          !has(c.securityContext.appArmorProfile) && !has(c.securityContext.seLinuxOptions) &&
          !has(c.securityContext.windowsOptions) && !has(c.securityContext.procMount))
      message: the runner container keeps the runner's security context
    - expression: >-
        variables.pod.containers.all(c, !has(c.ports) &&
          !has(c.livenessProbe) && !has(c.readinessProbe) && !has(c.startupProbe) && !has(c.lifecycle))
      message: the runner container opens no ports, and has no probes or lifecycle hooks, which the kubelet would run from the node
    - expression: >-
        has(variables.pod.volumes) && variables.pod.volumes.all(v,
          (v.name == 'scratch' && has(v.emptyDir)) ||
          (v.name == 'spec' && has(v.secret) && v.secret.secretName.matches(variables.runSecret)))
      message: a runner pod mounts an emptyDir and its run's own Secret alone
    - expression: >-
        variables.pod.containers.all(c, !has(c.envFrom) &&
          (!has(c.env) || c.env.all(e, !has(e.valueFrom) || !has(e.valueFrom.secretKeyRef) ||
            e.valueFrom.secretKeyRef.name == '{{ tpl .Values.database.runner.existingSecret . }}' ||
            e.valueFrom.secretKeyRef.name.matches(variables.runSecret) ||
            e.valueFrom.secretKeyRef.name in variables.envSecrets)))
      message: the runner container reads its run's own Secret, the runner database Secret and those runner.podTemplate's env names alone
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicyBinding
metadata:
  name: {{ $name }}-jobs
  labels:
    {{- include "kritika.labels" . | nindent 4 }}
spec:
  policyName: {{ $name }}-jobs
  validationActions: [Deny]
  matchResources:
    namespaceSelector:
      matchLabels:
        kubernetes.io/metadata.name: {{ .Release.Namespace }}
---
# A Job deleted any other way than in the foreground, as kritika deletes
# its runs', leaves its run's Secret behind, orphaned or not yet collected.
# A new Job of the same name then gets that Secret. A deletion that names no
# propagation orphans a Job's dependents.
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata:
  name: {{ $name }}-job-deletes
  labels:
    {{- include "kritika.labels" . | nindent 4 }}
spec:
  failurePolicy: Fail
  matchConstraints:
    resourceRules:
      - apiGroups: ["batch"]
        apiVersions: ["v1"]
        operations: ["DELETE"]
        resources: ["jobs"]
  matchConditions:
    - name: kritika
      expression: {{ printf "request.userInfo.username == '%s'" $kritika | quote }}
  validations:
    - expression: "has(request.options.propagationPolicy) && request.options.propagationPolicy == 'Foreground'"
      message: kritika deletes a runner Job in the foreground, its run's Secret with it
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicyBinding
metadata:
  name: {{ $name }}-job-deletes
  labels:
    {{- include "kritika.labels" . | nindent 4 }}
spec:
  policyName: {{ $name }}-job-deletes
  validationActions: [Deny]
  matchResources:
    namespaceSelector:
      matchLabels:
        kubernetes.io/metadata.name: {{ .Release.Namespace }}
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata:
  name: {{ $name }}-secrets
  labels:
    {{- include "kritika.labels" . | nindent 4 }}
spec:
  failurePolicy: Fail
  matchConstraints:
    resourceRules:
      - apiGroups: [""]
        apiVersions: ["v1"]
        operations: ["CREATE"]
        resources: ["secrets"]
  matchConditions:
    - name: kritika
      expression: {{ printf "request.userInfo.username == '%s'" $kritika | quote }}
  validations:
    - expression: "object.metadata.name.matches('^kritika-run-[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$')"
      message: a run Secret is named kritika-run- and its run's ID
    - expression: "object.type == 'Opaque'"
      message: a run Secret is Opaque, never a ServiceAccount token
    - expression: >-
        has(object.metadata.ownerReferences) && object.metadata.ownerReferences.size() == 1 &&
        object.metadata.ownerReferences.all(r, r.apiVersion == 'batch/v1' && r.kind == 'Job' &&
          r.name.matches('^kritika-run-[0-9a-f]{8}$') && object.metadata.name.startsWith(r.name + '-') &&
          has(r.blockOwnerDeletion) && r.blockOwnerDeletion)
      message: a run Secret is owned by its runner Job alone, whose deletion waits for the Secret's
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicyBinding
metadata:
  name: {{ $name }}-secrets
  labels:
    {{- include "kritika.labels" . | nindent 4 }}
spec:
  policyName: {{ $name }}-secrets
  validationActions: [Deny]
  matchResources:
    namespaceSelector:
      matchLabels:
        kubernetes.io/metadata.name: {{ .Release.Namespace }}
{{- end }}
