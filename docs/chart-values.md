# Helm chart values

The **kritika** chart is documented value by value in the generated
[chart README](https://github.com/home-operations/kritika/blob/main/charts/kritika/README.md),
kept in step with `values.yaml` by helm-docs (CI fails if it goes stale). The
chart also ships a
[`values.schema.json`](https://github.com/home-operations/kritika/blob/main/charts/kritika/values.schema.json)
for editor completion and `helm install` validation.

This page is the orientation: which groups of values exist and where their
behavior is explained, followed by the full `values.yaml`.

| Values                                                         | What they set                                                                                                                                                                                                                                     | Explained in                                                                                               |
| -------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------- |
| `image`, `replicas`, `strategy`, `resources`, the pod settings | the one Deployment of `kritika serve`                                                                                                                                                                                                             | [the chart README](https://github.com/home-operations/kritika/blob/main/charts/kritika/README.md#topology) |
| `serviceAccount`, `rbac`, `podDisruptionBudget`                | its ServiceAccount, the Role that lets it create runner Jobs, and, with more than one replica, the budget that lets a drain take one at a time                                                                                                    |                                                                                                            |
| `config`, `existingConfigMap` | Application YAML, inline or from an external ConfigMap; mutually exclusive | [Configuration](configuration.md) |
| `env`, `envFrom`                                               | the Secrets that set the variables the configuration file names                                                                                                                                                                                   | [Configuration file](configuration.md)                                                                     |
| `database`                                                     | the Postgres host and the owner, application and runner roles' Secrets                                                                                                                                                                            | [Postgres with CloudNativePG](database.md)                                                                 |
| `runner` | Runner image, ServiceAccount, resources, tools, TTL, deadline and runtime class | [Security](security.md#runner-tools) |
| `service`, `ingress`, `httpRoute`                              | the public, metrics and gateway ports, and how the public one is exposed                                                                                                                                                                          | [Security](security.md#the-egress-gateway)                                                                 |
| `networkPolicy`                                                | the policies that confine runner pods to the gateway                                                                                                                                                                                              | [Security](security.md#hardening-an-install)                                                               |
| `monitoring`, the probes                                       | the ServiceMonitor, alerts and Grafana dashboard                                                                                                                                                                                                  | [Metrics](metrics.md)                                                                                      |
| `tests`                                                        | the `helm test` pod                                                                                                                                                                                                                               |                                                                                                            |

## values.yaml

```yaml
--8<-- "charts/kritika/values.yaml"
```
