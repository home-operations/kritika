---
title: Helm chart values
---

Use the [setup example](/setup/#deploy) for a first installation. For every
available value, see the generated
[chart reference](https://github.com/home-operations/kritika/blob/main/charts/kritika/README.md).

| Values                            | Purpose                                             | Guide                                                                                                    |
| --------------------------------- | --------------------------------------------------- | -------------------------------------------------------------------------------------------------------- |
| `config`, `existingConfigMap`     | Application settings; choose one source.            | [Configuration](/configuration/)                                                                         |
| `env`, `envFrom`                  | Credentials from existing Secrets.                  | [Setup](/setup/#deploy)                                                                                  |
| `database`                        | Three database connection Secrets and role names.   | [Database](/database/)                                                                                   |
| `runner`                          | Runner resources, runtime, placement and deadlines. | [Security](/security/#runner-sandbox)                                                                    |
| `networkPolicy`                   | Allowed network access for pods.                    | [Security](/security/#hardening-an-install)                                                              |
| `service`, `ingress`, `httpRoute` | Ports and public access.                            | [Setup](/setup/#deploy)                                                                                  |
| `image`, `replicas`, `resources`  | Service image, replica count and resources.         | [Chart topology](https://github.com/home-operations/kritika/blob/main/charts/kritika/README.md#topology) |
| `monitoring`                      | Prometheus scraping, alerts and Grafana dashboards. | [Monitoring](/metrics/)                                                                                  |

## values.yaml

The chart's [values.yaml](https://github.com/home-operations/kritika/blob/main/charts/kritika/values.yaml)
contains all defaults. Its
[JSON Schema](https://github.com/home-operations/kritika/blob/main/charts/kritika/values.schema.json)
supports editor completion and Helm validation.
