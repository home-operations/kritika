---
title: Metrics
---

For a basic monitoring setup, enable the chart's ServiceMonitor and use its
[alerts](#alerts) and [Grafana dashboards](#grafana-dashboard).

```yaml
monitoring:
  serviceMonitor:
    enabled: true
```

This requires the Prometheus Operator. For a failed or stuck review, the
[dashboard Queue](/dashboard/#queue) is usually the quickest place to start.
The tables below are the metric reference for custom queries and alerts.

The management port serves `/metrics` alongside `/healthz` and `/readyz`.
Apart from the Go runtime and process collectors and
`promhttp_metric_handler_errors_total`, every series is prefixed `kritika_`.
None is labelled by a reviewed pull request or commit. A scrape that fails
is logged at error level and counted in
`promhttp_metric_handler_errors_total` by cause: gathering or encoding. The
chart's `monitoring.serviceMonitor.enabled` creates a Prometheus Operator
`ServiceMonitor` for it.

| Series                                                                                                            | Labels                          | What it counts                                                                                                                                                                                                                             |
| ----------------------------------------------------------------------------------------------------------------- | ------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `kritika_webhooks_total`                                                                                          | connection, outcome             | deliveries: enqueued, recorded, skipped, ignored, ping, unsigned (the App has no webhook secret), unauthorized, unparsable, too_large, unreadable, unknown_connection (with no connection), undeclared_account, error                      |
| `kritika_polls_total`                                                                                             | connection, outcome             | backstop polls: ok, error                                                                                                                                                                                                                  |
| `kritika_polled_pull_requests_total`                                                                              | connection                      | open pull requests the backstop polls handed to ingest                                                                                                                                                                                     |
| `kritika_reviews_total`                                                                                           | account, status                 | reviews by terminal status: completed, skipped, capped, superseded, canceled, failed                                                                                                                                                       |
| `kritika_review_duration_seconds`                                                                                 | account                         | a review's wall time, from its first job's pickup to its terminal status, across its stages                                                                                                                                                |
| `kritika_review_stage_duration_seconds`                                                                           | account, stage                  | durations of preparation and publication operations, failed attempts included. Nested stages overlap                                                                                                                                       |
| `kritika_review_slot_wait_seconds`                                                                                | account, model                  | actual elapsed slot-snooze intervals observed at the next pickup, queue delay included                                                                                                                                                     |
| `kritika_findings_total`                                                                                          | account, severity, category     | findings posted                                                                                                                                                                                                                            |
| `kritika_confidence_scores_total`                                                                                 | account, score, risk            | reviews the confidence model scored, by score (0 to 5) and risk: low, medium, high, critical                                                                                                                                               |
| `kritika_confidence_held_total`                                                                                   | account, what                   | confidence answers kritika held to its own rule: `risk` given as no level and taken as critical, `step` raised to a moved version's step floor, `floor` raised to a path's floor ([confidence](/confidence/))                              |
| `kritika_followups_total`                                                                                         | account, outcome                | mentions, and replies in finding threads that do not mention the bot, handled: answered, limited, ignored, failed                                                                                                                          |
| `kritika_threads_total`                                                                                           | account, outcome                | finding threads a person resolved or unresolved: dismissed, addressed, restored, ignored, failed                                                                                                                                           |
| `kritika_index_runs_total`                                                                                        | account, mode, status           | index runs                                                                                                                                                                                                                                 |
| `kritika_index_snoozes_total`                                                                                     | account, reason                 | index jobs put back on the queue to wait without spending an attempt: `forge`, a transient forge failure (5xx, timeout, reset); `build`, a pod on another embedder or index format than the leader applied                                 |
| `kritika_index_chunks_total`                                                                                      | account                         | chunks embedded into the index                                                                                                                                                                                                             |
| `kritika_index_vectors_total`                                                                                     | account, outcome                | vectors of index chunks: `reused` from a chunk of the repository with the same text and embedder, or `embedded`                                                                                                                            |
| `kritika_index_truncated_total`                                                                                   | account                         | full index builds published truncated: stopped at the indexer's limits, leaving out the files past them                                                                                                                                    |
| `kritika_context_chunks_total`                                                                                    | account, stage                  | context chunks a review's prompt was given, by stage: overlay, definition, caller, similar (the index)                                                                                                                                     |
| `kritika_runner_runs_total`                                                                                       | account, kind, outcome          | runner Jobs: success, failed, deadline                                                                                                                                                                                                     |
| `kritika_runner_duration_seconds`                                                                                 | kind                            | a runner Job's time from start to finish                                                                                                                                                                                                   |
| `kritika_lease_wait_seconds`                                                                                      | account, model                  | time waiting for a model concurrency slot                                                                                                                                                                                                  |
| `kritika_review_snoozes_total`                                                                                    | account, model                  | reviews put back on the queue because every model slot was held                                                                                                                                                                            |
| `kritika_jobs_rescued_total`                                                                                      | kind, state                     | jobs a replica died working, handed back to the queue by the leader: retryable, discarded (no attempts left), cancelled (a cancel was asked)                                                                                               |
| `kritika_model_calls_total`                                                                                       | account, model, role, outcome   | model calls: ok, error. `model` is the one that answered, so OpenRouter's fallback shows under its own. `role` on this and the series below is review (a split review's summary merge included), followup, confidence, embedding or rerank |
| `kritika_model_tokens_total`                                                                                      | account, model, role, direction | tokens: input, cached, output                                                                                                                                                                                                              |
| `kritika_model_cost_usd_total`                                                                                    | account, model, role            | provider-reported cost                                                                                                                                                                                                                     |
| `kritika_model_unpriced_calls_total`                                                                              | account, model, role            | calls with no reported cost and no configured price, which `kritika_model_cost_usd_total` leaves out                                                                                                                                       |
| `kritika_forge_rate_limits_total`                                                                                 | connection, outcome             | responses GitHub refused for a rate limit. waited: the request waited the limit out and was sent again. refused: the wait was over a minute, or the request cannot be resent                                                               |
| `kritika_egress_requests_total`                                                                                   | kind, outcome                   | requests runner pods made through the gateway. kind: connect or http. outcome: allowed, refused, error                                                                                                                                     |
| `kritika_transcript_writes_total`                                                                                 | kind, outcome                   | model calls recorded for the transcript view. kind: agent_step (reviews and follow-ups), confidence, or merge (a split review's summary). outcome: ok, error                                                                               |
| `kritika_db_pool_connections`                                                                                     | pool, state                     | connections the app and owner pools hold, by state: idle, acquired, constructing                                                                                                                                                           |
| `kritika_db_pool_max_connections`                                                                                 | pool                            | each pool's ceiling. A pool at its ceiling queues jobs and requests                                                                                                                                                                        |
| `kritika_db_pool_acquires_total`, `kritika_db_pool_empty_acquires_total`, `kritika_db_pool_acquire_seconds_total` | pool                            | connections taken from a pool, how many had to wait for one to free, and the time spent waiting                                                                                                                                            |
| `kritika_config_drift`                                                                                            |                                 | 1 while this replica's file differs from the applied one                                                                                                                                                                                   |
| `kritika_config_error`                                                                                            |                                 | 1 while the leader's latest attempt to apply the configuration to the store was refused. The last applied configuration stays live meanwhile                                                                                               |
| `kritika_leader`                                                                                                  |                                 | 1 while this replica holds the leader lock                                                                                                                                                                                                 |
| `kritika_build_info`                                                                                              | version, revision               | always 1, labelled with the running build's version and the commit it was built from                                                                                                                                                       |

## Queue and vector-search measurements

| Series                                  | Labels                   | Meaning                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| --------------------------------------- | ------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `kritika_queue_runnable_jobs`           | queue                    | Due available, retryable and scheduled jobs in the shared database                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        |
| `kritika_queue_oldest_runnable_seconds` | queue                    | Time since the oldest runnable job's `scheduled_at`. Zero when empty                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| `kritika_queue_stats_up`                |                          | Whether this replica read the queue snapshot successfully                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| `kritika_queue_wait_seconds`            | queue                    | Histogram from `scheduled_at` to worker start, recorded for every pickup                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| `kritika_vector_search_seconds`         | account, outcome         | Histogram of database search transaction time, outcome `ok`, `error` or `canceled`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        |
| `kritika_similar_requests_total`        | account, kind, outcome   | Similar-code requests. kind: `diff` (a review's stage 4), `search` (the agent's `search_code`), or `unknown` for a request too malformed to say. outcome: `ok`, `empty` (searched, nothing kept), `no_queries` (the run asked, before it searches, whether there is an index), `no_embedder`, `no_index`, `too_large` (a request over its bound, or a run's report of more than 5,000 changed paths, whose review then goes without similar code), `invalid`, `refused` (the run's budget) or `error`                                                                                                                                                                                                     |
| `kritika_similar_best_similarity`       | account, kind            | Histogram of the cosine similarity of each query's best match in the index. Read it against the embedder's `similarFloor`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| `kritika_similar_hits_total`            | account, outcome, source | Index chunks the searches found: `kept` (sent to the run, whose prompt can still leave a chunk out: one the head no longer has, one other stages mostly show, or one past the ten it shows), or dropped by `floor`, `margin` (below its query's best by more than `similarMargin`, or by name under half the weight of its query's best by name), `duplicate`, `file_cap` (past two of one file) or `limit` (past the four of its query, the candidates a reranker is sent, or the most an answer keeps, ten, or the sixteen a review's stage 4 asks for). source: the search that found it, `vector`, `lexical` (by name) or `both`. Paths the run excluded never leave the database and are not counted |

Each replica reads the same queue state on scrape with a two-second timeout.
Use `max by (namespace, job, queue)` for backlog and oldest age, never a sum
across replicas. Keep the deployment's cluster label as well when querying
multiple clusters. A failed read emits `kritika_queue_stats_up = 0` and omits
the queue gauges. It does not report an empty queue or retain stale values.
Known queues emit zero when empty. Queue labels are limited to `review`,
`review_prepare`, `review_publish`, `followup`, `thread`, `index` and
`index_update`. Preparation and publication each have their own worker
capacity and retry attempts. Thread jobs already queued on `followup` remain
attributed to that queue until they finish. `index` holds first indexes and
forced rebuilds, and `index_update` the updates that keep indexes current.
Updates already queued on `index` remain attributed to it until they finish.

Runnable work includes overdue retries and scheduled jobs that wait for
River's scheduler to promote them. It excludes future work, pending jobs,
running jobs and terminal jobs. A paused review, followup or thread queue
still counts its overdue work, so it can raise `KritikaQueueDelayed`.

Oldest age measures time since eligibility, not time since creation. Queue
wait also excludes intentional delays: scheduling, snooze and retry backoff.
It records another sample on each pickup. kritika clamps negative waits from
clock skew to zero. Recording at worker entry makes delays visible before
long jobs finish.

Histograms record different work on each replica, so sum their bucket rates
before computing quantiles:

```promql
histogram_quantile(0.95,
  sum by (namespace, job, queue, le) (rate(kritika_queue_wait_seconds_bucket[5m]))
)
```

Vector timing begins after embedding, lease release and usage recording. It
includes pool acquisition, account transaction setup, all vectors in the
request, the search by name when it is on, result decoding and commit. It
excludes embedding-provider latency, model-slot waits, reranking and result
filtering. Requests without a compatible index or
with an embedding failure produce no vector-search sample. The histogram's
`_count` provides search volume and database error counts. Use the existing
model-call counters for embedding failures. No repository, query text, job ID
or generation becomes a metric label.

Client cancellations and expired request contexts during the database phase
use `outcome="canceled"`. That outcome is excluded from both the numerator and
the denominator of the error ratio. All outcome series are initialized at zero
for configured accounts before workers start. Prometheus can then count a
first error after it scrapes a zero sample. `increase()` still cannot
reconstruct events before the first scrape.

The delay alert uses the latest successful snapshot within five minutes to
bridge short read failures. A fresh empty snapshot clears it. The
snapshot-success ratio also detects repeated failures. Queue snapshots use the
application pool and its least-privilege role, and acquisition time counts
against the two-second budget. Pool saturation is a legitimate snapshot
failure. Inspect the existing pool-wait metrics alongside queue health when
diagnosing it.

## Download cache

The gateway exports cache telemetry per replica, and Prometheus aggregates
the replicas. The admin UI's Download cache card only reports the responding
replica's configuration. It does not probe storage or claim that storage is
healthy.

| Metric                               | Labels             | Meaning                                                                                                                                |
| ------------------------------------ | ------------------ | -------------------------------------------------------------------------------------------------------------------------------------- |
| `kritika_cache_requests_total`       | outcome            | One decision per admitted HTTP request while caching is enabled: `local_hit`, `s3_hit`, `miss`, or `bypass`                            |
| `kritika_cache_bypasses_total`       | reason             | Why a request was forwarded without the cache                                                                                          |
| `kritika_cache_operation_seconds`    | operation, outcome | Duration histogram for `origin_fetch`, `s3_lookup`, `s3_read`, and `s3_upload`                                                         |
| `kritika_cache_transfer_bytes_total` | operation          | Actual body bytes consumed by cache fills, S3 reads/uploads, or accepted by response writers (`response_local`, `response_s3`)         |
| `kritika_cache_responses_total`      | source, outcome    | Responses after serving, by `local`/`s3` source and `ok`/`error`/`canceled` result                                                     |
| `kritika_cache_active_fills`         |                    | Current origin fills. Excludes background S3 uploads                                                                                   |
| `kritika_cache_local_bytes`          |                    | Retained files at the latest directory scan                                                                                            |
| `kritika_cache_local_limit_bytes`    |                    | Retained-entry budget, not a hard disk limit                                                                                           |
| `kritika_cache_evictions_total`      |                    | Local entries evicted to meet the budget                                                                                               |
| `kritika_cache_upload_skips_total`   | reason             | Publications skipped for `queue_full`, `circuit_open`, `entry_unavailable`, or `present`, the digest's entry another replica published |

Bypass reasons are bounded: `ineligible`, `fill_error`,
`entry_unavailable`, `canceled`, `fill_busy`, `fill_capacity`, `too_large`,
`private`, `encoded`, `unknown_length`, and `digest_mismatch` for an
origin's body without the digest the request named. URLs, object keys, run IDs and
arbitrary errors are not metric labels. CONNECT tunnels are excluded.

A miss is a request that the gateway serves after a wait for a fill. This
includes requests that share one in-process fill. Failed fills report a
bypass instead. The hit ratio is hits divided by hits plus misses. Inspect
bypasses alongside it. Operation outcomes are `ok`, `error`, and `canceled`,
plus these:

- `miss` for an absent or invalid S3 entry, or for one replaced or removed
  while it was being read.
- `skipped` for an origin response that cannot be stored.

Cache availability does not affect readiness. A failed or circuit-open S3
lookup is a miss that fills locally, not a bypass. Circuit-open lookups and
skipped uploads do not contribute to S3 operation counts, so they cannot
dilute the storage-error rate. Read and upload circuits recover
independently. Uploads run in the background, and their duration is separate
from origin fill time.

Transfer counters include partial operations. Origin bytes cover cache fill
attempts, not all gateway forwarding or estimated bandwidth savings. Upload
bytes count the HTTP request body, but not the SDK's checksum pass. Response
bytes count writes that the HTTP writer accepts, with multipart framing
included. HEAD and 304 responses contribute zero.

A hit does not prove delivery. To find stream failures and client
cancellations, read the response results. S3 read duration includes the wait
for the downstream reader.

Local usage excludes fills in progress and evicted files that are still held
open. It is updated at startup and at eviction scans. Compare it with the
entry budget to understand eviction pressure. Use pod filesystem metrics for
actual disk usage. Bucket usage and lifecycle retention belong in the storage
provider's monitoring.

At debug level, `egress cache download` records a completed serving attempt with
`cache_key`, `source`, `result`, HTTP status, method, bytes and elapsed time.
Storage failures produce `egress cache storage error` at warning level, limited
to one warning per minute per replica. Every failed operation still appears in
metrics. The runner's `gateway_cache` install field indicates the route used.
It is not proof of a cache hit. A retry around the cache has its own warning.

## Most used tools

The admin UI's **Most used tools** graph ranks the top ten command names over
7, 30 or 90 days, across currently served accounts. Each started process counts,
including nonzero exits, timeouts and retry runs. Rejected commands, failed
installations and processes that fail to start do not count. Versions and
providers share a command name. The counts are executions, not download
counts.

Counts are stored in `agent_runs.command_counts` alongside the saved result,
so they survive restarts. They also aggregate across replicas without
Prometheus labels for arbitrary command names. Split review parts share one
counter. The period uses the result's save time. Active runs, and runs lost
before saving their result, are not included.

## Alerts

The chart's `monitoring.prometheusRule.enabled` creates a Prometheus Operator
`PrometheusRule` with the alerts below, grouped by the metrics Service's
`namespace` and `job`. Each alert's wait is set by a value
(`monitoring.prometheusRule.*For`). `additionalRuleLabels` adds a routing
label to every rule, and `additionalRuleAnnotations` adds an annotation.

| Alert                          | Fires when                                                                                                                                               | Severity | Wait |
| ------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------- | -------- | ---- |
| `KritikaNoLeader`              | `sum(kritika_leader) == 0`: no replica holds the leader lock                                                                                             | critical | 5m   |
| `KritikaMultipleLeaders`       | `sum(last_over_time(kritika_leader[1m])) > 1`: more than one replica reported holding it within `multipleLeadersWindow`                                  | critical | 2m   |
| `KritikaConfigError`           | `kritika_config_error == 1`: the leader failed to apply the file                                                                                         | warning  | 5m   |
| `KritikaConfigDrift`           | `kritika_config_drift == 1`: a replica's file differs from the applied one                                                                               | warning  | 15m  |
| `KritikaQueueDelayed`          | Oldest runnable job outside the `index` and `index_update` queues exceeds 600 seconds. Uses the last successful sample within 5m and max across replicas | warning  | 15m  |
| `KritikaQueueStatsUnavailable` | Every replica succeeds on fewer than 75% of snapshot reads over 5m                                                                                       | warning  | 5m   |
| `KritikaCacheStorageErrors`    | More than 10% S3 operation errors, with at least 10 non-canceled operations in 15m. Cold misses are not errors                                           | warning  | 10m  |
| `KritikaVectorSearchErrors`    | More than 10% database search errors, with at least 10 non-canceled requests in 15m                                                                      | warning  | 10m  |

Without a leader, nothing migrates, applies configuration, polls or rescues
the jobs a dead replica left. A standby takes the lock within one leader
retry interval once its owner connection reaches the database. Postgres
drops a dead leader's session, and with it the lock, within about a minute.
Two leaders can show for one retry interval during a database failover,
before the old leader's lock connection fails. If they show for longer than
that, the replicas are not on one database. Drift is expected
for the minute or two a rollout takes, until the new pod leads and applies
its file.

Queue age and alert waits are configurable under `monitoring.prometheusRule`.
The defaults are starting thresholds, not measured production SLOs. Tune
queue age for normal review traffic. Index work remains visible on the
dashboard but is excluded from the delay alert, because onboarding
deliberately waits behind higher-priority updates. Vector latency is graphed
without an alert threshold until a representative production baseline is
available.

## Kubernetes Events

kritika records Events that `kubectl describe` and `kubectl events` show and
that an event collector gathers. On a runner Job, an Event says why kritika
ended its run. On a kritika serve pod, it says what its leader did.

| On                  | Reason             | Type    | When                                                                                                                                                                                  |
| ------------------- | ------------------ | ------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| a runner Job        | `Superseded`       | Normal  | a newer head of the pull request superseded the run. The note names it                                                                                                                |
| a runner Job        | `Replaced`         | Normal  | a newer review of the pull request replaced the run                                                                                                                                   |
| a runner Job        | `Cancelled`        | Normal  | the run was cancelled from the dashboard                                                                                                                                              |
| a runner Job        | `HeartbeatLost`    | Warning | the runner's heartbeat went stale, so kritika ended the run                                                                                                                           |
| a runner Job        | `Disrupted`        | Warning | Kubernetes stopped its pod, by a node drain, a preemption or an eviction. The note says how                                                                                           |
| a runner Job        | `NeverStarted`     | Warning | its pod failed to start, as for an image that cannot be pulled or a pod no node takes. Or its pod was never created, as when the namespace refuses it on admission. The note says why |
| a runner Job        | `Interrupted`      | Normal  | kritika serve stopped, and the job runs again                                                                                                                                         |
| a runner Job        | `Interrupted`      | Warning | the job's heartbeat stopped landing, so kritika handed it back to its queue                                                                                                           |
| a runner Job        | `TimedOut`         | Warning | the job ran out of time                                                                                                                                                               |
| a runner Job        | `StatusUnreadable` | Warning | kritika lost sight of the Job's status, as when someone else deleted it, so it deleted the Job                                                                                        |
| a kritika serve pod | `LeaderElected`    | Normal  | the replica took the lead                                                                                                                                                             |
| a kritika serve pod | `ConfigApplied`    | Normal  | the leader applied the configuration, named by its hash                                                                                                                               |
| a kritika serve pod | `ConfigRefused`    | Warning | the store refused the configuration, and the last applied one stays live, as `kritika_config_error` says too                                                                          |

A run that ended on its own, finished or failed, gets none of these. For such
a run, the Job controller's own Events on its Job tell what happened.
kritika deletes the Job of a run it ends, so
`kubectl events --for job/<name>` finds those Events once `kubectl describe`
no longer can. A pod's Events need its name and UID, which the chart passes
as `KRITIKA_POD_NAME` and `KRITIKA_POD_UID`. The chart's Role lets kritika
create Events in its namespace. An Event it cannot record is logged.
Kubernetes keeps Events for an hour unless the API server's `--event-ttl`
says otherwise.

## Grafana dashboard

The chart's `monitoring.dashboards.enabled` renders a Grafana dashboard of
most of the series above (`charts/kritika/dashboards/kritika.json`). It is a
ConfigMap labelled `grafana_dashboard: "1"` for the kube-prometheus-stack
sidecar. With `monitoring.dashboards.grafanaOperator.enabled`, it is a
`GrafanaDashboard` for grafana-operator, which imports the ConfigMap into the
Grafana instance that `grafanaOperator.matchLabels` selects.

The dashboard is filtered by the metrics Service's `namespace` and `job`,
then by `account` and `model`. Its rows follow a review's path:

- health (leaders, configuration, drift, rescued jobs)
- intake (webhooks, backstop polls, rate limits)
- reviews (status, duration, findings, follow-ups, the wait for a model slot)
- models (calls, tokens, cost per hour and over the time range)
- runner Jobs, the gateway's egress and the index
- database pools
- queue delays and vector searches
- the gateway's download cache

Queue panels span all accounts. Vector panels follow the account filter.

## Reproducing the vector-search baseline

Preparation also emits INFO logs for diagnosing time outside the
vector-search histogram:

- `parts context built` records the split review's shared scan counts and
  elapsed time.
- `similar-code request ended` records the runner's whole retrieval request.
- The gateway separates `similar embedding slot` wait time from
  `similar queries embedded` provider time.
- `review model slots granted` records the worker's estimated part count
  and the slots granted, both by model. Compare it with `agent started in
  parts` for the actual part count and initial slots.
- During a split review, `review model slots adjusted` records changes to the
  worker's grant, with `slots` and `remaining_parts` by model. Remaining
  parts include active work and exclude completed checkpoints.

The worker and runner read capacity every two seconds, so a review can gain
slots after it starts. Recorded reviews that wait for their first slot take
priority over extra slots: the grant drops by what they are short of, and the
worker lets a slot go once the runner's parts cannot be in it. Elapsed fields
are Go durations, and the JSON logger encodes them as nanoseconds. Split parts
retrieve similar code concurrently, bounded by their granted parallelism.
These request durations can overlap, so do not sum them to estimate
preparation wall time.

Run on a machine with Docker through the integration task, which creates and
removes a disposable database. No provider credentials or model calls are needed:

```sh
mise run test-integration -- -run '^$' -bench BenchmarkSimilarChunks -benchtime=20x -v
```

`BenchmarkSimilarChunks` exercises the `SimilarChunks` SQL with
`vchordrq.prefilter = on`, 1,024 and 1,536 dimensions, and 1,000 or 10,000
chunks in the target generation. A second repository in the same account and
a second account each contribute the same number of vectors: 3,000 or 30,000
total rows.
Each case excludes either no paths or 10% of target paths. Data comes from a
fixed-seed, 16-cluster synthetic distribution with one chunk per path.

All retrieval runs as `kritika_app` with RLS (row-level security) enabled, the
transaction's account setting, the generation filter and the actual path
exclusions. A foreign-account lookup must return no results. Recall@4 compares
the application's results with an exact table scan for 20 deterministic queries.

The harness disables index and bitmap scans only for that reference. It makes
sure that the reference uses a sequential-scan plan. It restores those scans
before further application queries. It compares chunk paths directly because
fixture paths have a distinct prefix for each generation. This also keeps the
ground truth on the same full-result SQL as the application, rather than on a
separate row-identifier query. VectorChord's
[recall evaluator](https://docs.vectorchord.ai/vectorchord/usage/measure-recall.html)
is another option for production query samples.

The verbose output captures `EXPLAIN (ANALYZE, BUFFERS)` for both plans, plus the
PostgreSQL and VectorChord versions. Setup, recall evaluation and plan collection
are excluded from the reported `ns/op`. Timing includes the account transaction,
vector serialization, search and result decoding for one query vector. It is a
warm microbenchmark with the integration task's race detector enabled, not a
cold-start or concurrent-traffic test.

### Local baseline, 2026-10-12

The measurements ran on Linux amd64, AMD EPYC 7502P, with the task's
`ghcr.io/tensorchord/vchord-postgres:pg18-v1.1.1` container (PostgreSQL 18.3,
VectorChord 1.1.1). Each mean below covers 20 timed requests. EXPLAIN times are
individual diagnostic queries, not averages or percentiles.

| Dimensions | Target chunks | Excluded paths | Mean request (ms) | Recall@4 | VectorChord EXPLAIN (ms) | Exact EXPLAIN (ms) |
| ---------- | ------------- | -------------- | ----------------- | -------- | ------------------------ | ------------------ |
| 1,024      | 1,000         | 0              | 2.27              | 1.00     | 0.44                     | 8.83               |
| 1,024      | 1,000         | 100            | 2.48              | 1.00     | 0.41                     | 8.71               |
| 1,024      | 10,000        | 0              | 6.75              | 1.00     | 5.05                     | 98.42              |
| 1,024      | 10,000        | 1,000          | 17.72             | 1.00     | 4.82                     | 92.23              |
| 1,536      | 1,000         | 0              | 2.88              | 1.00     | 0.52                     | 10.43              |
| 1,536      | 1,000         | 100            | 3.03              | 1.00     | 0.54                     | 9.40               |
| 1,536      | 10,000        | 0              | 9.43              | 1.00     | 6.13                     | 144.10             |
| 1,536      | 10,000        | 1,000          | 19.30             | 1.00     | 5.89                     | 97.31              |

Every application plan chose `index_chunks_embedding_idx`. Every exact plan
used a sequential scan and top-N sort. The filters included both the generation
and the RLS account predicate. Plans reported shared-buffer hits for these warm
queries. The command above emits the full buffer counts.

The index, then one plain table, performed well on this fixture. The longer
requests with large exclusion lists include their client-side encoding and
transfer, so do not attribute all of that difference to the index. These
small, synthetic samples are not evidence of production recall or an SLO. They
do not justify a change to search parameters, prewarming or worker concurrency.
Before you tune that configuration, use the runtime histograms and repeat with
representative embeddings and traffic. No production database was measured for
this baseline. At 30,000 rows it did not show how search cost grows with the
table. The [index scale benchmark](#reproducing-the-index-scale-benchmark)
does, and led to partitioning the table.

## Reproducing the index scale benchmark

`BenchmarkIndexScale` measures similar-code search when the index table holds
many repositories, as on an instance that indexes many of them. It is opt-in,
because loading millions of vectors takes minutes and tens of gigabytes of
disk:

```sh
KRITIKA_INDEX_SCALE_ROWS=3000000 mise run test-integration -- \
  -run '^$' -bench BenchmarkIndexScale -benchtime 1x -timeout 6h -v ./internal/store/
```

`KRITIKA_INDEX_SCALE_DIMS` (1,024 unless set) sets the dimension, and
`KRITIKA_INDEX_SCALE_QUERIES` (50) the queries for each target repository.
`KRITIKA_INDEX_SCALE_LAYOUTS` names the layouts to compare, comma-separated. By
default it compares `flat`, `exact`, `partitioned` and `drained`:

| Layout                                                | What it is                                                                                                                                      |
| ----------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------- |
| `flat`                                                | The plain table of a build before partitioning, with its one `vchordrq` index and no index options                                              |
| `exact`                                               | The same table with `vchordrq.enable_scan` off, which leaves the generation's B-tree and a sort                                                 |
| `partitioned`                                         | The plain table just partitioned by `PartitionIndexChunks`, with every chunk still in the legacy partition                                      |
| `drained`                                             | The partitioned table after `DrainLegacyIndexChunks` emptied the legacy partition, `DropLegacyIndexChunks` dropped it, and `VACUUM`             |
| `ivf-probes1pct`, `ivf-probes5pct`, `ivf-probes20pct` | The plain table with VectorChord's own partitioning: `lists` as its documentation recommends for the row count, and `probes` that share of them |
| `repo-hash`                                           | A copy of the table hash partitioned by repository, which the search then also filters on                                                       |

The corpus is synthetic and clustered: 512 topic centroids, and repositories
that each draw on 24 topics and add a style vector of their own. Repository
sizes follow a log-normal distribution with a median of 3,000 chunks, clipped
to between 100 and 100,000. Eight accounts own them. Three target repositories,
of 1,000, 10,000 and 100,000 chunks, belong to one account. Each query is a new
chunk drawn from its target's distribution.

Recall@10 compares the application's ten results with an exact search of the
same generation, made before any vector index exists. Every search runs the
application's SQL as the application role, with row-level security,
`vchordrq.prefilter` on and pgx's statement cache. After five executions,
Postgres can therefore switch to a generic plan, as it does in production. The
timed loop runs each query once, after an untimed pass that warms the cache and
logs the first query's `EXPLAIN (ANALYZE, BUFFERS)`. This is a warm,
single-client measurement under the integration task's race detector, on
synthetic data. It is not evidence of production recall or an SLO.

### Local measurement, 2026-11-20

The measurements ran on Linux amd64, AMD Ryzen 9 9950X3D (16 cores), 60 GiB of
RAM and NVMe storage, with PostgreSQL 18 in Docker. Postgres ran with
`shared_buffers=8GB`, `maintenance_work_mem=4GB`,
`max_parallel_maintenance_workers=8`, `max_wal_size=32GB` and
`effective_cache_size=32GB`. The integration task's container keeps Postgres's
defaults, with 128 MB of shared buffers, so its absolute numbers differ.
VectorChord 1.1.1 ran in `ghcr.io/tensorchord/vchord-postgres:pg18-v1.1.1`.
VectorChord 1.3.0 ran in `ghcr.io/cloudnative-pg/postgresql:18.6-standard-bookworm`
with `ghcr.io/home-operations/vchord:1.3.0` mounted.

The table held 3,000,000 chunks of 1,024 dimensions in 434 repositories. Each
cell gives the p50 and p95 latency in milliseconds over 50 queries, then
recall@10. Other workloads shared the host during the VectorChord 1.3.0 runs
and the end of the 1.1.1 run, so compare layouts within one run. On 1.1.1:

| Layout           | 1,000 chunks    | 10,000 chunks       | 100,000 chunks      |
| ---------------- | --------------- | ------------------- | ------------------- |
| `flat`           | 6.7 / 7.3, 1.00 | 28.5 / 29.3, 1.00   | 302.5 / 311.6, 1.00 |
| `exact`          | 5.9 / 6.0, 1.00 | 28.6 / 29.8, 1.00   | 304.2 / 310.3, 1.00 |
| `partitioned`    | 6.9 / 8.2, 1.00 | 109.0 / 117.8, 0.98 | 111.8 / 123.3, 0.98 |
| `drained`        | 3.6 / 4.2, 1.00 | 4.1 / 5.8, 0.98     | 13.2 / 17.1, 0.98   |
| `ivf-probes1pct` | 7.4 / 8.0, 1.00 | 34.2 / 35.0, 1.00   | 300.8 / 307.7, 1.00 |
| `ivf-probes5pct` | 7.5 / 8.2, 1.00 | 34.3 / 36.7, 1.00   | 301.4 / 305.2, 1.00 |
| `repo-hash`      | 3.1 / 3.7, 1.00 | 1.3 / 1.5, 0.98     | 13.4 / 22.2, 0.98   |

On 1.3.0, in two runs. The second ran while the host's load average was near
30, from work outside the benchmark:

| Layout        | Run | 1,000 chunks      | 10,000 chunks      | 100,000 chunks      |
| ------------- | --- | ----------------- | ------------------ | ------------------- |
| `flat`        | 1   | 7.0 / 7.9, 1.00   | 34.5 / 36.1, 1.00  | 304.3 / 312.7, 1.00 |
| `exact`       | 1   | 5.9 / 7.0, 1.00   | 30.7 / 33.7, 1.00  | 301.6 / 309.6, 1.00 |
| `partitioned` | 1   | 7.3 / 7.7, 1.00   | 28.7 / 34.7, 1.00  | 113.3 / 138.9, 1.00 |
| `drained`     | 1   | 6.9 / 8.8, 1.00   | 4.9 / 21.8, 1.00   | 28.1 / 83.2, 1.00   |
| `repo-hash`   | 1   | 3.2 / 3.9, 1.00   | 2.0 / 3.1, 1.00    | 13.9 / 16.0, 1.00   |
| `flat`        | 2   | 16.6 / 37.4, 1.00 | 77.2 / 125.4, 1.00 | 703.6 / 862.7, 1.00 |
| `partitioned` | 2   | 15.9 / 23.2, 1.00 | 85.1 / 207.0, 1.00 | 271.9 / 359.4, 1.00 |
| `drained`     | 2   | 2.9 / 3.0, 1.00   | 7.4 / 12.9, 1.00   | 6.2 / 16.1, 1.00    |

`PartitionIndexChunks` took 1 to 2 seconds on the 3,000,000-chunk table, most
of it attaching the legacy partition. `DrainLegacyIndexChunks`, called until it
moved nothing, and the `VACUUM ANALYZE` after it took 4 minutes 58 seconds on
1.1.1, and 6 minutes 36 seconds and 8 minutes 18 seconds in the two 1.3.0
runs. A smaller run of 1,000,000 chunks in 138
repositories on 1.1.1 gave the plain table 4.8, 37.2 and 40.4 ms at p50 for the
three targets, and 64 hash partitions by generation 0.6, 1.1 and 10.0 ms.
These runs kept the emptied legacy partition attached. The sweep now drops
it, which takes its empty vector index out of every search's plan.

What the measurements show:

- The plain table's search cost follows the table, not the repository. The
  `vchordrq` index of the plain table reads every chunk's quantized vector.
  The prefilter then checks each candidate against the generation. At
  100,000 rows that took about 3 ms. At 3,000,000 rows it took about 110 ms,
  so Postgres's generic plan read the generation through its B-tree and sorted
  it instead: exact, but 300 ms for a repository of 100,000 chunks.
- Partitioned by generation and drained, a search reads one of 64 partitions:
  6 to 28 ms at p50 for the large repository, and under 8 ms for the others,
  with the recall of the plain table's vector index.
- Until the sweep drops the legacy partition, a search also reads its vector
  index. While the partition holds the chunks, that costs about 110 ms at this
  size, which is slower than the plain table's exact plan for the
  10,000-chunk repository on 1.1.1. The sweep drains and drops it right after
  it partitions, so this lasts minutes.
- VectorChord's own partitioning (`lists`) did not help the application's
  query. Postgres's generic plan never chose the index and read the B-tree
  instead, so the results equal `exact`. A custom plan did use it, at about
  7 ms for the large repository. It would also need the vectors to exist when
  the index is built, and a rebuild as they change, while kritika builds the
  index on an empty table. With a filter this selective, the lists that a
  search probes can also hold too few of the repository's chunks. This run did
  not measure that, as no timed plan used the index.
- Partitioning by repository performed like partitioning by generation. The
  search must then also filter on the repository, and with that filter on the
  plain table Postgres underestimated the rows that the correlated repository
  and generation filters match. It chose the exact plan for the large
  repository: 287 ms at 1,000,000 rows, against 44 ms without the filter.
  Partitioning by generation needs no change to the query.
- The first 1.3.0 run's `drained` p95 latencies are high. Its logged plans
  read the partitions that the drain had just written from outside shared
  buffers.

### The search by name

`BenchmarkLexicalScale` measures the [search by name](/models/#search-by-name)
over one generation of each size in `KRITIKA_LEXICAL_SCALE_CHUNKS`, a
comma-separated list of chunk counts. It is opt-in, because a hundred thousand
chunks take minutes to load:

```sh
KRITIKA_LEXICAL_SCALE_CHUNKS=10000,100000 mise run test-integration -- \
  -run '^$' -bench BenchmarkLexicalScale -benchtime 1x -timeout 1h -v ./internal/store/
```

Its code is synthetic: thirty lines of five identifiers a chunk, each of one to
three parts drawn by a Zipf law from 4,000 words, which gives about 250
postings a chunk, five times what this repository's code gives. Each of 30
requests holds twelve queries of the 32 longest words of eight such lines, as a
review's similar-code stage sends. It reports the p50 and p95 latency of a
request, and how long the chunks took to insert, postings included.

On 2026-11-20, on the machine above, with the integration task's images and
Postgres run with `shared_buffers=1GB`, `work_mem=64MB` and
`max_wal_size=4GB`, and the race detector off:

| Chunks  | Before postings: p50 / p95 ms | With postings: p50 / p95 ms | Insert before / with postings |
| ------- | ----------------------------- | --------------------------- | ----------------------------- |
| 10,000  | 298.9 / 311.7                 | 30.6 / 35.2                 | 2.1 s / 8.3 s                 |
| 100,000 | 2,875 / 2,959                 | 29.4 / 35.4                 | 21 s / 126 s                  |

Before postings, the search read every word of the generation's chunks for
each request, so its time grew with the generation. With them, it reads each
query word's postings, at most the count at which a word is too common to
search for, so its time follows the queries rather than the generation.

## Review completion timings

`kritika_review_stage_duration_seconds` separates the work after the runner
answers. Stage labels are fixed operation names:

| Stage                                     | Interval                                                                                    |
| ----------------------------------------- | ------------------------------------------------------------------------------------------- |
| `agent_to_prepare`                        | Agent row creation to the start of a preparation attempt                                    |
| `prepare`                                 | Finding processing, summary merging and confidence scoring                                  |
| `merge`, `confidence`                     | Each operation, with its model-slot wait and retries                                        |
| `checkpoint`                              | Saving preparation or publication state and any queue handoff                               |
| `deliver`                                 | Publication lock acquisition, delivery and persistence                                      |
| `write_back`                              | Rendering and forge operations                                                              |
| `forge_sticky`                            | Finding, creating or updating the sticky comment                                            |
| `forge_inline`, `forge_status`            | Posting the inline review or setting commit status                                          |
| `forge_approval`, `forge_resolve_threads` | Approval processing or resolving the batch of old threads                                   |
| `forge_thread_states`                     | Reading back the state of every thread, when resolving left an old one open                 |
| `forge_summary_update`                    | Rewriting the summary with thread links and approval                                        |
| `persist`                                 | Committing the completed publication                                                        |
| `agent_to_published`                      | Agent row creation to successfully committed publication, across queue handoffs and retries |

These intervals overlap: do not sum stages or their percentiles. Forge stages
are operations and can make multiple HTTP requests.

`agent_to_*` uses the database's agent-record timestamp, which approximates
agent completion and requires aligned database and worker clocks. kritika
clamps negative durations to zero. A checkpoint without an agent row produces
no such sample. Other stages include failed attempts.

All `review stage ended` logs carry `review`, `run`, `job`, `stage` and
`elapsed`. JSON duration fields are nanoseconds.

For example, compare publication-tail p95 by account:

```promql
histogram_quantile(0.95, sum by (account, le) (
  rate(kritika_review_stage_duration_seconds_bucket{stage="agent_to_published"}[1h])
))
```

Queue delay already uses `kritika_queue_wait_seconds` with
`queue="review_prepare"` or `queue="review_publish"`. The
`resuming review publication` log adds review/run/job identity, attempt number,
`queue_wait` (since scheduled eligibility) and `since_enqueued` (including
retry backoff and earlier attempts). Both end at worker pickup.

Executor logs split completion handling into `runner pods read`,
`runner log tail read` and the enclosing `runner result collected`, joined by
`job_name`. `runner job completion observed` marks when the watch sees
completion. `agent completion observed` links that job to the run and reports
`agent_to_executor_return` and `elapsed`.

`agent_to_executor_return` ends when the supervised run returns to the worker,
so it includes log collection and any supervisor check still in progress.
`elapsed` also includes token revocation and loading the agent row. Their
individual durations appear in `runner completion handled` as `revoke_elapsed`
and `row_read_elapsed`. These logs distinguish lifecycle delay from log-read
latency.

`kritika_review_slot_wait_seconds` measures each recorded slot snooze from its
start to the next review pickup. Early wakes shorten it. Scheduling delays
lengthen it. For aggregate observed waiting time, sum
`increase(kritika_review_slot_wait_seconds_sum[1h])` by account and model. To
inspect repeated waits, group `review model slot wait ended` logs by job.

This metric is separate from `kritika_lease_wait_seconds`, which measures waits
inside an active worker. The slot wait overlaps queue delay and must not be
added to it. Canceled jobs never picked up again have no sample.

A process crash before River saves cleared metadata can cause a repeated
observation. A pickup that fails before the wait is recorded leaves it to the
next attempt, retry backoff included. These are operational metrics, not an
exactly-once accounting ledger.
