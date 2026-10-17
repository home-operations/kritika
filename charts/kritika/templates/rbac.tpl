{{- if .Values.rbac.create }}
# kritika serve creates runner Jobs in its own namespace and watches them and
# their pods, one watch of each by the runner role label however many runs
# are in flight, lists a Job's pods and reads their logs, records Events on
# those Jobs and on its own pods and reads why a Job created no pod; nothing
# cluster-wide, and nothing else. get on Jobs is what a replica of an older
# release, still finishing its runs mid-rollout, reads them with.
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: {{ include "kritika.fullname" . }}-worker
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "kritika.labels" . | nindent 4 }}
rules:
  - apiGroups: ["batch"]
    resources: ["jobs"]
    verbs: ["create", "get", "list", "watch", "delete"]
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["get", "list", "watch"]
  - apiGroups: [""]
    resources: ["pods/log"]
    verbs: ["get"]
  # Why kritika ended a run, on its Job, and what the leader did with the
  # configuration, on its pod; each is created whole, never updated. Listed
  # for a Job's FailedCreate, why its pod was never created.
  - apiGroups: ["events.k8s.io"]
    resources: ["events"]
    verbs: ["create", "list"]
  # One Secret per runner Job carries that run's credentials, created with
  # the Job as its owner, so garbage collection deletes it with the Job.
  # Create alone: the worker writes them and never reads, changes or
  # deletes any.
  - apiGroups: [""]
    resources: ["secrets"]
    verbs: ["create"]
  # A run Secret's owner reference holds its Job's foreground deletion until
  # the Secret is gone, which, where owner references' permissions are
  # enforced, takes update on the Job's finalizers: a check, not an endpoint.
  - apiGroups: ["batch"]
    resources: ["jobs/finalizers"]
    verbs: ["update"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: {{ include "kritika.fullname" . }}-worker
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "kritika.labels" . | nindent 4 }}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: {{ include "kritika.fullname" . }}-worker
subjects:
  - kind: ServiceAccount
    name: {{ include "kritika.serviceAccountName" . | quote }}
    namespace: {{ .Release.Namespace }}
{{- end }}
