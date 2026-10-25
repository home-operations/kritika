---
title: Setup
---

This guide gets kritika from a fresh installation to its first GitHub review.
You do not need a `.kritika.yaml` in your repositories. In place of the App,
declare:

- A bot for a Forgejo instance, as [Git forges](/forges/#forgejo) describes.
- A service account for a GitLab instance, as
  [Git forges](/forges/#gitlab) describes.
- A bot user for a Gitea instance, as [Git forges](/forges/#gitea)
  describes.

## Before you start

Have these ready:

- A Kubernetes cluster and Helm.
- [Postgres with VectorChord and three database roles](/database/).
- An HTTPS address, such as `https://kritika.example.com`, reachable by GitHub.
- A model provider credential. The example below uses OpenRouter.

Install kritika in its own namespace. Keep the chart's admission policy enabled,
configure its NetworkPolicy for your DNS and database, and use a sandboxed runner
runtime where available. See [deployment security](/security/#hardening-an-install).

## Register the GitHub App

In your GitHub account or organization's **Developer settings**, create a GitHub App.

1. Enable its webhook at `https://kritika.example.com/hooks/github`.
   The final path segment must match the name under `config.apps`.
2. Set a random webhook secret.
3. Give it the permissions below and subscribe to the listed events.
4. Generate a private key and save the App's **client ID**.

| Permission             | Access                                                                              |
| ---------------------- | ----------------------------------------------------------------------------------- |
| Contents               | Read-only; read and write if the bot should resolve finding threads.                |
| Metadata               | Read-only.                                                                          |
| Pull requests          | Read and write.                                                                     |
| Issues                 | Read-only; read and write for reactions on pull requests and conversation comments. |
| Commit statuses        | Read and write.                                                                     |
| Members (organization) | Read-only, only for GitHub sign-in that uses organization role mappings.            |

Subscribe to **Pull request**, **Pull request review comment**,
**Pull request review thread**, **Issue comment**, **Push** and **Repository**.
Installation events arrive automatically.

Contents write access also permits pushing code, although kritika does not push.
If you change permissions later, each installation must approve them.

Allow installation only on your account unless you intend to serve several.
Anyone can install a public App. kritika reviews only the accounts listed in
`apps` and ignores any other installation. An admin can uninstall such an
installation from the dashboard's Connections section.

## Deploy

Create a namespace for kritika and provision these Secrets there using your
usual secret management tool. Do not commit their values.

| Secret in this example         | Keys                                                                                           |
| ------------------------------ | ---------------------------------------------------------------------------------------------- |
| `kritika-postgres-credentials` | `uri`: owner database connection string.                                                       |
| `kritika-postgres-app`         | `uri`: application database connection string.                                                 |
| `kritika-postgres-runner`      | `uri`: runner database connection string.                                                      |
| `kritika-credentials`          | `ADMIN_PASSWORD`, `OPENROUTER_API_KEY`, `GITHUB_APP_PRIVATE_KEY`, `GITHUB_APP_WEBHOOK_SECRET`. |
| `kritika-tls`                  | TLS certificate and key for your public hostname.                                              |

The [database guide](/database/#secrets-and-connection-uris) explains the connection
strings. Owner and application connections need direct or session-mode access;
transaction-mode pooling is not supported.

Create Helm values like these. Replace the hostname, account, client ID and
`vendor/large-model` with your own values. Adapt ingress, DNS and database
network settings to your cluster.

```yaml
config:
  webUrl: https://kritika.example.com
  auth:
    admin:
      password: { env: ADMIN_PASSWORD }
  apps:
    github:
      accounts: [your-org]
      clientId: Iv1.example
      privateKey: { env: GITHUB_APP_PRIVATE_KEY }
      webhookSecret: { env: GITHUB_APP_WEBHOOK_SECRET }
  providers:
    openrouter:
      type: openrouter
      apiKey: { env: OPENROUTER_API_KEY }
  review:
    model: openrouter/vendor/large-model

database:
  owner:
    existingSecret: kritika-postgres-credentials
  app:
    existingSecret: kritika-postgres-app
  runner:
    existingSecret: kritika-postgres-runner

envFrom:
  - secretRef:
      name: kritika-credentials

ingress:
  enabled: true
  hosts:
    - host: kritika.example.com
      paths: [{ path: / }]
  tls:
    - hosts: [kritika.example.com]
      secretName: kritika-tls
```

The chart's NetworkPolicies, on by default, expect Postgres in the release
namespace, standard cluster DNS, and forges and models on port 443. Set
[`networkPolicy.postgres`](/database/#secrets-and-connection-uris),
[`networkPolicy.dns`](/security/#name-lookups) and
`networkPolicy.egressPorts` if yours differ. Allow what kritika serve
reaches in the cluster, such as an S3 bucket or a model server, in
`networkPolicy.extraEgress`.

`networkPolicy.type: auto` renders Cilium's or Calico's own policies where
the cluster serves their API, and standard NetworkPolicies otherwise. Argo CD
passes the cluster's APIs to `helm template` for it, but plain
`helm template` needs `--api-versions` or a type set.

Install the chart using your values file:

```sh
helm upgrade --install kritika oci://ghcr.io/home-operations/charts/kritika \
  --namespace kritika --create-namespace --values my-values.yaml
```

The chart restarts pods when `config` changes. Changes to external Secrets or
ConfigMaps need a restart or a controller such as Reloader.

## Install the App

Install the GitHub App on each account listed under `config.apps.github.accounts`.
Choose all repositories or selected repositories.

Installing the App grants access. Newly discovered repositories still need to
be enabled in the dashboard.

## Sign in

Open your public URL and sign in as `admin` with the password you supplied.

1. Open **All accounts → Accounts** and check the setup checklist.
2. Select your account and open **Repositories**.
3. Enable a repository you want reviewed.

You can add [GitHub, Forgejo, Gitea, GitLab or OIDC sign-in](/configuration/#auth)
later.
[Embedding](/models/#the-embedder) is optional: it adds similar-code search
but is not needed for the first review.

## Check that it works

Open a pull request in the enabled repository, or comment `@<app-slug> review`
on an existing one as a user with write access. Look for a running review in
the dashboard, then a summary comment on GitHub.

If nothing happens, check:

1. **Repository enabled?** Check the account's Repositories page.
2. **Webhook delivered?** Open the GitHub App's recent webhook deliveries.
3. **Job failed or waiting?** Open the dashboard's Queue and expand its error.

| Webhook response | Meaning                                                                 |
| ---------------- | ----------------------------------------------------------------------- |
| `202`            | Accepted; the account and repository must still be enabled for reviews. |
| `204`            | Ping received.                                                          |
| `400`, `413`     | Invalid or oversized delivery.                                          |
| `401`            | Webhook secret missing or different from GitHub's.                      |
| `404`            | App name in the URL is wrong.                                           |
| `500`            | kritika could not queue the delivery.                                   |

For everyday use, see [reviews and commands](/reviews/).

## Without webhooks

Polling catches missed pull request updates every 10 minutes by default.
Set `config.pollInterval` to change the interval, or `0s` to disable it.
Polling also catches up the code index when webhooks stop arriving.

Reviews must wait for a poll, and mentions are never answered without webhooks.
Shorter intervals use more GitHub API requests.
