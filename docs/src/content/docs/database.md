---
title: Postgres with CloudNativePG
---

kritika keeps everything it knows in one Postgres database: reviews, findings,
the job queue, transcripts and the similar-code index. The chart supports that
database as a [CloudNativePG](https://cloudnative-pg.io/docs/1.30/) (CNPG)
cluster in the same Kubernetes cluster, which this page sets up.

## What kritika needs

- **Postgres 18 with VectorChord.** The `vchord`
  ([VectorChord](https://github.com/tensorchord/VectorChord)) and `vector`
  (pgvector) extensions must exist before the first start. `vchord` builds
  on the types from pgvector, and it must be in `shared_preload_libraries`.
- **Three roles**, which the chart requires and kritika checks at start:
  - the **owner** runs migrations and applies the configuration on the
    leader. It owns the database, and kritika refuses an owner that is a
    superuser;
  - the **application** role (`database.app.role`, `kritika_app` by
    default) serves everything else. kritika refuses an application role
    that owns a table, is a superuser or has `BYPASSRLS`. Any of these
    would turn row-level security off;
  - the **runner** role (`database.runner.role`, `kritika_runner` by
    default) is handed to runner Jobs and can only write its own run.
- **Direct or session-mode connections** for the owner and application
  roles. The leader is whichever replica holds a session advisory lock on its
  owner connection. The dashboard's live updates and the job queue's
  notifications `LISTEN` on the application connection. A transaction-mode
  pooler, such as PgBouncer or a CNPG `Pooler` with `poolMode: transaction`,
  hands a session to other clients between transactions. That breaks these
  sessions and the review publication locks.

## The cluster

CNPG's own `standard` image ships pgvector. VectorChord mounts into it from
TensorChord's
[`vchord-scratch`](https://docs.vectorchord.ai/vectorchord/admin/kubernetes.html)
image as an
[image volume extension](https://cloudnative-pg.io/docs/1.30/imagevolume_extensions/).
The two-instance example favors availability when its standby is unavailable.
While synchronous replication is active and commits use
`synchronous_commit=on`, acknowledgement waits for the standby to flush WAL.
With `dataDurability: preferred`, that requirement can be relaxed. If the
primary fails before those writes reach a surviving standby, acknowledged
changes can be lost. This is not an unconditional zero-data-loss failover
configuration.

```yaml
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: kritika-postgres
spec:
  instances: 2
  imageName: ghcr.io/cloudnative-pg/postgresql:18.6-standard-bookworm
  enableSuperuserAccess: false
  postgresql:
    extensions:
      - name: vchord
        image:
          reference: ghcr.io/tensorchord/vchord-scratch:pg18-v1.1.1
        # The image keeps Debian's paths rather than CNPG's default
        # /extensions/vchord/lib and /extensions/vchord/share.
        dynamic_library_path:
          - /usr/lib/postgresql/18/lib/
        extension_control_path:
          - /usr/share/postgresql/18/
    shared_preload_libraries:
      - vchord
    # Wait for the healthy standby; allow writes without it when unavailable.
    # Those writes can be lost if the primary fails before replication catches up.
    synchronous:
      method: any
      number: 1
      dataDurability: preferred
  primaryUpdateStrategy: unsupervised
  # Update the standby first, then switch over to it, rather than restart
  # the primary in place.
  primaryUpdateMethod: switchover
  affinity:
    podAntiAffinityType: required
  storage:
    size: 10Gi
  bootstrap:
    initdb:
      database: kritika
      owner: kritika
      secret:
        name: kritika-postgres-credentials
```

- Image volume extensions need Kubernetes 1.35 (or 1.33 and 1.34 with the
  `ImageVolume` feature gate on) and containerd 2.1 or CRI-O 1.31. Without
  them, use TensorChord's
  [`cloudnative-vectorchord`](https://github.com/tensorchord/cloudnative-vectorchord)
  image instead and leave out `extensions`. That image has VectorChord built
  in and is tagged `<postgres>-<vchord>` (`18.6-1.1.1`).
- `dataDurability: preferred` relaxes the synchronous requirement while the
  standby is unavailable. `required` keeps the requested synchronous quorum
  and blocks commits without enough standbys. Neither replaces backups or
  protects against losing all copies. Choose this policy based on recovery
  objectives; see
  [CNPG's durability tradeoff](https://cloudnative-pg.io/docs/1.30/replication/).
- `primaryUpdateMethod: switchover` rejects an update that changes the image
  and the Postgres parameters at once; apply them one at a time.
- `podAntiAffinityType: required` keeps the two instances on different nodes.
  CNPG's default, `preferred`, lets them share one when the scheduler finds
  no other, and then that node takes both down.
- Each instance has its own volume and Postgres replicates between them, so
  the storage class need not replicate as well: a node-local one is enough.
- The bootstrap owner, `kritika`, is the owner role.

A `Database` resource creates the extensions. The operator runs it as
superuser, so the owner never needs that privilege. The `version` of
`vchord` is the version CNPG installs. When that value changes, CNPG updates
the extension to the new version, so move it with the `vchord-scratch` tag:

```yaml
apiVersion: postgresql.cnpg.io/v1
kind: Database
metadata:
  name: kritika
spec:
  cluster:
    name: kritika-postgres
  name: kritika
  owner: kritika
  extensions:
    - name: vector
      ensure: present
    - name: vchord
      version: "1.1.1"
      ensure: present
```

The application and runner roles are `DatabaseRole` resources, which CNPG
recommends over the Cluster's `managed.roles`
([declarative role management](https://cloudnative-pg.io/docs/1.30/declarative_role_management/)).
kritika's migrations grant them what they need:

```yaml
apiVersion: postgresql.cnpg.io/v1
kind: DatabaseRole
metadata:
  name: kritika-app
spec:
  cluster:
    name: kritika-postgres
  name: kritika_app
  login: true
  passwordSecret:
    name: kritika-postgres-app
  # Deleting this resource leaves the role, so a running kritika keeps its
  # connections.
  databaseRoleReclaimPolicy: retain
---
apiVersion: postgresql.cnpg.io/v1
kind: DatabaseRole
metadata:
  name: kritika-runner
spec:
  cluster:
    name: kritika-postgres
  name: kritika_runner
  login: true
  passwordSecret:
    name: kritika-postgres-runner
  databaseRoleReclaimPolicy: retain
```

## Secrets and connection URIs

Each role's password Secret is a `kubernetes.io/basic-auth` Secret with
`username` and `password`, as CNPG expects. The chart reads a connection
URI from a `uri` key, so put it in the same Secret:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: kritika-postgres-app
type: kubernetes.io/basic-auth
stringData:
  username: kritika_app
  password: <password>
  uri: postgres://kritika_app:<password>@kritika-postgres-rw.kritika.svc:5432/kritika?sslmode=require&connect_timeout=10
```

The owner's Secret, `kritika-postgres-credentials`, and the runner's,
`kritika-postgres-runner`, look the same, each with its own role. A password
generator, such as an External Secrets `Password` generator feeding a
templated `ExternalSecret`, can produce all three, `uri` included. Keep the
generated passwords free of characters that need escaping in a URI, or
escape them.

Set `connect_timeout`. Without it, a connection attempt waits as long as the
kernel does. After an undeploy and a quick redeploy, a pod's first dial can
go to a Service address that DNS still caches but that no longer exists.
That dial hangs for minutes before kritika retries. With a timeout, the
attempt fails fast and the retry looks the name up again.

Point the chart at the three Secrets:

```yaml
database:
  owner:
    existingSecret: kritika-postgres-credentials
  app:
    existingSecret: kritika-postgres-app
  runner:
    existingSecret: kritika-postgres-runner
```

`database.app.role` and `database.runner.role` must name the roles the URIs
log in as, if they are not `kritika_app` and `kritika_runner`.

The chart's NetworkPolicies are on by default. With them, kritika serve and
runner pods reach the Postgres port only where `networkPolicy.postgres` says
the database is. Unless `networkPolicy.postgres` is set, that is any pod in
the release namespace. Name the cluster's pods by the label CNPG gives them.
Add their namespace when it is not the release namespace. CNPG gives a
`Pooler`'s pods the same label, so the selector also covers a URI that goes
through a session-mode pooler:

```yaml
networkPolicy:
  postgres:
    podSelector:
      cnpg.io/cluster: kritika-postgres
```

The selector must take in every pod the three URIs connect to. When the
database is not in the release namespace, set these before installing or
upgrading. Otherwise neither kritika serve nor a run can connect, and a new
replica never becomes ready.

## Connections

Postgres allows 100 connections unless `max_connections` says otherwise.
Connections open on demand, so an idle replica holds few:

| Connections              | Per        | At most                                                                                                                                                                                                                                    | `statement_timeout`                                                                      | `application_name`    |
| ------------------------ | ---------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ---------------------------------------------------------------------------------------- | --------------------- |
| the application pool     | replica    | 16                                                                                                                                                                                                                                         | one minute, so a slow dashboard query cannot hold the connections webhooks and jobs need | `kritika-app`         |
| the owner pool           | replica    | 4                                                                                                                                                                                                                                          | ten minutes, for a migration that builds an index                                        | `kritika-owner`       |
| the `LISTEN` connection  | replica    | 1                                                                                                                                                                                                                                          | one minute, as the application pool's                                                    | `kritika-listen`      |
| review publication locks | replica    | 3 × `KRITIKA_REVIEW_WORKERS` (2 unless set) + `KRITIKA_THREAD_WORKERS` (1 unless set), and one more on the leader; another `KRITIKA_REVIEW_WORKERS` while thread jobs wait on `followup` ([queue upgrades](#queue-upgrades-and-rollbacks)) | one minute                                                                               | `kritika-publication` |
| a runner's pool          | runner Job | uses one or two                                                                                                                                                                                                                            | one minute, as the application pool's                                                    | `kritika-app`         |

Each active review publication holds a separate application-role connection
for its advisory lock, without an open transaction. This keeps slow forge
calls from occupying the application pool used by heartbeats and requests.
Include these connections when sizing `max_connections`.

A `pool_max_conns` in a URI replaces that pool's ceiling. Raise the
application pool's ceiling if jobs wait on the pool.
`kritika_db_pool_empty_acquires_total` counts those waits, and
`kritika_db_pool_connections` shows the pool's connections against the
ceiling ([metrics](/metrics/)). For more replicas, raise `max_connections`
under the Cluster's `postgresql.parameters`. A `pool_ping_timeout` in a URI
replaces that pool's five-second bound on a ping of an idle connection.
CNPG's `cnpg_backends_total` metric shows what each `application_name`
holds. Runner Jobs connect as `kritika-app` too, so that name counts their
connections together with the replicas' application pools.

The leader runs hourly retention sweeps of transcripts, egress refusals,
stored diffs and agent conversations. The sweeps work in batches, and each
batch is its own statement. A backlog larger than one statement could clear
is worked off over the sweep rather than rolled back.

Prometheus scrapes CNPG's metrics through a `PodMonitor`. CNPG deprecates the
Cluster's `monitoring.enablePodMonitor` in favor of one you create
([monitoring](https://cloudnative-pg.io/docs/1.30/monitoring/)):

```yaml
apiVersion: monitoring.coreos.com/v1
kind: PodMonitor
metadata:
  name: kritika-postgres
spec:
  selector:
    matchLabels:
      cnpg.io/cluster: kritika-postgres
  podMetricsEndpoints:
    - port: metrics
```

## Failover and maintenance

When the primary changes, by a failover or a switchover, kritika's
connections drop and reconnect on their own. The replica that held the leader
lock loses it with its owner connection and steps down. Another replica takes
the lock within one `KRITIKA_LEADER_RETRY_INTERVAL`, which is 15 seconds
unless set. Until then, the leader's duties and job fetching pause.
No kritika pod restarts.

These application reconnect intervals are not an end-to-end recovery-time
promise. CNPG must first detect the failure, promote a usable standby, and
make the service reachable. Storage, scheduling and replication state also
matter.

kritika's pools ask Postgres for TCP keepalives: 30s idle, 10s apart, three
missed. A kritika pod that dies without closing its sockets therefore loses
its session, and with it the leader lock, within about a minute. The
Cluster's own `tcp_keepalives_*` parameters are not involved.

In the other direction, a primary whose node dies answers nothing. kritika
bounds every ping of an idle pooled connection and every lock attempt to five
seconds. The leader therefore steps down within one
`KRITIKA_LEADER_RETRY_INTERVAL` plus that bound, and a standby leads once its
connections reach the new primary.

A switchover terminates the old primary's sessions outright, so the handover
takes one retry interval at most. During that interval, the new leader may
start its duties before the old one has noticed it lost the lock. For that
reason, `KritikaMultipleLeaders` in the chart's alerts waits longer than one
interval.

CNPG's disruption budget for the primary allows no eviction, so draining the
primary's node waits until the primary moves. Switch over first, with the
[`cnpg` kubectl plugin](https://cloudnative-pg.io/docs/1.30/kubectl-plugin/):

```sh
kubectl cnpg status kritika-postgres -n kritika
kubectl cnpg promote kritika-postgres kritika-postgres-2 -n kritika
```

## Backups

The configuration lives in git and the similar-code index can be rebuilt, but
reviews, findings, dismissals, feedback, usage, the audit log and queued jobs
exist in the database. Embedding checkpoints and staging save repeated work,
but they are also database state. Losing them can repeat paid embedding
calls. CNPG backs a cluster up two ways
([backups](https://cloudnative-pg.io/docs/1.30/backup/)):

- **Object storage** through the
  [Barman Cloud plugin](https://cloudnative-pg.io/plugin-barman-cloud/),
  which also archives WAL for point-in-time recovery. The Cluster's native
  `barmanObjectStore` does the same but is deprecated since CNPG 1.26.
- **Volume snapshots** through the storage class's CSI driver
  ([volume snapshots](https://cloudnative-pg.io/docs/1.30/appendixes/backup_volumesnapshot/)),
  with `backup.volumeSnapshot` on the Cluster and a `ScheduledBackup`. They
  stay in the same storage system, and point-in-time recovery still needs a
  WAL archive in object storage.

A replica is not a backup: it copies a mistake as fast as anything else.

Backups hold the roles but not their Secrets. A restored cluster's roles
keep the passwords they had when the backup was taken
([recovery](https://cloudnative-pg.io/docs/1.30/recovery/)). Name the owner's
Secret in `bootstrap.recovery`, with `database: kritika` and `owner: kritika`.
Once recovery completes, CNPG sets the owner's password from it. The
`DatabaseRole`s apply the passwords from their own Secrets the same way.
kritika's `uri` keys then match, whether the Secrets were kept or generated
anew.

## Recovery objectives and verification

Recovery-point objective (RPO) is the amount of data loss the deployment
accepts. Recovery-time objective (RTO) is how long service may be
unavailable. Both are **undecided** for the deployment reviewed on
2026-10-12. No numerical recovery guarantee has been established, and the
replication policy was left unchanged.
With `preferred`, acknowledged queue inserts, review records and embedding
checkpoints share the same potential loss window as other database writes.

A read-only inspection on that date found:

| Area                 | Evidence                                                                                                              | Meaning / remaining gap                                                                                                                                    |
| -------------------- | --------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Replication policy   | Two instances; `method: any`, `number: 1`, `dataDurability: preferred`, `failoverQuorum: false`                       | Matches this example's availability preference; a zero-loss objective would require a separate policy decision and validation.                             |
| Live replication     | Both instances ready; primary `synchronous_commit=on`; one streaming quorum standby in `synchronous_standby_names`    | Synchronous replication was active at inspection time. This does not prove behavior during standby loss or failover.                                       |
| Backup configuration | No Cluster backup configuration or plugins; no CNPG `Backup` or `ScheduledBackup` resources for this cluster          | No CNPG-managed base-backup coverage was demonstrated. External backup systems were not verified.                                                          |
| WAL archiving        | `archive_mode=on`, `archive_timeout=5min`, CNPG archive hook, nonzero archive counters and `ContinuousArchiving=True` | Hook success and counters do not establish an independently stored, retrievable WAL chain. No archive destination was configured in the inspected Cluster. |
| Snapshots            | No `VolumeSnapshot` resources in the deployment namespace                                                             | No snapshot recovery point was demonstrated by this inspection.                                                                                            |
| Restore evidence     | No last-successful-backup or first-recoverability timestamp reported; no restore exercise performed                   | Restore coverage and measured RTO remain unverified. A healthy standby is not restore-test evidence.                                                       |

This is a dated observation, not a statement that the example manifest proves
production durability. Recheck the Cluster specification and status, backup
resources, snapshots, and PostgreSQL's `pg_stat_replication` and
`pg_stat_archiver` when deployment settings change.

Before claiming recoverability:

1. Decide acceptable RPO/RTO and whether writes should pause when synchronous
   standbys are unavailable. Compare those targets with the deployed policy and
   effective session settings, including any `synchronous_commit` overrides.
2. Establish and verify base backups and a continuous WAL archive in an
   independent failure domain, with retention sufficient for the intended
   recovery window. Check actual objects, recent backup results and archive
   failures, not just the presence of an archive hook. See
   [CNPG backup requirements](https://cloudnative-pg.io/docs/1.30/backup/).
3. Restore a selected recovery point into an isolated cluster and record the
   last recovered transaction and measured recovery time. Verify reviews,
   findings/dismissals, usage and queue rows; test application-role access and
   the restored embedding checkpoint's agreement with its vectors. Keep
   production workers and forge/provider egress disconnected during validation
   so restored jobs cannot post duplicate reviews or spend tokens.
4. Record the tested backup, recovery target, WAL coverage, result and remaining
   gaps. Rebuild vector indexes when appropriate, but do not substitute an index
   rebuild for recovery of durable application records. See
   [CNPG recovery procedures](https://cloudnative-pg.io/docs/1.30/recovery/).

The inspection did not perform failover, restore, or replication-policy changes.
Those are separate operational work after recovery objectives are agreed.

## Interrupted embedding builds

Index jobs retain completed staging and checkpoint each batch in the same
transaction as its vectors. A retry of the same River job resumes at the last
committed chunk, reusing its index and runner records. Compatibility includes
the target commit, incremental base, ignore rules, embedding endpoint/model,
dimensions and input limits. Changed inputs discard incompatible progress and
build anew. A runner that did not finish staging is restarted.

An embedding request can still be repeated if the process stops after the
provider handles it but before its vectors and checkpoint commit. Usage is
recorded separately when the provider reports it. Checkpoints do not promise
exactly-once external calls. No database transaction stays open during an
embedding request.

Publication still swaps or advances the generation atomically, so searches see
only a completed index. Forced rebuilds also complete their River job and enqueue
catch-up work in that transaction. Retryable failures retain partial vectors.
Completed or superseded work clears them as appropriate. The leader's bounded
recovery sweep removes remaining progress for completed, cancelled, discarded
or missing jobs. To restart a cancelled index, request a fresh reindex. The
pinned River version retains `cancel_attempted_at` when retrying a cancelled
job ID. Its cancellation polling and state transitions still treat that job
as cancelled. The worker keeps that cancellation fence rather than ignoring
the marker. Checkpoints inherit the database's replication and backup
limitations described above.

## Queue upgrades and rollbacks

The `thread` queue separates finding resolution/restoration from model
follow-ups. Releases that support it consume thread jobs on both `thread` and
`followup`. Queued jobs on `followup` still wait for a follow-up worker.
Uniqueness spans both queues. Workers check GitHub's current resolved state
and ignore superseded events. They keep the original event's sender
permission and outdated-line checks.

Older binaries apply the event state without that check. To avoid stale writes
from those workers during the transition, stop all older replicas before
starting the new release. This requires a service interruption. Arrange
webhook redelivery for deliveries that fail during it. A rolling upgrade
retains jobs, but cannot guarantee thread-state ordering while old workers
are still running.

A rollback to a release without the `thread` consumer leaves its queued jobs
in Postgres. They stay unprocessed and are absent from that release's queue
UI and metrics. To recover them without relying on those views:

1. Stop **all** replicas of the newer release, including webhook producers,
   and wait for their workers to exit. Do not run the update below while any
   worker could still complete a job.
2. Using the database owner connection, move unfinished thread jobs back to
   `followup`. The following transaction also makes interrupted running jobs
   available again. Retry schedules, arguments and uniqueness keys are
   retained.

   ```sql
   BEGIN;
   UPDATE river_job
   SET queue = 'followup',
       state = CASE WHEN state = 'running' THEN 'available'::river_job_state ELSE state END
   WHERE kind = 'thread' AND queue = 'thread'
     AND state IN ('available', 'pending', 'retryable', 'running', 'scheduled');
   SELECT state, count(*) FROM river_job
   WHERE kind = 'thread' AND queue = 'thread'
     AND state IN ('available', 'pending', 'retryable', 'running', 'scheduled')
   GROUP BY state;
   COMMIT;
   ```

3. Verify the query returned no unfinished `thread` rows, then start the older
   release. Redeliver webhooks that failed during the interruption. The older
   release's follow-up workers can execute the unchanged thread-job arguments.

The transaction can be repeated after the replicas are stopped. Completed,
cancelled and discarded history stays on its original queue. Rolling back also
restores the older worker's ordering limitations. Review affected finding
states if thread changes overlapped the interruption.

## Model-call accounting recovery

`model_invocations` records each logical model call before dispatch. For runner
calls, the record and token-budget reservation commit together. The provider's
outcome is then saved separately from settlement. Settlement replaces the
reservation with actual usage and inserts a usage row with the invocation's ID
and original timestamp, in one transaction. Repeating settlement cannot charge
the same invocation twice.

The leader's rescuer retries completed, unsettled outcomes. An expired call
without a saved outcome becomes `uncertain` and retains its reservation: the
provider may have charged for it. A late outcome can still settle that call.
Operators can inspect pending records with an owner connection:

```sql
SELECT id, account_id, runner_run_id, state, reserved_tokens, created_at
FROM model_invocations
WHERE state <> 'settled'
ORDER BY created_at;
```

Recovery never repeats a provider request. This ledger does not deduplicate
client retries or guarantee exactly-once provider execution, and it does not
reconstruct an outcome lost before persistence. Transcripts remain best effort
and are separate from accounting. The ledger contains usage metadata and token
hashes, not prompts, responses, or credentials.
