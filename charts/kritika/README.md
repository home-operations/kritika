# kritika

![Version](https://img.shields.io/static/v1?label=Version&message=0.0.75&color=informational&style=flat-square) <!-- x-release-please-version -->
![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square)
![AppVersion](https://img.shields.io/static/v1?label=AppVersion&message=0.0.75&color=informational&style=flat-square) <!-- x-release-please-version -->

Self-hosted AI pull request reviewer for GitHub, backed by Postgres and per-review Kubernetes Jobs

**Homepage:** <https://github.com/home-operations/kritika>

## Usage

kritika ships as an OCI Helm chart. It needs a Postgres with
[VectorChord](https://github.com/tensorchord/VectorChord) and pgvector, three
roles, its public URL, a way to sign in, and its configuration file:

```sh
helm install kritika oci://ghcr.io/home-operations/charts/kritika \
  --set database.app.existingSecret=kritika-postgres-app \
  --set database.owner.existingSecret=kritika-postgres-credentials \
  --set database.runner.existingSecret=kritika-postgres-runner \
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

`config` is the application's YAML configuration: `webUrl`, logging, workers,
polling, retention, `auth`, `apps`, `providers`, review policy and account settings.
It has the same structure as the configuration file and is evaluated with
`tpl`, so `webUrl: "https://{{ .Release.Name }}.example.com"` uses the release name. Omitted values use operational env settings or application defaults; zero and false are preserved.
The chart's Ingress or HTTPRoute must route `config.webUrl`'s host.

- Secrets are `{ env: NAME }` references, supplied by `env` or `envFrom`.
- Runner Jobs are configured under `runner`, including `runtimeClass` and
  `deadline`. The chart derives application settings from runner, database
  and service values and rejects duplicates under `config`.
- A checksum of the rendered file rolls pods when configuration changes. The
  ConfigMap carries `reloader.stakater.com/ignore`, so Reloader does not roll
  them a second time.
- `existingConfigMap` supplies an external `config.yaml` instead of `config`;
  setting both is an error. Put `webUrl` in that file and omit chart-owned
  deployment settings. The chart supplies those through environment variables.
  External ConfigMap and Secret changes need a restart or Reloader.
- Operational settings such as logging, worker counts and polling support
  `env` and `envFrom`, but a file setting and its environment variable cannot
  both be set. Chart-managed environment variables are reserved.
- Auth, apps, providers, embedding and review policy are declared in the file.
  Their environment values must be explicitly referenced with `{ env: NAME }`;
  automatic env declarations and policy overrides are not supported.
- The config path, database DSNs and per-run credentials/spec path are env-only.
  The chart supplies the config path and database DSNs from its deployment values.

**Migration:** move `configFile` into `config`, flat policy keys such as
`config.reviewModel` into `config.review.model`, and password `valueFrom`
entries into `env` with references in the file. Move
`config.runnerRuntimeClass` and `config.runnerDeadline` into
`runner.runtimeClass` and `runner.deadline`. Move automatic auth, app, provider
and policy env configuration into the file, keeping secret values in env with
explicit file references. See the
[configuration reference](https://github.com/home-operations/kritika/blob/main/docs/configuration.md)
for the complete migration and external ConfigMap rules.

The
[setup guide](https://github.com/home-operations/kritika/blob/main/docs/setup.md)
covers creating the GitHub App, and the dashboard's setup checklist shows
what a fresh instance still lacks.

### Database

The chart requires three Postgres roles, and kritika checks them at
start:

| Role        | Values                                                   | What it does                                             | kritika refuses one that                        |
| ----------- | -------------------------------------------------------- | -------------------------------------------------------- | ----------------------------------------------- |
| owner       | `database.owner.existingSecret`                          | runs migrations and applies configuration on the leader  | is a superuser                                  |
| application | `database.app.existingSecret`, `database.app.role`       | serves everything else, under row-level security         | owns a table, is a superuser or has `BYPASSRLS` |
| runner      | `database.runner.existingSecret`, `database.runner.role` | is handed to runner Jobs, and can only write its own run |                                                 |

The `vchord` (VectorChord) and `vector` (pgvector, whose types it builds on)
extensions must exist before the first start, and `vchord` must be in
`shared_preload_libraries`.

The owner and application connections must reach Postgres directly, or
through a pooler in session mode. The leader is whichever replica holds a
session advisory lock on its owner connection, and the dashboard's live
updates and the job queue's notifications `LISTEN` on the application
connection. A transaction-mode pooler, such as PgBouncer or a CloudNativePG
`Pooler` with `poolMode: transaction`, hands a session to other clients
between transactions, which breaks both. `kritika_leader` is 1 on the replica
that holds the lock.

[Postgres with CloudNativePG](https://kritika.home-operations.com/database/)
sets this up end to end: a two-instance cluster with VectorChord mounted as
an image volume extension, the extensions through a `Database` resource, the
application and runner roles as `DatabaseRole` resources, the Secrets with
their `uri` key for `database.*.existingSecret`, failover and backups.

### Security

Runner Jobs parse untrusted content and run what the model asks, so the
chart confines them:

- With `networkPolicy.enabled`, a runner pod reaches nothing but the
  cluster's DNS, the database `networkPolicy.postgres` locates and the
  egress gateway kritika serve runs on
  `service.gatewayPort`, which allows what the configuration file's
  `egress` lists and is the runner's only way to its model. With the
  Cilium flavor, it may look up only the names of the cluster's Services
  and those `networkPolicy.dns.runnerNames` lists.
- A runner holds no provider key or App key: only a GitHub token for its
  run, a gateway token and the runner role's database URI.
- The review's run tool executes the six programs the runner image
  declares and those a repository's `.kritika/mise.toml` adds, each
  download checked against the repository's lock;
  `runner.mise.urlReplacements` sends the downloads to a mirror.
  `runner.runtimeClass` puts runner Jobs under a sandboxed runtime such as
  gVisor; `runner.resources` keeps a runaway run in its own cgroup.

[Security](https://kritika.home-operations.com/security/) covers the
gateway, runner tools and the sandbox in full.

### Migrating runner tools

`runner.mise.config` and `KRITIKA_RUNNER_MISE_CONFIG` are removed, as
`runner.tools` and `KRITIKA_RUNNER_TOOLS` were before them. Declare each
tool in the `.kritika/mise.toml` of the repositories whose reviews use
it, pinned by `.kritika/mise.lock`, and move a `url_replacements` mirror
to `runner.mise.urlReplacements`. The runner image declares its six tools
itself, so they stay available. See the
[migration guide](https://kritika.home-operations.com/security/#migrating-from-the-instance-mise-config).

### Migrating NetworkPolicies

With `networkPolicy.enabled`, the pods send DNS only to the cluster's DNS.
Before upgrading, set `networkPolicy.dns.namespace` and `podSelector` if
its pods are not `k8s-app: kube-dns` in `kube-system`, and list a NodeLocal
DNSCache's address in `networkPolicy.dns.nodeLocalAddresses`. With the
Cilium flavor, runner pods may look up only the cluster's Service names
and those `networkPolicy.dns.runnerNames` lists: add the host of a
database outside the cluster, or runs cannot look it up. See
[name lookups](https://kritika.home-operations.com/security/#name-lookups).

Runner pods reach the Postgres port only where `networkPolicy.postgres`
says the database is, any pod in the release namespace unless set. Before
upgrading, set its `namespace`, `podSelector` or `cidrs` when the database
is elsewhere, or runs cannot connect while kritika serve still can. See
[Postgres](https://kritika.home-operations.com/database/#secrets-and-connection-uris).

### Topology

The chart runs one Deployment of `kritika serve`, one replica by default.
Its rolling update surges a new pod and takes the old one down only once the
new one is ready, so a rollout, such as the one a changed `config`
starts, keeps one serving on its own: a pod is ready only once its database
answers and it serves webhooks. Every replica serves webhooks and
the dashboard and works jobs, and one holds the leader lock at a time, so
`replicas: 2` keeps one serving while a pod or node is lost; the chart then
adds a PodDisruptionBudget so a drain takes one at a time. Until a lost
replica is back, webhooks are refused; the next poll picks up the pull
requests they missed, though not the mentions, which only a webhook
delivers. Runner pods are the Jobs `kritika serve` creates, one per review,
follow-up and index run, running `kritika run`. A replica that stops drains its jobs
first; one that dies outright leaves them to the leader, which hands them
back to the queue within a few minutes and deletes the runner Jobs they
left, and a remaining replica works them.

## Maintainers

| Name | Email | Url |
| ---- | ------ | --- |
| home-operations | <contact@home-operations.com> |  |

## Source Code

* <https://github.com/home-operations/kritika>

## Requirements

Kubernetes: `>=1.31.0-0`

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| affinity | object | `{}` | Affinity rules for pod scheduling. |
| config | object | `{}` | Application configuration, using the same YAML schema as config.yaml. Secrets are references such as { env: ADMIN_PASSWORD }, supplied through env or envFrom. Omitted settings use application defaults. Rendered through tpl with the release context. Mutually exclusive with existingConfigMap. |
| database.app.existingSecret | required | `""` | Secret holding the application role's connection URI. |
| database.app.key | string | `"uri"` | Key in that Secret. |
| database.app.role | string | `"kritika_app"` | Name of the application role, asserted at startup (not superuser, no BYPASSRLS, owns nothing). |
| database.owner.existingSecret | required | `""` | Secret holding the owner role's connection URI, used only by the leader for migrations and configuration sync. |
| database.owner.key | string | `"uri"` | Key in that Secret. |
| database.runner.existingSecret | required | `""` | Secret holding the runner role's connection URI; referenced by runner Jobs, never read by kritika serve. |
| database.runner.key | string | `"uri"` | Key in that Secret. |
| database.runner.role | string | `"kritika_runner"` | Name of the runner role, granted only what runner Jobs need. |
| deploymentAnnotations | object | `{}` | Annotations added to the Deployment (e.g. `reloader.stakater.com/auto: "true"`). Pod-level annotations go in `podAnnotations`. |
| env | object | `{}` | Operational settings and referenced secrets, keyed by environment variable name. Use valueFrom.secretKeyRef for credentials. Do not duplicate file settings or chart-managed variables. Rendered through tpl. |
| envFrom | list | `[]` | Secrets and ConfigMaps loaded as environment variables in bulk. Keys must match the secret references in config. Application settings must not also be set in the file. |
| existingConfigMap | string | `""` | Existing ConfigMap containing config.yaml instead of inline config. Put webUrl in that file. Changes require a restart, for example through a Reloader annotation. |
| fullnameOverride | string | `""` | Override the full release name. |
| gatewayCache.enabled | bool | `false` | Give each replica an emptyDir for the cache (KRITIKA_GATEWAY_CACHE_DIR), which keeps what a repository's `.kritika/mise.lock` pins; `config.egress.cache` names more hosts whose downloads it keeps. |
| gatewayCache.maxAge | string | `"24h"` | How long a kept download is served before it is fetched again (KRITIKA_GATEWAY_CACHE_MAX_AGE), a Go duration. |
| gatewayCache.maxSize | string | `"2Gi"` | The most the cache keeps (KRITIKA_GATEWAY_CACHE_MAX_SIZE), in bytes or with Ki, Mi, Gi or Ti, the least recently used downloads going first, and apart from that the most its fetches under way may write. Allow ephemeral storage for twice this size plus evicted files still being served and the pod's other storage use. Evicted files retain disk space until their readers finish, for up to two minutes; their total size is not bounded by this setting. |
| httpRoute.additionalRules | list | `[]` | Custom rules prepended before the default rule (templated). |
| httpRoute.annotations | object | `{}` | HTTPRoute annotations. |
| httpRoute.apiVersion | string | `""` | HTTPRoute apiVersion; empty defaults to gateway.networking.k8s.io/v1. |
| httpRoute.enabled | bool | `false` | Expose the public Service through a Gateway API HTTPRoute (alternative to ingress). |
| httpRoute.filters | list | `[]` | Filters applied to the default rule. |
| httpRoute.hostnames | list | `[]` | Hostnames matched against the Host header; `config.webUrl`'s host (templated). |
| httpRoute.httpsRedirect | bool | `false` | Redirect HTTP to HTTPS (301) instead of routing to the backend (needs HTTP+HTTPS listeners). |
| httpRoute.kind | string | `""` | HTTPRoute kind; empty defaults to HTTPRoute. |
| httpRoute.labels | object | `{}` | HTTPRoute labels. |
| httpRoute.matches | list | `[{"path":{"type":"PathPrefix","value":"/"}}]` | Match conditions for the default rule. |
| httpRoute.parentRefs | list | `[]` | Gateways (and listeners) this route attaches to. |
| image.digest | string | `""` | Pin the image by digest (sha256:…); when set, overrides the tag. The release pipeline fills it with the published image's digest. |
| image.pullPolicy | string | `"IfNotPresent"` | Image pull policy. |
| image.repository | string | `"ghcr.io/home-operations/kritika"` | Image repository. |
| image.tag | string | `""` | Overrides the image tag; defaults to the chart appVersion. |
| imagePullSecrets | list | `[]` | Image pull secrets for private registries, for the kritika serve pods and, through the runner ServiceAccount, runner Jobs. |
| ingress.annotations | object | `{}` | Ingress annotations. |
| ingress.className | string | `""` | IngressClass name. |
| ingress.enabled | bool | `false` | Expose the public Service through an Ingress. |
| ingress.hosts | list | `[{"host":"kritika.example.com","paths":[{"path":"/","pathType":"Prefix"}]}]` | Ingress hosts and their paths; the host is `config.webUrl`'s. |
| ingress.tls | list | `[]` | Ingress TLS configuration. |
| livenessProbe | object | `{"httpGet":{"path":"/healthz","port":"metrics"},"periodSeconds":20}` | Liveness probe, on the metrics port. |
| monitoring.dashboards.annotations | object | `{}` | Annotations added to the dashboard ConfigMap (templated). |
| monitoring.dashboards.enabled | bool | `false` | Render the Grafana dashboard ConfigMap (for the kube-prometheus-stack sidecar or grafana-operator): kritika's health, intake, reviews, model spend, runner Jobs, database pools, queue delays and vector searches, from the metrics the ServiceMonitor scrapes. |
| monitoring.dashboards.grafanaOperator.allowCrossNamespaceImport | bool | `true` | Allow a Grafana in any namespace to import this GrafanaDashboard. |
| monitoring.dashboards.grafanaOperator.enabled | bool | `false` | Render a GrafanaDashboard CR (grafana-operator) referencing the ConfigMap, instead of relying on the sidecar label. |
| monitoring.dashboards.grafanaOperator.folder | string | `""` | Folder to create the dashboard in (templated); empty uses the Grafana default. |
| monitoring.dashboards.grafanaOperator.matchLabels | object | `{}` | Label selector matching the target Grafana instance (templated; required when grafanaOperator is enabled). |
| monitoring.dashboards.grafanaOperator.resyncPeriod | string | `"10m"` | Resync period for the operator to re-check the dashboard. |
| monitoring.dashboards.labels | object | `{}` | Labels added to the dashboard ConfigMap (templated). |
| monitoring.dashboards.namespace | string | `""` | Namespace for the dashboard objects; defaults to the release namespace. |
| monitoring.prometheusRule.additionalRuleAnnotations | object | `{}` | Extra annotations on every alert rule. |
| monitoring.prometheusRule.additionalRuleLabels | object | `{}` | Extra labels on every alert rule, e.g. a routing label for Alertmanager. The rule's own `severity` wins. |
| monitoring.prometheusRule.annotations | object | `{}` | PrometheusRule annotations. |
| monitoring.prometheusRule.configDriftFor | string | `"15m"` | How long a replica's configuration file may differ from the applied one before KritikaConfigDrift fires; a rollout drifts briefly until the new leader applies its file. |
| monitoring.prometheusRule.configErrorFor | string | `"5m"` | How long the leader's latest apply of the configuration may stay refused before KritikaConfigError fires. |
| monitoring.prometheusRule.enabled | bool | `false` | Create a Prometheus Operator PrometheusRule with kritika's alerts (requires its CRDs): no leader, more than one leader, a configuration the leader could not apply, configuration drift, delayed queues, unavailable queue metrics and vector-search errors. |
| monitoring.prometheusRule.labels | object | `{}` | PrometheusRule labels. |
| monitoring.prometheusRule.multipleLeadersFor | string | `"2m"` | How long more than one replica may report the lock before KritikaMultipleLeaders fires. A database failover can show two for one leader retry interval. |
| monitoring.prometheusRule.noLeaderFor | string | `"5m"` | How long no replica may hold the leader lock before KritikaNoLeader fires. A standby takes the lock within one leader retry interval of the database answering, and a dead leader's session is dropped by Postgres within about a minute. |
| monitoring.prometheusRule.queueAgeSeconds | int | `600` | Oldest runnable review/followup/thread job age in seconds that triggers KritikaQueueDelayed. Index onboarding is excluded; its age remains on the dashboard. |
| monitoring.prometheusRule.queueDelayFor | string | `"15m"` | How long runnable work must exceed queueAgeSeconds before warning. |
| monitoring.prometheusRule.queueStatsUnavailableFor | string | `"5m"` | How long every scraped replica may succeed on fewer than 75% of queue snapshot reads over 5 minutes before warning. |
| monitoring.prometheusRule.vectorSearchErrorsFor | string | `"10m"` | How long the vector-search error ratio must exceed 10% (at least 10 non-canceled requests in 15 minutes) before warning. |
| monitoring.serviceMonitor.annotations | object | `{}` | ServiceMonitor annotations. |
| monitoring.serviceMonitor.enabled | bool | `false` | Create a Prometheus Operator ServiceMonitor for the metrics Service (requires its CRDs). |
| monitoring.serviceMonitor.interval | string | `"30s"` | Scrape interval. |
| monitoring.serviceMonitor.labels | object | `{}` | ServiceMonitor labels. |
| monitoring.serviceMonitor.metricRelabelings | list | `[]` | Prometheus metric relabelings. |
| monitoring.serviceMonitor.relabelings | list | `[]` | Prometheus relabelings. |
| monitoring.serviceMonitor.scrapeTimeout | string | `"10s"` | Scrape timeout. |
| nameOverride | string | `""` | Override the chart name used in resource names. |
| networkPolicy.allowDNS | bool | `true` | Allow DNS egress (UDP/TCP 53) to the cluster's DNS, as `dns` describes it, and nowhere else. |
| networkPolicy.dns.namespace | string | `"kube-system"` | Namespace of the cluster's DNS pods. |
| networkPolicy.dns.nodeLocalAddresses | list | `[]` | Addresses of a NodeLocal DNSCache on each node's own network, such as `169.254.20.10`, which the pods query in place of those pods. The Cilium flavor allows DNS to the node itself instead, since Cilium gives the node's addresses the host's identity. |
| networkPolicy.dns.podSelector | object | `{}` | Labels of the cluster's DNS pods; empty stands for `k8s-app: kube-dns`. |
| networkPolicy.dns.runnerNames | list | `[]` | With the Cilium flavor, the names runner pods may look up besides those of the cluster's Services, `<service>.<namespace>.svc.cluster.local`, which the cluster's DNS answers itself (an ExternalName Service's goes on to the name its author set): the database's host when it is outside the cluster. Each is a name, or a Cilium `matchPattern` in which `*` stands for part of one label. Cilium refuses any other name; the other flavors do not filter names. |
| networkPolicy.egressPorts | list | `[443]` | TCP ports the service pods may egress to for forges and model endpoints. Runner pods reach the gateway alone. |
| networkPolicy.enabled | bool | `false` | Create the NetworkPolicies. |
| networkPolicy.postgres.cidrs | list | `[]` | CIDRs of a database outside the cluster, such as `192.0.2.10/32`. |
| networkPolicy.postgres.namespace | string | `""` | Namespace of the database's pods; the release namespace when empty. |
| networkPolicy.postgres.podSelector | object | `{}` | Labels of the database's pods, such as `cnpg.io/cluster: kritika-postgres` for a CloudNativePG cluster, or of the pods of a session-mode pooler the runner's URI goes through. Empty selects every pod in `namespace`, or none when only `cidrs` is set. |
| networkPolicy.postgresPort | int | `5432` | Postgres port allowed for egress. |
| networkPolicy.type | string | `"default"` | Policy flavor for your CNI: "default" (networking.k8s.io/v1 NetworkPolicy), "cilium" (CiliumNetworkPolicy) or "calico" (projectcalico.org/v3 NetworkPolicy). |
| nodeSelector | object | `{}` | Node selector for pod scheduling. |
| podAnnotations | object | `{}` | Annotations added to the pods. |
| podDisruptionBudget.enabled | bool | `true` | Create a PodDisruptionBudget when there is more than one replica. |
| podDisruptionBudget.maxUnavailable | int | `1` | Maximum pods that may be unavailable, as a count or percentage; takes precedence over `minAvailable` when set. |
| podDisruptionBudget.minAvailable | string | `""` | Minimum pods that must stay available, as a count or percentage. Used unless `maxUnavailable` is set. |
| podLabels | object | `{}` | Labels added to the pods. |
| podSecurityContext | object | `{"runAsGroup":65532,"runAsNonRoot":true,"runAsUser":65532,"seccompProfile":{"type":"RuntimeDefault"}}` | Pod-level securityContext (non-root uid/gid 65532, RuntimeDefault seccomp). |
| priorityClassName | string | `""` | PriorityClass for the pods. Empty uses the cluster default. |
| rbac.create | bool | `true` | Create the Role and RoleBinding kritika serve needs: Jobs in the release namespace, their pods and logs, and the Secrets it hands them. Nothing cluster-wide. |
| readinessProbe | object | `{"httpGet":{"path":"/readyz","port":"metrics"},"periodSeconds":10}` | Readiness probe, on the metrics port. A replica is ready once its configuration file has loaded, the database answers and it serves webhooks and the dashboard, so a rollout takes the previous pod down only once the new one can hold a delivery. |
| replicas | int | `1` | Replicas of kritika serve. Every replica serves webhooks and the dashboard and works jobs; exactly one holds the leader lock at a time. A rollout keeps one serving on its own; set 2 for one to keep serving while a pod or node is lost. |
| resources | object | `{"limits":{"memory":"512Mi"},"requests":{"cpu":"50m","memory":"128Mi"}}` | Resource requests and limits of the kritika serve pods. |
| runner.deadline | string | `""` | Maximum duration of a runner Job; omitted uses the application's default of 15m. |
| runner.image.digest | string | `""` | Pin the runner image by digest (sha256:…); when set, overrides the tag. The release pipeline fills it with the published `-tools` image's digest. |
| runner.image.pullPolicy | string | `"IfNotPresent"` | Runner image pull policy (KRITIKA_RUNNER_IMAGE_PULL_POLICY). |
| runner.image.repository | string | `"ghcr.io/home-operations/kritika"` | Runner image repository. |
| runner.image.tag | string | `""` | Overrides the runner image tag; defaults to the chart appVersion with `-tools` appended. |
| runner.mise.urlReplacements | list | `[]` | Rewrites of the URLs runner tools download from (KRITIKA_RUNNER_MISE_URL_REPLACEMENTS), such as to a caching mirror: mise's `url_replacements`, tried in order until one matches. `from` is a URL prefix, or `regex:` and a pattern whose groups `to` may name as `$1`. The mirror's host must pass the egress gateway; a download is still checked against the repository's lock. |
| runner.resources | object | `{}` | Resources for runner pods (KRITIKA_RUNNER_RESOURCES), copied into the pod spec. |
| runner.runtimeClass | string | `""` | RuntimeClass for runner Jobs, such as gvisor or kata. Empty uses the cluster default. |
| runner.serviceAccount.annotations | object | `{}` | Annotations for the runner ServiceAccount. |
| runner.serviceAccount.create | bool | `true` | Create the runner ServiceAccount: no permissions, no token mounted, and the chart's `imagePullSecrets` so runner Jobs can pull from a private registry. |
| runner.serviceAccount.name | string | `""` | Runner ServiceAccount name; generated from the release name if empty. |
| runner.ttl | string | `"10m"` | How long a finished Job stays for kubectl before Kubernetes removes it (Go duration); the run row keeps everything the Job knew. |
| securityContext | object | `{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]},"readOnlyRootFilesystem":true}` | Container securityContext (no privilege escalation, read-only root filesystem, drops ALL capabilities). |
| service.gatewayPort | int | `8082` | Port of the gateway the kritika serve pods run, on the pods and its Service: the forward proxy runner Jobs are handed as `HTTPS_PROXY`, allowing only the hosts the configuration names (github.com once an app is configured, `egress.allow` less `egress.deny`), so runner pods need no direct internet egress, and the model and similar-code endpoints a runner calls with a per-run token, so no provider key enters a runner pod. |
| service.metricsPort | int | `8081` | Metrics and probe port. |
| service.port | int | `8080` | Public port: the webhooks (`POST /hooks/{app}`) and the dashboard. |
| service.type | string | `"ClusterIP"` | Service type of the public Service. |
| serviceAccount.annotations | object | `{}` | Annotations for the ServiceAccount. |
| serviceAccount.automount | bool | `true` | Automount the API token, which kritika serve needs to create runner Jobs. |
| serviceAccount.create | bool | `true` | Create the ServiceAccount kritika serve runs as. |
| serviceAccount.name | string | `""` | ServiceAccount name; generated from the release name if empty. |
| startupProbe | object | `{"failureThreshold":30,"httpGet":{"path":"/healthz","port":"metrics"},"periodSeconds":2}` | Startup probe, on the metrics port. The liveness and readiness probes wait until it passes, so a pod still opening its listeners is not reported unready; it allows a minute. |
| strategy | object | `{"rollingUpdate":{"maxSurge":1,"maxUnavailable":0},"type":"RollingUpdate"}` | Deployment update strategy. A rolling update that surges one pod and takes none down keeps a replica serving while each is replaced. Helm merges maps, so a switch to `Recreate` also sets `rollingUpdate: null`. |
| terminationGracePeriodSeconds | int | `200` | Grace period for a clean shutdown: kritika serve keeps accepting webhooks, dashboard requests and model steps for 5s while traffic moves off the pod, stops taking jobs and lets running ones finish for up to 100s, then takes up to 90s to hand back the reviews it cut for a retry, and its gateway lets model steps in flight finish for up to 2m. |
| tests.image.pullPolicy | string | `"IfNotPresent"` | `helm test` image pull policy. |
| tests.image.repository | string | `"mirror.gcr.io/curlimages/curl"` | `helm test` connection-pod image; a gcr-mirrored curl, so the test never pulls from Docker Hub. |
| tests.image.tag | string | `"8.22.0@sha256:58adaa4e8dca9c988bae2aba4ab3434a0bb2da16bbe3f92dec39ec7785166777"` | `helm test` image, pinned as `tag@sha256:digest` so Renovate bumps the tag and its digest together. |
| tolerations | list | `[]` | Tolerations for pod scheduling. |
| topologySpreadConstraints | list | `[{"labelSelector":{"matchLabels":{"app.kubernetes.io/instance":"{{ .Release.Name }}","app.kubernetes.io/name":"{{ include \"kritika.name\" . }}"}},"maxSkew":1,"topologyKey":"kubernetes.io/hostname","whenUnsatisfiable":"ScheduleAnyway"}]` | Spread the kritika serve pods across nodes, so with `replicas: 2` a node loss takes one of them: a soft constraint, so a one-node cluster still schedules them. Rendered through `tpl`; empty leaves scheduling to Kubernetes. |
| volumeMounts | list | `[]` | Additional volume mounts on the kritika serve container. |
| volumes | list | `[]` | Additional volumes on the Deployment. |

---

_This README is generated by [helm-docs](https://github.com/norwoodj/helm-docs) from `Chart.yaml` and `values.yaml`. Edit those (or `README.md.gotmpl`) and run `mise run generate`._
