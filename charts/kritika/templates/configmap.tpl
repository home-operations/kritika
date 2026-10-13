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
  # NOT tpl'd: the file is kritika's own schema, validated at load with unknown
  # keys rejected; expressions in application values remain literal.
  config.yaml: |
    {{- $config | nindent 4 }}
{{- end }}
