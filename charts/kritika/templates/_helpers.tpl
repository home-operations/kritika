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
Runner image reference, as kritika.image but with the tag defaulting to the
release's -tools image.
*/}}
{{- define "kritika.runnerImage" -}}
{{- $img := .Values.runner.image -}}
{{- if $img.digest -}}
{{- printf "%s@%s" $img.repository $img.digest -}}
{{- else -}}
{{- printf "%s:%s" $img.repository ($img.tag | default (printf "%s-tools" .Chart.AppVersion)) -}}
{{- end -}}
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
{{- if hasKey .Values "configFile" -}}
{{- fail "configFile has moved to config; migrate flat config keys to the application YAML schema" -}}
{{- end -}}
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
gatewayCacheDir: {{ include "kritika.gatewayCacheDir" . | quote }}
gatewayCacheMaxSize: {{ .Values.gatewayCache.maxSize | quote }}
gatewayCacheMaxAge: {{ .Values.gatewayCache.maxAge | quote }}
{{- end }}
databaseAppRole: {{ .Values.database.app.role | quote }}
databaseRunnerRole: {{ .Values.database.runner.role | quote }}
executor: kubernetes
runner:
  image: {{ include "kritika.runnerImage" . | quote }}
  imagePullPolicy: {{ .Values.runner.image.pullPolicy | quote }}
  serviceAccount: {{ include "kritika.runnerServiceAccountName" . | quote }}
  databaseSecret: {{ tpl .Values.database.runner.existingSecret . | quote }}
  databaseSecretKey: {{ .Values.database.runner.key | quote }}
  ttl: {{ tpl (toString .Values.runner.ttl) . | quote }}
  runtimeClass: {{ tpl .Values.runner.runtimeClass . | quote }}
  {{- with .Values.runner.deadline }}
  deadline: {{ . | quote }}
  {{- end }}
  {{- with .Values.runner.resources }}
  resources: {{ toYaml . | nindent 4 }}
  {{- end }}
  {{- with .Values.runner.mise.urlReplacements }}
  miseUrlReplacements: {{ toYaml . | nindent 4 }}
  {{- end }}
{{- end -}}

{{/*
The variables the chart derives from its values: the kritika serve container's
environment before `env`. Rendered as a list so the deployment can also refuse
an `env` key that would duplicate one of them.
*/}}
{{- define "kritika.serveEnv" -}}
- name: KRITIKA_CONFIG_FILE
  value: /etc/kritika/config.yaml
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
- name: KRITIKA_GATEWAY_CACHE_DIR
  value: {{ include "kritika.gatewayCacheDir" . | quote }}
- name: KRITIKA_GATEWAY_CACHE_MAX_SIZE
  value: {{ .Values.gatewayCache.maxSize | quote }}
- name: KRITIKA_GATEWAY_CACHE_MAX_AGE
  value: {{ .Values.gatewayCache.maxAge | quote }}
{{- end }}
- name: KRITIKA_DATABASE_APP_ROLE
  value: {{ .Values.database.app.role | quote }}
- name: KRITIKA_DATABASE_RUNNER_ROLE
  value: {{ .Values.database.runner.role | quote }}
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
- name: KRITIKA_RUNNER_IMAGE
  value: {{ include "kritika.runnerImage" . | quote }}
- name: KRITIKA_RUNNER_IMAGE_PULL_POLICY
  value: {{ .Values.runner.image.pullPolicy | quote }}
- name: KRITIKA_RUNNER_SERVICE_ACCOUNT
  value: {{ include "kritika.runnerServiceAccountName" . | quote }}
- name: KRITIKA_RUNNER_DATABASE_SECRET
  value: {{ tpl .Values.database.runner.existingSecret . | quote }}
- name: KRITIKA_RUNNER_DATABASE_SECRET_KEY
  value: {{ .Values.database.runner.key | quote }}
- name: KRITIKA_RUNNER_RUNTIME_CLASS
  value: {{ tpl .Values.runner.runtimeClass . | quote }}
{{- with .Values.runner.deadline }}
- name: KRITIKA_RUNNER_DEADLINE
  value: {{ . | quote }}
{{- end }}
- name: KRITIKA_RUNNER_TTL
  value: {{ tpl (toString .Values.runner.ttl) . | quote }}
{{- with .Values.runner.resources }}
- name: KRITIKA_RUNNER_RESOURCES
  value: {{ toJson . | quote }}
{{- end }}
{{- with .Values.runner.mise.urlReplacements }}
- name: KRITIKA_RUNNER_MISE_URL_REPLACEMENTS
  value: {{ toJson . | quote }}
{{- end }}
{{- end }}
{{- end }}

{{/* Environment names corresponding to operational settings present in the file. */}}
{{- define "kritika.configEnv" -}}
{{- $names := dict -}}
{{- range $key := list "addr" "metricsAddr" "gatewayAddr" "gatewayUrl" "gatewayTokenTtl" "gatewayCacheDir" "gatewayCacheMaxSize" "gatewayCacheMaxAge" "webUrl" "databaseAppRole" "databaseRunnerRole" "leaderRetryInterval" "reviewWorkers" "indexWorkers" "threadWorkers" "executor" "logLevel" "logFormat" "pollInterval" "pollLookback" "onboardWindow" "indexGrace" "transcriptRetention" "diffRetention" -}}
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
