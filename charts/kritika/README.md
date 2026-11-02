# kritika

![Version](https://img.shields.io/static/v1?label=Version&message=0.0.117&color=informational&style=flat-square) <!-- x-release-please-version -->
![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square)
![AppVersion](https://img.shields.io/static/v1?label=AppVersion&message=0.0.117&color=informational&style=flat-square) <!-- x-release-please-version -->

Self-hosted AI pull request reviewer for GitHub, Forgejo, GitLab and Gitea, backed by Postgres and per-review Kubernetes Jobs

**Homepage:** <https://github.com/home-operations/kritika>

## Usage

kritika ships as an OCI Helm chart. It needs a Postgres with
[VectorChord](https://github.com/tensorchord/VectorChord) and pgvector, three
roles, its public URL, a way to sign in, and its configuration file:

```sh
helm install kritika oci://ghcr.io/home-operations/charts/kritika \
  --set database.app.existingSecret=kritika-postgres-app \
  --set database.owner.existingSecret=kritika-postgres-credentials \
  --values my-values.yaml
```

where `my-values.yaml` sets:

```yaml
config:
  webUrl: https://kritika.example.com
  auth:
    admin:
      password: { env: ADMIN_PASSWORD }
  providers:
    openrouter:
      type: openrouter
      apiKey: { env: OPENROUTER_API_KEY }
  review:
    model: openrouter/vendor/large-model
  apps:
    github:
      accounts: [org-1]
      clientId: Iv1.example
      privateKey: { env: GITHUB_APP_PRIVATE_KEY }
      webhookSecret: { env: GITHUB_APP_WEBHOOK_SECRET }
env:
  ADMIN_PASSWORD:
    valueFrom:
      secretKeyRef: { name: kritika-admin, key: password }
  OPENROUTER_API_KEY:
    valueFrom:
      secretKeyRef: { name: kritika-openrouter, key: api-key }
# A Secret with the keys GITHUB_APP_PRIVATE_KEY and GITHUB_APP_WEBHOOK_SECRET.
envFrom:
  - secretRef:
      name: kritika-bot
ingress:
  enabled: true
  hosts: [{ host: kritika.example.com, paths: [{ path: / }] }]
  tls: [{ hosts: [kritika.example.com], secretName: kritika-tls }]
```

`config` is the application's YAML configuration: `webUrl`, logging,
workers, polling, retention, `auth`, `apps`, `providers`, review policy
and account configuration. It has the same structure as the
configuration file. Helm evaluates it with `tpl`, so
`webUrl: "https://{{ .Release.Name }}.example.com"` uses the
release name. Omitted values use the operational configuration from the
environment, or application defaults. Zero and false are preserved. The
chart's Ingress or HTTPRoute must route `config.webUrl`'s host.

- Secrets are `{ env: NAME }` references, supplied by `env` or `envFrom`.
- Runner Jobs are configured under `runner`: `runner.jobTemplate` and
  `runner.podTemplate` set what kritika takes from a Job's and a pod's
  template, such as the deadline, resources, placement and runtime
  class. The chart derives application configuration from runner,
  database and service values. It rejects duplicates under `config`.
- A checksum of the rendered file rolls pods when configuration changes. The
  ConfigMap carries `reloader.stakater.com/ignore`, so Reloader does not roll
  them a second time.
- `existingConfigMap` supplies an external `config.yaml` instead of
  `config`. Setting both is an error. Put `webUrl` in that file. Omit
  chart-owned deployment configuration. The chart supplies it through
  environment variables. External ConfigMap and Secret changes need a
  restart or Reloader.
- Operational configuration such as logging, worker counts and polling
  supports `env` and `envFrom`, but a file setting and its environment
  variable cannot both be set. Chart-managed environment variables are
  reserved.
- Auth, apps, providers, embedding and review policy are declared in the file.
  Their environment values must be explicitly referenced with `{ env: NAME }`.
- The configuration file path, database DSNs and per-run
  credentials/spec path are env-only. The chart supplies the
  configuration file path and database DSNs from its deployment values.

The
[setup guide](https://kritika.home-operations.com/setup/)
covers creating the GitHub App, and the dashboard's setup checklist shows
what a fresh instance still lacks.

### Database

The chart requires two Postgres roles, and kritika makes sure that
they are valid at start:

| Role        | Values                                             | What it does                                            | kritika refuses one that                        |
| ----------- | -------------------------------------------------- | ------------------------------------------------------- | ----------------------------------------------- |
| owner       | `database.owner.existingSecret`                    | runs migrations and applies configuration on the leader | is a superuser                                  |
| application | `database.app.existingSecret`, `database.app.role` | serves everything else, under row-level security        | owns a table, is a superuser or has `BYPASSRLS` |

The `vchord` (VectorChord) and `vector` (pgvector, whose types it builds on)
extensions must exist before the first start, and `vchord` must be in
`shared_preload_libraries`.

The owner and application connections must reach Postgres directly, or
through a pooler in session mode. The leader is whichever replica holds
a session advisory lock on its owner connection. The dashboard's live
updates and the job queue's notifications `LISTEN` on the application
connection. A transaction-mode pooler, such as PgBouncer or a
CloudNativePG `Pooler` with `poolMode: transaction`, hands a session to
other clients between transactions. That breaks both. `kritika_leader`
is 1 on the replica that holds the lock.

The chart supports the database as a CloudNativePG cluster in the same
Kubernetes cluster. The chart's NetworkPolicies let kritika serve reach the
Postgres port only on the pods that `networkPolicy.postgres` selects, and
runner pods not at all.

[Postgres with
CloudNativePG](https://kritika.home-operations.com/database/) sets this
up end to end. It covers a two-instance cluster with VectorChord mounted
as an image volume extension, and the extensions through a `Database`
resource. It also covers the application role as a
`DatabaseRole` resource, the Secrets with their `uri` key for
`database.*.existingSecret`, failover and backups.

### Security

Runner Jobs parse untrusted content and run what the model asks, so the
chart confines them:

- `networkPolicy.enabled`, on by default, keeps a runner pod to the
  cluster's DNS and the egress gateway. kritika serve runs that gateway on
  `service.gatewayPort`. The gateway allows what the configuration
  file's `egress` lists, and it is the runner's only way to its model and
  to the database, where it writes what the runner reports.
  With the Cilium flavor, the runner pod can look up only the names of
  the cluster's Services.
- A runner holds no provider key, App key or database credential: only a
  GitHub token for its run, a gateway token and a token to report what it
  found to the gateway with.
- The review's run tool executes the five programs that the runner image
  carries: `curl`, `find`, `gh`, `grep` and `jq`. It runs them in a root
  with bash but no package manager. It also executes those that the
  `tools` of a repository's `.kritika.yaml` add from the aqua registry.
  The runner makes sure that each download matches its digest or
  checksum. If the runner verifies the form of signature that the
  registry declares, it also verifies that signature. The run tool
  executes each program with whatever arguments
  the model gives: the pod's bounds confine a command, not a check of
  its arguments.
- `runner.podTemplate` puts runner pods under a sandboxed runtime such
  as gVisor with `runtimeClassName`. It keeps a runaway run in its own
  cgroup with `resources`. It also keeps runner pods on nodes of their
  own, and in a user namespace with `hostUsers: false`.
- `runner.admissionPolicy.enabled`, on by default, has the cluster
  refuse, from kritika serve's ServiceAccount, any Job but a runner Job
  and any Secret but a run's. It uses ValidatingAdmissionPolicies:
  cluster-scoped objects that the chart's installer must be allowed to
  create.

[Security](https://kritika.home-operations.com/security/) covers the
gateway, runner tools and the sandbox in full.

### Topology

The chart runs one Deployment of `kritika serve`, one replica by
default. Its rolling update surges a new pod and takes the old one down
only once the new one is ready. A rollout, such as the one that a
changed `config` starts, therefore keeps one serving on its own. A pod
is ready only once its database answers and it serves webhooks.

Every replica serves webhooks and the dashboard and works jobs, and one
holds the leader lock at a time. `replicas: 2` therefore keeps one
serving while a pod or node is lost. The chart then adds a
PodDisruptionBudget so that a drain takes one at a time. Until a lost
replica is back, webhooks are refused. The next poll picks up the pull
requests that they missed, though not the mentions, which only a webhook
delivers.

Runner pods are the Jobs that `kritika serve` creates, one per review,
follow-up and index run. They run `kritika run`. A replica that stops
drains its jobs first. A replica that dies outright leaves them to the
leader. The leader hands them back to the queue within a few minutes and
deletes the runner Jobs that they left. A remaining replica works them.

## Maintainers

| Name | Email | Url |
| ---- | ------ | --- |
| home-operations | <contact@home-operations.com> |  |

## Source Code

* <https://github.com/home-operations/kritika>

## Requirements

Kubernetes: `>=1.34.0-0`

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| affinity | object | `{}` | Affinity rules for pod scheduling. |
| clusterDomain | string | `"cluster.local"` | The cluster's DNS domain, the kubelet's `clusterDomain`. The gateway's in-cluster address, which runner Jobs are handed as `HTTPS_PROXY`, is `<release>-gateway.<namespace>.svc.<clusterDomain>`. The Service names that the `cilium` network policy lets runner pods look up also end in it. On a cluster with another domain, every runner fails to reach the gateway until this matches. |
| config | object | `{}` | Application configuration, with the same YAML schema as config.yaml. Secrets are references such as { env: ADMIN_PASSWORD }, supplied through env or envFrom. Omitted keys use application defaults. Rendered through tpl with the release context. Mutually exclusive with existingConfigMap. The keys that the chart derives from its deployment values fail the render. These keys are `addr`, `metricsAddr`, `gatewayAddr`, `gatewayUrl`, `databaseAppRole`, `executor` and `runner`. When `gatewayCache.enabled` is set, they also include `gatewayCacheDir`, `gatewayCacheStorage` and `gatewayCacheMaxEntrySize`. For filesystem storage, they also include `gatewayCacheMaxSize`. For S3, they also include the `gatewayCacheS3*` keys. |
| database.app.existingSecret | required | `""` | Secret holding the application role's connection URI. |
| database.app.key | string | `"uri"` | Key in that Secret. |
| database.app.role | string | `"kritika_app"` | Name of the application role, asserted at startup (not superuser, no BYPASSRLS, owns nothing). |
| database.owner.existingSecret | required | `""` | Secret holding the owner role's connection URI, used only by the leader for migrations and configuration sync. |
| database.owner.key | string | `"uri"` | Key in that Secret. |
| deploymentAnnotations | object | `{}` | Annotations added to the Deployment (for example, `reloader.stakater.com/auto: "true"`). Pod-level annotations go in `podAnnotations`. |
| env | object | `{}` | Operational configuration and referenced secrets, keyed by environment variable name. Use valueFrom.secretKeyRef for credentials. Do not duplicate file configuration or chart-managed variables. Rendered through tpl. |
| envFrom | list | `[]` | Secrets and ConfigMaps loaded as environment variables in bulk. Keys must match the secret references in config. Application configuration must not also be set in the file. |
| existingConfigMap | string | `""` | Existing ConfigMap containing config.yaml instead of inline config. Put webUrl in that file. Changes require a restart, for example through a Reloader annotation. |
| fullnameOverride | string | `""` | Override the full release name. |
| gatewayCache.enabled | bool | `false` | Cache eligible gateway downloads. Filesystem storage uses a private emptyDir per replica. S3 shares completed objects across replicas and rollouts. |
| gatewayCache.maxEntrySize | string | `"256Mi"` | Maximum size of one cached download (KRITIKA_GATEWAY_CACHE_MAX_ENTRY_SIZE). S3 uses single-request uploads, limited to 5Gi. |
| gatewayCache.storage.bucket | string | `""` | Existing S3 bucket. Provision its credentials and retention outside this chart. |
| gatewayCache.storage.endpoint | string | `""` | S3 API origin, with http:// or https:// and an optional port. No bucket path. |
| gatewayCache.storage.existingSecret | string | `""` | Secret with AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY keys, as a Rook ObjectBucketClaim or the AWS tooling writes them. Empty uses the SDK's IAM credentials. Only serve receives these credentials. |
| gatewayCache.storage.forcePathStyle | bool | `true` | Address buckets in the URL path instead of requiring wildcard bucket DNS. |
| gatewayCache.storage.maxSize | string | `"2Gi"` | Filesystem storage only: retained-entry budget, and separately the aggregate fill budget. Allow twice this plus evicted files still being served. S3's private local cache instead uses eight times maxEntrySize for each budget. This does not bound bucket usage. |
| gatewayCache.storage.prefix | string | `"gateway/"` | Object prefix dedicated to this instance. kritika appends the storage format version. |
| gatewayCache.storage.region | string | `""` | Signing region configured on the S3 server. |
| gatewayCache.storage.type | string | `"filesystem"` | Cache storage backend. |
| httpRoute.additionalRules | list | `[]` | Custom rules prepended before the default rule (templated). |
| httpRoute.annotations | object | `{}` | HTTPRoute annotations. |
| httpRoute.apiVersion | string | `""` | HTTPRoute apiVersion. Empty defaults to gateway.networking.k8s.io/v1. |
| httpRoute.enabled | bool | `false` | Expose the public Service through a Gateway API HTTPRoute (alternative to ingress). |
| httpRoute.filters | list | `[]` | Filters applied to the default rule. |
| httpRoute.hostnames | list | `[]` | Hostnames matched against the Host header. `config.webUrl`'s host (templated). |
| httpRoute.httpsRedirect | bool | `false` | Redirect HTTP to HTTPS (301) instead of routing to the backend (needs HTTP+HTTPS listeners). |
| httpRoute.kind | string | `""` | HTTPRoute kind. Empty defaults to HTTPRoute. |
| httpRoute.labels | object | `{}` | HTTPRoute labels. |
| httpRoute.matches | list | `[{"path":{"type":"PathPrefix","value":"/"}}]` | Match conditions for the default rule. |
| httpRoute.parentRefs | list | `[]` | Gateways (and listeners) this route attaches to. |
| image.digest | string | `""` | Pin the image by digest (sha256:…). When set, it overrides the tag. The release pipeline fills it with the published image's digest. |
| image.pullPolicy | string | `"IfNotPresent"` | Image pull policy. |
| image.repository | string | `"ghcr.io/home-operations/kritika"` | Image repository. |
| image.tag | string | `""` | Overrides the image tag. Defaults to the chart appVersion. |
| imagePullSecrets | list | `[]` | Image pull secrets for private registries, for the kritika serve pods and, through the runner ServiceAccount, runner Jobs. |
| ingress.annotations | object | `{}` | Ingress annotations. |
| ingress.className | string | `""` | IngressClass name. |
| ingress.enabled | bool | `false` | Expose the public Service through an Ingress. |
| ingress.hosts | list | `[{"host":"kritika.example.com","paths":[{"path":"/","pathType":"Prefix"}]}]` | Ingress hosts and their paths. The host is `config.webUrl`'s. |
| ingress.tls | list | `[]` | Ingress TLS configuration. |
| livenessProbe | object | `{"httpGet":{"path":"/healthz","port":"metrics"},"periodSeconds":20}` | Liveness probe, on the metrics port. |
| monitoring.dashboards.annotations | object | `{}` | Annotations added to the dashboard ConfigMap (templated). |
| monitoring.dashboards.enabled | bool | `false` | Render the Grafana dashboard ConfigMap (for the kube-prometheus-stack sidecar or grafana-operator): kritika's health, intake, reviews, model spend, runner Jobs, database pools, queue delays and vector searches, from the metrics the ServiceMonitor scrapes. |
| monitoring.dashboards.grafanaOperator.allowCrossNamespaceImport | bool | `true` | Allow a Grafana in any namespace to import this GrafanaDashboard. |
| monitoring.dashboards.grafanaOperator.enabled | bool | `false` | Render a GrafanaDashboard CR (grafana-operator) referencing the ConfigMap, instead of relying on the sidecar label. |
| monitoring.dashboards.grafanaOperator.folder | string | `""` | Folder to create the dashboard in (templated). Empty uses the Grafana default. |
| monitoring.dashboards.grafanaOperator.matchLabels | object | `{}` | Label selector matching the target Grafana instance (templated, and required when grafanaOperator is enabled). |
| monitoring.dashboards.grafanaOperator.resyncPeriod | string | `"10m"` | Resync period for the operator to re-check the dashboard. |
| monitoring.dashboards.labels | object | `{}` | Labels added to the dashboard ConfigMap (templated). |
| monitoring.dashboards.namespace | string | `""` | Namespace for the dashboard objects. Defaults to the release namespace. |
| monitoring.prometheusRule.additionalRuleAnnotations | object | `{}` | Extra annotations on every alert rule. |
| monitoring.prometheusRule.additionalRuleLabels | object | `{}` | Extra labels on every alert rule, for example, a routing label for Alertmanager. The rule's own `severity` wins. |
| monitoring.prometheusRule.annotations | object | `{}` | PrometheusRule annotations. |
| monitoring.prometheusRule.cacheStorageErrorsFor | string | `"10m"` | How long sustained S3 cache errors must persist before alerting. Cold misses and client cancellations are excluded. |
| monitoring.prometheusRule.configDriftFor | string | `"15m"` | How long a replica's configuration file can differ from the applied one before KritikaConfigDrift fires. A rollout drifts briefly until the new leader applies its file. |
| monitoring.prometheusRule.configErrorFor | string | `"5m"` | How long the leader's latest apply of the configuration can stay refused before KritikaConfigError fires. |
| monitoring.prometheusRule.enabled | bool | `false` | Create a Prometheus Operator PrometheusRule with kritika's alerts (requires its CRDs): no leader, more than one leader, a configuration that the leader failed to apply, configuration drift, delayed queues, unavailable queue metrics, vector-search errors and gateway cache storage errors. |
| monitoring.prometheusRule.labels | object | `{}` | PrometheusRule labels. |
| monitoring.prometheusRule.multipleLeadersFor | string | `"2m"` | How long more than one replica can report the lock before KritikaMultipleLeaders fires. A database failover can show two for one leader retry interval. |
| monitoring.prometheusRule.multipleLeadersWindow | string | `"1m"` | How far back KritikaMultipleLeaders looks for each replica's latest leader sample. Keep it longer than the ServiceMonitor interval and shorter than multipleLeadersFor, so a replaced leader drops out of the sum before the alert fires. |
| monitoring.prometheusRule.noLeaderFor | string | `"5m"` | How long the leader lock can go without a holder before KritikaNoLeader fires. A standby takes the lock within one leader retry interval of the database answering. Postgres drops a dead leader's session within about a minute. |
| monitoring.prometheusRule.queueAgeSeconds | int | `600` | Age in seconds of the oldest runnable review/followup/thread job that triggers KritikaQueueDelayed. Index onboarding is excluded. Its age remains on the dashboard. |
| monitoring.prometheusRule.queueDelayFor | string | `"15m"` | How long runnable work must exceed queueAgeSeconds before warning. |
| monitoring.prometheusRule.queueStatsUnavailableFor | string | `"5m"` | How long every scraped replica can succeed on fewer than 75% of queue snapshot reads over 5 minutes before warning. |
| monitoring.prometheusRule.vectorSearchErrorsFor | string | `"10m"` | How long the vector-search error ratio must exceed 10% (at least 10 non-canceled requests in 15 minutes) before warning. |
| monitoring.serviceMonitor.annotations | object | `{}` | ServiceMonitor annotations. |
| monitoring.serviceMonitor.enabled | bool | `false` | Create a Prometheus Operator ServiceMonitor for the metrics Service (requires its CRDs). |
| monitoring.serviceMonitor.interval | string | `"30s"` | Scrape interval. |
| monitoring.serviceMonitor.labels | object | `{}` | ServiceMonitor labels. |
| monitoring.serviceMonitor.metricRelabelings | list | `[]` | Prometheus metric relabelings. |
| monitoring.serviceMonitor.relabelings | list | `[]` | Prometheus relabelings. |
| monitoring.serviceMonitor.scrapeTimeout | string | `"10s"` | Scrape timeout. |
| nameOverride | string | `""` | Override the chart name used in resource names. |
| networkPolicy.dns.namespace | string | `"kube-system"` | Namespace of the cluster's DNS pods. |
| networkPolicy.dns.nodeLocalAddresses | list | `[]` | Addresses of a NodeLocal DNSCache on each node's own network, such as `169.254.20.10`, which the pods query in place of those pods. The Cilium flavor allows DNS to the node itself instead, since Cilium gives the node's addresses the host's identity. |
| networkPolicy.dns.podSelector | object | `{}` | Labels of the cluster's DNS pods. Empty stands for `k8s-app: kube-dns`. |
| networkPolicy.egressPorts | list | `[443]` | TCP ports that the service pods can egress to at any address, for forges, model endpoints and S3 storage outside the cluster. Runner pods reach the gateway alone. |
| networkPolicy.enabled | bool | `true` | Create the NetworkPolicies. The runner's is what keeps every command that a review runs on the egress gateway. The run tool passes the model's arguments as given, so without it a command can connect around the gateway. Turn it off only where something else confines runner pods' egress. |
| networkPolicy.extraEgress | list | `[]` | More egress rules for the service pods, as a networking.k8s.io/v1 NetworkPolicy's `egress`. They render as a NetworkPolicy of their own whatever the type, since Cilium and Calico allow what any policy that selects a pod allows. For a destination in the cluster, such as an S3 bucket on Ceph RGW or a model server, name its pods and their own ports. A NetworkPolicy matches those rather than a Service's. Runner pods never get them. |
| networkPolicy.postgres.namespace | string | `""` | Namespace of the database's pods. The release namespace when empty. |
| networkPolicy.postgres.podSelector | object | `{}` | Labels of the database's pods, such as `cnpg.io/cluster: kritika-postgres`. CloudNativePG gives that label to a cluster's instances and its Poolers' pods alike, so it also covers a URI that goes through a session-mode Pooler. Empty selects every pod in `namespace`. |
| networkPolicy.postgres.port | int | `5432` | Port of the database, which the pods can reach only where the rest of `postgres` says. |
| networkPolicy.type | string | `"auto"` | Policy flavor for your CNI: "default" (networking.k8s.io/v1 NetworkPolicy), "cilium" (CiliumNetworkPolicy), "calico" (projectcalico.org/v3 NetworkPolicy), or "auto". "auto" is "cilium" when the cluster serves CiliumNetworkPolicy, else "calico" when it serves Calico's projectcalico.org/v3 NetworkPolicy, else "default". Helm and Flux read what the cluster serves from it, and Argo CD passes it to `helm template`. Plain `helm template` without `--api-versions` renders "default". The Cilium flavor's DNS rules need Cilium's L7 proxy, on unless turned off. Without it, Cilium refuses the runner's policy. A NetworkPolicy that the flavor adds then keeps runner pods from looking up any name, so runs fail until the type is "default". Set the flavor where the cluster serves a policy API that its CNI does not enforce. |
| nodeSelector | object | `{}` | Node selector for pod scheduling. |
| podAnnotations | object | `{}` | Annotations added to the pods. |
| podDisruptionBudget.enabled | bool | `true` | Create a PodDisruptionBudget when there is more than one replica. |
| podDisruptionBudget.maxUnavailable | int | `1` | Maximum pods that can be unavailable, as a count or percentage. Takes precedence over `minAvailable` when set. |
| podDisruptionBudget.minAvailable | string | `""` | Minimum pods that must stay available, as a count or percentage. Used unless `maxUnavailable` is set. |
| podLabels | object | `{}` | Labels added to the pods. |
| podSecurityContext | object | `{"runAsGroup":65532,"runAsNonRoot":true,"runAsUser":65532,"seccompProfile":{"type":"RuntimeDefault"}}` | Pod-level securityContext (non-root uid/gid 65532, RuntimeDefault seccomp). |
| priorityClassName | string | `""` | PriorityClass for the pods. Empty uses the cluster default. |
| rbac.create | bool | `true` | Create the Role and RoleBinding kritika serve needs: Jobs in the release namespace, their pods and logs, the Secrets it hands them, the Jobs' finalizers, and Events on its Jobs and pods. Nothing cluster-wide. |
| readinessProbe | object | `{"httpGet":{"path":"/readyz","port":"metrics"},"periodSeconds":10}` | Readiness probe, on the metrics port. A replica is ready once its configuration file loads, the database answers and it serves webhooks and the dashboard. A rollout therefore takes the previous pod down only once the new one can hold a delivery. |
| replicas | int | `1` | Replicas of kritika serve. Every replica serves webhooks and the dashboard and works jobs. Exactly one holds the leader lock at a time. A rollout keeps one serving on its own. To keep one serving while a pod or node is lost, set 2. |
| resources | object | `{"limits":{"memory":"512Mi"},"requests":{"cpu":"50m","memory":"128Mi"}}` | Resource requests and limits of the kritika serve pods. |
| runner.admissionPolicy.enabled | bool | `true` | Have the cluster refuse three things from kritika serve's ServiceAccount. The first is any Job that is not a runner Job as kritika builds it. The second is any Secret that is not a run's. The third is any deletion of a Job but in the foreground, which takes its run's Secret with it. The cluster uses three ValidatingAdmissionPolicies and their bindings: cluster-scoped objects that the chart's installer must be allowed to create. Without them, the Role lets kritika serve create any Job in its namespace. That Job's pod can run as another ServiceAccount and mount another Secret. Any tag or digest of the runner image's repository passes, so the runs that an older replica starts mid-rollout pass too. Some changes refuse those runs until the rollout ends, and the runs are retried. These are changes to the repository, the runner ServiceAccount, RuntimeClass or a Secret that the runner container's `env` names. |
| runner.jobTemplate.metadata.annotations | object | `{}` | Annotations for runner Jobs. kritika refuses its own, under `kritika.home-operations.com/`. |
| runner.jobTemplate.metadata.labels | object | `{}` | Labels for runner Jobs. kritika refuses its own, the instance label and the Job controller's. |
| runner.jobTemplate.spec.activeDeadlineSeconds | int | `900` | How long a runner Job can run, in seconds. It is at most 7200, since River's job timeout cap otherwise cuts the run off early. A review's or follow-up's Job runs as long as its agent's `timeout` needs, if that is longer. That need covers each part that a review is split into. The deadline bounds the work around the run and the run's gateway token too. |
| runner.jobTemplate.spec.ttlSecondsAfterFinished | int | `600` | How long a finished Job stays for kubectl before Kubernetes removes it, in seconds. The run row keeps everything that the Job knew. |
| runner.podTemplate.metadata.annotations | object | `{}` | Annotations for runner pods, such as `karpenter.sh/do-not-disrupt: "true"`. kritika refuses AppArmor's, which will otherwise set the runner's profile. |
| runner.podTemplate.metadata.labels | object | `{}` | Labels for runner pods. kritika refuses its own, the instance label and the Job controller's. |
| runner.podTemplate.spec.affinity | object | `{}` | Affinity for runner pods. |
| runner.podTemplate.spec.containers | list | `[{"env":[],"image":"","imagePullPolicy":"IfNotPresent","name":"runner"}]` | The runner container, the pod's one, named runner. It has an `image`, an `imagePullPolicy` and an `env`. An empty `image` is this release's `-tools` image pinned by digest. That image carries curl, find, gh, grep and jq for a review's or follow-up's run tool. `env` holds variables that the run tool's commands get, each a `value` or a Secret's key (`valueFrom.secretKeyRef`). The value of a Secret's key is masked out of what the commands print. kritika sets these variables on the container as `KRITIKA_COMMAND_ENV_<name>`, so they configure the commands and never kritika itself. kritika refuses the names that it keeps for the commands, such as `HOME`, `PATH`, the proxy variables and `GH_*`. The Secret must be in the release namespace. |
| runner.podTemplate.spec.hostUsers | bool | `true` | Set false to run runner pods in a user namespace of their own, which the nodes' kernel and container runtime must support. True shares the node's, as Kubernetes does by default. |
| runner.podTemplate.spec.nodeSelector | object | `{}` | Node labels runner pods are scheduled by. |
| runner.podTemplate.spec.priorityClassName | string | `""` | PriorityClass for runner pods. Empty uses the cluster default. |
| runner.podTemplate.spec.resources | object | `{}` | Requests and limits of cpu, memory and hugepages for the whole runner pod, the run tool's commands included. These are pod-level resources, on by default from Kubernetes 1.34. A LimitRange's container defaults in the release namespace still apply to the runner container. Its default limit bounds the runner within the pod's, and the API refuses a runner pod whose requests are below its default requests. |
| runner.podTemplate.spec.runtimeClassName | string | `""` | RuntimeClass for runner pods, such as gvisor or kata. Empty uses the cluster default. |
| runner.podTemplate.spec.tolerations | list | `[]` | Tolerations for runner pods. |
| runner.podTemplate.spec.topologySpreadConstraints | list | `[]` | Topology spread constraints for runner pods. |
| runner.serviceAccount.annotations | object | `{}` | Annotations for the runner ServiceAccount. |
| runner.serviceAccount.create | bool | `true` | Create the runner ServiceAccount: no permissions, no token mounted, and the chart's `imagePullSecrets` so runner Jobs can pull from a private registry. |
| runner.serviceAccount.name | string | `""` | Runner ServiceAccount name. Generated from the release name if empty. |
| securityContext | object | `{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]},"readOnlyRootFilesystem":true}` | Container securityContext (no privilege escalation, read-only root filesystem, drops ALL capabilities). |
| service.gatewayPort | int | `8082` | Port of the gateway that the kritika serve pods run, on the pods and its Service. The gateway is the forward proxy that runner Jobs are handed as `HTTPS_PROXY`. It allows only the hosts that the configuration names, so runner pods need no direct internet egress. These hosts are the connections' forges, plus `egress.allow` less `egress.deny`. The forges are github.com and api.github.com for an app, and the instance host for a Forgejo, GitLab or Gitea connection. The gateway also serves the model and similar-code endpoints that a runner calls with a per-run token, so no provider key enters a runner pod. |
| service.metricsPort | int | `8081` | Metrics and probe port. |
| service.port | int | `8080` | Public port: the webhooks (`POST /hooks/{connection}`) and the dashboard. |
| service.type | string | `"ClusterIP"` | Service type of the public Service. |
| serviceAccount.annotations | object | `{}` | Annotations for the ServiceAccount. |
| serviceAccount.automount | bool | `true` | Automount the API token, which kritika serve needs to create runner Jobs. |
| serviceAccount.create | bool | `true` | Create the ServiceAccount kritika serve runs as. |
| serviceAccount.name | string | `""` | ServiceAccount name. Generated from the release name if empty. |
| startupProbe | object | `{"failureThreshold":30,"httpGet":{"path":"/healthz","port":"metrics"},"periodSeconds":2}` | Startup probe, on the metrics port. The liveness and readiness probes wait until it passes, so a pod still opening its listeners is not reported unready. It allows a minute. |
| strategy | object | `{"rollingUpdate":{"maxSurge":1,"maxUnavailable":0},"type":"RollingUpdate"}` | Deployment update strategy. A rolling update that surges one pod and takes none down keeps a replica serving while each is replaced. Helm merges maps, so a switch to `Recreate` also sets `rollingUpdate: null`. |
| terminationGracePeriodSeconds | int | `200` | Grace period for a clean shutdown. kritika serve keeps accepting webhooks, dashboard requests and model steps for 5s while traffic moves off the pod. It stops taking jobs and lets running ones finish for up to 100s. It then takes up to 90s to hand back the reviews that it cut for a retry. Its gateway lets model steps in flight finish for up to 2m. |
| tests.image.pullPolicy | string | `"IfNotPresent"` | `helm test` image pull policy. |
| tests.image.repository | string | `"mirror.gcr.io/curlimages/curl"` | `helm test` connection-pod image. A gcr-mirrored curl, so the test never pulls from Docker Hub. |
| tests.image.tag | string | `"8.23.0@sha256:3fb7e13e5d179a845c97d78204f3f1064d1907037f26a3ebf91710571151fd93"` | `helm test` image, pinned as `tag@sha256:digest` so Renovate bumps the tag and its digest together. |
| tolerations | list | `[]` | Tolerations for pod scheduling. |
| topologySpreadConstraints | list | `[{"labelSelector":{"matchLabels":{"app.kubernetes.io/instance":"{{ .Release.Name }}","app.kubernetes.io/name":"{{ include \"kritika.name\" . }}"}},"maxSkew":1,"topologyKey":"kubernetes.io/hostname","whenUnsatisfiable":"ScheduleAnyway"}]` | Spread the kritika serve pods across nodes, so with `replicas: 2` a node loss takes one of them. This is a soft constraint, so a one-node cluster still schedules them. Rendered through `tpl`. Empty leaves scheduling to Kubernetes. |
| volumeMounts | list | `[]` | Additional volume mounts on the kritika serve container. |
| volumes | list | `[]` | Additional volumes on the Deployment. |

---

_This README is generated by [helm-docs](https://github.com/norwoodj/helm-docs) from `Chart.yaml` and `values.yaml`. Edit those (or `README.md.gotmpl`) and run `mise run generate`._
