{{- /* Per-rule labels: the rule's severity, then additionalRuleLabels
(severity wins). */}}
{{- define "kritika.alertRuleLabels" -}}
{{- $labels := dict "severity" .severity -}}
{{- with .root.Values.monitoring.prometheusRule.additionalRuleLabels -}}
{{- $labels = merge $labels . -}}
{{- end -}}
{{- toYaml $labels -}}
{{- end -}}
{{- if .Values.monitoring.prometheusRule.enabled }}
apiVersion: monitoring.coreos.com/v1
kind: PrometheusRule
metadata:
  name: {{ include "kritika.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "kritika.labels" . | nindent 4 }}
    {{- with .Values.monitoring.prometheusRule.labels }}
    {{- tpl (toYaml .) $ | nindent 4 }}
    {{- end }}
  {{- with .Values.monitoring.prometheusRule.annotations }}
  annotations:
    {{- tpl (toYaml .) $ | nindent 4 }}
  {{- end }}
spec:
  groups:
    # Every replica exports kritika_leader; the one holding the lock exports 1.
    # job is the metrics Service, so two releases in a namespace stay apart.
    - name: kritika.leader
      rules:
        - alert: KritikaNoLeader
          expr: sum by (namespace, job) (kritika_leader) == 0
          for: {{ .Values.monitoring.prometheusRule.noLeaderFor }}
          labels:
            {{- include "kritika.alertRuleLabels" (dict "root" $ "severity" "critical") | nindent 12 }}
          annotations:
            summary: >-
              kritika in {{ "{{" }} $labels.namespace {{ "}}" }} has no leader
            description: >-
              No replica has held the leader lock for {{ .Values.monitoring.prometheusRule.noLeaderFor }}:
              migrations, configuration, the backstop poll and the rescue of
              jobs a dead replica left are not running. A standby takes the
              lock within its retry interval once the owner connection
              reaches the database, so check the owner DSN, the database and
              the replicas' logs for "leader".
            {{- with .Values.monitoring.prometheusRule.additionalRuleAnnotations }}
            {{- toYaml . | nindent 12 }}
            {{- end }}
        - alert: KritikaMultipleLeaders
          expr: sum by (namespace, job) (kritika_leader) > 1
          for: {{ .Values.monitoring.prometheusRule.multipleLeadersFor }}
          labels:
            {{- include "kritika.alertRuleLabels" (dict "root" $ "severity" "critical") | nindent 12 }}
          annotations:
            summary: >-
              kritika in {{ "{{" }} $labels.namespace {{ "}}" }} has
              {{ "{{" }} $value {{ "}}" }} leaders
            description: >-
              More than one replica reports holding the leader lock, so two
              replicas run the leader duties at once. A database failover
              can cause this for one leader retry interval while the old
              leader's lock connection has not yet failed; longer than that
              means the replicas are not on the same database, as through a
              transaction-mode pooler or two primaries.
            {{- with .Values.monitoring.prometheusRule.additionalRuleAnnotations }}
            {{- toYaml . | nindent 12 }}
            {{- end }}
    - name: kritika.config
      rules:
        - alert: KritikaConfigError
          expr: max by (namespace, job) (kritika_config_error) == 1
          for: {{ .Values.monitoring.prometheusRule.configErrorFor }}
          labels:
            {{- include "kritika.alertRuleLabels" (dict "root" $ "severity" "warning") | nindent 12 }}
          annotations:
            summary: >-
              kritika in {{ "{{" }} $labels.namespace {{ "}}" }} could not apply its configuration
            description: >-
              The leader's latest attempt to apply the configuration file to
              the store was refused, and the last applied configuration stays
              live meanwhile. The leader's log names the refused section.
            {{- with .Values.monitoring.prometheusRule.additionalRuleAnnotations }}
            {{- toYaml . | nindent 12 }}
            {{- end }}
        - alert: KritikaConfigDrift
          expr: max by (namespace, job) (kritika_config_drift) == 1
          for: {{ .Values.monitoring.prometheusRule.configDriftFor }}
          labels:
            {{- include "kritika.alertRuleLabels" (dict "root" $ "severity" "warning") | nindent 12 }}
          annotations:
            summary: >-
              A kritika replica in {{ "{{" }} $labels.namespace {{ "}}" }} runs a configuration the leader has not applied
            description: >-
              A replica's configuration file differs from the one the leader
              applied for longer than a rollout takes. A rollout that stalls
              leaves the new pod drifting until the old leader goes; otherwise
              the replicas mount different files, as when an existing
              ConfigMap changed under a running leader.
            {{- with .Values.monitoring.prometheusRule.additionalRuleAnnotations }}
            {{- toYaml . | nindent 12 }}
            {{- end }}
    - name: kritika.queue
      rules:
        - alert: KritikaQueueDelayed
          expr: max by (namespace, job, queue) (last_over_time(kritika_queue_oldest_runnable_seconds{queue!="index"}[5m])) > {{ .Values.monitoring.prometheusRule.queueAgeSeconds }}
          for: {{ .Values.monitoring.prometheusRule.queueDelayFor }}
          labels:
            {{- include "kritika.alertRuleLabels" (dict "root" $ "severity" "warning") | nindent 12 }}
          annotations:
            summary: >-
              kritika queue {{ "{{" }} $labels.queue {{ "}}" }} has overdue work
            description: >-
              The oldest runnable job has waited more than {{ .Values.monitoring.prometheusRule.queueAgeSeconds }} seconds.
              Check worker capacity, paused queues, model-slot contention and database health.
              Future scheduled work and the index queue are excluded.
              The latest successful snapshot is retained for up to 5 minutes across read failures.
            {{- with .Values.monitoring.prometheusRule.additionalRuleAnnotations }}
            {{- toYaml . | nindent 12 }}
            {{- end }}
        - alert: KritikaQueueStatsUnavailable
          expr: max by (namespace, job) (avg_over_time(kritika_queue_stats_up[5m])) < 0.75
          for: {{ .Values.monitoring.prometheusRule.queueStatsUnavailableFor }}
          labels:
            {{- include "kritika.alertRuleLabels" (dict "root" $ "severity" "warning") | nindent 12 }}
          annotations:
            summary: kritika queue snapshots are unreliable
            description: >-
              Every scraped replica has succeeded on fewer than 75% of snapshot reads over 5 minutes.
              Check app-pool contention, database connectivity and the queue metrics logs.
            {{- with .Values.monitoring.prometheusRule.additionalRuleAnnotations }}
            {{- toYaml . | nindent 12 }}
            {{- end }}
    - name: kritika.vector
      rules:
        - alert: KritikaVectorSearchErrors
          expr: >-
            (sum by (namespace, job) (increase(kritika_vector_search_seconds_count{outcome="error"}[15m]))
            / sum by (namespace, job) (increase(kritika_vector_search_seconds_count{outcome!="canceled"}[15m])) > 0.1)
            and on (namespace, job)
            (sum by (namespace, job) (increase(kritika_vector_search_seconds_count{outcome!="canceled"}[15m])) >= 10)
          for: {{ .Values.monitoring.prometheusRule.vectorSearchErrorsFor }}
          labels:
            {{- include "kritika.alertRuleLabels" (dict "root" $ "severity" "warning") | nindent 12 }}
          annotations:
            summary: kritika vector searches are failing
            description: >-
              More than 10% of database search requests failed, with at least 10 requests in 15 minutes.
              Embedding-provider errors and client cancellations are excluded. Check the gateway logs, database and index schema.
            {{- with .Values.monitoring.prometheusRule.additionalRuleAnnotations }}
            {{- toYaml . | nindent 12 }}
            {{- end }}
    - name: kritika.cache
      rules:
        - alert: KritikaCacheStorageErrors
          expr: >-
            (sum by (namespace, job) (increase(kritika_cache_operation_seconds_count{operation=~"s3_.*",outcome="error"}[15m]))
            / sum by (namespace, job) (increase(kritika_cache_operation_seconds_count{operation=~"s3_.*",outcome!="canceled"}[15m])) > 0.1)
            and on (namespace, job)
            (sum by (namespace, job) (increase(kritika_cache_operation_seconds_count{operation=~"s3_.*",outcome!="canceled"}[15m])) >= 10)
          for: {{ .Values.monitoring.prometheusRule.cacheStorageErrorsFor }}
          labels:
            {{- include "kritika.alertRuleLabels" (dict "root" $ "severity" "warning") | nindent 12 }}
          annotations:
            summary: kritika download cache storage is failing
            description: >-
              More than 10% of S3 cache operations failed, with at least 10 operations in 15 minutes.
              Cold misses and client cancellations are not errors. Downloads may fall back to origin or need an uncached retry.
              Check S3 availability, credentials and the gateway logs for "egress cache storage error".
            {{- with .Values.monitoring.prometheusRule.additionalRuleAnnotations }}
            {{- toYaml . | nindent 12 }}
            {{- end }}
{{- end }}
