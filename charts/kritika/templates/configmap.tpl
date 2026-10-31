{{- $config := include "kritika.configuration" . -}}
{{- if not .Values.existingConfigMap }}
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ include "kritika.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "kritika.labels" . | nindent 4 }}
  annotations:
    # The pods' checksum/config rolls them on a change. A Reloader rollout
    # too races Helm's and adds a ReplicaSet of the previous release.
    reloader.stakater.com/ignore: "true"
data:
  config.yaml: |
    {{- $config | nindent 4 }}
{{- end }}
