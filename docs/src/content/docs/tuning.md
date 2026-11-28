---
title: Tuning
---

kritika has several hundred tuning values, all listed in the
[tuning reference](/configuration/#tuning). This page helps you find the
few that matter for what you see. It names the symptom that each limit
leaves, the values that move it, and what a change costs.

## Before you change a value

- The defaults suit nearly every installation. Most values exist so that a
  limit is a setting rather than a rebuild, not because they need setting.
- Change a value only for a symptom you can see. That can be a note in a
  review's summary, a log line, a metric or a mark on the dashboard. Each
  section below names the symptom.
- Change one value at a time, and check the symptom again before the next.
- Try the settings first. They are the usual levers, and their own pages
  describe them:

| Lever                                                                | Where it is documented                                     | What it changes                                              |
| -------------------------------------------------------------------- | ---------------------------------------------------------- | ------------------------------------------------------------ |
| `reviewWorkers`, `threadWorkers`, `indexWorkers`, `onboardWindow`    | [How kritika runs](/configuration/#how-kritika-runs)       | how many jobs each replica runs at once                      |
| `pollInterval`                                                       | [How kritika runs](/configuration/#how-kritika-runs)       | how often the backstop poll lists open pull requests         |
| `transcriptRetention`, `diffRetention`, `indexGrace`                 | [How kritika runs](/configuration/#how-kritika-runs)       | how much the database keeps                                  |
| `runner.podTemplate` `spec.resources`, `runner.jobTemplate` deadline | [How kritika runs](/configuration/#how-kritika-runs)       | a runner pod's memory and CPU, and its Job's deadline        |
| a provider's `concurrency`, `timeout` and `retries`                  | [Models and providers](/models/#providers)                 | how many model calls run at once, and how long and how often |
| `agent.steps`, `tokens`, `prompt`, `parts`, `timeout`, `fetches`     | [The agent](/configuration/#the-agent)                     | how much a review reads and spends                           |
| `embedding.similarFloor`, `similarMargin`, `rerank`                  | [The embedder](/models/#the-embedder)                      | which similar code is kept                                   |
| a repository's `ignore` globs                                        | [Repository settings](/configuration/#repository-settings) | what the index, the diff and the agent's searches leave out  |

A tuning value whose description says it is the default when the
configuration file does not set a key is only a fallback. For example,
`model.defaultStepTimeout` is the default of a provider's `timeout`. Set
the key instead. A key that the file or a repository's settings set wins
over the fallback.

## Large repositories and monorepos

Signs that you hit a limit:

- The repository's preview and its **Index contents** show a **partial**
  pill ([dashboard](/dashboard/#repositories)), and
  `kritika_index_truncated_total` counts up.
- An index run's log says `index truncated at the indexer's budget: the
  files past it are left out`. The publish logs `index published truncated`.
- A push's update logs `incremental build over budget, building the index
  in full`. The update hit the same budget, so it rebuilt the whole index.
- A review's run log line `context built` (or `parts context built`) has
  `scan_truncated=true` (`truncated=true` for parts). The review's walk of
  the head tree stopped early, so its definitions and callers come from
  part of the repository.

Leave vendored, generated and data paths out first, with the repository's
`ignore` globs. That costs nothing, while every limit below costs memory,
embedding calls and database space.

| Value                                                       | Default                | What it bounds                                                                                                                  |
| ----------------------------------------------------------- | ---------------------- | ------------------------------------------------------------------------------------------------------------------------------- |
| `indexer.maxFiles`, `indexer.maxBytes`, `indexer.maxChunks` | 50000, `500Mi`, 100000 | where an index build stops: the **partial** mark                                                                                |
| `indexer.maxFileBytes`                                      | `1Mi`                  | larger files are skipped. The run's `chunked` log line counts them under `skipped`                                              |
| `contextPack.maxScanFiles`, `contextPack.maxScanBytes`      | 20000, `200Mi`         | a review's walk of the head tree: `scan_truncated`                                                                              |
| `gateway.maxIndexBody`                                      | `2Gi`                  | one index upload. It must hold `indexer.maxChunks` chunks of `indexer.maxChunkBytes`, and kritika refuses a value that does not |
| `gateway.maxIndexLine`                                      | `32Mi`                 | an upload's opening line, which lists every changed path                                                                        |
| `runSpec.indexUploadTimeout`                                | `30m`                  | one upload attempt. The runner Job's deadline bounds the fetch, the build and the upload together (see below)                   |
| `jobTimeout.indexWriteHeadroom`                             | `60m`                  | the job time after the runner ends, in which the chunks are embedded and the new index swapped in                               |

An index runner gets the runner Job's deadline
(`runner.jobTemplate.spec.activeDeadlineSeconds`, 900 seconds in the chart)
for the fetch, the parse and the upload together, so a larger build may
need a longer one, not only a longer `runSpec.indexUploadTimeout`.
`jobTimeout.indexWriteHeadroom` is for the work after the runner exits.

What raising the indexer's limits costs:

- **Runner pod memory.** A build holds every chunk in memory until it
  uploads them: up to `indexer.maxChunks` × `indexer.maxChunkBytes` of
  text, 600 MB at the defaults, plus chunk metadata and encoding buffers.
  The runner streams the upload one line at a time. Raise
  `runner.podTemplate`'s `spec.resources` if the larger build needs it.
- **Runner pod disk.** The runner fetches the repository into a temporary
  directory on the pod. No tuning value bounds that, so size the pod for
  the repository.
- **Embedding.** Each new chunk is one more text for the embedder, in
  time and in the provider's spend. A longer embedding pass can need a
  larger `jobTimeout.indexWriteHeadroom`, which in turn lowers the largest
  runner deadline that an account can set
  ([the job's timeout](#slow-or-unreliable-model-providers)).
- **Database space** for the chunks, their vectors and the search by name.

The `indexer` values except `indexer.queryContext`, the `lexical` values,
and `chunk.parseMemoryBudget` and `chunk.parseStackDepth` can change what a
build stores: a parser budget that a file runs out of makes the build
chunk it in fixed windows instead of by its declarations. A build caps a
file at `indexer.maxFileBytes`, not `chunk.maxFileBytes`, the parser's own
fallback. A repository's index keeps what it was built with until a
reindex rebuilds it: a tuning change rebuilds nothing on its own, a push updates only the files that it
changes, and the **partial** mark stays until a full build fits within the
limits. To rebuild, use **Reindex** on the repository's page, or select
repositories on the **Repositories** page and use **Reindex…**.

The `contextPack` values and `indexer.queryContext` shape a review's search
of the index, and need no reindex. `lexical.maxLexemeBytes` also changes
how a search's query is split into lexemes at once, before the stored
postings are rebuilt.

For example, for a repository that needs three times the default limits:

```yaml
tuning:
  indexer:
    maxFiles: 150000
    maxBytes: 1500Mi
    maxChunks: 300000
  gateway:
    maxIndexBody: 5Gi
```

`kritika config check` budgets `gateway.maxIndexBody` for an opening line
of up to `gateway.maxIndexLine`, then up to `indexer.maxChunks` chunks,
each allowed twice its text and signature size, `indexer.maxChunkBytes`
and `indexer.maxSignatureBytes`, plus room for its other fields. At
300,000 chunks and the other defaults, that is 4,367,954,432 bytes, past
the default `2Gi`, so the example raises it to `5Gi`. JSON can expand
some text more than twofold; the runner measures the encoded upload and
refuses one past the cap.

Reference: [`tuning.indexer`](/configuration/#tuningindexer),
[`tuning.chunk`](/configuration/#tuningchunk),
[`tuning.lexical`](/configuration/#tuninglexical),
[`tuning.contextPack`](/configuration/#tuningcontextpack) and
[`tuning.gateway`](/configuration/#tuninggateway).

## Reviews that miss context

A review's prompt is filled from the context pack up to the prompt's
budget. A note in the review's summary says what the budget left out:

| Note in the summary                                                                               | What to raise                                                                              |
| ------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------ |
| `N context chunk(s) left out of the prompt to fit its budget`                                     | the agent's `prompt`, in the configuration file, with `tokens` to keep the agent its steps |
| `N reference file(s) left out of the prompt to fit its budget`                                    | the same                                                                                   |
| `N review rule(s) left out, past the … KiB of rule text or … KiB of rule files a review is given` | `repoConfig.maxRulesBytes` (`16Ki`) or `repoConfig.maxRuleFileBytes` (`32Ki`)              |
| `more than N rule files found; the rest were left out`                                            | `repoConfig.maxRuleFiles` (100)                                                            |
| `N skill(s) left out, past the … KiB their names and descriptions are given`                      | `repoConfig.maxSkillListingBytes` (`4Ki`)                                                  |
| `N skill(s) offered by name rather than loaded, past the … KiB loaded skills are given`           | `repoConfig.maxSkillLoadedBytes` (`32Ki`)                                                  |
| `more than N skills found; the rest were left out`                                                | `repoConfig.maxSkills` (50)                                                                |

Every step sends the whole prompt again, so a larger prompt costs tokens on
every step of every review. Raising the context pack's own limits without
the prompt's budget only makes the first note appear.

With no such note, the pack itself may be thin.
`kritika_context_chunks_total` counts what each stage gave a prompt, by
stage: `overlay`, `definition`, `caller` and `similar`. The run log's
`context built` line counts the first three for one review.

| Value                                                          | Default  | What it bounds                                           |
| -------------------------------------------------------------- | -------- | -------------------------------------------------------- |
| `contextPack.maxIdentifiers`                                   | 40       | the identifiers whose definitions are looked up          |
| `contextPack.definitionsPerName`, `contextPack.maxDefinitions` | 2, 30    | definitions per identifier, and in all                   |
| `contextPack.callersPerSymbol`, `contextPack.maxCallers`       | 5, 30    | callers per changed symbol, and in all                   |
| `contextPack.maxPackBytes`                                     | `300000` | the whole pack, about 24k tokens with room for the diff  |
| `contextPack.maxDeclLines`                                     | 150      | the largest declaration shown whole, instead of a window |

### Similar code

`kritika_similar_hits_total` says why the similar-code search dropped what
it found. Each outcome points at its own value:

| Outcome           | What dropped the chunk                                                 | What to change                                                                                                                                                                                                                     |
| ----------------- | ---------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `floor`, `margin` | a cosine similarity under the floor, or too far below the query's best | the embedder's `similarFloor` and `similarMargin`. Read `kritika_similar_best_similarity` against the floor                                                                                                                        |
| `lexical_floor`   | a match by name alone, too weak                                        | `gateway.lexicalShare` (0.5)                                                                                                                                                                                                       |
| `file_cap`        | more than the chunks one file can give                                 | `gateway.similarPerFile` (2)                                                                                                                                                                                                       |
| `limit`           | more than one query keeps, or one answer                               | `gateway.similarPerQuery` (4) and `contextPack.similarLimit` (16), which a review's search asks for. `search_code` asks for twice `runner.searchShown`. `gateway.similarMax` (10) applies only to a request that asks for no limit |

A review's prompt shows at most `runner.maxSimilarShown` (10) similar
chunks, whatever the answer holds. For noisy similar code, raise the
embedder's `similarFloor` or `gateway.lexicalShare`, or lower
`gateway.similarPerQuery`.

The agent's `search_code` tool can run `runner.searchCalls` (10) times a
review. Past that, it answers `the limit of 10 searches per review is
reached`. Each search shows `runner.searchShown` (4) chunks.

These values are bound together, and kritika refuses a set that breaks a
rule:

- `contextPack.similarLimit` is at least `gateway.similarMax`.
- `runner.maxSimilarAnswerBody` (`1Mi`) holds `contextPack.similarLimit`
  chunks of `indexer.maxChunkBytes`.
- `gateway.maxSimilarRequest` (`256Ki`) holds `contextPack.similarQueries`
  queries of `contextPack.similarQueryChars`.
- `gateway.similarWithin` (`50s`) is less than `runner.similarTimeout`
  (`1m`). The gateway ends its searches by then, so that the answer gets
  back before the run stops waiting. Raise both together.

Similar code is best effort. When it fails, the run logs `similar-code
retrieval skipped` and the review goes on without it.

Reference: [`tuning.contextPack`](/configuration/#tuningcontextpack),
[`tuning.gateway`](/configuration/#tuninggateway),
[`tuning.runner`](/configuration/#tuningrunner) and
[`tuning.repoConfig`](/configuration/#tuningrepoconfig).

## Large pull requests

Signs that you hit a limit, in the review's summary notes:

- `Reviewed in N parts, each by an agent of its own`. The diff was over
  `review.partBytes`, so the review was split.
- `N diff file(s) left out of the prompt to fit its budget, for the agent
  to read with read_diff`. Nothing is lost: the agent reads those files'
  diff with a tool. A larger agent `prompt` puts more of the diff in the
  prompt from the start.
- `N diff file(s) too large to keep, so findings in them have no line to
  attach to`. The run also logs `diff too large to keep whole`. The diff
  was over `runner.maxPackDiffBytes`.

| Value                                             | Default   | What it bounds                                                                                                                                              |
| ------------------------------------------------- | --------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------- |
| the agent's `parts`                               | 8         | the most parts a review is split into. When a diff needs more, the parts grow until this many hold it, so no file is left out                               |
| `review.partBytes`                                | `64Ki`    | the diff in one part. Smaller parts give each agent fewer files to read with care, but each part is an agent run of its own, in tokens                      |
| `runner.maxPackDiffBytes`                         | `4Mi`     | the diff, and the delta diff, that a review keeps. The service reads it whole on every publish and follow-up, so it costs serve's memory and database space |
| `runner.noteOmittedPaths`                         | 5         | how many paths a note names before it counts the rest                                                                                                       |
| `runSpec.maxPriorFindings`                        | 200       | the last review's findings, and the dismissed ones, that a re-review is given                                                                               |
| `runSpec.maxBodyBytes`                            | `64Ki`    | the pull request's description that a run is given                                                                                                          |
| `review.maxLinkedIssues`, `runSpec.maxIssueBytes` | 3, `16Ki` | the linked issues that a review reads, and each one's body                                                                                                  |

The parts of a split review run at once only as far as the review model's
provider [`concurrency`](/models/#concurrency) has free slots. A review's
job timeout allows for the agent's most `parts`, up to
`jobTimeout.maxJobTimeout`.

`gateway.maxReportBody` (`64Mi`) must hold two kinds of report, each on its
own:

- A context report: two diffs of `runner.maxPackDiffBytes` and a pack of
  `contextPack.maxPackBytes`, each JSON-escaped up to sixfold, and room for
  its other fields.
- An agent report: a base64-encoded conversation of
  `gateway.maxConversationBytes` (`16Mi`), and room for its other fields.

Recheck it when raising any of those three; `kritika config check` names
the bound that it needs.

A run's spec travels in a Kubernetes Secret, so `runSpec.maxBytes` and
`runSpec.maxPromptBytes` have little room to grow. See
[leave these alone](#limits-set-by-something-else).

The most files that an incremental re-review takes is the repository's
[`review.incremental`](/configuration/#incremental-reviews) setting, with
`configFile.defaultMaxDeltaFiles` (25) as its fallback.

Reference: [`tuning.review`](/configuration/#tuningreview),
[`tuning.runner`](/configuration/#tuningrunner) and
[`tuning.runSpec`](/configuration/#tuningrunspec).

## Slow or unreliable model providers

Signs: `kritika_model_calls_total` counts calls with `outcome="error"`, and
reviews fail. A review whose agent ran out of room says so in its
summary: `The review's agent was told to submit what it had found …, so it
may not cover all it meant to check`. The middle names the limit: `on the
last step it may take` is the agent's `steps`, `with 90% of its tokens
spent` is its `tokens`, and `as its time ran out` is its `timeout`. Those
are keys of [the agent](/configuration/#the-agent), not tuning values.

Set the provider's own keys first. Its
[`timeout`](/models/#timeouts) fits a slow self-hosted model, and its
[`retries`](/models/#retries), 0 unless set, fit a flaky route. The tuning
values below are their fallbacks and bounds.

Each layer below must outlast the one inside it:

| Layer                        | Set by                                                                                                                                                                                                                                                                    |
| ---------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| one request to the provider  | the provider's `timeout`. `model.defaultStepTimeout` (`5m`) unless set, at most `configFile.maxProviderTimeout` (`30m`)                                                                                                                                                   |
| a step's attempts            | the provider's `retries`, at most `configFile.maxProviderRetries` (5). The waits run from `adapter.retryMin` (`1s`), doubling, to `adapter.retryMax` (`30s`). A provider's `Retry-After` is waited out up to `adapter.retryAfterMax` (`1m`)                               |
| a step's budget              | the review model's `timeout`, plus its fallback's (or its own again, without a fallback on another provider), plus `model.stepBudgetWaits` (`2m`): 12 minutes at the defaults                                                                                             |
| the runner's wait for a step | the step's budget plus `model.gatewaySettle` (`5m`), in which the gateway charges and records the step                                                                                                                                                                    |
| a failed step sent again     | `agent.stepRetryWaits` (`30s,1m,2m,4m`), each while the agent's `timeout` allows                                                                                                                                                                                          |
| the agent's loop             | the agent's `timeout`. `configFile.defaultAgentTimeout` (`20m`) unless set, at most 2h35m at the defaults                                                                                                                                                                 |
| the runner Job               | the instance's `runner.jobTemplate.spec.activeDeadlineSeconds`, 900 seconds in the chart; `jobTimeout.runnerDeadline` (`15m`) only where that field is absent. At most 2h at the defaults. The agent's `timeout` plus `jobTimeout.agentFetchHeadroom` (`5m`) lengthens it |
| the River job                | the runner's deadline plus `jobTimeout.leaseWaitHeadroom` (`15m`) and `jobTimeout.publishHeadroom` (`5m`), at most `jobTimeout.maxJobTimeout` (`3h`). An index job adds `jobTimeout.indexWriteHeadroom` (`60m`) instead                                                   |

The two "at most" values derive from `jobTimeout.maxJobTimeout` less the
headrooms. Raise `jobTimeout.maxJobTimeout` only if an agent `timeout` or a
runner deadline must go past them. A longer job holds a worker slot for
longer, and River waits `jobTimeout.maxJobTimeout` plus
`jobTimeout.rescueGrace` (`1h`), never under 4 hours, before it treats a
running job as abandoned. Under Helm, the chart's schema caps
`runner.jobTemplate.spec.activeDeadlineSeconds` at 7200 whatever
`jobTimeout.maxJobTimeout` allows.

For a provider outage longer than its retries, add to or lengthen
`agent.stepRetryWaits`. The waits spend the agent's own time, so a longer
list is worth it only with a `timeout` that leaves room for it.

kritika refuses these combinations:

- `model.defaultStepTimeout` over `configFile.maxProviderTimeout`.
- `adapter.chargeTimeout` and `adapter.recordTimeout` together over
  `model.gatewaySettle`, and `adapter.reservationPad` over it.
- `worker.confidenceTimeout` (`2m`) or `worker.mergeTimeout` (`100s`)
  over `jobTimeout.publishHeadroom`.
- `configFile.defaultAgentTimeout` over the agent timeout that
  `jobTimeout` leaves, and `jobTimeout.runnerDeadline` over the runner
  deadline that it leaves.

Two more values matter for particular providers:

- `agent.maxOutputTokensPerStep` (16384) is the most output that one step
  can produce. A reasoning model at a high
  [`effort`](/models/#effort) can spend it before it calls a tool.
- `model.planPause` (`15m`) is how long kritika sends nothing on a
  [ChatGPT plan](/models/#chatgpt-plans) after the plan refused a step for
  its usage limit.

Reference: [`tuning.model`](/configuration/#tuningmodel),
[`tuning.adapter`](/configuration/#tuningadapter),
[`tuning.agent`](/configuration/#tuningagent),
[`tuning.jobTimeout`](/configuration/#tuningjobtimeout) and
[`tuning.configFile`](/configuration/#tuningconfigfile).

## Forge rate limits and large installations

Signs:

- `kritika_forge_rate_limits_total` counts GitHub responses refused for a
  rate limit. `outcome="refused"` means the wait was too long to take in
  place, and the job is retried later.
- The poller logs `default branches not polled past the rate limit`.
- `kritika_polls_total` counts polls with `outcome="error"`.

Make sure webhooks arrive first, so that the poll is only a backstop, and
then lengthen `pollInterval`. The tuning values below trim what each poll
and request spends.

| Value                       | Default | What it bounds                                                                                                                              |
| --------------------------- | ------- | ------------------------------------------------------------------------------------------------------------------------------------------- |
| `gitHub.rateLimitMaxWait`   | `1m`    | the longest rate-limit wait that a request waits out in place. A longer one holds a worker for longer. Past it, River retries the job later |
| `poller.reactionPulls`      | 30      | the pull requests whose reactions one poll reads, per account                                                                               |
| `poller.reactionWindow`     | `168h`  | how long after its latest review a pull request's reactions are read                                                                        |
| `poller.maxCloseChecks`     | 50      | the pull requests that one poll of a repository asks about one by one. The rest wait for the next poll                                      |
| `poller.tipCheckInterval`   | `1h`    | how often default branches are checked when webhooks arrive. Each check is one request per indexed repository                               |
| `poller.forgeRateLimitWait` | `1m`    | the pause of those checks after a 429 that names no wait                                                                                    |
| `gitHub.maxListedComments`  | 5000    | the comments read of one pull request, the oldest first                                                                                     |
| `gitLab.pageSize`           | 100     | items per page. GitLab serves at most 100                                                                                                   |
| `forgejo.pageSize`          | 50      | items per page. The instance caps it at its own largest page                                                                                |

Reference: [`tuning.poller`](/configuration/#tuningpoller),
[`tuning.gitHub`](/configuration/#tuninggithub),
[`tuning.gitLab`](/configuration/#tuninggitlab) and
[`tuning.forgejo`](/configuration/#tuningforgejo).

## Database load and growth

Signs:

- `kritika_db_pool_empty_acquires_total` counts up, or
  `kritika_db_pool_connections` sits at `kritika_db_pool_max_connections`.
- Postgres cancels statements for their `statement_timeout`.
- The leader logs `retention sweep failed`, with the sweep's name.

Retention settings and the connection URI come first. A `pool_max_conns`
in a URI replaces the pool's ceiling, and
[connections](/database/#connections) lists every connection that kritika
opens.

| Value                                                                | Default               | What it bounds                                                                                                                                                                    |
| -------------------------------------------------------------------- | --------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `store.appPoolMaxConns`, `store.ownerPoolMaxConns`                   | 16, 4                 | each replica's pools when the URI sets no `pool_max_conns`. Multiply by the replicas, and keep it within Postgres's `max_connections`                                             |
| `store.appStatementTimeout`                                          | `1m`                  | every statement on the application pool, which River, webhooks, the gateway and the dashboard share. A longer one lets a slow dashboard query hold connections that the rest need |
| `store.ownerStatementTimeout`                                        | `10m`                 | the owner pool's statements, such as a migration that builds an index                                                                                                             |
| `store.modelCallSweepBatch`, `store.diffSweepBatch`                  | 5000, 200             | rows per statement of a retention sweep. Lower them if a sweep's statements time out                                                                                              |
| `worker.retentionSweepInterval`                                      | `1h`                  | how often the leader sweeps                                                                                                                                                       |
| `transcript.toolResultCap`, `transcript.rowCap`, `transcript.runCap` | `64Ki`, `1Mi`, `16Mi` | what a run's transcript stores. Past `runCap`, later rows keep only the response and usage                                                                                        |
| `worker.conversationRetention`                                       | `2h`                  | how long an agent's conversation is kept for a re-review to carry on                                                                                                              |
| `executor.logTailBytes`                                              | `64Ki`                | the runner log kept for each run                                                                                                                                                  |
| `webApi.maxUsageWindow`                                              | `8784h`               | the span of a usage or analytics request, which reads every row in it and in the span before                                                                                      |

`worker.minRetention` (`24h`) is the shortest `transcriptRetention` and
`diffRetention` that the configuration takes.

Reference: [`tuning.store`](/configuration/#tuningstore),
[`tuning.transcript`](/configuration/#tuningtranscript) and
[`tuning.worker`](/configuration/#tuningworker).

## Throughput, replicas and the queue

Signs: `kritika_review_snoozes_total` and `kritika_lease_wait_seconds`
grow, the `KritikaQueueDelayed` alert fires, or
`kritika_queue_oldest_runnable_seconds` climbs.

Tuning values do not add throughput. The levers are the provider's
`concurrency`, `reviewWorkers`, `indexWorkers`, `threadWorkers` and the
number of replicas. Leave the values below alone. They keep a job from
running twice and an answer that was already paid for from being lost, and
they are tied to each other:

- **Job liveness.** A replica beats on its jobs every
  `jobTimeout.heartbeatInterval` (`30s`). A job that misses
  `jobTimeout.abandonedBeats` (6) beats, 3 minutes, belongs to a dead
  replica and is handed to another. The replica fences its own job one beat
  sooner, at 2m30s, so that a replica cut off from the database never has
  its job worked twice. kritika refuses fewer than 2 beats. Shorter beats
  hand a dead replica's jobs on sooner, and end live jobs on a slow
  database.
- **Runner liveness.** A runner beats every `runSpec.heartbeatInterval`
  (`15s`). It counts as dead after `runSpec.reportRetryWindow` (`3m`) plus
  `runSpec.reportTimeout` (`2m`) plus two beats, 5m30s, which outlasts a
  gateway restart in a rollout. Shorter values end runs whose answer is
  still on its way.
- **Model slots.** A slot's holder renews it every `store.leaseHeartbeat`
  (`30s`). A slot not renewed for `store.leaseExpiry` (`2m`) is free to
  take, and kritika refuses an expiry that is not longer than the
  heartbeat. `store.leasePollMin` and `store.leasePollMax`, and
  `worker.snoozeMin` and `worker.snoozeMax`, set how often waiters look
  again. A slot let go wakes them sooner anyway.
- **Rescue.** The leader looks for a dead replica's jobs and runs every
  `worker.rescueInterval` (`30s`).

One value here does need care on some clusters. `executor.startGrace`
(`3m`) is how long a runner pod can stay unscheduled or unable to start
before its run is given up. Raise it when a cluster autoscaler takes longer
than that to bring a node up.

Reference: [`tuning.jobTimeout`](/configuration/#tuningjobtimeout),
[`tuning.runSpec`](/configuration/#tuningrunspec),
[`tuning.store`](/configuration/#tuningstore),
[`tuning.worker`](/configuration/#tuningworker) and
[`tuning.executor`](/configuration/#tuningexecutor).

## Shutdown and rollouts

Signs: serve logs `the queues did not stop in time`, reviews are retried
after a rollout, or pods are killed before they exit.

On stopping, serve runs three paths side by side. The job queues take
`process.queueDrain` (`100s`), `process.queueStopHeadroom` (`90s`) and
`process.eventsDrain` (`5s`) one after another, 195 seconds at the
defaults. The gateway and the public listener first keep taking
connections for `process.linger` (`5s`), while traffic moves off the pod.
Then the gateway lets model steps in flight finish for `gateway.drain`
(`2m`), and the public listener lets requests finish for
`process.publicDrain` (`10s`): 125 and 15 seconds at the defaults. The
longest path, with room for the process to exit, must fit within the
pod's `terminationGracePeriodSeconds`, 200 in the chart. Raise that when
you raise any of these. With ChatGPT providers, the leader's token
refresh is a fourth path: it runs on past the stop for up to
`worker.chatGptRefreshTimeout` (`1m`) to save the token it rotated, then
releases its claim within as long again.

On the first rollout of this release, keep every job within 4 hours, the
rescue horizon from before it was tuned, until every serve replica runs
it: a River leader from before this release judges a running job by its
own 3-hour timeout and rescues it at 4 hours. Let any longer job finish
before rolling back to an earlier release.

A shorter `process.queueDrain` cuts more running reviews, which are
retried. A shorter `gateway.drain` cuts more model steps. A cut step is
paid for but not recorded, and the runner's retry is paid for again.

Reference: [`tuning.process`](/configuration/#tuningprocess) and
[`tuning.gateway`](/configuration/#tuninggateway).

## The dashboard and the API

| Value                                              | Default   | What it bounds                                                                                              |
| -------------------------------------------------- | --------- | ----------------------------------------------------------------------------------------------------------- |
| `webApi.defaultLimit`, `webApi.maxLimit`           | 50, 200   | items in a list, without and with `?limit=`. The default is at most the maximum                             |
| `webApi.exportLimit`                               | 1000      | the findings in one export, the newest                                                                      |
| `webApi.heartbeatInterval`                         | `25s`     | how often an idle event stream sends a comment. Lower it below a proxy's idle timeout if live updates drop  |
| `auth.maxFailedSignIns`, `auth.failedSignInWindow` | 10, `15m` | wrong passwords from one client address before sign-in answers `Too many failed attempts. Try again later.` |

Behind a proxy, every client can share one address, so the sign-in limit
applies to the whole instance. Raising it lets password guessing go
faster. A dashboard session's length is `auth.sessionTTL` in the
configuration file.

Some values are mirrored where kritika cannot change them. The dashboard's
token form offers the default `webApi.tokenLifetimes` (never, 7, 30, 90
and 365 days) and checks the default `webApi.tokenNameMax` of 100
characters. Some dashboard pages ask for a fixed number of items, which
`webApi.maxLimit` clamps. The published
[repository schema](https://kritika.home-operations.com/kritika.schema.json)
states the default `configFile.maxReviewers` (8),
`configFile.maxReviewerChars` (8000) and `configFile.maxRuleChars` (2000).
Changing those values updates neither the dashboard nor the schema.

Reference: [`tuning.webApi`](/configuration/#tuningwebapi) and
[`tuning.auth`](/configuration/#tuningauth).

## Leave these alone

### Safety bounds

These values bound what untrusted or unchecked input can make kritika do.
Each default leaves legitimate use its room, so raising one mostly widens
what a bad input can cost.

| Values                                                                                                                                        | What they guard against                                                                |
| --------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------- |
| `aqua.maxDownloadBytes`, `maxUnpackedBytes`, `maxEntries`, `maxSignatureBytes`, `maxChecksumFileBytes`, `maxReleaseBytes`, `maxRegistryBytes` | a repository tool's download that is too large, or an archive that expands without end |
| `review.maxIterations`, `review.maxMeasureDepth`, `review.renderTimeout`                                                                      | a repository's comment template that loops, nests or runs without end                  |
| `prFilter.evalCostLimit`, `roleMap.evalCostLimit`                                                                                             | a CEL expression that costs too much to evaluate                                       |
| `auth.maxFailedSignIns`, `auth.failedSignInWindow`, `auth.maxTrackedClients`, `auth.maxLocalBody`, `auth.maxApiBody`, `store.maxLoginStates`  | password guessing, and sign-ins that anyone can start without a session                |
| `webhook.maxBody`                                                                                                                             | an oversized delivery. `kritika_webhooks_total` counts one as `too_large`              |
| `webhook.gitLabTolerance`                                                                                                                     | a replayed GitLab delivery                                                             |
| `webApi.maxBodyBytes`                                                                                                                         | an oversized API request                                                               |
| `gateway.maxConversationBytes`, `maxReportBody`, `maxCheckpointBody`, `maxSmallReport`, `runner.maxReportAnswer`                              | what a runner can make serve hold in memory                                            |
| `store.refusalsPerRun`                                                                                                                        | a looping agent that fills the egress refusal table                                    |

`aqua.maxDownloadBytes` (`1Gi`) is the one to raise if a tool you need
ships a larger release. The run logs `tool not installed` with the error.

### Limits set by something else

kritika refuses these past what the outside limit allows:

- `review.maxRenderBytes` cannot exceed 65,536, the longest comment that
  GitHub takes.
- `runSpec.maxBytes` must be less than 1 MiB, the most that a Kubernetes
  Secret holds, with the run's credentials beside it.
  `runSpec.maxPromptBytes` must be less than `runSpec.maxBytes`.
- `gitHub.appJwtLifetime` cannot exceed GitHub's 10 minutes, and must
  exceed `gitHub.tokenRefreshLead`.
- `store.keepaliveIdle` and `store.keepaliveInterval` must be at least a
  second, since Postgres counts them in whole seconds.

Others are capped by the service on the other end whatever they say:
GitHub inlines a file only up to 1 MiB whatever `forge.maxFileBytes` says,
and GitLab serves at most 100 items a page.

## Changing a value safely

1. Put the value in one place: the file's `tuning` block (`config.tuning`
   under Helm) or its `KRITIKA_TUNING_*` variable. A value in both is
   refused at startup.
2. Run [`kritika config check`](/configuration/#checking-a-file-before-deploying)
   on the file. It validates every tuning value, alone and against the
   others, as `serve` does at startup. It reads the environment, so set
   the same `KRITIKA_TUNING_*` variables that serve gets.
3. Read the error. It names each value by its file path and its
   variable, as in this refusal of `indexer.maxChunks: 600000` (the
   `error` field of the `kritika exited` line):

   ```text
   config: tuning.gateway.maxIndexBody: KRITIKA_TUNING_GATEWAY_MAX_INDEX_BODY (2Gi) must hold an index upload's opening line of up to tuning.gateway.maxIndexLine: KRITIKA_TUNING_GATEWAY_MAX_INDEX_LINE and tuning.indexer.maxChunks: KRITIKA_TUNING_INDEXER_MAX_CHUNKS chunks of tuning.indexer.maxChunkBytes: KRITIKA_TUNING_INDEXER_MAX_CHUNK_BYTES and tuning.indexer.maxSignatureBytes: KRITIKA_TUNING_INDEXER_MAX_SIGNATURE_BYTES, as the upload encodes them, 8702354432 bytes
   ```

4. Set it on serve only. serve hands every value that differs from its
   default to the runner Jobs that it creates. `runner.podTemplate`'s
   `env` is for the run tool's commands, not for the runner.
5. Roll out. kritika reads its values at startup, and Helm rolls the pods
   when `config` changes. During a rolling update both revisions serve at
   once: a runner Job keeps the values of the replica that created it, and
   the gateway Service sends its requests to replicas of either revision.
   So a change to a limit that a runner and the gateway must agree on,
   such as `gateway.maxIndexBody`, `gateway.maxReportBody`,
   `contextPack.similarLimit` or a `runner.*` request size, can have an old
   replica refuse a new Job's request, or a new replica an old Job's.
   Where the runner's side has a setting of its own, stage the change in
   two rollouts that each keep both revisions compatible: raise the
   gateway's limit, then the runner's; lower the runner's, then the
   gateway's. A cap that both sides read from one setting has no such
   step: the runner checks an index upload against `gateway.maxIndexBody`
   itself, for one. For those, stop new work and let running Jobs finish
   before rolling the change out.
6. Check the symptom again. To undo a change, remove the key.
7. After a change to a value that shapes what an index build stores, as
   [Large repositories and monorepos](#large-repositories-and-monorepos)
   lists them, reindex the repositories that it is for. Existing indexes
   keep what they were built with.
