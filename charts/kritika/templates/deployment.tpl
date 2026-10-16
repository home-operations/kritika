{{- if not .Values.database.app.existingSecret -}}
{{- fail "database.app.existingSecret is required: the Secret holding the application role's connection URI" -}}
{{- end -}}
{{- if not .Values.database.owner.existingSecret -}}
{{- fail "database.owner.existingSecret is required: the leader runs migrations with it" -}}
{{- end -}}
{{- if not .Values.database.runner.existingSecret -}}
{{- fail "database.runner.existingSecret is required: runner Jobs connect with it" -}}
{{- end -}}
{{- $configuration := include "kritika.configuration" . -}}
{{- if not .Values.existingConfigMap -}}
{{- $webURL := get ($configuration | fromYaml) "webUrl" | default "" | toString -}}
{{- if not $webURL -}}
{{- fail "config.webUrl is required: the dashboard's public URL, which the webhook listener shares under /hooks" -}}
{{- end -}}
{{- if not (regexMatch "^https?://" $webURL) -}}
{{- fail "config.webUrl must be an http(s) URL" -}}
{{- end -}}
{{- end -}}
{{- if hasKey .Values.runner "tools" -}}
{{- fail "runner.tools has been removed; declare tools in a repository's .kritika/mise.toml instead" -}}
{{- end -}}
{{- if hasKey (.Values.runner.mise | default dict) "config" -}}
{{- fail "runner.mise.config has been removed; declare tools in a repository's .kritika/mise.toml and mirrors in runner.mise.urlReplacements instead" -}}
{{- end -}}
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "kritika.fullname" $ }}
  namespace: {{ $.Release.Namespace }}
  labels:
    {{- include "kritika.labels" $ | nindent 4 }}
    app.kubernetes.io/component: server
  {{- with $.Values.deploymentAnnotations }}
  annotations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
spec:
  replicas: {{ $.Values.replicas }}
  {{- with $.Values.strategy }}
  strategy:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  selector:
    matchLabels:
      {{- include "kritika.selectorLabels" $ | nindent 6 }}
      app.kubernetes.io/component: server
  template:
    metadata:
      labels:
        {{- include "kritika.labels" $ | nindent 8 }}
        app.kubernetes.io/component: server
        {{- with $.Values.podLabels }}
        {{- tpl (toYaml .) $ | nindent 8 }}
        {{- end }}
      {{- $checksum := "" }}
      {{- if not $.Values.existingConfigMap }}
      {{- $checksum = $configuration | sha256sum }}
      {{- end }}
      {{- if or $checksum $.Values.podAnnotations }}
      annotations:
        {{- with $checksum }}
        # kritika reads the file at startup, so a change rolls the pods.
        checksum/config: {{ . }}
        {{- end }}
        {{- with $.Values.podAnnotations }}
        {{- tpl (toYaml .) $ | nindent 8 }}
        {{- end }}
      {{- end }}
    spec:
      # The Service is named after the release; the kubelet's legacy link
      # variables would otherwise inject KRITIKA_PORT and collide with config.
      enableServiceLinks: false
      {{- with $.Values.imagePullSecrets }}
      imagePullSecrets:
        {{- tpl (toYaml .) $ | nindent 8 }}
      {{- end }}
      serviceAccountName: {{ include "kritika.serviceAccountName" $ | quote }}
      automountServiceAccountToken: {{ $.Values.serviceAccount.automount }}
      {{- with $.Values.priorityClassName }}
      priorityClassName: {{ tpl . $ | quote }}
      {{- end }}
      terminationGracePeriodSeconds: {{ $.Values.terminationGracePeriodSeconds }}
      securityContext:
        {{- tpl (toYaml $.Values.podSecurityContext) $ | nindent 8 }}
      containers:
        - name: kritika
          image: {{ include "kritika.image" $ | quote }}
          imagePullPolicy: {{ $.Values.image.pullPolicy }}
          args:
            - serve
          securityContext:
            {{- tpl (toYaml $.Values.securityContext) $ | nindent 12 }}
          {{- $serveEnv := include "kritika.serveEnv" $ | fromYamlArray }}
          {{- $fileEnv := include "kritika.configEnv" ($configuration | fromYaml) | fromYaml }}
          env:
            {{- toYaml $serveEnv | nindent 12 }}
            {{- range $name, $value := $.Values.env }}
            {{- range $serveEnv }}
            {{- if eq .name $name }}
            {{- fail (printf "env.%s: the chart sets this variable from its other values" $name) }}
            {{- end }}
            {{- end }}
            {{- if and (not $.Values.existingConfigMap) (hasKey $fileEnv $name) }}
            {{- fail (printf "env.%s conflicts with config.%s: use one source for each setting" $name (get $fileEnv $name)) }}
            {{- end }}
            - name: {{ $name }}
              {{- if kindIs "map" $value }}
              {{- tpl (toYaml $value) $ | nindent 14 }}
              {{- else }}
              value: {{ tpl (toString $value) $ | quote }}
              {{- end }}
            {{- end }}
          {{- with $.Values.envFrom }}
          envFrom:
            {{- tpl (toYaml .) $ | nindent 12 }}
          {{- end }}
          ports:
            - name: http
              containerPort: {{ $.Values.service.port }}
              protocol: TCP
            - name: metrics
              containerPort: {{ $.Values.service.metricsPort }}
              protocol: TCP
            - name: gateway
              containerPort: {{ $.Values.service.gatewayPort }}
              protocol: TCP

          livenessProbe:
            {{- tpl (toYaml $.Values.livenessProbe) $ | nindent 12 }}
          readinessProbe:
            {{- tpl (toYaml $.Values.readinessProbe) $ | nindent 12 }}
          {{- with $.Values.startupProbe }}
          startupProbe:
            {{- tpl (toYaml .) $ | nindent 12 }}
          {{- end }}
          {{- with $.Values.resources }}
          resources:
            {{- tpl (toYaml .) $ | nindent 12 }}
          {{- end }}
          volumeMounts:
            - name: config
              mountPath: /etc/kritika
              readOnly: true
            {{- if $.Values.gatewayCache.enabled }}
            - name: gateway-cache
              mountPath: {{ include "kritika.gatewayCacheDir" $ }}
            {{- end }}
            {{- with $.Values.volumeMounts }}
            {{- tpl (toYaml .) $ | nindent 12 }}
            {{- end }}
      volumes:
        - name: config
          configMap:
            name: {{ include "kritika.configMapName" $ | quote }}
        {{- if $.Values.gatewayCache.enabled }}
        - name: gateway-cache
          emptyDir: {}
        {{- end }}
        {{- with $.Values.volumes }}
        {{- tpl (toYaml .) $ | nindent 8 }}
        {{- end }}
      {{- with $.Values.nodeSelector }}
      nodeSelector:
        {{- tpl (toYaml .) $ | nindent 8 }}
      {{- end }}
      {{- with $.Values.affinity }}
      affinity:
        {{- tpl (toYaml .) $ | nindent 8 }}
      {{- end }}
      {{- with $.Values.topologySpreadConstraints }}
      topologySpreadConstraints:
        {{- tpl (toYaml .) $ | nindent 8 }}
      {{- end }}
      {{- with $.Values.tolerations }}
      tolerations:
        {{- tpl (toYaml .) $ | nindent 8 }}
      {{- end }}
