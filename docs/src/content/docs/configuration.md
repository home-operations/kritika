---
title: Configuration
---

For a first installation, use [Setup](/setup/). This page is the full settings
reference.

## Where settings go

| What you want to change                                 | Where to put it                                         |
| ------------------------------------------------------- | ------------------------------------------------------- |
| Sign-in, forge connections, providers and review policy | Helm's `config`, or the application YAML file.          |
| Database connections, ingress and runner pods           | Helm values such as `database`, `ingress` and `runner`. |
| Passwords and API keys                                  | Kubernetes Secrets, exposed through `env` or `envFrom`. |
| Rules for one repository                                | An optional [`.kritika.yaml`](/repository-config/).     |
| Enable or disable a repository                          | The dashboard's Repositories page.                      |

Examples on this page use application YAML unless they show a `config` wrapper.
For Helm, put that YAML under `config` in your values file.

Secrets use references such as `{ env: ADMIN_PASSWORD }`. Do not put secret
values in the file. The [setup example](/setup/#deploy) shows the wiring.

## Applying changes

kritika reads configuration at startup. Helm rolls pods when `config` changes.
External ConfigMap changes and secret rotations need a restart or Reloader.
Unknown keys, invalid values and duplicate file/environment settings fail
startup. Use durations such as `10m`. Omit a setting to use its default.
Explicit `false` and `0` are preserved.

For an external ConfigMap, set `existingConfigMap` instead of `config`. It must
contain `config.yaml` with `webUrl` and application settings. Omit the keys
the chart owns:

- `addr`, `metricsAddr`, `gatewayAddr`, `gatewayUrl`, `databaseAppRole`,
  `databaseRunnerRole`, `executor` and `runner`;
- when the chart's `gatewayCache.enabled` is set, the gateway cache keys it
  sets: `gatewayCacheDir`, `gatewayCacheStorage`,
  `gatewayCacheMaxEntrySize`, and `gatewayCacheMaxSize` for filesystem
  storage or the `gatewayCacheS3*` keys for S3.

Outside Helm, `KRITIKA_CONFIG_FILE` names the file and is required by `serve`
and `chatgpt login`. Operational settings can also come from environment
variables, but the same setting cannot appear in both sources. Application
policy uses the file; environment variables supply only explicitly referenced
values. CEL expression keys end in `Expr`, or use `expr` inside a condition.

### Checking a file before deploying

`kritika config check [<file>]` loads a file, or `KRITIKA_CONFIG_FILE`'s, the
way `serve` does at startup, and fails on what would fail startup: unknown
keys, invalid values, a setting in both the file and the environment, and no
way to sign in. It does not connect to the database or any forge, and does
not need the database variables or the settings the chart passes besides the
file, such as `gatewayUrl` with `existingConfigMap`. The dashboard's
`webUrl` is still required, from the file or `KRITIKA_WEB_URL`. It decodes
the runner templates as `serve` does, but leaves to startup the dry run in
which the Kubernetes API validates a runner Job. It reads the environment,
so each secret reference's variable must be set. Any value works except a
GitLab `webhookSecret`, which must look like a signing token, `whsec_`
followed by base64:

```sh
docker run --rm -v "$PWD/config.yaml:/config.yaml:ro" \
  -e ADMIN_PASSWORD=x -e GITEA_TOKEN=x -e GITEA_WEBHOOK_SECRET=x \
  ghcr.io/home-operations/kritika:<version> config check /config.yaml
```

To check the `config` you give Helm, render the chart's ConfigMap and check
its `config.yaml`, which also has the keys the chart derives:

```sh
helm template kritika oci://ghcr.io/home-operations/charts/kritika -f values.yaml \
  -s templates/configmap.tpl | yq '.data["config.yaml"]' > config.yaml
```

## Helm templates

Helm renders inline `config` through `tpl` with the release context. For example:

```yaml
config:
  webUrl: "https://{{ .Release.Name }}.turbo.ac"
```

Templates can access `.Release`, `.Values` and other Helm context. This also
applies to strings in prompts and rules. To preserve a literal expression, use
`{{ "{{ .Example }}" }}`. External ConfigMaps and files loaded directly by the
application are not processed by Helm.

## Environment-only inputs

These inputs have no configuration-file equivalent:

| Variable                              | Purpose                                                                                                                                         |
| ------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------- |
| `KRITIKA_CONFIG_FILE`                 | Configuration file path, required by `serve` and `chatgpt login`, and `config check`'s file when it names none                                  |
| `KRITIKA_DATABASE_URL`                | Application DSN for `serve`; runner DSN for `run`                                                                                               |
| `KRITIKA_DATABASE_OWNER_URL`          | Owner DSN for migrations and leader work                                                                                                        |
| `KRITIKA_GATEWAY_CACHE_S3_ACCESS_KEY` | S3 access key for serve only; paired with the secret key                                                                                        |
| `KRITIKA_GATEWAY_CACHE_S3_SECRET_KEY` | S3 secret key for serve only; when both keys are absent, use IAM credentials                                                                    |
| `KRITIKA_RUNNER_DATABASE_URL`         | Runner DSN for the local executor                                                                                                               |
| `KRITIKA_RUN_SPEC_FILE`               | Per-run job document path                                                                                                                       |
| `KRITIKA_GIT_TOKEN`                   | Per-run Git credential                                                                                                                          |
| `KRITIKA_GATEWAY_TOKEN`               | Per-run model gateway credential                                                                                                                |
| `KRITIKA_DATABASE_TOKEN`              | Per-run database credential                                                                                                                     |
| `KRITIKA_POD_NAME`, `KRITIKA_POD_UID` | The serve pod's name and UID, from the downward API, which the pod's [Kubernetes Events](/metrics/#kubernetes-events) name. The chart sets both |
| `GOMEMLIMIT`, `AUTOMEMLIMIT`          | Go's soft memory limit. The chart sets neither                                                                                                  |

The service supplies per-run inputs to runner Jobs. Secret references such as
`{ env: OPENROUTER_API_KEY }` can use any environment variable name. The file
defines how that value is used.

`GOMEMLIMIT` and `AUTOMEMLIMIT`: unless `GOMEMLIMIT` is set or
`AUTOMEMLIMIT` is `off`, kritika sets the soft memory limit itself. It uses
90% of the container's memory limit, or the share `AUTOMEMLIMIT` names,
above 0 and at most 1. On Linux, kritika follows a resized limit every
minute. A container with no memory limit gets no soft limit.

## `auth`

`auth` sets how people sign in and what each may do. In Helm these keys
belong under `config.auth`. Secret references read variables supplied from
existing Kubernetes Secrets.

| Key                       | Purpose                                                  |
| ------------------------- | -------------------------------------------------------- |
| `sessionTTL`              | Dashboard session lifetime.                              |
| `admin.user`              | Local administrator username.                            |
| `admin.password`          | Environment reference for the administrator password.    |
| `oidc.name`               | Sign-in button label.                                    |
| `oidc.issuer`             | OpenID Connect issuer URL.                               |
| `oidc.clientId`           | Client ID, inline or an environment reference.           |
| `oidc.clientSecret`       | Environment reference for the client secret.             |
| `oidc.scopes`             | Requested scopes as a YAML list.                         |
| `oidc.rolesClaim`         | Claim containing the user's roles.                       |
| `oidc.roleMappingExpr`    | Expression mapping identity claims to a dashboard role.  |
| `oidc.defaultRole`        | Role when no mapping applies.                            |
| `github.clientId`         | Client ID, inline or an environment reference.           |
| `github.clientSecret`     | Environment reference for the client secret.             |
| `github.roleMappingExpr`  | Expression mapping GitHub identity to a dashboard role.  |
| `forgejo.host`            | The Forgejo instance's hostname, reached over HTTPS.     |
| `forgejo.clientId`        | Client ID, inline or an environment reference.           |
| `forgejo.clientSecret`    | Environment reference for the client secret.             |
| `forgejo.roleMappingExpr` | Expression mapping Forgejo identity to a dashboard role. |
| `gitea.*`                 | As `forgejo.*`, for a Gitea instance.                    |
| `gitlab.*`                | As `forgejo.*`, for a GitLab instance.                   |

```yaml
auth:
  admin:
    password: { env: ADMIN_PASSWORD }
  oidc:
    name: Company SSO
    issuer: https://idp.example.com
    clientId: kritika-dashboard
    clientSecret: { env: OIDC_CLIENT_SECRET }
    scopes: [openid, email, profile]
    rolesClaim: groups
    roleMappingExpr: '"kritika-admins" in roles ? "admin" : ("kritika-users" in roles ? "member" : "")'
  github:
    clientId: Iv1.abc123
    clientSecret: { env: GITHUB_CLIENT_SECRET }
    roleMappingExpr: 'login == "user-1" ? "admin" : ""'
  forgejo:
    host: codeberg.org
    clientId: 0a1b2c3d-…
    clientSecret: { env: FORGEJO_CLIENT_SECRET }
```

- `admin` is the local admin. It signs in on the sign-in page with a
  username and `password`. The username is `admin` unless `user` sets
  another. The local admin exists only while a password is set. It is the
  way into a fresh instance, and a way in when every provider is down. Ten
  failed attempts from one address within 15 minutes lock that address
  out until the window passes. Each replica counts its own attempts.
- `oidc` signs in through any OpenID Connect issuer. The issuer is an
  `https` URL. The sign-in page labels it `name`, or "SSO" when unset.
- `github` signs in on github.com with the client of an OAuth App or of a
  GitHub App. The App kritika reviews through can also be the sign-in
  client, with a client secret generated on its settings page (see
  [setup](/setup/)).
- `forgejo` signs in through an OAuth2 application of the Forgejo instance
  at `host`, as [Git forges](/forges/#sign-in-with-forgejo) describes.
- `gitea` signs in through an OAuth2 application of the Gitea instance at
  its `host`, as [Git forges](/forges/#sign-in-with-gitea) describes.
- `gitlab` signs in through an OAuth application of the GitLab instance at
  its `host`, as [Git forges](/forges/#sign-in-with-gitlab) describes.
- `sessionTTL` is how long a dashboard session lasts, between 5 minutes and
  30 days. It defaults to 12 hours.

A provider must allow the callback URL
`<KRITIKA_WEB_URL>/auth/callback/<type>`, where `<type>` is `oidc`,
`github`, `forgejo`, `gitea` or `gitlab`. The dashboard refuses to start
with no way to sign in. The configuration is also refused when nothing
could make an admin. To avoid that, set an admin password, or a
`roleMappingExpr` on a provider.

### Roles

| Role   | What it may do                                                                                                                                                                                         |
| ------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Admin  | runs the instance from the dashboard: turns repositories on and off, queues re-runs, cancels and reindexes, and reads every account, the audit log and the [Settings page](/dashboard/#administration) |
| Member | reads reviews, conversations and transcripts, with no write access: every account, or only the accounts that serve the forge accounts their sign-in gave them ([role mappings](#role-mappings))        |

The Settings page lists the instance settings read-only, each with
its source: the environment, the configuration file, or kritika's default.
A secret shows only whether it is set, and a URL's credentials are hidden.
Every admin write is audit-logged in the same transaction as the change it
makes.

A session holds the role its sign-in gave it. Editing a provider's role
mapping, or rotating the admin password, ends the sessions it granted, so
the next request signs in again under the new rules. Sessions from a
Forgejo, Gitea or GitLab sign-in also end when the set of accounts served
by that instance's connections changes, such as when an account moves to
another instance.

#### Role mappings

A `roleMappingExpr` is a [CEL](https://cel.dev) expression evaluated at
sign-in. It yields one of:

- a role for every account: `"admin"`, `"member"` or `""` for none;
- a map from forge account to `"member"`, such as
  `{"github/org-1": "member"}`, which reads only the accounts serving
  those forge accounts. `"*"` as a key stands for every account.

CEL gives both branches of a conditional one type. An expression that
yields a role on one branch and a map on the other wraps one of them in
`dyn()`.

| Provider       | Variables the expression sees                                                                                                                                                   |
| -------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| OIDC           | `claims`, the ID token's claims merged with the UserInfo response; `roles`, the values of the claim that `rolesClaim` names, read from a list, a map's keys, or a single string |
| GitHub         | `login`; `email`; `orgs`, the organizations the user is an active member of; `teams`, each as `"<org>/<team slug>"`                                                             |
| Forgejo, Gitea | as GitHub's, except that a team is `"<org>/<team name>"`; `email` is empty unless the instance verified the address                                                             |
| GitLab         | `login`; `email`, empty unless the instance verified the address; `groups`, the full paths of the groups the user is a member of, directly or through an ancestor group         |

When the mapping places nobody:

- An OIDC sign-in is refused, unless `defaultRole: member` lets it read
  every account. `defaultRole` defaults to `none`, so a wrong mapping fails
  closed.
- A GitHub, Forgejo, Gitea or GitLab sign-in reads the accounts serving the
  user's own account, or an organization they are an active member of (a
  top-level group on GitLab). It is refused when there are none. Accounts
  a mapping names are added to those. A sign-in on a Forgejo, Gitea or
  GitLab instance reads only the accounts a connection serves on that
  instance. An account of the same name on another instance is not the
  user's.

A mapping that fails to evaluate refuses the sign-in.

## `apps`

`apps` declares the GitHub Apps kritika serves accounts through. The
[setup guide](/setup/) covers creating one. [Git forges](/forges/)
covers what each forge supports.

```yaml
apps:
  github:
    accounts: [org-1, user-1]
    clientId: Iv1.example
    privateKey: { env: GITHUB_APP_PRIVATE_KEY }
    webhookSecret: { env: GITHUB_APP_WEBHOOK_SECRET }
```

| Key             | What                                                                                                           |
| --------------- | -------------------------------------------------------------------------------------------------------------- |
| the entry's key | the app's name, which is its webhook path, `/hooks/<name>`                                                     |
| `accounts`      | the users and organizations it serves; a webhook for any other is ignored, and an account is served by one app |
| `clientId`      | the App's client ID, inline or as a reference like the secrets; an App is declared by one entry                |
| `privateKey`    | the App's private key                                                                                          |
| `webhookSecret` | the same value as the App's webhook secret                                                                     |

## `forgejo`

`forgejo` declares the Forgejo authorized integrations kritika serves
accounts through, one per instance and set of accounts. [Git forges](/forges/#forgejo)
covers the bot user, the integration and the webhooks.

```yaml
forgejo:
  codeberg:
    host: codeberg.org
    accounts: [org-1]
    audience: 7f1c…
    runnerAudience: 2b9e…
    privateKey: { env: FORGEJO_PRIVATE_KEY }
    webhookSecret: { env: FORGEJO_WEBHOOK_SECRET }
```

| Key              | What                                                                                                                                                             |
| ---------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| the entry's key  | the connection's name, which is its webhook path, `/hooks/<name>`; a name belongs to one connection only, under `apps`, `forgejo`, `gitlab` or `gitea`           |
| `host`           | the instance's hostname, reached over HTTPS                                                                                                                      |
| `accounts`       | the users and organizations it serves; an account is served by one connection on its forge                                                                       |
| `audience`       | the audience Forgejo shows for the integration, inline or as a reference; until it is set, the connection only serves its key, so the integration can be created |
| `runnerAudience` | optional: the audience of a second, read-only integration that runner pods fetch as, so the pod that reads untrusted content never holds a token that can write  |
| `privateKey`     | the RSA key kritika signs the integration's tokens with; kritika serves its public half at `<webUrl>/forgejo/.well-known/keys`, and `webUrl` must be HTTPS       |
| `previousKey`    | optional: a key being rotated out; it is served for Forgejo to verify with, and signs nothing                                                                    |
| `webhookSecret`  | the secret of the webhooks added on each account                                                                                                                 |
| `bots`           | optional: the logins on the instance that are bots, such as a Renovate user, whose pull requests and comments kritika treats as a bot's                          |

## `gitlab`

`gitlab` declares the GitLab service accounts kritika serves accounts
through, one per instance and set of accounts. GitLab 19 or later is
needed. [Git forges](/forges/#gitlab) covers the account, its tokens and
the webhooks.

```yaml
gitlab:
  work:
    host: gitlab.example.com
    accounts: [platform]
    token: { env: GITLAB_TOKEN }
    runnerToken: { env: GITLAB_RUNNER_TOKEN }
    webhookSecret: { env: GITLAB_WEBHOOK_SIGNING_TOKEN }
```

| Key             | What                                                                                                                                                   |
| --------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------ |
| the entry's key | the connection's name, which is its webhook path, `/hooks/<name>`; a name belongs to one connection only, under `apps`, `forgejo`, `gitlab` or `gitea` |
| `host`          | the instance's hostname, reached over HTTPS                                                                                                            |
| `accounts`      | the top-level groups and users it serves; an account is served by one connection on its forge                                                          |
| `token`         | a personal access token of the service account with the `api` scope                                                                                    |
| `runnerToken`   | optional: a token of the account with the `read_repository` scope alone, which runner pods fetch with                                                  |
| `webhookSecret` | the signing token of the webhooks, `whsec_` followed by base64                                                                                         |

## `gitea`

`gitea` declares the Gitea bot users kritika serves accounts through, one
per instance and set of accounts. Gitea 28 or later is needed.
[Git forges](/forges/#gitea) covers the user, its tokens and the
webhooks.

```yaml
gitea:
  home:
    host: gitea.example.com
    accounts: [org-1]
    token: { env: GITEA_TOKEN }
    runnerToken: { env: GITEA_RUNNER_TOKEN }
    webhookSecret: { env: GITEA_WEBHOOK_SECRET }
```

| Key             | What                                                                                                                                                   |
| --------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------ |
| the entry's key | the connection's name, which is its webhook path, `/hooks/<name>`; a name belongs to one connection only, under `apps`, `forgejo`, `gitlab` or `gitea` |
| `host`          | the instance's hostname, reached over HTTPS                                                                                                            |
| `accounts`      | the users and organizations it serves; an account is served by one connection on its forge                                                             |
| `token`         | an access token of the bot user with the `read:user`, `write:repository` and `write:issue` scopes                                                      |
| `runnerToken`   | optional: a token of the user with the `read:repository` scope alone, which runner pods fetch with                                                     |
| `webhookSecret` | the secret of the webhooks added on each account                                                                                                       |
| `bots`          | optional: the logins on the instance that are bots beside the bot users Gitea marks, whose pull requests and comments kritika treats as a bot's        |

An entry under `accounts` names an account by its name alone, so it applies
to the account of that name on the forge that serves it. A name served on
two forges can have no entry.

## `providers` and `embedding`

`providers` are the instance's model keys. `embedding` is the embedder
that builds each repository's similar-code index from one of them.
[Models](/models/) describes both, with retries, fallback, local models
and OpenCode.

```yaml
providers:
  openrouter:
    type: openrouter
    apiKey: { env: OPENROUTER_API_KEY }
embedding:
  model: openrouter/voyageai/voyage-code-4
  dims: 1024
```

## Repository settings

The repository settings are written at the file's root and apply to every
repository. A repository changes them for itself in its own
[`.kritika.yaml`](/repository-config/). They come in five groups, with a
few keys beside them:

| Group        | Keys                                                                                                     | What they set                              |
| ------------ | -------------------------------------------------------------------------------------------------------- | ------------------------------------------ |
| `review`     | `model`, `fallback`, `effort`, `fixes`, `approve`, `incremental`, `diagram`, `cost`, `replies`, `agents` | what a review runs on and what it says     |
| `confidence` | `model`, `fallback`, `effort`, `threshold`, `gate`, `risk`, `instructions`, `floors`, `when`             | how a review is judged                     |
| `trigger`    | `include`, `exclude`, `limit`                                                                            | which pull requests are reviewed, and when |
| `comments`   | `inline`, `summary`, `finding`, `dashboard`                                                              | what is posted                             |
| `agent`      | `steps`, `output`, `tokens`, `prompt`, `parts`, `timeout`, `commandTimeout`, `fetches`                   | the bounds of a review's tool loop         |
| beside them  | `enabled`, `rules`, `context`, `skills`, `ignore`, and `limits` at the root alone                        |                                            |

`ignore` lists globs of paths left out of a review's initial diff and
context, the index, and the agent's searches and checkout. These add to
kritika's own: vendored trees, lockfiles, generated and minified code,
source maps and logs. A pull request that changes nothing else is skipped.
The agent can still read an ignored path with `read_file`, or its diff
with `read_diff`. `ignore` is not an access restriction.

Changing the effective ignore rules triggers a full rebuild on the next
index run, including files whose contents have not changed. Editing
instance configuration does not queue index jobs by itself. To apply new
ignore rules immediately, reindex an idle repository from the dashboard.

```yaml
review:
  model: openrouter/vendor/large-model
  fallback: openrouter/vendor/small-model
trigger:
  exclude:
    - expr: pr.draft
    - { name: forks, expr: pr.fork }
rules:
  - { id: no-tokens, rule: "Never log a token, key or password." }
  - id: renovate
    rule: Say what the update breaks, from the release notes in the body.
    when: [{ expr: pr.headRef.startsWith("renovate/") }]
skills:
  scope:
    review-renovate-pr:
      when: [{ expr: pr.headRef.startsWith("renovate/") }]
```

Settings that differ between repositories belong in the repositories'
`.kritika.yaml`, such as a model an organization's own key pays for, or a
rule for one language. The instance file holds what every repository
shares.

### What only the admin sets

The root takes the keys a repository's own `.kritika.yaml` takes, which
the [`.kritika.yaml` reference](/repository-config/) describes. It also
takes these keys, which are the admin's alone:

| Key                       | What                                                                                                                                                                                                                                                           |
| ------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `confidence.instructions` | guidance to the scorer on rating risk ([risk](/confidence/#risk))                                                                                                                                                                                              |
| `confidence.floors`       | globs by risk level: a change touching a matching path is rated at least that level, whatever the scorer says ([risk](/confidence/#risk))                                                                                                                      |
| `review.cost`             | `true` ends the summary's footer with what the pull request's reviews have cost together; off unless set                                                                                                                                                       |
| `review.replies`          | `true` has kritika answer a reply in one of its finding threads that does not mention it, when the reply needs the reviewer; off unless set                                                                                                                    |
| `comments.dashboard`      | `false` keeps the dashboard's URL out of the comments, which anyone can read on a public repository; on unless set                                                                                                                                             |
| `review.incremental`      | how many files of the change may move since the last review before a re-review covers the whole pull request again; 25 unless set ([incremental reviews](#incremental-reviews))                                                                                |
| `trigger.limit`           | how many automatic reviews a pull request gets before kritika pauses them; unlimited unless set                                                                                                                                                                |
| `agent`                   | the bounds of a review's tool loop ([the agent](#the-agent))                                                                                                                                                                                                   |
| `limits`                  | every account's limits, unless its entry sets its own ([`accounts`](#accounts))                                                                                                                                                                                |
| `enabled`                 | the default for existing repositories without a dashboard choice ([which repositories run](#which-repositories-run)). New discoveries wait for UI enablement. A repository's `.kritika.yaml` can only set `enabled: false`, which turns its own repository off |

`review.cost` totals every model call charged to one of the pull
request's reviews. It is off unless set, since the summary comment is
public, on a fork's pull request too, and the figure is the instance's
spend.

`review.replies` covers only a reply from someone with write access
([replies in finding threads](/reviews/#replies-in-finding-threads)). It
is off unless set, since each answer is a model call nobody asked for by
name.

`comments.dashboard` set to `false` leaves the summary without a re-run
badge, and shows each severity as its label in text rather than an image
the dashboard serves. `@<app slug> review` still re-runs a review
([commands](/reviews/#commands)).

`trigger.limit` stops a long-lived pull request from spending on every
push. The summary of the last automatic review says that reviews are
paused. `@<app slug> review` and `resume` still work
([commands](/reviews/#commands)).

A repository adds tools of its own through the
[`tools`](/repository-config/#repository-tools) of its `.kritika.yaml`.

The keys in the table above stay at the root: a `.kritika.yaml` that
names one does not parse.

### Precedence

A value applies in this order: kritika's default, the file's root, and the
repository's `.kritika.yaml`. A value the `.kritika.yaml` writes replaces
the root's, except:

| Key                                  | From a `.kritika.yaml`                                                                                                        |
| ------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------- |
| `ignore`                             | globs add up                                                                                                                  |
| `rules`                              | added; a rule under an admin's id is dropped                                                                                  |
| `context`                            | added                                                                                                                         |
| `trigger.include`, `trigger.exclude` | judged beside the admin's lists; a name an admin's has is dropped                                                             |
| `skills.paths`                       | replaces                                                                                                                      |
| `skills.scope`                       | adds up by skill name; the file's entry wins                                                                                  |
| `review.agents`                      | specialists added, and an admin's specialist gains only rules and skill scopes; agents without categories replace the admin's |
| `review.fixes`                       | can only turn it on                                                                                                           |
| `confidence.risk`                    | can only lower it                                                                                                             |
| `review.approve`'s `when`            | judged beside the admin's; `approve: true` keeps the admin's                                                                  |
| `confidence.when`                    | judged beside the admin's; a pull request it alone leaves unscored is not approved where a score is asked for                 |
| `enabled`                            | can only turn it off                                                                                                          |

### Confidence and risk

`confidence` sets a second model's score of each reviewed pull request,
whether the commit status is gated on it, and how risky a change may be
and still be approved. [Confidence and approvals](/confidence/) describes
the keys, how a score is reached, the risk tables and how approvals follow
them.

### Which pull requests are reviewed

`trigger.include` and `trigger.exclude` decide which pull requests are
reviewed. A pull request is reviewed when both of these are true:

- one `include` condition holds, or there are none;
- no `exclude` condition holds.

A condition has:

| Key     | What                                                                                                                       |
| ------- | -------------------------------------------------------------------------------------------------------------------------- |
| `expr`  | a CEL expression over the pull request ([the `pr` variable](/repository-config/#include-and-exclude-recipes))              |
| `paths` | globs that hold when a changed path matches one of them                                                                    |
| `name`  | optional; names the condition in a skipped review's commit status; a repository's condition under the same name is dropped |

A condition takes an `expr`, `paths` or both. When it has both, both must
hold.

```yaml
trigger:
  include:
    - expr: pr.baseRef == "main"
  exclude:
    - expr: pr.draft
    - { name: skip-label, expr: 'pr.labels.exists(l, l.name == "skip-review")' }
    - { name: migrations, paths: ["db/migrations/**"] }
```

In an expression, `pr.lines` is the lines the pull request's diff adds
and removes, leaving out paths the `ignore` globs match. A size limit is
an exclusion on it:

```yaml
trigger:
  exclude: [{ name: too-large, expr: pr.lines > 2000 }]
```

- **When it is decided:** a condition on the pull request alone is decided
  at once, and a pull request it keeps out leaves no review behind. A
  condition with `paths` or `pr.lines` is decided once the pull request is
  fetched, before any model is called. A pull request it keeps out shows
  as a skipped review. Its commit status names the exclusion that held,
  when that exclusion has a name. The lists are then judged with the first
  64 KiB of the description.
- **Names:** a name may not be given twice in one list.
- **A repository's lists:** a `.kritika.yaml` has lists of its own,
  judged beside the admin's. A pull request must pass both. A condition in
  the file is dropped when one of the admin's has the same name.
- **Checked at startup:** an expression is compiled and smoke-tested
  against a sample pull request, so a broken one fails startup.
- **Forks:** a pull request from a fork is reviewed like any other unless
  excluded, such as with `exclude: [{ name: forks, expr: pr.fork }]`.
- **Asked-for reviews:** a review someone asks for, with
  `@<app slug> review` or a re-run from the dashboard, passes the admin's
  lists whatever they say. A `.kritika.yaml`'s lists still apply to it.

### Incremental reviews

A later push is reviewed against what changed since the last review, as
long as no more than `review.incremental` files of the change moved. Past
that, the re-review covers the whole pull request again. What moves
between the two heads outside the change's own paths is the base, under a
merge or a rebase, and does not count. Nor does what the base gained in
the change's own files since the last review, as long as the merge base
that review was made against can still be fetched.

These always cover the whole pull request:

- a re-run at the head the last review saw;
- a re-run someone asked for, with `@<app slug> review` or from the
  dashboard;
- a merge or rebase that leaves the change as it was.

A re-review that covers the whole pull request is still shown the last
review's findings, on the diff or off it, to report again those that
still hold. The exceptions are a re-run at the head the last review saw
and one someone asked for: these look afresh. Findings those fresh
reviews omit stay open and counted, marked as not rechecked. A finding
reported again keeps the inline thread its first report opened, however
it is worded now. Each review also leaves notes on what it checked beyond
the diff and found sound. No comment shows these notes. The next
re-review is shown them beside the findings.

When it can, an incremental re-review carries on the last review's
conversation instead. The model then keeps what it read, and the provider
serves the earlier part of the conversation from its prompt cache. The
re-review is told the head moved, shown the diff since, and asked for the
whole review again. Carrying on takes all of these:

- the merge base has not moved since the last review, so the diff since
  shows everything that changed;
- the last review's conversation was kept on the same review model less
  than 25 minutes before, or 4 for an Anthropic model, whose cache lasts
  5 minutes;
- the conversation is under 100,000 tokens, since past that, reading it
  again at every step costs more than starting afresh;
- it is also under a quarter of the review's `agent.tokens`, or of what
  the monthly cap leaves of them;
- the review would send the same system prompt and tools, and the pull
  request's title, description and linked issues are as they were.

Otherwise the re-review starts afresh, with the last review's findings
and notes. On the dashboard, a review that carried on names the review
before it on its Timeline. Its Conversation starts where that review's
left off, and links to it.

An incremental re-review raises as new only what the new commits brought,
at P0 or P1. It reports again each of the last review's findings that
still holds, whatever its severity. Every review that is shown earlier
findings, including a full re-review, names the ones that no longer hold.
The summary lists those as resolved and resolves their threads. A finding
the review neither reports again nor names stays open. The summary lists
it among the findings as not rechecked since the head a review last
reported it at. It counts toward the score, the status and the approval,
and the next re-review is asked about it again.

A finding in the diff that the last review did not make, and that is more
than 3 lines from every line changed since, is held back. The summary
lists it folded away, with no inline comment. It counts toward nothing,
the score, the status and the approval included. A file where one of the
last review's findings went unreported is spared, since that finding may
be back under a new title.

### The agent

`agent` bounds a review's tool loop, and a follow-up's:

| Key              | Default              | What                                                                                                                                            |
| ---------------- | -------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------- |
| `steps`          | 60                   | model calls the loop may make                                                                                                                   |
| `output`         | 32768                | bytes one tool call may return; a command's output cut to it is kept whole beside the checkout                                                  |
| `tokens`         | 6,000,000            | prompt and output tokens the review, or each part of a split one, may spend across its steps                                                    |
| `prompt`         | 64000, at least 8000 | tokens of the prompt a review and a follow-up start from; the [confidence score](/confidence/#the-score) prompt budget defaults to 96000 tokens |
| `parts`          | 8                    | the most parts a review over 64 KiB of diff is split into; 1 never splits                                                                       |
| `timeout`        | 20m, at most 2h35m   | the loop's wall time, each part's in a split review                                                                                             |
| `commandTimeout` | 30s, at least 1s     | one command's wall time                                                                                                                         |
| `fetches`        | 8                    | repositories `fetch_repo` may fetch, and images `fetch_oci` may inspect, for a review; a split review's parts share them                        |

- **Submitting:** the last step is told to submit what the review has
  found. So is any step once the review has spent nine tenths of its
  `tokens`, or once its `timeout` is close. The `timeout` is close when
  the time left is no more than twice the slowest step's answer. That
  margin is at least two minutes, or a quarter of the time the loop was
  given when that is less. For a part of a split review, the time the loop
  was given can be under its `timeout`.

  A step told to submit can fail to do so because the submission was
  rejected, it answered in prose, or it called another tool, which is
  refused. Such a step is told again, up to twice, so the steps spent
  reading are not lost to one slip at the end. A review submitted this way
  says in its summary that it may not cover all it meant to check.
  `steps: 1` is the cheapest review. Its one step works over the same
  prompt and must submit the findings, and the review is not taken as cut
  short.

- **Output per step:** a step may produce 16384 tokens, and a model
  reasoning at a high [`review.effort`](/models/#effort) can spend them
  all before it calls a tool. Such a step is told to go on rather than to
  submit, up to twice in a row. A third in a row is taken as an answer in
  prose.
- **The prompt** holds the system prompt, the pull request, as much of the
  diff as fits, whole files only, and then the context. A re-review's
  earlier findings and notes take what the diff leaves, and the diff gives
  way to them up to a quarter of the prompt. A file left out is named in
  the review's notes. The agent of a review or a follow-up reads that
  file's part of the diff with `read_diff`, which numbers each line as the
  head has it. The scorer has no tools and never sees it.

  The list of changed files marks each one the change adds, deletes or
  renames. A review's findings may point at any line of the pull request's
  diff, in a file left out as well as in one shown. A model with a larger
  context window can take a larger prompt, so a large file's diff can be in
  it from the start instead of read with `read_diff`. Every step sends the
  prompt again and counts it against `tokens`. A larger `prompt` leaves the
  agent fewer steps unless `tokens` is raised with it, and a prompt near
  `tokens` leaves it none.

- **Large reviews:** a review whose diff is over 64 KiB is split into parts
  of about 64 KiB each, at most `parts` of them. The size counts only the
  files no `ignore` glob matches, and the cuts fall at directory boundaries
  where they can. Each part is reviewed by an agent of its own with the
  bounds above, from its own files' diff and the context built from it. It
  reports findings only on its own files, and can still read any other file.

  As many parts run at once as the review model has
  [`concurrency`](#accounts) slots free when the review starts, counting
  its own and those no other review holds. The rest run as those end. The
  parts' tool calls run one at a time. They are published as one review.
  One more call to the review model writes its summary from theirs. That
  call is tried again and falls back as a review's step is
  ([retries](/models/#retries)), and counts towards `tokensPerMonth`.
  When it still fails, or the cap is reached, the summary joins the parts'
  own.

  A part that ends before it submits leaves its files unreviewed, and the
  summary names them. The review counts as incomplete for the commit
  status and approves nothing, withdrawing an approval given before. No
  later review builds on it. An incremental re-review splits only the diff
  since the last review, when that is over 64 KiB. The token grant and the
  Job deadline are sized for the parts from the pull request's size as the
  forge reports it. The deadline is sized for running the parts in the
  slots taken.

- **Commands:** the runner's `-tools` image offers `curl`, `find`, `gh`,
  `grep` and `jq`. The [`tools`](/repository-config/#repository-tools) of
  a repository's `.kritika.yaml`, read from the merge base, add packages
  of the aqua registry for that repository, such as `helm` or `rg`, each
  offering the commands the registry names for it. On a custom image, the
  five default commands are offered when found on `PATH`, and a
  repository's tools as on any image. Tool install and command resolution
  failures also appear in the summary.

  The agent is told to use `gh` for GitHub. `gh` signs in with a token
  minted for the run, which can only read the repository under review and
  public repositories. With `gh` or `curl` offered, the agent can also use
  `fetch_repo` and `fetch_oci`. `fetch_repo` fetches another repository's
  files, such as the upstream of a dependency on any host, or lists its
  tags. `fetch_oci` inspects a container image or chart in a registry, or
  lists its tags. [Runner tools](/security/#runner-tools) says what bounds
  a command, a fetch and an inspection.

### Configuration sources

App and provider declarations, authentication settings, review policy, and
embedding settings come from the file. For these sections, environment variables
supply values only where the file explicitly references them, such as
`auth.admin.password: { env: ADMIN_PASSWORD }` or
`providers.openrouter.apiKey: { env: OPENROUTER_API_KEY }`.

### Which repositories run

kritika registers every repository each connection reaches, once the
configuration is applied and again on every poll. The Repositories page's
"Resync from" button, which names the account's forge, does the same at
once. Each of these also
disables the repositories the connection no longer reaches, such as one
deleted, renamed or transferred on the forge. A listing that fails or
comes back empty disables nothing. Newly discovered repositories start
**off**, even when the root sets `enabled: true`. Turn them on from the Repositories page to begin
polling, indexing and reviews. Resyncs and webhooks preserve that choice.
Existing repositories keep their current state. A rename webhook within
the same account preserves enablement. A transfer to another account
requires that account's admin to enable the repository. If a rename
webhook is missed, a sync cannot link the old and new names, so the new
name waits for enablement. Whether one runs is decided in this order:

1. an archived repository never runs: unarchive it on the forge, then
   resync, then turn it on in the UI if it is off;
2. a newly discovered repository stays off until an admin turns it on;
   one an admin turned on or off on the Repositories page runs as they chose;
3. a fork does not run, since an account can reach many forks it never
   meant to review;
4. any other runs as the root's `enabled` says: on unless it sets
   `enabled: false`.

The dashboard owns a repository's on or off. The root's `enabled` applies
only to existing repositories without a dashboard choice.
A repository that is off, that its connection no longer reaches, or whose
account no connection serves is neither reviewed, polled nor indexed. Its
index is dropped once it has been off for `KRITIKA_INDEX_GRACE`, counted from the
leader's hourly sweep that first finds it off. A repository that runs
again before then keeps its index.

## `accounts`

An account is a user or organization a connection serves:
`github/<name>`, `forgejo/<name>`, `gitlab/<name>` or `gitea/<name>`. Its
entry under `accounts`, keyed by its name, holds what is the account's
alone:

```yaml
accounts:
  org-1:
    limits: { reviewsPerDay: 50, tokensPerMonth: 20000000 }
    providers:
      org-1-key: { type: openrouter, apiKey: { env: ORG_1_OPENROUTER_API_KEY } }
```

`limits` caps the account. The root's `limits` sets every account's
limits, and an account's entry replaces them key by key:

| Key              | Default   | What                                                                                                                                                       |
| ---------------- | --------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `concurrency`    | 2         | how many model calls it runs at once; index runs hold all but one while they embed, so a review's similar-code lookup has a slot unless `concurrency` is 1 |
| `reviewsPerDay`  | unlimited | reviews in a calendar day                                                                                                                                  |
| `tokensPerMonth` | unlimited | input and output tokens in a calendar month                                                                                                                |

`providers` are its own model keys, which its repositories' models can
name ([an account's own keys](/models/#an-accounts-own-keys)).

An account runs while a connection serves it. An entry for an account no
connection serves is kept, but not run, and the Accounts page lists
it as not served.

## `egress`

`egress` is what runner pods may reach through kritika's gateway beyond
the connections' forges. Those forges are `github.com` and
`api.github.com` for an app, and the instance's host for a Forgejo,
GitLab or Gitea connection. `allow` and `deny` list hosts, IP addresses
and CIDRs, and `credentials` names hosts the gateway adds a token to.
Once `gatewayCacheDir` is set, the gateway keeps the downloads a
repository's lock pins there.
[Security](/security/#the-egress-gateway) describes them with the
gateway, and the cache with [runner tools](/security/#the-gateways-download-cache).

```yaml
# The chart's gatewayCache.enabled sets this one.
gatewayCacheDir: /var/cache/kritika
egress:
  allow: ["*", 10.10.0.5]
  deny: ["*.pastebin.com", 203.0.113.0/24]
  credentials:
    api.github.com: { env: GITHUB_TOKEN }
```

## How kritika runs

The following keys live at the file's root unless they start with `runner.`.
Under Helm, root keys go under `config`, while runner settings go under the
chart's top-level `runner`. Operational environment variables remain supported.

| File key                       | Environment variable                        | What                                                                                                                                                                                                                                                                                                                                         |
| ------------------------------ | ------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `pollInterval`                 | `KRITIKA_POLL_INTERVAL`                     | how often the leader lists each app's open pull requests, its backstop for missed webhooks; `0s` turns it off; 10m unless set                                                                                                                                                                                                                |
| `pollLookback`                 | `KRITIKA_POLL_LOOKBACK`                     | how far back a first or long-idle poll looks; 24h unless set                                                                                                                                                                                                                                                                                 |
| `onboardWindow`                | `KRITIKA_ONBOARD_WINDOW`                    | how many onboarding index jobs the leader keeps queued or running at once; 4 unless set                                                                                                                                                                                                                                                      |
| `indexGrace`                   | `KRITIKA_INDEX_GRACE`                       | how long the index of a repository that stopped running is kept; 720h unless set                                                                                                                                                                                                                                                             |
| `transcriptRetention`          | `KRITIKA_TRANSCRIPT_RETENTION`              | how long a review's full model transcript is kept, and the hosts the gateway refused its runs, at least 24h; 720h unless set                                                                                                                                                                                                                 |
| `diffRetention`                | `KRITIKA_DIFF_RETENTION`                    | how long a review keeps the diff it was made from, the context it read and the repository files it named, at least 24h; 720h unless set                                                                                                                                                                                                      |
| `reviewWorkers`                | `KRITIKA_REVIEW_WORKERS`                    | jobs one replica runs at once in each of the separate review and follow-up queues; 2 per queue unless set                                                                                                                                                                                                                                    |
| `threadWorkers`                | `KRITIKA_THREAD_WORKERS`                    | finding resolution/restoration jobs one replica runs at once, independently of model follow-ups; 1 unless set                                                                                                                                                                                                                                |
| `indexWorkers`                 | `KRITIKA_INDEX_WORKERS`                     | index jobs one replica runs at once; 1 unless set                                                                                                                                                                                                                                                                                            |
| `leaderRetryInterval`          | `KRITIKA_LEADER_RETRY_INTERVAL`             | how often a replica retries the leader lock, and the holder checks it still has it; 15s unless set                                                                                                                                                                                                                                           |
| `gatewayTokenTtl`              | `KRITIKA_GATEWAY_TOKEN_TTL`                 | how long a run's gateway token outlives its Job's deadline, in case the replica that minted it dies first; 1h unless set                                                                                                                                                                                                                     |
| `gatewayCacheDir`              | `KRITIKA_GATEWAY_CACHE_DIR`                 | where the gateway keeps downloads, in a directory of its own. It keeps what a repository's lock pins, from any host the egress rules allow. Nothing is kept unless set; the chart's `gatewayCache.enabled` renders it ([the gateway's cache](/security/#the-gateways-download-cache))                                                        |
| `gatewayCacheMaxSize`          | `KRITIKA_GATEWAY_CACHE_MAX_SIZE`            | filesystem backend only: the most the gateway's cache keeps, in bytes or with `Ki`, `Mi`, `Gi` or `Ti`, the least recently used downloads going first. Apart from that, it is the most its fetches under way may write; 2Gi unless set                                                                                                       |
| `gatewayCacheStorage`          | `KRITIKA_GATEWAY_CACHE_STORAGE`             | `filesystem` (default) or `s3`; both require a private `gatewayCacheDir`, used for completed local fills                                                                                                                                                                                                                                     |
| `gatewayCacheMaxEntrySize`     | `KRITIKA_GATEWAY_CACHE_MAX_ENTRY_SIZE`      | largest eligible download; 256Mi by default, at most 5Gi for S3                                                                                                                                                                                                                                                                              |
| `gatewayCacheS3Endpoint`       | `KRITIKA_GATEWAY_CACHE_S3_ENDPOINT`         | S3 API origin including scheme and optional port; required for S3                                                                                                                                                                                                                                                                            |
| `gatewayCacheS3Region`         | `KRITIKA_GATEWAY_CACHE_S3_REGION`           | signing region; required for S3                                                                                                                                                                                                                                                                                                              |
| `gatewayCacheS3Bucket`         | `KRITIKA_GATEWAY_CACHE_S3_BUCKET`           | existing bucket; required for S3                                                                                                                                                                                                                                                                                                             |
| `gatewayCacheS3Prefix`         | `KRITIKA_GATEWAY_CACHE_S3_PREFIX`           | instance-specific object prefix; `gateway/` by default                                                                                                                                                                                                                                                                                       |
| `gatewayCacheS3ForcePathStyle` | `KRITIKA_GATEWAY_CACHE_S3_FORCE_PATH_STYLE` | use path-style bucket addressing; true by default                                                                                                                                                                                                                                                                                            |
| `aquaRegistryUrl`              | `KRITIKA_AQUA_REGISTRY_URL`                 | the aqua registry the gateway keeps for runners to resolve a repository's `tools` against; aqua-registry's `registry.yaml` on its main branch unless set. A release's tag, such as `https://raw.githubusercontent.com/aquaproj/aqua-registry/v4.578.0/registry.yaml`, holds it still, and a mirror serves a cluster that cannot reach GitHub |
| `aquaRegistryRefresh`          | `KRITIKA_AQUA_REGISTRY_REFRESH`             | how often the gateway asks for the registry again, conditionally, keeping the last one that parsed when a fetch fails; 1h unless set                                                                                                                                                                                                         |
| `logLevel`                     | `KRITIKA_LOG_LEVEL`                         | `debug`, `info`, `warn` or `error`; `info` unless set                                                                                                                                                                                                                                                                                        |
| `logFormat`                    | `KRITIKA_LOG_FORMAT`                        | `json` or `text`; `json` unless set                                                                                                                                                                                                                                                                                                          |
| `runner.jobTemplate`           | `KRITIKA_RUNNER_JOB_TEMPLATE`               | what kritika sets on every runner Job, as a Job template spells it                                                                                                                                                                                                                                                                           |
| `runner.podTemplate`           | `KRITIKA_RUNNER_POD_TEMPLATE`               | what kritika sets on every runner pod, as a pod template spells it                                                                                                                                                                                                                                                                           |

A transcript may contain repository content the agent read, and every
member of its account can read it. A review past the diff retention keeps
its findings, summary and what it read by name and size. The dashboard's
diff and raw views say the bodies were not kept.

`runner.jobTemplate` takes these fields:

- `metadata.labels` and `metadata.annotations`.
- `spec.activeDeadlineSeconds`, a runner Job's deadline: at most 7200, and
  900 unless set. A review's or follow-up's agent `timeout` lengthens it
  when it needs more.
- `spec.ttlSecondsAfterFinished`: at least 1, and 600 unless set.

kritika refuses any other field, and its own labels and annotations, at
startup. Set it as JSON in the environment variable or as a YAML mapping
in the file. The chart's `runner.jobTemplate` renders it.

`runner.podTemplate` takes these fields:

- `metadata.labels` and `metadata.annotations`.
- `spec.resources` for the whole pod, `runtimeClassName`,
  `priorityClassName`, `hostUsers`, `nodeSelector`, `tolerations`,
  `affinity` and `topologySpreadConstraints`.
- `spec.containers`: one container named `runner`, with its `image`
  (required), `imagePullPolicy` and `env`. `env` holds the admin's
  variables for the run tool's commands, each a `value` or a Secret's key
  (`valueFrom.secretKeyRef`).

kritika refuses any other field, its own labels and AppArmor annotations
at startup. Set it as JSON in the environment variable or as a YAML
mapping in the file. The chart's `runner.podTemplate` renders it, with
this release's `-tools` image unless it names one.

### Thread workers

Finding resolution/restoration jobs use a dedicated `thread` queue, with one
worker per replica by default. They call the forge and update the database;
they do not launch runner pods or acquire model slots. The default reserves
capacity during model follow-ups, but permits less parallelism than two idle
follow-up workers. Increase `threadWorkers` if queue-delay metrics show
it is needed. Database connections and forge API limits remain shared with
reviews, so raising worker counts does not guarantee higher throughput.

Other file settings are `webUrl` (required public URL), `addr`, `metricsAddr`,
`gatewayAddr`, `gatewayUrl`, `executor`, `databaseAppRole` and
`databaseRunnerRole`. The chart derives all except `webUrl` from deployment
values. The `runner` section also accepts `serviceAccount`, `databaseSecret`
and `databaseSecretKey`; the chart derives these from its runner and
database values. The runner image is
`runner.podTemplate.spec.containers[0].image`. How long a finished Job is
kept is `runner.jobTemplate.spec.ttlSecondsAfterFinished`. Database Secret
names are references, not DSNs.
