{{- $config := include "kritika.configuration" . -}}
{{- if not .Values.existingConfigMap }}
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ include "kritika.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "kritika.labels" . | nindent 4 }}
data:
  config.yaml: |
    {{- $config | nindent 4 }}
{{- end }}
