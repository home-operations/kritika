---
title: Postgres with CloudNativePG
---

kritika keeps everything it knows in one Postgres database: reviews, findings,
the job queue, transcripts and the similar-code index. The chart supports that
database as a [CloudNativePG](https://cloudnative-pg.io/docs/1.30/) (CNPG)
cluster in the same Kubernetes cluster, which this page sets up.

## What kritika needs

- kritika needs Postgres 18 with VectorChord. The `vchord`
  ([VectorChord](https://github.com/pgsty/vchord)) and `vector`
  (pgvector) extensions must exist before the first start. `vchord` builds
  on the types from pgvector, and it must be in `shared_preload_libraries`.
- kritika needs two roles. The chart requires them, and at start kritika
  makes sure that they are correct:
  - The owner runs migrations and applies the configuration on the
    leader. It owns the database, and kritika refuses an owner that is a
    superuser.
  - The application role (`database.app.role`, `kritika_app` by
    default) serves everything else. kritika refuses an application role
    that owns a table, is a superuser or has `BYPASSRLS`. Any of these
    turns row-level security off.
- kritika needs direct or session-mode connections for the owner and
  application roles. The leader is whichever replica holds a session advisory
  lock (a Postgres lock that lasts for the session) on its owner connection.
  The dashboard's live updates and the job queue's
  notifications `LISTEN` on the application connection. A transaction-mode
  pooler, such as PgBouncer or a CNPG `Pooler` with `poolMode: transaction`,
  hands a session to other clients between transactions. That breaks these
  sessions and the review publication locks.

## The cluster

CNPG's own `standard` image ships pgvector. VectorChord mounts into it from
the
[`ghcr.io/home-operations/vchord`](https://github.com/home-operations/containers/tree/main/apps/vchord)
image as an
[image volume extension](https://cloudnative-pg.io/docs/1.30/imagevolume_extensions/).
That image packages the releases of [pgsty/vchord](https://github.com/pgsty/vchord),
a maintained fork of VectorChord under AGPL-3.0-only. TensorChord's own
releases stop at 1.1.1.
The two-instance example favors availability when its standby is unavailable.
While synchronous replication is active and commits use
`synchronous_commit=on`, acknowledgement waits for the standby to flush WAL
(the write-ahead log of changes).
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
          reference: ghcr.io/home-operations/vchord:1.3.0
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
  in and is tagged `<postgres>-<vchord>` (`18.6-1.1.1`). It stays on
  TensorChord's 1.1.1, without the fork's fixes, so set the `Database`'s
  `vchord` version to `"1.1.1"` with it.
- `dataDurability: preferred` relaxes the synchronous requirement while the
  standby is unavailable. `required` keeps the requested synchronous quorum
  and blocks commits without enough standbys. Neither replaces backups or
  protects against losing all copies. Choose this policy based on recovery
  objectives. See
  [CNPG's durability tradeoff](https://cloudnative-pg.io/docs/1.30/replication/).
- `primaryUpdateMethod: switchover` rejects an update that changes the image
  and the Postgres parameters at once. Apply them one at a time.
- `podAntiAffinityType: required` keeps the two instances on different nodes.
  When the scheduler finds no other node, CNPG's default, `preferred`, lets
  them share one. Then that node takes both down.
- Each instance has its own volume and Postgres replicates between them, so
  the storage class need not replicate as well: a node-local one is enough.
- The bootstrap owner, `kritika`, is the owner role.

A `Database` resource creates the extensions. The operator runs it as
superuser, so the owner never needs that privilege. The `version` of
`vchord` is the version CNPG installs. When that value changes, CNPG updates
the extension to the new version, so move it with the `vchord` image tag:

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
      version: "1.3.0"
      ensure: present
```

### Upgrading VectorChord

Change the Cluster's image reference first, so that every instance restarts
with the new library, and then the `Database`'s `version`, so that CNPG
updates the extension. The new library works with the old extension in
between. 1.3.0 upgrades in place from 1.1.1, TensorChord's last release, and
keeps the on-disk index format. Outside CNPG, a superuser runs this in the
`kritika` database after the restart:

```sql
ALTER EXTENSION vchord UPDATE;
```

The upgrade leaves existing index pages as they are. kritika's embedding
index is a `halfvec` cosine index. One built before 1.2.0 on a CPU with fp16
arithmetic (ARM with fp16, or x86 with AVX512-FP16) can have lost recall to
an fp16 overflow. One built before 1.2.0 for one-dimensional vectors
(`embedding.dims: 1`) stores invalid metadata for them, and one built
before 1.3.0 that holds a zero vector stores it wrongly. Rebuild the index
after the upgrade when any of these can apply. The owner role can do it
without blocking writes:

```sql
REINDEX INDEX CONCURRENTLY index_chunks_embedding_idx;
```

The application role is a `DatabaseRole` resource, which CNPG
recommends over the Cluster's `managed.roles`
([declarative role management](https://cloudnative-pg.io/docs/1.30/declarative_role_management/)).
kritika's migrations grant it what it needs:

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

The owner's Secret, `kritika-postgres-credentials`, looks the same, with its
own role. A password generator, such as an External Secrets `Password`
generator feeding a templated `ExternalSecret`, can produce both, `uri`
included. Keep the
generated passwords free of characters that need escaping in a URI, or
escape them.

Set `connect_timeout`. Without it, a connection attempt waits as long as the
kernel does. After an undeploy and a quick redeploy, a pod's first dial can
go to a Service address that no longer exists. DNS still caches that address.
That dial hangs for minutes before kritika retries. With a timeout, the
attempt fails fast and the retry looks the name up again.

Point the chart at the two Secrets:

```yaml
database:
  owner:
    existingSecret: kritika-postgres-credentials
  app:
    existingSecret: kritika-postgres-app
```

If the role that the application URI logs in as is not `kritika_app`,
`database.app.role` must name it.

The chart's NetworkPolicies are on by default. With them, kritika serve
reaches the Postgres port only where `networkPolicy.postgres` says the
database is, and runner pods do not reach it at all. Unless `networkPolicy.postgres` is set, that is any pod in
the release namespace. Name the cluster's pods by the label CNPG gives them.
When their namespace is not the release namespace, add it. CNPG gives a
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

| Connections              | Per     | At most                                                                                                           | `statement_timeout`                                                                      | `application_name`    |
| ------------------------ | ------- | ----------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------- | --------------------- |
| the application pool     | replica | 16                                                                                                                | one minute, so a slow dashboard query cannot hold the connections webhooks and jobs need | `kritika-app`         |
| the owner pool           | replica | 4                                                                                                                 | ten minutes, for a migration that builds an index                                        | `kritika-owner`       |
| the `LISTEN` connection  | replica | 1                                                                                                                 | one minute, as the application pool's                                                    | `kritika-listen`      |
| review publication locks | replica | 3 × `KRITIKA_REVIEW_WORKERS` (2 unless set) + `KRITIKA_THREAD_WORKERS` (1 unless set), and one more on the leader | one minute                                                                               | `kritika-publication` |
| index update slot waits  | replica | `KRITIKA_INDEX_WORKERS` (1 unless set)                                                                            | one minute                                                                               | `kritika-index-wait`  |

Each active review publication holds a separate application-role connection
for its advisory lock, without an open transaction. This keeps slow forge
calls from occupying the application pool used by heartbeats and requests.
An `index_update` job that has to wait for an embedding slot likewise holds a
connection of its own for as long as it waits, one per job and so at most one
per index worker. That connection is what puts the update before first builds
for the next slot, and the update tries for the slot on it, so the wait needs
no connection of the application pool. Where it cannot be opened, or its login
and first query take longer than five seconds, as behind a pooler with no
server connection to give it, or it fails during the wait, the update logs a
warning and waits through the application pool instead, without that
priority. When you size `max_connections`, include these connections:
they are outside both pools, so raising a pool's `pool_max_conns` makes no room
for them.

A `pool_max_conns` in a URI replaces that pool's ceiling. If jobs wait on the
pool, raise the application pool's ceiling.
`kritika_db_pool_empty_acquires_total` counts those waits, and
`kritika_db_pool_connections` shows the pool's connections against the
ceiling ([metrics](/metrics/)). For more replicas, raise `max_connections`
under the Cluster's `postgresql.parameters`.

A `pool_ping_timeout` in a URI
replaces that pool's five-second bound on a ping of an idle connection.
CNPG's `cnpg_backends_total` metric shows what each `application_name`
holds. `kritika-app` counts the replicas' application pools, which also write
what runner Jobs report to the gateway. Runner Jobs open no connection of their
own.

The leader runs hourly retention sweeps of transcripts, egress refusals,
settled model invocations, stored diffs, agent conversations, and the paths
that finished runs' similar-code searches left out. The sweeps work in
batches, and each batch is its own statement. A backlog larger than what one
statement can clear is worked off over the sweep rather than rolled
back.

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
make the service reachable. Storage, replication state and scheduling also
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
takes one retry interval at most. During that interval, the new leader can
start its duties before the old one notices that it lost the lock. For that
reason, `KritikaMultipleLeaders` in the chart's alerts waits longer than one
interval.

A migration that changes a table that searches, index builds or model calls
use waits at most five seconds for that table's lock, so that the queries
queued behind it wait no longer. The five seconds bound only the wait for
the lock, not how long the migration then holds it. One that builds an index
of such a table does so in a transaction of its own, which lets the table be
read while it builds but holds up writes to it for as long as the build
reads the table. When another transaction holds the lock longer, the
migration fails and the leader logs it and releases the leader lock. A
leader tries the migration again within one `KRITIKA_LEADER_RETRY_INTERVAL`.
Replicas start working jobs once every migration is applied.

CNPG's disruption budget for the primary allows no eviction, so draining the
primary's node waits until the primary moves. Switch over first, with the
[`cnpg` kubectl plugin](https://cloudnative-pg.io/docs/1.30/kubectl-plugin/):

```sh
kubectl cnpg status kritika-postgres -n kritika
kubectl cnpg promote kritika-postgres kritika-postgres-2 -n kritika
```

## Backups

The configuration lives in git and the similar-code index can be rebuilt. But
reviews, findings, dismissals, feedback, usage, the audit log and queued jobs
exist in the database. Embedding checkpoints and staging save repeated work,
but they are also database state. Losing them can repeat paid embedding
calls. CNPG backs a cluster up two ways
([backups](https://cloudnative-pg.io/docs/1.30/backup/)).

The first way is object storage through the
[Barman Cloud plugin](https://cloudnative-pg.io/plugin-barman-cloud/),
which also archives WAL for point-in-time recovery. The Cluster's native
`barmanObjectStore` does the same but is deprecated since CNPG 1.26.

The second way is volume snapshots through the storage class's CSI driver
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
accepts. Recovery-time objective (RTO) is how long service can be
unavailable. Both are undecided for the deployment reviewed on
2026-10-12. No numerical recovery guarantee was established, and the
replication policy was left unchanged.
With `preferred`, acknowledged queue inserts, review records and embedding
checkpoints share the same potential loss window as other database writes.

A read-only inspection on that date found:

| Area                 | Evidence                                                                                                              | Meaning / remaining gap                                                                                                                                    |
| -------------------- | --------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Replication policy   | Two instances, with `method: any`, `number: 1`, `dataDurability: preferred` and `failoverQuorum: false`               | Matches this example's availability preference. A zero-loss objective requires a separate policy decision and validation.                                  |
| Live replication     | Both instances ready. Primary `synchronous_commit=on`. One streaming quorum standby in `synchronous_standby_names`.   | Synchronous replication was active at inspection time. This does not prove behavior during standby loss or failover.                                       |
| Backup configuration | No Cluster backup configuration or plugins. No CNPG `Backup` or `ScheduledBackup` resources for this cluster.         | No CNPG-managed base-backup coverage was demonstrated. External backup systems were not verified.                                                          |
| WAL archiving        | `archive_mode=on`, `archive_timeout=5min`, CNPG archive hook, nonzero archive counters and `ContinuousArchiving=True` | Hook success and counters do not establish an independently stored, retrievable WAL chain. No archive destination was configured in the inspected Cluster. |
| Snapshots            | No `VolumeSnapshot` resources in the deployment namespace                                                             | No snapshot recovery point was demonstrated by this inspection.                                                                                            |
| Restore evidence     | No last-successful-backup or first-recoverability timestamp reported. No restore exercise performed.                  | Restore coverage and measured RTO remain unverified. A healthy standby is not restore-test evidence.                                                       |

This is a dated observation, not a statement that the example manifest proves
production durability. When the deployment's configuration changes, recheck
the Cluster specification and status, backup resources, snapshots, and
PostgreSQL's `pg_stat_replication` and `pg_stat_archiver`.

Before claiming recoverability:

1. Decide the acceptable RPO/RTO. Decide whether writes must pause when
   synchronous standbys are unavailable. Compare those targets with the
   deployed policy and the effective session configuration. Include any
   `synchronous_commit` overrides.
2. Establish base backups and a continuous WAL archive in an independent
   failure domain. Make sure that they work, and that retention is sufficient
   for the intended recovery window. Inspect actual objects, recent backup
   results and archive failures, not just the presence of an archive hook. See
   [CNPG backup requirements](https://cloudnative-pg.io/docs/1.30/backup/).
3. Restore a selected recovery point into an isolated cluster. Record the
   last recovered transaction and the measured recovery time. Make sure that
   reviews, findings/dismissals, usage and queue rows are correct. Test
   application-role access. Test that the restored embedding checkpoint agrees
   with its vectors. During validation, keep production workers and
   forge/provider egress disconnected. Then restored jobs cannot post duplicate
   reviews or spend tokens.
4. Record the tested backup, recovery target, WAL coverage, result and remaining
   gaps. When appropriate, rebuild vector indexes. Do not substitute an index
   rebuild for recovery of durable application records. See
   [CNPG recovery procedures](https://cloudnative-pg.io/docs/1.30/recovery/).

The inspection did not perform failover, restore, or replication-policy changes.
Those are separate operational work after recovery objectives are agreed.

## The similar-code index

Every account's and repository's chunks share one table, `index_chunks`. A
similar-code search names one index generation of one repository. So that its
cost follows that generation's partition rather than the whole table, the
table is partitioned:

- `index_chunks` is list partitioned by a Boolean column, `legacy`, not by
  `created_at`. A chunk is written with `legacy` false, whatever its date.
- `index_chunks_current` is the partition of `legacy` false, so it holds
  every chunk but those of a table partitioned on upgrade that have yet to
  move (see below). It is hash partitioned by generation (`index_run_id`)
  into 64 partitions, `index_chunks_00` to `index_chunks_63`.
- Each leaf partition has its own VectorChord (`vchordrq`) index. A search
  reads the hash partition of its generation and that partition's index,
  and also the legacy partition and its index while that is attached. Once
  the legacy partition is dropped, or on a table created partitioned, it
  reads the hash partition alone, about 1/64 of the table if the chunks are
  spread evenly.
- The [search by name](/models/#search-by-name) reads `index_postings`, which
  holds a row for each word of a generation's chunks under the key
  generation and word: how many of the chunks hold the word and, while
  fewer than 200 do, which. A word in 200 chunks or more is too common for
  any search to read, so its row keeps the count alone. A search reads each
  of its words' rows from that B-tree, and then reads from the generation's
  partition only the chunks that those rows list, for their paths, to leave
  out those of the excluded paths, and the chunks it ranks. A change that
  excludes most of a repository adds no read for the chunks of its paths
  that no word of the search lists.
  Under row-level security a B-tree of text equality, which is leakproof,
  serves the search, where no index of a full-text `tsvector` could:
  Postgres applies the full-text match operator, `@@`, which is not
  leakproof, only to the rows that the policy has let through.
  `index_postings` is not partitioned: its key starts with the generation,
  so a search and a generation's deletion already read that generation's
  rows alone.
- A full build's publication writes its generation's postings in one
  statement, from the words that each chunk records (`index_chunks.lexemes`).
  An incremental step's publication updates the rows of the words of the
  chunks it replaces and adds, and lists a word's chunks again, from the
  generation's chunks that hold it, when it falls back under 200. A run that
  does not publish writes none, and every path that deletes a generation's
  chunks deletes its postings. This repository's code holds about 50 words a
  chunk, but about 23,000 words in all: 7.4 MB of postings for 10,000 of its
  chunks, where a row for each word of each chunk took 95 MB. Its 9,605
  chunks took 49 MB with vectors of 256 dimensions. Each publication records
  how many chunks its generation holds (`index_runs.generation_chunks`),
  which the search weighs words against.

The application role is granted `index_chunks` alone, and its row-level
security policy applies to every partition read through it. The role cannot
read a partition directly: each is created without privileges for the
application role or `PUBLIC`, even where the owner's default privileges
would grant them, and with row-level security but no policy, so a role
granted a partition through a role it is a member of reads no row of it.
Row-level security does not apply to `TRUNCATE`, so no table of the tree,
`index_chunks` and `index_chunks_legacy` among them, keeps a grant of
`TRUNCATE` to any role but its owner. The
[index scale benchmark](/metrics/#reproducing-the-index-scale-benchmark)
measures the effect.

### Partitioning an existing index

A build before partitioning created `index_chunks` as a plain table. The
leader's hourly retention sweep converts such a table, first once a new
leader has applied the configuration and ensured the index schema, which its
other sweeps do not wait for. The leader of such a build can also create the
table again during a rollout, after the migrations ran, for a new embedder.
That table lacks the
columns that the migrations add, `content_hash` and `lexemes`, and the
content hash's index. When it takes the lead with an embedder configured,
and before it converts the table, the leader adds the columns in a
transaction that holds the table's exclusive lock for a moment, as adding a
column writes no row. It looks for each column first, gives up its wait for
the lock after five seconds, and is tried again later.

Before it converts a plain table, whichever build created it, the sweep
builds the content hash's index with `CREATE INDEX CONCURRENTLY`. That reads
every chunk without holding up searches or index builds' writes, but waits
for every transaction that writes the table, or that began before the
build, to end. A build that stops early, as when the leader steps down,
leaves the index invalid, and the next sweep drops it and builds it again.

Then, also in two transactions, the leader marks every chunk of the table
as legacy, so that the conversion can attach it without reading it. The
first adds `legacy`, with a default of `true`, and a `NOT VALID` check that
`legacy` is true. Neither writes or reads a row, so it holds the table's
exclusive lock for a moment, and waits for it as the conversion's first
step does. The second validates the check, which reads every chunk under a
lock that lets searches read the table and index builds write it. A chunk
written meanwhile is checked as it is written. Each looks for what it adds
first, so a retry after the first committed only validates. The conversion
then reuses the content hash's index, and copies and rebuilds nothing:

1. It takes the table's exclusive lock, and then a lock on `accounts`,
   `repositories` and `index_runs` that blocks writes to them. A publication
   takes those locks in the other order, so the sweep takes the second lock
   at once or gives way, rather than wait on a publication that waits on
   it: the publication's role can have a shorter `deadlock_timeout` than
   the owner's, and PostgreSQL would then abort the publication. The wait
   for the table's lock is bounded to an eighth of PostgreSQL's
   `deadlock_timeout`, as searches and index builds queue behind it. When
   either lock is not to be had, the sweep changes nothing and tries again a
   second later. After five tries it logs a warning and tries again an hour
   later.
2. It renames the table, with its indexes and constraints, to
   `index_chunks_legacy`. It creates the partitioned `index_chunks` and
   attaches the old table as its partition for `legacy` true, so every chunk
   it held is in the legacy partition. Its indexes and foreign keys become
   those of the partitioned table. The validated check proves the
   partition's bound, so attaching reads no chunk.
3. That sweep, and each later one, empties `index_chunks_legacy`, up to
   5,000 chunks of one generation per transaction. The active generations
   go first, moved into the hash partitions. Then the chunks of the other
   generations: those of a build that can still publish or resume are
   moved, and the rest are deleted, as the next index build of their
   repository would delete them. Each transaction holds that repository's
   lock, as a publication does, for the few seconds its slice takes, and
   skips a repository whose lock another transaction holds. A generation
   whose move is under way is split between the two partitions, and a
   search reads it whole. A move writes the generation's rows again, into
   the hash partitions and their indexes. Autovacuum can mark the space of
   the old rows free, but only for new rows of the legacy partition, which
   gets none, so its files keep their size on the volume until the sweep
   drops it. Before upgrading, leave room for the whole legacy partition
   and its indexes, a copy of the chunks that move and their indexes, and
   the WAL that the moves write.
4. Once the legacy partition is empty, the sweep drops it. It first checks
   the partition before it asks for an exclusive lock, holding only
   PostgreSQL's ordinary read lock of the partition and the advisory lock
   that serializes changes to the index schema, and leaves the partition if
   a chunk is left. It then takes the exclusive lock on `index_chunks` at
   once or not at all, but at its last try, rather than queue searches
   behind a waiting lock request, and drops `index_chunks_legacy`, waiting
   for that partition with the same short wait as the first step. When
   either lock is not to be had, it changes nothing and tries again a
   second later, five times, and the next hourly sweep tries again.
   Searches and index builds that overlap without a pause would keep the
   exclusive lock from a request that does not wait, so the last try waits
   for it, with the first step's short wait, which bounds how long searches
   queue behind it. The hash partitions stay attached throughout: no chunk
   is moved and no index is built, and this step takes no lock on
   `accounts`, `repositories` or `index_runs`.

While the legacy partition is attached, a search can still scan its vector
index. The interim plan can be slower than the old plain-table plan: in the
[index scale benchmark](/metrics/#reproducing-the-index-scale-benchmark) on
VectorChord 1.1.1, a search of a repository of 10,000 chunks took 109 ms at
the median just after the conversion, against 28.5 ms on the plain table,
until the drain emptied the partition. Expect searches to be slower for as
long as the drain takes. A rebuild for a new embedder drops the whole table,
legacy partition included, and creates it partitioned.

The legacy partition keeps the original table's vector index. If its
definition was changed by hand, attaching builds a new one, under the lock.

VectorChord 1.1.1 records no rows for a table when a `vchordrq` index is built
on it. 1.3.0, from the [pgsty fork](https://github.com/pgsty/vchord), records them.
On 1.1.1, after a manual `CREATE INDEX` or `REINDEX`
on a partition, run `ANALYZE` on that partition. Otherwise the planner
estimates the partition to be empty and can choose an exact scan until
autovacuum analyzes it.

## Interrupted embedding builds

Index jobs retain completed staging and checkpoint each batch in the same
transaction as its vectors. A retry of the same River job resumes at the last
committed chunk and reuses its index and runner records. Compatibility includes
the target commit, incremental base, ignore rules, dimensions, input limits,
embedding endpoint/model and index format. Changed inputs discard incompatible
progress and build anew. A runner that did not finish staging is restarted.

If the process stops after the provider handles an embedding request but
before its vectors and checkpoint commit, the request can still be repeated.
When the provider reports usage, it is recorded separately. Checkpoints do not promise
exactly-once external calls. No database transaction stays open during an
embedding request.

Publication still swaps or advances the generation atomically, so searches see
only a completed index. Forced rebuilds also complete their River job and enqueue
catch-up work in that transaction. Retryable failures retain partial vectors.
Completed or superseded work clears them as appropriate. The leader's bounded
recovery sweep removes remaining progress for completed, cancelled, discarded
or missing jobs. To restart a cancelled index, request a fresh reindex.

An explicit retry of a cancelled job clears the earlier attempt's
cancellation request, so the retried attempt runs. A cancellation requested
while an attempt runs still fences it: the worker refuses to lock or
continue a job whose metadata carries `cancel_attempted_at`. Checkpoints
inherit the database's replication and backup limitations described above.

## Model-call accounting recovery

`model_invocations` records each logical model call before dispatch. For runner
calls, the record and token-budget reservation commit together. The provider's
outcome is then saved separately from settlement. Settlement replaces the
reservation with actual usage and inserts a usage row with the invocation's ID
and original timestamp, in one transaction. Repeating settlement cannot charge
the same invocation twice.

The leader's rescuer retries completed, unsettled outcomes. An expired call
without a saved outcome becomes `uncertain` and retains its reservation: the
provider possibly charged for it. A late outcome can still settle that call.
A settled record is swept once its settlement is older than the transcript
retention. Its usage row stays. Operators can inspect pending records with an
owner connection:

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
