{{/*
Expand the name of the chart.
*/}}
{{- define "kritika.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name (truncated to the 63-char DNS limit).
*/}}
{{- define "kritika.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Chart name and version as used by the chart label.
*/}}
{{- define "kritika.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "kritika.labels" -}}
helm.sh/chart: {{ include "kritika.chart" . }}
{{ include "kritika.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels of the server pods. Runner pods lack the instance label, so
these select the server alone.
*/}}
{{- define "kritika.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kritika.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Service account name to use.
*/}}
{{- define "kritika.serviceAccountName" -}}
{{- $name := tpl (.Values.serviceAccount.name | default "") $ -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "kritika.fullname" .) $name }}
{{- else }}
{{- default "default" $name }}
{{- end }}
{{- end }}

{{/*
Runner service account name.
*/}}
{{- define "kritika.runnerServiceAccountName" -}}
{{- $name := tpl (.Values.runner.serviceAccount.name | default "") $ -}}
{{- if .Values.runner.serviceAccount.create }}
{{- default (printf "%s-runner" (include "kritika.fullname" .)) $name }}
{{- else }}
{{- default "default" $name }}
{{- end }}
{{- end }}

{{/*
Container image reference. A digest pins immutably and wins when set;
otherwise it's repository:tag, with tag defaulting to the chart appVersion.
*/}}
{{- define "kritika.image" -}}
{{- if .Values.image.digest -}}
{{- printf "%s@%s" .Values.image.repository .Values.image.digest -}}
{{- else -}}
{{- printf "%s:%s" .Values.image.repository (.Values.image.tag | default .Chart.AppVersion) -}}
{{- end -}}
{{- end }}

{{/*
The helm test pod's curl image.
*/}}
{{- define "kritika.testImage" -}}
{{- $img := .Values.tests.image -}}
{{- printf "%s:%s" $img.repository $img.tag -}}
{{- end }}

{{/*
This release's -tools image from the image repository, pinned by the digest
the release workflow writes to Chart.yaml.
*/}}
{{- define "kritika.runnerImage" -}}
{{- $image := printf "%s:%s-tools" .Values.image.repository .Chart.AppVersion -}}
{{- with index .Chart.Annotations "kritika.home-operations.com/runner-image-digest" -}}
{{- $image = printf "%s@%s" $image . -}}
{{- end -}}
{{- $image -}}
{{- end }}

{{/*
runner.jobTemplate, rendered through tpl.
*/}}
{{- define "kritika.runnerJobTemplate" -}}
{{- tpl (toYaml .Values.runner.jobTemplate) . -}}
{{- end }}

{{/*
runner.podTemplate, rendered through tpl, the runner container given
kritika.runnerImage when it names no image.
*/}}
{{- define "kritika.runnerPodTemplate" -}}
{{- $template := tpl (toYaml .Values.runner.podTemplate) . | fromYaml -}}
{{- range (dig "spec" "containers" list $template) -}}
{{- if and (kindIs "map" .) (eq (get . "name") "runner") (not (get . "image")) -}}
{{- $_ := set . "image" (include "kritika.runnerImage" $) -}}
{{- end -}}
{{- end -}}
{{- toYaml $template -}}
{{- end }}

{{/*
ConfigMap the file is read from.
*/}}
{{- define "kritika.configMapName" -}}
{{- if .Values.existingConfigMap -}}
{{- tpl .Values.existingConfigMap $ -}}
{{- else -}}
{{- include "kritika.fullname" . -}}
{{- end -}}
{{- end }}

{{/*
In-cluster URL runner Jobs are handed as HTTPS_PROXY and their model endpoint.
*/}}
{{- define "kritika.gatewayURL" -}}
{{- printf "http://%s-gateway.%s.svc.cluster.local:%d" (include "kritika.fullname" .) .Release.Namespace (int .Values.service.gatewayPort) -}}
{{- end }}

{{/* Where the gateway's cache emptyDir is mounted, when gatewayCache is enabled. */}}
{{- define "kritika.gatewayCacheDir" -}}
/var/cache/kritika
{{- end }}

{{/*
The application file, with deployment settings derived from chart values.
*/}}
{{- define "kritika.configuration" -}}
{{- if and .Values.existingConfigMap .Values.config -}}
{{- fail "config and existingConfigMap are mutually exclusive" -}}
{{- end -}}
{{- $config := tpl (toYaml (.Values.config | default dict)) . | fromYaml -}}
{{- if hasKey $config "Error" -}}
{{- fail (printf "config must render valid YAML: %s" (get $config "Error")) -}}
{{- end -}}
{{- $runtime := include "kritika.runtimeConfig" . | fromYaml -}}
{{- range $key, $_ := $config -}}
{{- if hasKey $runtime $key -}}
{{- fail (printf "config.%s: the chart derives this setting from its deployment values" $key) -}}
{{- end -}}
{{- end -}}
{{- toYaml (mergeOverwrite $config $runtime) -}}
{{- end -}}

{{- define "kritika.runtimeConfig" -}}
addr: {{ printf ":%d" (int .Values.service.port) | quote }}
metricsAddr: {{ printf ":%d" (int .Values.service.metricsPort) | quote }}
gatewayAddr: {{ printf ":%d" (int .Values.service.gatewayPort) | quote }}
gatewayUrl: {{ include "kritika.gatewayURL" . | quote }}
{{- if .Values.gatewayCache.enabled }}
{{ include "kritika.gatewayCacheSettings" . }}
{{- end }}
databaseAppRole: {{ .Values.database.app.role | quote }}
databaseRunnerRole: {{ .Values.database.runner.role | quote }}
executor: kubernetes
runner:
  serviceAccount: {{ include "kritika.runnerServiceAccountName" . | quote }}
  databaseSecret: {{ tpl .Values.database.runner.existingSecret . | quote }}
  databaseSecretKey: {{ .Values.database.runner.key | quote }}
  jobTemplate: {{ include "kritika.runnerJobTemplate" . | nindent 4 }}
  podTemplate: {{ include "kritika.runnerPodTemplate" . | nindent 4 }}
{{- end -}}

{{/*
The variables the chart derives from its values: the kritika serve container's
environment before `env`. Rendered as a list so the deployment can also refuse
an `env` key that would duplicate one of them.
*/}}
{{- define "kritika.serveEnv" -}}
- name: KRITIKA_CONFIG_FILE
  value: /etc/kritika/config.yaml
# The pod's own name and UID, which its Kubernetes Events name.
- name: KRITIKA_POD_NAME
  valueFrom:
    fieldRef:
      fieldPath: metadata.name
- name: KRITIKA_POD_UID
  valueFrom:
    fieldRef:
      fieldPath: metadata.uid
{{- if .Values.existingConfigMap }}
- name: KRITIKA_ADDR
  value: {{ printf ":%d" (int .Values.service.port) | quote }}
- name: KRITIKA_METRICS_ADDR
  value: {{ printf ":%d" (int .Values.service.metricsPort) | quote }}
- name: KRITIKA_GATEWAY_ADDR
  value: {{ printf ":%d" (int .Values.service.gatewayPort) | quote }}
- name: KRITIKA_GATEWAY_URL
  value: {{ include "kritika.gatewayURL" . | quote }}
{{- if .Values.gatewayCache.enabled }}
{{- range $key, $value := (include "kritika.gatewayCacheSettings" . | fromYaml) }}
- name: {{ printf "KRITIKA_%s" ($key | snakecase | upper) }}
  value: {{ $value | toString | quote }}
{{- end }}
{{- end }}
- name: KRITIKA_DATABASE_APP_ROLE
  value: {{ .Values.database.app.role | quote }}
- name: KRITIKA_DATABASE_RUNNER_ROLE
  value: {{ .Values.database.runner.role | quote }}
{{- end }}
{{- if and .Values.gatewayCache.enabled (eq .Values.gatewayCache.storage.type "s3") .Values.gatewayCache.storage.existingSecret }}
- name: KRITIKA_GATEWAY_CACHE_S3_ACCESS_KEY
  valueFrom:
    secretKeyRef:
      name: {{ tpl .Values.gatewayCache.storage.existingSecret . | quote }}
      key: AWS_ACCESS_KEY_ID
- name: KRITIKA_GATEWAY_CACHE_S3_SECRET_KEY
  valueFrom:
    secretKeyRef:
      name: {{ tpl .Values.gatewayCache.storage.existingSecret . | quote }}
      key: AWS_SECRET_ACCESS_KEY
{{- end }}
- name: KRITIKA_DATABASE_URL
  valueFrom:
    secretKeyRef:
      name: {{ tpl .Values.database.app.existingSecret . | quote }}
      key: {{ .Values.database.app.key | quote }}
- name: KRITIKA_DATABASE_OWNER_URL
  valueFrom:
    secretKeyRef:
      name: {{ tpl .Values.database.owner.existingSecret . | quote }}
      key: {{ .Values.database.owner.key | quote }}
{{- if .Values.existingConfigMap }}
- name: KRITIKA_EXECUTOR
  value: kubernetes
- name: KRITIKA_RUNNER_SERVICE_ACCOUNT
  value: {{ include "kritika.runnerServiceAccountName" . | quote }}
- name: KRITIKA_RUNNER_DATABASE_SECRET
  value: {{ tpl .Values.database.runner.existingSecret . | quote }}
- name: KRITIKA_RUNNER_DATABASE_SECRET_KEY
  value: {{ .Values.database.runner.key | quote }}
- name: KRITIKA_RUNNER_JOB_TEMPLATE
  value: {{ include "kritika.runnerJobTemplate" . | fromYaml | toJson | quote }}
- name: KRITIKA_RUNNER_POD_TEMPLATE
  value: {{ include "kritika.runnerPodTemplate" . | fromYaml | toJson | quote }}
{{- end }}
{{- end }}

{{/* Environment names corresponding to operational settings present in the file. */}}
{{- define "kritika.configEnv" -}}
{{- $names := dict -}}
{{- range $key := list "addr" "metricsAddr" "gatewayAddr" "gatewayUrl" "gatewayTokenTtl" "gatewayCacheDir" "gatewayCacheMaxSize" "gatewayCacheMaxEntrySize" "gatewayCacheStorage" "gatewayCacheS3Endpoint" "gatewayCacheS3Region" "gatewayCacheS3Bucket" "gatewayCacheS3Prefix" "gatewayCacheS3ForcePathStyle" "webUrl" "databaseAppRole" "databaseRunnerRole" "leaderRetryInterval" "reviewWorkers" "indexWorkers" "threadWorkers" "executor" "logLevel" "logFormat" "pollInterval" "pollLookback" "onboardWindow" "indexGrace" "transcriptRetention" "diffRetention" -}}
{{- if and (hasKey $ $key) (ne (get $ $key) nil) -}}
{{- $_ := set $names (printf "KRITIKA_%s" ($key | snakecase | upper)) $key -}}
{{- end -}}
{{- end -}}
{{- range $key, $value := (get . "runner" | default dict) -}}
{{- if ne $value nil -}}
{{- $_ := set $names (printf "KRITIKA_RUNNER_%s" ($key | snakecase | upper)) (printf "runner.%s" $key) -}}
{{- end -}}
{{- end -}}
{{- toYaml $names -}}
{{- end -}}
