{{- define "kritika.gatewayCacheSettings" -}}
gatewayCacheDir: {{ include "kritika.gatewayCacheDir" . | quote }}
gatewayCacheStorage: {{ .Values.gatewayCache.storage.type | quote }}
gatewayCacheMaxEntrySize: {{ .Values.gatewayCache.maxEntrySize | quote }}
{{- if eq .Values.gatewayCache.storage.type "s3" }}
gatewayCacheS3Endpoint: {{ tpl .Values.gatewayCache.storage.endpoint . | quote }}
gatewayCacheS3Region: {{ .Values.gatewayCache.storage.region | quote }}
gatewayCacheS3Bucket: {{ tpl .Values.gatewayCache.storage.bucket . | quote }}
gatewayCacheS3Prefix: {{ tpl .Values.gatewayCache.storage.prefix . | quote }}
gatewayCacheS3ForcePathStyle: {{ .Values.gatewayCache.storage.forcePathStyle }}
{{- else }}
gatewayCacheMaxSize: {{ .Values.gatewayCache.storage.maxSize | quote }}
{{- end }}
{{- end }}
