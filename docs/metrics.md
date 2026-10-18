# Metrics

For a basic monitoring setup, enable the chart's ServiceMonitor and use its
[alerts](#alerts) and [Grafana dashboards](#grafana-dashboard).

```yaml
monitoring:
  serviceMonitor:
    enabled: true
```

This requires the Prometheus Operator. For a failed or stuck review, the
[dashboard Queue](dashboard.md#queue) is usually the quickest place to start.
The tables below are the metric reference for custom queries and alerts.

The management port serves `/metrics` alongside `/healthz` and `/readyz`.
Beyond the Go runtime and process collectors and
`promhttp_metric_handler_errors_total`, every series is prefixed `kritika_`,
and none is labelled by a reviewed pull request or commit. A scrape that
fails is logged at error level and counted in
`promhttp_metric_handler_errors_total` by cause: gathering or encoding. The chart's
`monitoring.serviceMonitor.enabled` creates a Prometheus Operator
`ServiceMonitor` for it.

| Series                                  | Labels                         | What it counts                                                                                                                                                                                                        |
| --------------------------------------- | ------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `kritika_webhooks_total`                | connection, outcome            | deliveries: enqueued, recorded, skipped, ignored, ping, unsigned (the App has no webhook secret), unauthorized, unparsable, too_large, unreadable, unknown_connection (with no connection), undeclared_account, error |
| `kritika_polls_total`                   | connection, outcome            | backstop polls: ok, error                                                                                                                                                                                             |
| `kritika_polled_pull_requests_total`    | connection                     | open pull requests the backstop polls handed to ingest                                                                                                                                                                |
| `kritika_reviews_total`                 | account, status                | reviews by terminal status                                                                                                                                                                                            |
| `kritika_review_duration_seconds`       | account                        | a review job's wall time, from pickup to terminal status                                                                                                                                                              |
| `kritika_findings_total`                | account, severity, category    | findings posted                                                                                                                                                                                                       |
| `kritika_confidence_scores_total`       | account, score, risk           | reviews the confidence model scored, by score (0 to 5) and risk: low, medium, high, critical                                                                                                                          |
| `kritika_followups_total`               | account, outcome               | mentions handled: answered, limited, ignored, failed                                                                                                                                                                  |
| `kritika_threads_total`                 | account, outcome               | finding threads a person resolved or unresolved: dismissed, addressed, restored, ignored, failed                                                                                                                      |
| `kritika_index_runs_total`              | account, mode, status          | index runs                                                                                                                                                                                                            |
| `kritika_index_chunks_total`            | account                        | chunks embedded into the index                                                                                                                                                                                        |
| `kritika_context_chunks_total`          | account, stage                 | context chunks a review's prompt was given, by stage: overlay, definition, caller, similar (the index)                                                                                                                |
| `kritika_runner_runs_total`             | account, kind, outcome         | runner Jobs: success, failed, deadline                                                                                                                                                                                |
| `kritika_runner_duration_seconds`       | kind                           | a runner Job's time from start to finish                                                                                                                                                                              |
| `kritika_lease_wait_seconds`            | account, model                 | time waiting for a model concurrency slot                                                                                                                                                                             |
| `kritika_review_snoozes_total`          | account, model                 | reviews put back on the queue because every model slot was held                                                                                                                                                       |
| `kritika_jobs_rescued_total`            | kind, state                    | jobs a replica died working, handed back to the queue by the leader: retryable, discarded (no attempts left), cancelled (a cancel was asked)                                                                          |
| `kritika_model_calls_total`             | account, model, role, outcome  | model calls: ok, error; `model` is the one that answered, so OpenRouter's fallback shows under its own                                                                                                                |
| `kritika_model_tokens_total`            | account, model, role, direction | tokens: input, cached, output                                                                                                                                                                                        |
| `kritika_model_cost_usd_total`          | account, model, role           | provider-reported cost                                                                                                                                                                                                |
| `kritika_model_unpriced_calls_total`    | account, model, role           | calls with no reported cost and no configured price, which `kritika_model_cost_usd_total` leaves out                                                                                                                  |
| `kritika_forge_rate_limits_total`       | connection, outcome            | responses GitHub refused for a rate limit: waited (the request waited the limit out and was sent again) or refused (the wait was over a minute, or the request could not be resent)                                   |
| `kritika_egress_requests_total`         | kind, outcome                  | requests runner pods made through the gateway: connect or http; allowed, refused, error                                                                                                                               |
| `kritika_transcript_writes_total`       | kind, outcome                  | model calls recorded for the transcript view: agent_step (reviews and follow-ups), confidence, or merge (a split review's summary); ok, error |
| `kritika_db_pool_connections`           | pool, state                    | connections the app and owner pools hold, by state: idle, acquired, constructing                                                                                                                                      |
| `kritika_db_pool_max_connections`       | pool                           | each pool's ceiling; a pool at its ceiling queues jobs and requests                                                                                                                                                   |
| `kritika_db_pool_acquires_total`, `kritika_db_pool_empty_acquires_total`, `kritika_db_pool_acquire_seconds_total` | pool | connections taken from a pool, how many had to wait for one to free, and the time spent waiting                                                                    |
| `kritika_config_drift`                  |                                | 1 while this replica's file differs from the applied one                                                                                                                                                              |
| `kritika_config_error`                  |                                | 1 while the leader's latest attempt to apply the configuration to the store was refused; the last applied configuration stays live meanwhile                                                                          |
| `kritika_leader`                        |                                | 1 while this replica holds the leader lock                                                                                                                                                                            |
| `kritika_build_info`                    | version, revision              | always 1, labelled with the running build's version and the commit it was built from                                                                                                                                  |

## Queue and vector-search measurements

| Series | Labels | Meaning |
| ------ | ------ | ------- |
| `kritika_queue_runnable_jobs` | queue | Due available, retryable and scheduled jobs in the shared database |
| `kritika_queue_oldest_runnable_seconds` | queue | Time since the oldest runnable job's `scheduled_at`; zero when empty |
| `kritika_queue_stats_up` | | Whether this replica read the queue snapshot successfully |
| `kritika_queue_wait_seconds` | queue | Histogram from `scheduled_at` to worker start, recorded for every pickup |
| `kritika_vector_search_seconds` | account, outcome | Histogram of database search transaction time, outcome `ok`, `error` or `canceled` |

Each replica reads the same queue state on scrape with a two-second timeout.
Use `max by (namespace, job, queue)` for backlog and oldest age, never a sum
across replicas. Keep the deployment's cluster label as well when querying
multiple clusters. A failed read emits `kritika_queue_stats_up = 0` and omits
the queue gauges; it does not report an empty queue or retain stale values.
Known queues emit zero when empty. Queue labels are limited to `review`,
`followup`, `thread` and `index`. Thread jobs already queued on `followup`
remain attributed to that queue until they finish.

Runnable work includes overdue retries and scheduled jobs waiting for River's
scheduler to promote them. Future work, pending jobs, running jobs and terminal
jobs are excluded. Paused review/followup/thread queues still have overdue work and can
alert. Oldest age measures time since eligibility, not time since creation. Queue
wait also excludes intentional retry backoff, scheduling and snooze delays, and records
another sample on each pickup. Negative waits from clock skew are clamped to
zero. Recording at worker entry makes delays visible before long jobs finish.

Histograms record different work on each replica, so sum their bucket rates
before computing quantiles:

```promql
histogram_quantile(0.95,
  sum by (namespace, job, queue, le) (rate(kritika_queue_wait_seconds_bucket[5m]))
)
```

Vector timing begins after embedding, lease release and usage recording. It
includes pool acquisition, account transaction setup, all vectors in the
request, result decoding and commit. It excludes embedding-provider latency,
model-slot waits and result filtering. Requests without a compatible index or
with an embedding failure produce no vector-search sample. The histogram's
`_count` provides search volume and database error counts; use the existing
model-call counters for embedding failures. No repository, query text, job ID
or generation becomes a metric label.

Client cancellations and expired request contexts during the database phase use
`outcome="canceled"`, excluded from both the error-ratio numerator and denominator.
All outcome series are initialized at zero for configured accounts before workers
start. Prometheus can then count a first error after a zero sample has been scraped;
events before the first scrape still cannot be reconstructed by `increase()`.

The delay alert uses the latest successful snapshot within five minutes to bridge
short read failures, while a fresh empty snapshot clears it. Repeated failures are
also detected by the snapshot-success ratio. Queue snapshots use the application
pool and its least-privilege role; acquisition time counts against the two-second
budget. Pool saturation is a legitimate snapshot failure. Inspect the existing
pool-wait metrics alongside queue health when diagnosing it.

## Download cache

The gateway exports cache telemetry per replica. Prometheus aggregates the
replicas; the admin UI's Download cache card only reports the responding
replica's configuration, without probing storage or claiming it is healthy.

| Metric | Labels | Meaning |
| --- | --- | --- |
| `kritika_cache_requests_total` | outcome | One decision per admitted HTTP request while caching is enabled: `local_hit`, `s3_hit`, `miss`, or `bypass` |
| `kritika_cache_bypasses_total` | reason | Why a request was forwarded without the cache |
| `kritika_cache_operation_seconds` | operation, outcome | Duration histogram for `origin_fetch`, `s3_lookup`, `s3_read`, and `s3_upload` |
| `kritika_cache_transfer_bytes_total` | operation | Actual body bytes consumed by cache fills, S3 reads/uploads, or accepted by response writers (`response_local`, `response_s3`) |
| `kritika_cache_responses_total` | source, outcome | Responses after serving, by `local`/`s3` source and `ok`/`error`/`canceled` result |
| `kritika_cache_active_fills` | | Current origin fills, excluding background S3 uploads |
| `kritika_cache_local_bytes` | | Retained files at the latest directory scan |
| `kritika_cache_local_limit_bytes` | | Retained-entry budget, not a hard disk limit |
| `kritika_cache_evictions_total` | | Local entries evicted to meet the budget |
| `kritika_cache_upload_skips_total` | reason | Publications skipped for `queue_full`, `circuit_open`, or `entry_unavailable` |

Bypass reasons are bounded: `ineligible`, `retry`, `fill_error`,
`entry_unavailable`, `canceled`, `fill_busy`, `fill_capacity`, `too_large`,
`private`, `encoded`, and `unknown_length`. URLs, object keys, run IDs and
arbitrary errors are not metric labels. CONNECT tunnels are excluded.

A miss is a request served after waiting for a fill, including requests sharing
one in-process fill. Failed fills instead report a bypass. The hit ratio is
hits divided by hits plus misses; inspect bypasses alongside it. Operation
outcomes are `ok`, `error`, and `canceled`, plus `miss` for an absent, expired or
invalid S3 entry, or one replaced or removed while it was being read, `skipped`
for an origin response that cannot be stored, and
`not_modified` for an origin that confirmed an expired entry unchanged, which is
then kept without a download.
Cache availability does not affect readiness. A failed or circuit-open S3
lookup is a miss that fills locally, not a bypass. Circuit-open lookups and
skipped uploads do not contribute to S3 operation counts, so they cannot dilute
the storage-error rate. Read and upload circuits recover independently; uploads run
in the background and their duration is separate from origin fill time.

Transfer counters include partial operations. Origin bytes cover cache fill
attempts, not all gateway forwarding or estimated bandwidth savings. Upload
bytes count the HTTP request body, excluding the SDK's checksum pass. Response
bytes count writes accepted by the HTTP writer, including multipart framing;
HEAD and 304 responses contribute zero. A hit does not prove delivery: check
response results for stream failures and client cancellations. S3 read duration
includes time waiting for the downstream reader.

Local usage excludes fills in progress and evicted files still held open, and
is updated at startup and eviction scans. Compare it with the entry budget to
understand eviction pressure; use pod filesystem metrics for actual disk usage.
Bucket usage and lifecycle retention belong in the storage provider's monitoring.

At debug level, `egress cache download` records a completed serving attempt with
`cache_key`, `source`, `result`, HTTP status, method, bytes and elapsed time.
Storage failures produce `egress cache storage error` at warning level, limited
to one warning per minute per replica. Every failed operation still appears in
metrics. The runner's `gateway_cache` install field indicates the route used,
not proof of a cache hit; an uncached retry has its own warning.

## Most used tools

The admin UI's **Most used tools** graph ranks the top ten command names over
7, 30 or 90 days, across currently served accounts. Each started process counts,
including nonzero exits, timeouts and retry runs. Rejected commands, failed
installations and processes that fail to start do not count. Versions and
providers share a command name; these are executions, not download counts.

Counts are stored in `agent_runs.command_counts` alongside the saved result,
so they survive restarts and aggregate across replicas without Prometheus labels
for arbitrary command names. Split review parts share one counter. The period
uses the result's save time; active runs and runs lost before saving their result
are not included. Runs from older runners have no counts and are shown as
excluded, rather than estimated from the distinct commands they used.

## Alerts

The chart's `monitoring.prometheusRule.enabled` creates a Prometheus Operator
`PrometheusRule` with the alerts below, grouped by the metrics Service's
`namespace` and `job`. Each one's wait is a value
(`monitoring.prometheusRule.*For`); `additionalRuleLabels` adds a routing
label to every rule, and `additionalRuleAnnotations` an annotation.

| Alert                    | Fires when                                                           | Severity | Wait |
| ------------------------ | -------------------------------------------------------------------- | -------- | ---- |
| `KritikaNoLeader`        | `sum(kritika_leader) == 0`: no replica holds the leader lock          | critical | 5m   |
| `KritikaMultipleLeaders` | `sum(kritika_leader) > 1`: more than one replica reports holding it   | critical | 2m   |
| `KritikaConfigError`     | `kritika_config_error == 1`: the leader could not apply the file      | warning  | 5m   |
| `KritikaConfigDrift`     | `kritika_config_drift == 1`: a replica's file differs from the applied one | warning | 15m |
| `KritikaQueueDelayed` | Oldest runnable review/followup/thread job exceeds 600 seconds, using the last successful sample within 5m and max across replicas | warning | 15m |
| `KritikaQueueStatsUnavailable` | Every replica succeeds on fewer than 75% of snapshot reads over 5m | warning | 5m |
| `KritikaCacheStorageErrors` | More than 10% S3 operation errors, with at least 10 non-canceled operations in 15m; cold misses are not errors | warning | 10m |
| `KritikaVectorSearchErrors` | More than 10% database search errors, with at least 10 non-canceled requests in 15m | warning | 10m |

Without a leader nothing migrates, applies configuration, polls or rescues
the jobs a dead replica left; a standby takes the lock within one leader
retry interval once its owner connection reaches the database, and Postgres
drops a dead leader's session, and with it the lock, within about a minute.
Two leaders can show for one retry interval through a database failover,
while the old leader's lock connection has not yet failed; longer than that
means the replicas are not on one database. Drift is expected for the
minute or two a rollout takes, until the new pod leads and applies its
file.

Queue age and alert waits are configurable under `monitoring.prometheusRule`.
The defaults are starting thresholds, not measured production SLOs; tune queue
age for normal review traffic. Index work remains visible on the dashboard but
is excluded from the delay alert because onboarding deliberately waits behind
higher-priority updates. Vector latency is graphed without an alert threshold
until a representative production baseline is available.

## Kubernetes Events

kritika records Events that `kubectl describe` and `kubectl events` show and
an event collector gathers: on a runner Job, why kritika ended its run, and
on a kritika serve pod, what its leader did.

| On                  | Reason          | Type    | When                                                                                                         |
| ------------------- | --------------- | ------- | ------------------------------------------------------------------------------------------------------------ |
| a runner Job        | `Superseded`    | Normal  | a newer head of the pull request superseded the run; the note names it                                       |
| a runner Job        | `Replaced`      | Normal  | a newer review of the pull request replaced the run                                                          |
| a runner Job        | `Cancelled`     | Normal  | the run was cancelled from the dashboard                                                                     |
| a runner Job        | `HeartbeatLost` | Warning | the runner's heartbeat went stale, so kritika ended the run                                                  |
| a runner Job        | `Disrupted`     | Warning | Kubernetes stopped its pod, by a node drain, a preemption or an eviction; the note says how                  |
| a runner Job        | `NeverStarted`  | Warning | its pod could not start, as for an image that cannot be pulled or a pod no node takes, or was never created, as when the namespace refuses it on admission; the note says why |
| a runner Job        | `Interrupted`   | Normal  | kritika serve stopped, and the job runs again                                                                |
| a runner Job        | `Interrupted`   | Warning | the job's heartbeat stopped landing, so kritika handed it back to its queue                                  |
| a runner Job        | `TimedOut`      | Warning | the job ran out of time                                                                                      |
| a runner Job        | `StatusUnreadable` | Warning | kritika lost sight of the Job's status, as when someone else deleted it, so it deleted the Job           |
| a kritika serve pod | `LeaderElected` | Normal  | the replica took the lead                                                                                    |
| a kritika serve pod | `ConfigApplied` | Normal  | the leader applied the configuration, named by its hash                                                      |
| a kritika serve pod | `ConfigRefused` | Warning | the store refused the configuration, and the last applied one stays live, as `kritika_config_error` says too |

A run that ended on its own, finished or failed, gets none: its Job's own
Events, from the Job controller, tell. kritika deletes the Job of a run it
ends, so `kubectl events --for job/<name>` finds those Events once
`kubectl describe` no longer can. A pod's Events need its name and UID,
which the chart passes as `KRITIKA_POD_NAME` and `KRITIKA_POD_UID`. The
chart's Role lets kritika create Events in its namespace; one it cannot
record is logged. Kubernetes keeps Events for an hour unless the API
server's `--event-ttl` says otherwise.

## Grafana dashboard

The chart's `monitoring.dashboards.enabled` renders a Grafana dashboard of
most of the series above (`charts/kritika/dashboards/kritika.json`) as a ConfigMap
labelled `grafana_dashboard: "1"` for the kube-prometheus-stack sidecar, or,
with `monitoring.dashboards.grafanaOperator.enabled`, as a `GrafanaDashboard`
for grafana-operator that imports the ConfigMap into the Grafana instance
`grafanaOperator.matchLabels` selects. It is filtered by the metrics
Service's `namespace` and `job`, then by `account` and `model`, and its rows
follow a review's path: health (leaders, configuration, drift, rescued
jobs), intake (webhooks, backstop polls, rate limits), reviews (status,
duration, findings, follow-ups, the wait for a model slot), models (calls,
tokens, cost per hour and over the time range), runner Jobs, the gateway's
egress and the index, database pools, and queue delays and vector searches.
Queue panels span all accounts; vector panels follow the account filter.


## Reproducing the vector-search baseline

Run on a machine with Docker through the integration task, which creates and
removes a disposable database. No provider credentials or model calls are needed:

```sh
mise run test-integration -- -run '^$' -bench BenchmarkSimilarChunks -benchtime=20x -v
```

`BenchmarkSimilarChunks` exercises the `SimilarChunks` SQL with
`vchordrq.prefilter = on`, 1,024 and 1,536 dimensions, and 1,000 or 10,000 chunks
in the target generation. A second repository in the same account and a second
account each contribute the same number of vectors: 3,000 or 30,000 total rows.
Each case excludes either no paths or 10% of target paths. Data comes from a
fixed-seed, 16-cluster synthetic distribution with one chunk per path.

All retrieval runs as `kritika_app` with RLS enabled, the transaction's account
setting, the generation filter and the actual path exclusions. A foreign-account
lookup must return no results. Recall@4 compares the application's results with
an exact table scan for 20 deterministic queries. The harness disables index
and bitmap scans only for that reference, verifies its sequential-scan plan,
and restores them before further application queries. It compares chunk paths
directly because fixture paths have a distinct prefix for each generation; this
also keeps the ground truth on the same full-result SQL as the application rather
than a separate row-identifier query. VectorChord's
[recall evaluator](https://docs.vectorchord.ai/vectorchord/usage/measure-recall.html)
is another option for production query samples.

The verbose output captures `EXPLAIN (ANALYZE, BUFFERS)` for both plans, plus the
PostgreSQL and VectorChord versions. Setup, recall evaluation and plan collection
are excluded from the reported `ns/op`. Timing includes the account transaction,
vector serialization, search and result decoding for one query vector. It is a
warm microbenchmark with the integration task's race detector enabled, not a
cold-start or concurrent-traffic test.

### Local baseline, 2026-10-12

Measured on Linux amd64, AMD EPYC 7502P, using the task's
`ghcr.io/tensorchord/vchord-postgres:pg18-v1.1.1` container (PostgreSQL 18.3,
VectorChord 1.1.1). Each mean below covers 20 timed requests. EXPLAIN times are
individual diagnostic queries, not averages or percentiles.

| Dimensions | Target chunks | Excluded paths | Mean request (ms) | Recall@4 | VectorChord EXPLAIN (ms) | Exact EXPLAIN (ms) |
| ---------- | ------------- | -------------- | ----------------- | -------- | ----------------------- | ------------------ |
| 1,024 | 1,000 | 0 | 2.27 | 1.00 | 0.44 | 8.83 |
| 1,024 | 1,000 | 100 | 2.48 | 1.00 | 0.41 | 8.71 |
| 1,024 | 10,000 | 0 | 6.75 | 1.00 | 5.05 | 98.42 |
| 1,024 | 10,000 | 1,000 | 17.72 | 1.00 | 4.82 | 92.23 |
| 1,536 | 1,000 | 0 | 2.88 | 1.00 | 0.52 | 10.43 |
| 1,536 | 1,000 | 100 | 3.03 | 1.00 | 0.54 | 9.40 |
| 1,536 | 10,000 | 0 | 9.43 | 1.00 | 6.13 | 144.10 |
| 1,536 | 10,000 | 1,000 | 19.30 | 1.00 | 5.89 | 97.31 |

Every application plan chose `index_chunks_embedding_idx`; every exact plan
used a sequential scan and top-N sort. The filters included both the generation
and the RLS account predicate. Plans reported shared-buffer hits for these warm
queries; the full buffer counts are emitted by the command above.

The current index performed well on this fixture. The longer requests with
large exclusion lists include their client-side encoding and transfer, so do not
attribute all of that difference to the index. These small, synthetic samples
are not evidence of production recall or an SLO, and do not justify changing
partitioning, search parameters, prewarming or worker concurrency. Use the
runtime histograms and repeat with representative embeddings and traffic before
tuning those settings. No production database was measured for this baseline.
