---
title: Configuration
---

For a first installation, use [Setup](/setup/). This page is the full
configuration reference.

## Where settings go

| What you want to change                                            | Where to put it                                         |
| ------------------------------------------------------------------ | ------------------------------------------------------- |
| Sign-in, forge (Git host) connections, providers and review policy | Helm's `config`, or the application YAML file.          |
| Database connections, ingress and runner pods                      | Helm values such as `database`, `ingress` and `runner`. |
| Passwords and API keys                                             | Kubernetes Secrets, exposed through `env` or `envFrom`. |
| Rules for one repository                                           | An optional [`.kritika.yaml`](/repository-config/).     |
| Enable or disable a repository                                     | The dashboard's Repositories page.                      |

Examples on this page use application YAML unless they show a `config` wrapper.
For Helm, put that YAML under `config` in your values file.

Secrets use references such as `{ env: ADMIN_PASSWORD }`. Do not put secret
values in the file. The [setup example](/setup/#deploy) shows the wiring.

## Applying changes

kritika reads configuration at startup. Helm rolls pods when `config`
changes. External ConfigMap changes and secret rotations need a restart
or Reloader.

Unknown keys, invalid values and a setting in both the file and the
environment fail startup. Use durations such as `10m`. Omit a setting to
use its default. Explicit `false` and `0` are preserved.

For an external ConfigMap, set `existingConfigMap` instead of `config`.
It must contain `config.yaml` with `webUrl` and the application
configuration. Omit the keys that the chart owns: `addr`, `metricsAddr`,
`gatewayAddr`, `gatewayUrl`, `databaseAppRole`, `databaseRunnerRole`,
`executor` and `runner`.

If the chart's `gatewayCache.enabled` is set, also omit the gateway
cache keys that it sets. These keys are `gatewayCacheDir`,
`gatewayCacheStorage` and `gatewayCacheMaxEntrySize`. For filesystem
storage, they also include `gatewayCacheMaxSize`. For S3, they also
include the `gatewayCacheS3*` keys.

Outside Helm, `KRITIKA_CONFIG_FILE` names the file. `serve` and
`chatgpt login` require it. Operational configuration can also come from
environment variables, but the same setting cannot appear in both
sources. Application policy uses the file. Environment variables supply
only explicitly referenced values. CEL expression keys end in `Expr`, or
use `expr` inside a condition.

### Checking a file before deploying

`kritika config check [<file>]` loads a file, or
`KRITIKA_CONFIG_FILE`'s, the way `serve` does at startup. It fails on
what fails startup: unknown keys, invalid values, a setting in both the
file and the environment, and no way to sign in.

The command does not connect to the database or any forge. It does not
need the database variables. It also does not need the configuration
that the chart passes besides the file, such as `gatewayUrl` with
`existingConfigMap`. The dashboard's `webUrl` is still required, from
the file or `KRITIKA_WEB_URL`.

The command decodes the runner templates as `serve` does, but it leaves
the dry run to startup. In that dry run, the Kubernetes API makes sure
that a runner Job is valid. The command reads the environment, so each
secret reference's variable must be set. Any value works except a GitLab
`webhookSecret`, which must look like a signing token, `whsec_` followed
by base64:

```sh
docker run --rm -v "$PWD/config.yaml:/config.yaml:ro" \
  -e ADMIN_PASSWORD=x -e GITEA_TOKEN=x -e GITEA_WEBHOOK_SECRET=x \
  ghcr.io/home-operations/kritika:<version> config check /config.yaml
```

To run this check on the `config` that you give Helm, render the chart's
ConfigMap. Then run the check on its `config.yaml`, which also has the
keys that the chart derives:

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
| `KRITIKA_DATABASE_URL`                | Application DSN (database connection string) for `serve`. Runner DSN for `run`                                                                  |
| `KRITIKA_DATABASE_OWNER_URL`          | Owner DSN for migrations and leader work. The leader is the replica, one running kritika pod, that holds the leader lock                        |
| `KRITIKA_GATEWAY_CACHE_S3_ACCESS_KEY` | S3 access key for serve only. Paired with the secret key                                                                                        |
| `KRITIKA_GATEWAY_CACHE_S3_SECRET_KEY` | S3 secret key for serve only. When both keys are absent, serve uses IAM credentials                                                             |
| `KRITIKA_RUNNER_DATABASE_URL`         | Runner DSN for the local executor                                                                                                               |
| `KRITIKA_RUN_SPEC_FILE`               | Per-run job document path                                                                                                                       |
| `KRITIKA_GIT_TOKEN`                   | Per-run Git credential                                                                                                                          |
| `KRITIKA_GATEWAY_TOKEN`               | Per-run model gateway credential                                                                                                                |
| `KRITIKA_DATABASE_TOKEN`              | Per-run database credential                                                                                                                     |
| `KRITIKA_POD_NAME`, `KRITIKA_POD_UID` | The serve pod's name and UID, from the downward API, which the pod's [Kubernetes Events](/metrics/#kubernetes-events) name. The chart sets both |
| `GOMEMLIMIT`, `AUTOMEMLIMIT`          | Go's soft memory limit. The chart sets neither                                                                                                  |

The service supplies per-run inputs to runner Jobs, one Job for each
review, follow-up and index run. Secret references such as
`{ env: OPENROUTER_API_KEY }` can use any environment variable name. The
file defines how that value is used.

`GOMEMLIMIT` and `AUTOMEMLIMIT`: unless `GOMEMLIMIT` is set or
`AUTOMEMLIMIT` is `off`, kritika sets the soft memory limit itself. It uses
90% of the container's memory limit, or the share `AUTOMEMLIMIT` names,
above 0 and at most 1. On Linux, kritika follows a resized limit every
minute. A container with no memory limit gets no soft limit.

## `auth`

`auth` sets how people sign in and what each can do. In Helm these keys
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
with no way to sign in. kritika also refuses the configuration when
nothing can make an admin. To avoid that, set an admin password, or a
`roleMappingExpr` on a provider.

### Roles

| Role   | What it can do                                                                                                                                                                                         |
| ------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Admin  | runs the instance from the dashboard: turns repositories on and off, queues re-runs, cancels and reindexes, and reads every account, the audit log and the [Settings page](/dashboard/#administration) |
| Member | reads reviews, conversations and transcripts, with no write access: every account, or only the accounts that serve the forge accounts their sign-in gave them ([role mappings](#role-mappings))        |

The Settings page lists the instance configuration read-only, each value
with its source: the environment, the configuration file, or kritika's
default.
A secret shows only whether it is set, and a URL's credentials are hidden.
Every admin write is audit-logged in the same transaction as the change it
makes.

A session holds the role that its sign-in gave it. Editing a provider's
role mapping, or rotating the admin password, ends the sessions that it
granted. The next request then signs in again under the new rules.
Sessions from a Forgejo, Gitea or GitLab sign-in also end when the set
of accounts that the instance's connections serve changes. For example,
an account can move to another instance.

#### Role mappings

A `roleMappingExpr` is a [CEL](https://cel.dev) expression evaluated at
sign-in. It yields one of two things. One is a role for every account:
`"admin"`, `"member"` or `""` for none. The other is a map from forge
account to `"member"`, such as `{"github/org-1": "member"}`. With a map,
the user reads only the accounts that serve those forge accounts. `"*"`
as a key stands for every account.

CEL gives both branches of a conditional one type. An expression that
yields a role on one branch and a map on the other wraps one of them in
`dyn()`.

| Provider       | Variables the expression sees                                                                                                                                                   |
| -------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| OIDC           | `claims`: the ID token's claims merged with the UserInfo response. `roles`: the values of the claim that `rolesClaim` names, read from a list, a map's keys, or a single string |
| GitHub         | `login` and `email`. `orgs`: the organizations that the user is an active member of. `teams`: each as `"<org>/<team slug>"`                                                     |
| Forgejo, Gitea | as GitHub's, except that a team is `"<org>/<team name>"`. `email` is empty unless the instance verified the address                                                             |
| GitLab         | `login`. `email`: empty unless the instance verified the address. `groups`: the full paths of the groups that the user is a member of, directly or through an ancestor group    |

A mapping can place a user nowhere, with no role and no accounts. An
OIDC sign-in is then refused, unless `defaultRole: member` lets it read
every account. `defaultRole` defaults to `none`, so a wrong mapping
fails closed.

When the mapping places a user nowhere, a GitHub, Forgejo, Gitea or
GitLab sign-in reads the accounts that serve the user's own account. It
also reads the accounts that serve an organization that the user is an
active member of, which is a top-level group on GitLab. It is refused
when there are none. Accounts that a mapping names are added to those. A
sign-in on a Forgejo, Gitea or GitLab instance reads only the accounts
that a connection serves on that instance. An account of the same name
on another instance is not the user's.

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
| `accounts`      | the users and organizations that it serves. kritika ignores a webhook for any other. One app serves an account |
| `clientId`      | the App's client ID, inline or as a reference like the secrets. One entry declares an App                      |
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

| Key              | What                                                                                                                                                                       |
| ---------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| the entry's key  | the connection's name, which is its webhook path, `/hooks/<name>`. A name belongs to one connection only, under `apps`, `forgejo`, `gitlab` or `gitea`                     |
| `host`           | the instance's hostname, reached over HTTPS                                                                                                                                |
| `accounts`       | the users and organizations that it serves. One connection on its forge serves an account                                                                                  |
| `audience`       | the audience that Forgejo shows for the integration, inline or as a reference. Until it is set, the connection only serves its key, so that you can create the integration |
| `runnerAudience` | optional: the audience of a second, read-only integration that runner pods fetch as. The pod that reads untrusted content then never holds a token that can write          |
| `privateKey`     | the RSA key that kritika signs the integration's tokens with. kritika serves its public half at `<webUrl>/forgejo/.well-known/keys`, and `webUrl` must be HTTPS            |
| `previousKey`    | optional: a key being rotated out. kritika serves it for signature verification by Forgejo, and it signs nothing                                                           |
| `webhookSecret`  | the secret of the webhooks added on each account                                                                                                                           |
| `bots`           | optional: the logins on the instance that are bots, such as a Renovate user, whose pull requests and comments kritika treats as a bot's                                    |

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
| the entry's key | the connection's name, which is its webhook path, `/hooks/<name>`. A name belongs to one connection only, under `apps`, `forgejo`, `gitlab` or `gitea` |
| `host`          | the instance's hostname, reached over HTTPS                                                                                                            |
| `accounts`      | the top-level groups and users that it serves. One connection on its forge serves an account                                                           |
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
| the entry's key | the connection's name, which is its webhook path, `/hooks/<name>`. A name belongs to one connection only, under `apps`, `forgejo`, `gitlab` or `gitea` |
| `host`          | the instance's hostname, reached over HTTPS                                                                                                            |
| `accounts`      | the users and organizations that it serves. One connection on its forge serves an account                                                              |
| `token`         | an access token of the bot user with the `read:user`, `write:repository` and `write:issue` scopes                                                      |
| `runnerToken`   | optional: a token of the user with the `read:repository` scope alone, which runner pods fetch with                                                     |
| `webhookSecret` | the secret of the webhooks added on each account                                                                                                       |
| `bots`          | optional: the logins on the instance that are bots beside the bot users Gitea marks, whose pull requests and comments kritika treats as a bot's        |

An entry under `accounts` names an account by its name alone. The entry
applies to the account of that name on the forge that serves it. A name
served on two forges cannot have an entry.

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

The repository configuration is written at the file's root and applies
to every repository. A repository changes it for itself in its own
[`.kritika.yaml`](/repository-config/). Its keys come in five groups,
with a few keys beside them:

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
index run. The rebuild includes files whose contents did not change.
Editing instance configuration does not queue index jobs by itself. To
apply new ignore rules immediately, reindex an idle repository from the
dashboard.

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

Configuration that differs between repositories belongs in the
repositories' `.kritika.yaml`. For example, this includes a model that
an organization's own key pays for, or a rule for one language. The
instance file holds what every repository shares.

### What only the admin sets

The root takes the keys a repository's own `.kritika.yaml` takes, which
the [`.kritika.yaml` reference](/repository-config/) describes. It also
takes these keys, which are the admin's alone:

| Key                       | What                                                                                                                                                                                                                                                           |
| ------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `confidence.instructions` | guidance on rating risk to the scorer, the second model that scores each reviewed pull request ([risk](/confidence/#risk))                                                                                                                                     |
| `confidence.floors`       | globs by risk level: a change touching a matching path is rated at least that level, whatever the scorer says ([risk](/confidence/#risk))                                                                                                                      |
| `review.cost`             | `true` ends the summary's footer with what the pull request's reviews cost together. Off unless set                                                                                                                                                            |
| `review.replies`          | `true` has kritika answer a reply in one of its finding threads that does not mention it. kritika answers when the reply needs the reviewer. Off unless set                                                                                                    |
| `comments.dashboard`      | `false` keeps the dashboard's URL out of the comments, which anyone can read on a public repository. On unless set                                                                                                                                             |
| `review.incremental`      | how many files of the change can move since the last review before a re-review covers the whole pull request again. 25 unless set ([incremental reviews](#incremental-reviews))                                                                                |
| `trigger.limit`           | how many automatic reviews a pull request gets before kritika pauses them. Unlimited unless set                                                                                                                                                                |
| `trigger.settle`          | how long the review of a push waits to start. A burst of pushes then gets one review rather than one for each head, the pull request's latest commit. At once unless set, at most `1h` ([when a review runs](/reviews/#when-a-review-runs))                    |
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
badge. It also shows each severity as its label in text rather than an
image that the dashboard serves. `@<app slug> review` still re-runs a
review ([commands](/reviews/#commands)).

`trigger.limit` stops a long-lived pull request from spending on every
push. The summary of the last automatic review says that reviews are
paused. `@<app slug> review` and `resume` still work
([commands](/reviews/#commands)).

`trigger.settle` holds the review that a push starts for that long, such
as `90s`. A push within the wait moves the review to the new head. When
its turn comes, the earlier head's job finds that the head moved and
starts nothing. A burst of pushes therefore costs one review, instead of
one started and replaced for each head. The review that opening a pull
request starts is not held.

Someone can ask for a review during the wait, with `@<app slug> review`
or a re-run from the dashboard. That review takes the place of the one
waiting and starts at once. A label change or a poll for the head leaves
it waiting.

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
| `rules`                              | added. A rule under an admin's id is dropped                                                                                  |
| `context`                            | added                                                                                                                         |
| `trigger.include`, `trigger.exclude` | judged beside the admin's lists. A name that an admin's list has is dropped                                                   |
| `skills.paths`                       | replaces                                                                                                                      |
| `skills.scope`                       | adds up by skill name. The file's entry wins                                                                                  |
| `review.agents`                      | specialists added, and an admin's specialist gains only rules and skill scopes. Agents without categories replace the admin's |
| `review.fixes`                       | can only turn it on                                                                                                           |
| `confidence.risk`                    | can only lower it                                                                                                             |
| `review.approve`'s `when`            | judged beside the admin's. `approve: true` keeps the admin's                                                                  |
| `confidence.when`                    | judged beside the admin's. A pull request that it alone leaves unscored is not approved where a score is asked for            |
| `enabled`                            | can only turn it off                                                                                                          |

### Confidence and risk

`confidence` sets a second model's score of each reviewed pull request.
It also sets whether the commit status is gated on that score, and how
risky a change can be and still be approved. [Confidence and
approvals](/confidence/) describes the keys, how a score is reached, the
risk tables and how approvals follow them.

### Which pull requests are reviewed

`trigger.include` and `trigger.exclude` decide which pull requests are
reviewed. A pull request is reviewed when both of these are true. First,
one `include` condition holds, or there are none. Second, no `exclude`
condition holds.

A condition has:

| Key     | What                                                                                                                       |
| ------- | -------------------------------------------------------------------------------------------------------------------------- |
| `expr`  | a CEL expression over the pull request ([the `pr` variable](/repository-config/#include-and-exclude-recipes))              |
| `paths` | globs that hold when a changed path matches one of them                                                                    |
| `name`  | optional: names the condition in a skipped review's commit status. A repository's condition under the same name is dropped |

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

In an expression, `pr.lines` is the lines that the pull request's diff
adds and removes. It leaves out paths that the `ignore` globs match. A
size limit is an exclusion on it:

```yaml
trigger:
  exclude: [{ name: too-large, expr: pr.lines > 2000 }]
```

- A condition on the pull request alone is decided at once, and a pull
  request that it keeps out leaves no review behind. A condition with
  `paths` or `pr.lines` is decided once the pull request is fetched,
  before any model is called. A pull request that it keeps out shows as
  a skipped review. Its commit status names the exclusion that held,
  when that exclusion has a name. The lists are then judged with the
  first 64 KiB of the description.
- A name cannot be given twice in one list.
- A `.kritika.yaml` has lists of its own, judged beside the admin's. A
  pull request must pass both. A condition in the file is dropped when
  one of the admin's has the same name.
- At startup, an expression is compiled and smoke-tested against a
  sample pull request, so a broken one fails startup.
- A pull request from a fork is reviewed like any other unless excluded,
  such as with `exclude: [{ name: forks, expr: pr.fork }]`.
- A review that someone asks for, with `@<app slug> review` or a re-run
  from the dashboard, passes the admin's lists whatever they say. A
  `.kritika.yaml`'s lists still apply to it.

### Incremental reviews

A later push is reviewed against what changed since the last review, as
long as no more than `review.incremental` files of the change moved.
Past that, the re-review covers the whole pull request again.

What moves between the two heads outside the change's own paths is the
base (the target branch), under a merge or a rebase. It does not count.
What the base gained in the change's own files since the last review
also does not count. That is true as long as the merge base that the
last review was made against can still be fetched. The merge base is the
shared ancestor of the pull request and its target branch.

These always cover the whole pull request:

- A re-run at the head that the last review saw.
- A re-run that someone asked for, with `@<app slug> review` or from the
  dashboard.
- A merge or rebase that leaves the change as it was.

A re-review that covers the whole pull request is still shown the last
review's findings, on the diff or off it. It reports again those that
still hold. The exceptions are a re-run at the head that the last review
saw and a re-run that someone asked for. These look afresh. Findings
that those fresh reviews omit stay open and counted, marked as not
rechecked. A finding reported again keeps the inline thread that its
first report opened, however it is worded now.

Each review also leaves notes on what it examined beyond the diff and
found sound. No comment shows these notes. The next re-review is shown
them beside the findings.

When it can, an incremental re-review carries on the last review's
conversation instead. The model then keeps what it read, and the provider
serves the earlier part of the conversation from its prompt cache. The
re-review is told the head moved, shown the diff since, and asked for the
whole review again. Carrying on takes all of these:

- The merge base did not move since the last review, so the diff since
  shows everything that changed.
- The last review's conversation was kept on the same review model less
  than 25 minutes before. For an Anthropic model, whose cache lasts 5
  minutes, the limit is 4.
- The conversation is under 100,000 tokens. Past that size, it costs
  more to read it again at every step than to start afresh.
- The conversation is also under a quarter of the review's
  `agent.tokens`, or of what the monthly cap leaves of them.
- The review will send the same system prompt and tools, and the pull
  request's title, description and linked issues are as they were.

Otherwise the re-review starts afresh, with the last review's findings
and notes. On the dashboard, a review that carried on names the review
before it on its Timeline. Its Conversation starts where that review's
left off, and links to it.

An incremental re-review raises as new only what the new commits
brought, at P0 or P1. It reports again each of the last review's
findings that still holds, whatever its severity. Every review that is
shown earlier findings names the ones that no longer hold, and that
includes a full re-review. The summary lists those as resolved and
resolves their threads.

A finding that the review neither reports again nor names stays open.
The summary lists it among the findings as not rechecked since the head
that a review last reported it at. It counts toward the score, the
status and the approval, and the next re-review is asked about it again.

Some findings in the diff are ones that the last review did not make. If
such a finding is more than 3 lines from every line changed since, it is
held back. The summary lists it folded away, with no inline comment. It
counts toward nothing. That includes the score, the status and the
approval. A file where one of the last review's findings went unreported
is spared, since that finding can be back under a new title.

### The agent

`agent` bounds a review's tool loop, in which the model calls tools step
by step, and a follow-up's:

| Key              | Default              | What                                                                                                                                                 |
| ---------------- | -------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------- |
| `steps`          | 60                   | model calls that the loop can make                                                                                                                   |
| `output`         | 32768                | bytes that one tool call can return. A command's output cut to it is kept whole beside the checkout                                                  |
| `tokens`         | 6,000,000            | prompt and output tokens that the review, or each part of a split one, can spend across its steps                                                    |
| `prompt`         | 64000, at least 8000 | tokens of the prompt that a review and a follow-up start from. The [confidence score](/confidence/#the-score) prompt budget defaults to 96000 tokens |
| `parts`          | 8                    | the most parts that a review over 64 KiB of diff is split into. A value of 1 never splits                                                            |
| `timeout`        | 20m, at most 2h35m   | the loop's wall time, each part's in a split review                                                                                                  |
| `commandTimeout` | 30s, at least 1s     | one command's wall time                                                                                                                              |
| `fetches`        | 8                    | repositories that `fetch_repo` can fetch, and images that `fetch_oci` can inspect, for a review. A split review's parts share them                   |

- The last step is told to submit what the review found. So is any step
  after the review spends nine tenths of its `tokens`, or once its
  `timeout` is close. The `timeout` is close when the time left is no
  more than twice the slowest step's answer. That margin is at least two
  minutes, or a quarter of the time the loop was given when that is
  less. For a part of a split review, the time the loop was given can be
  under its `timeout`.

  A step told to submit can fail to do so in three ways. The submission
  is rejected, the step answers in prose, or the step calls another
  tool, which is refused. Such a step is told again, up to twice, so the steps spent
  reading are not lost to one slip at the end. A review submitted this
  way says in its summary that it possibly did not cover all it meant to
  examine.

  `steps: 1` is the cheapest review. Its one step works over the same
  prompt and must submit the findings, and the review is not taken as
  cut short.

- A finding must be on a file that the review read with `read_file` or
  `read_diff`. That read can be in this review or in the conversation
  that it carries on. A `grep` match is not a read. A submission with a
  finding on a file that the review never read is sent back, with the
  files named. The review then reads each file around the finding's
  lines. It makes sure that the finding holds, or it leaves it out.

  On a step told to submit, the review can read nothing more. There, the
  submission is taken, and such findings are withheld: not posted,
  counted or scored, but named in the summary's notes. A submission is
  sent back once per set of files read. One submitted again with nothing
  more read is taken, as on a step told to submit. The system prompt
  tells the review all this. A review of one step, which reads nothing,
  is not held to this.

- A step can produce 16384 tokens of output. A model that reasons at a
  high [`review.effort`](/models/#effort) can spend them all before it
  calls a tool. Such a step is told to go on rather than to submit, up
  to twice in a row. A third in a row is taken as an answer in prose.
- The prompt holds the system prompt, the pull request, as much of the
  diff as fits, whole files only, and then the context. A re-review's
  earlier findings and notes take what the diff leaves, and the diff
  gives way to them up to a quarter of the prompt. A file left out is
  named in the review's notes. The agent of a review or a follow-up
  reads that file's part of the diff with `read_diff`. That tool numbers
  each line as the head has it. The scorer has no tools and never sees
  that part of the diff.

  The list of changed files marks each one that the change adds, deletes
  or renames. A review's findings can point at any line of the pull
  request's diff, in a file left out as well as in one shown.

  A model with a larger context window can take a larger prompt. A large
  file's diff can then be in the prompt from the start, instead of read
  with `read_diff`. Every step sends the prompt again and counts it
  against `tokens`. A larger `prompt` leaves the agent fewer steps
  unless `tokens` is raised with it. A prompt near `tokens` leaves it
  none.

- A large review, one whose diff is over 64 KiB, is split into parts of
  about 64 KiB each, at most `parts` of them. The size counts only the
  files that no `ignore` glob matches, and the cuts fall at directory
  boundaries where they can. Each part has an agent of its own, with the
  bounds above. That agent reviews the part from the diff of its own
  files and the context built from that diff. It reports findings only
  on its own files, and can still read any other file.

  As many parts run at once as the review model has
  [`concurrency`](#accounts) slots free when the review starts. The free
  slots that count are the review's own and those that no other review
  holds. The rest run as those end. The parts' tool calls run one at a
  time. The parts are published as one review.

  One more call to the review model writes the review's summary from the
  parts' summaries. That call is tried again and falls back as a
  review's step does ([retries](/models/#retries)), and it counts toward
  `tokensPerMonth`. When it still fails, or the cap is reached, the
  summary joins the parts' own.

  A part that ends before it submits leaves its files unreviewed, and
  the summary names them. The review counts as incomplete for the commit
  status and approves nothing. It also withdraws an approval given
  before. No later review builds on it.

  An incremental re-review splits only the diff since the last review,
  when that is over 64 KiB. The token grant and the Job deadline are
  sized for the parts from the pull request's size as the forge reports
  it. The deadline is sized for running the parts in the slots taken.

- The commands that the runner's `-tools` image offers are `curl`,
  `find`, `gh`, `grep` and `jq`. The
  [`tools`](/repository-config/#repository-tools) of a repository's
  `.kritika.yaml` add packages of the aqua registry for that repository,
  such as `helm` or `rg`. That file is read from the merge base. Each
  package offers the commands that the registry names for it. On a
  custom image, the five default commands are offered when found on
  `PATH`, and a repository's tools are offered as on any image. Tool
  install and command resolution failures also appear in the summary.

  The agent is told to use `gh` for GitHub. `gh` signs in with a token
  minted for the run, which can only read the repository under review and
  public repositories. With `gh` or `curl` offered, the agent can also use
  `fetch_repo` and `fetch_oci`. `fetch_repo` fetches another repository's
  files, such as the upstream of a dependency on any host, or lists its
  tags. `fetch_oci` inspects a container image or chart in a registry, or
  lists its tags. [Runner tools](/security/#runner-tools) says what bounds
  a command, a fetch and an inspection.

### Configuration sources

App and provider declarations, authentication configuration, review
policy, and embedding configuration come from the file. For these
sections, environment variables supply values only where the file
explicitly references them, such as
`auth.admin.password: { env: ADMIN_PASSWORD }` or
`providers.openrouter.apiKey: { env: OPENROUTER_API_KEY }`.

### Which repositories run

kritika registers every repository that each connection reaches, once
the configuration is applied and again on every poll. The Repositories
page's "Resync from" button, which names the account's forge, does the
same at once. Each of these also disables the repositories that the
connection no longer reaches, such as one deleted, renamed or
transferred on the forge. A listing that fails or comes back empty
disables nothing.

Newly discovered repositories start turned off, even when the root sets
`enabled: true`. To begin polling, indexing and reviews, turn them on
from the Repositories page. Resyncs and webhooks preserve that choice.
Existing repositories keep their current state.

A rename webhook within the same account preserves enablement. A
transfer to another account requires that account's admin to enable the
repository. If a rename webhook is missed, a sync cannot link the old
and new names, so the new name waits for enablement.

Whether a repository runs is decided in this order:

1. An archived repository never runs. To run it, unarchive it on the
   forge. Then resync. If it is off, turn it on in the UI.
2. A newly discovered repository stays off until an admin turns it on. A
   repository that an admin turned on or off on the Repositories page
   runs as they chose.
3. A fork does not run, since an account can reach many forks that it
   never meant to review.
4. Any other repository runs as the root's `enabled` says: on unless it
   sets `enabled: false`.

The dashboard owns whether a repository is on or off. The root's
`enabled` applies only to existing repositories without a dashboard
choice. A repository that is off, that its connection no longer reaches,
or whose account no connection serves is neither reviewed, polled nor
indexed. Its index is dropped after it is off for `KRITIKA_INDEX_GRACE`,
counted from the leader's hourly sweep that first finds it off. A
repository that runs again before then keeps its index.

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
| `concurrency`    | 2         | how many model calls it runs at once. Index runs hold all but one while they embed, so a review's similar-code lookup has a slot unless `concurrency` is 1 |
| `reviewsPerDay`  | unlimited | reviews in a calendar day                                                                                                                                  |
| `tokensPerMonth` | unlimited | input and output tokens in a calendar month                                                                                                                |

`providers` are its own model keys, which its repositories' models can
name ([an account's own keys](/models/#an-accounts-own-keys)).

An account runs while a connection serves it. An entry for an account no
connection serves is kept, but not run, and the Accounts page lists
it as not served.

## `egress`

`egress` is what runner pods can reach through kritika's gateway, a
forward proxy, beyond the connections' forges. Those forges are
`github.com` and `api.github.com` for an app, and the instance's host
for a Forgejo, GitLab or Gitea connection. `allow` and `deny` list
hosts, IP addresses and CIDRs, and `credentials` names hosts that the
gateway adds a token to. Once `gatewayCacheDir` is set, the gateway
keeps the downloads that a repository's lock pins there.
[Security](/security/#the-egress-gateway) describes them with the
gateway, and the cache with [runner
tools](/security/#the-gateways-download-cache).

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

The following keys live at the file's root unless they start with
`runner.`. Under Helm, root keys go under `config`, while runner
configuration goes under the chart's top-level `runner`. Operational
environment variables remain supported.

| File key                       | Environment variable                        | What                                                                                                                                                                                                                                                                                                                                              |
| ------------------------------ | ------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `pollInterval`                 | `KRITIKA_POLL_INTERVAL`                     | how often the leader lists each app's open pull requests, its backstop for missed webhooks, and once as it is elected. `0s` turns it off. 10m unless set                                                                                                                                                                                          |
| `pollLookback`                 | `KRITIKA_POLL_LOOKBACK`                     | how far back a first or long-idle poll looks. 24h unless set                                                                                                                                                                                                                                                                                      |
| `onboardWindow`                | `KRITIKA_ONBOARD_WINDOW`                    | how many onboarding index jobs the leader keeps queued or running at once. 4 unless set                                                                                                                                                                                                                                                           |
| `indexGrace`                   | `KRITIKA_INDEX_GRACE`                       | how long the index of a repository that stopped running is kept. 720h unless set                                                                                                                                                                                                                                                                  |
| `transcriptRetention`          | `KRITIKA_TRANSCRIPT_RETENTION`              | how long a review's full model transcript is kept, with the hosts that the gateway refused its runs and its settled model-call records. At least 24h. 720h unless set                                                                                                                                                                             |
| `diffRetention`                | `KRITIKA_DIFF_RETENTION`                    | how long a review keeps the diff that it was made from, the context that it read and the repository files that it named. At least 24h. 720h unless set                                                                                                                                                                                            |
| `reviewWorkers`                | `KRITIKA_REVIEW_WORKERS`                    | jobs that one replica runs at once in each of the separate review and follow-up queues. 2 per queue unless set                                                                                                                                                                                                                                    |
| `threadWorkers`                | `KRITIKA_THREAD_WORKERS`                    | finding resolution/restoration jobs that one replica runs at once, independently of model follow-ups. 1 unless set                                                                                                                                                                                                                                |
| `indexWorkers`                 | `KRITIKA_INDEX_WORKERS`                     | index jobs that one replica runs at once. 1 unless set                                                                                                                                                                                                                                                                                            |
| `leaderRetryInterval`          | `KRITIKA_LEADER_RETRY_INTERVAL`             | how often a replica retries the leader lock, and the holder makes sure that it still has it. 15s unless set                                                                                                                                                                                                                                       |
| `gatewayTokenTtl`              | `KRITIKA_GATEWAY_TOKEN_TTL`                 | how long a run's gateway token outlives its Job's deadline, in case the replica that minted it dies first. 1h unless set                                                                                                                                                                                                                          |
| `gatewayCacheDir`              | `KRITIKA_GATEWAY_CACHE_DIR`                 | where the gateway keeps downloads, in a directory of its own. It keeps what a repository's lock pins, from any host the egress rules allow. Nothing is kept unless set. The chart's `gatewayCache.enabled` renders it ([the gateway's cache](/security/#the-gateways-download-cache))                                                             |
| `gatewayCacheMaxSize`          | `KRITIKA_GATEWAY_CACHE_MAX_SIZE`            | filesystem backend only: the most that the gateway's cache keeps, in bytes or with `Ki`, `Mi`, `Gi` or `Ti`. The least recently used downloads go first. Apart from that, it is the most that its fetches under way can write. 2Gi unless set                                                                                                     |
| `gatewayCacheStorage`          | `KRITIKA_GATEWAY_CACHE_STORAGE`             | `filesystem` (default) or `s3`. Both require a private `gatewayCacheDir`, used for completed local fills                                                                                                                                                                                                                                          |
| `gatewayCacheMaxEntrySize`     | `KRITIKA_GATEWAY_CACHE_MAX_ENTRY_SIZE`      | largest eligible download. 256Mi by default, at most 5Gi for S3                                                                                                                                                                                                                                                                                   |
| `gatewayCacheS3Endpoint`       | `KRITIKA_GATEWAY_CACHE_S3_ENDPOINT`         | S3 API origin including scheme and optional port. Required for S3                                                                                                                                                                                                                                                                                 |
| `gatewayCacheS3Region`         | `KRITIKA_GATEWAY_CACHE_S3_REGION`           | signing region. Required for S3                                                                                                                                                                                                                                                                                                                   |
| `gatewayCacheS3Bucket`         | `KRITIKA_GATEWAY_CACHE_S3_BUCKET`           | existing bucket. Required for S3                                                                                                                                                                                                                                                                                                                  |
| `gatewayCacheS3Prefix`         | `KRITIKA_GATEWAY_CACHE_S3_PREFIX`           | instance-specific object prefix. `gateway/` by default                                                                                                                                                                                                                                                                                            |
| `gatewayCacheS3ForcePathStyle` | `KRITIKA_GATEWAY_CACHE_S3_FORCE_PATH_STYLE` | use path-style bucket addressing. True by default                                                                                                                                                                                                                                                                                                 |
| `aquaRegistryUrl`              | `KRITIKA_AQUA_REGISTRY_URL`                 | the aqua registry that the gateway keeps for runners to resolve a repository's `tools` against. aqua-registry's `registry.yaml` on its main branch unless set. A release's tag, such as `https://raw.githubusercontent.com/aquaproj/aqua-registry/v4.578.0/registry.yaml`, holds it still, and a mirror serves a cluster that cannot reach GitHub |
| `aquaRegistryRefresh`          | `KRITIKA_AQUA_REGISTRY_REFRESH`             | how often the gateway asks for the registry again, conditionally. When a fetch fails, the gateway keeps the last one that parsed. 1h unless set                                                                                                                                                                                                   |
| `logLevel`                     | `KRITIKA_LOG_LEVEL`                         | `debug`, `info`, `warn` or `error`. `info` unless set                                                                                                                                                                                                                                                                                             |
| `logFormat`                    | `KRITIKA_LOG_FORMAT`                        | `json` or `text`. `json` unless set                                                                                                                                                                                                                                                                                                               |
| `runner.jobTemplate`           | `KRITIKA_RUNNER_JOB_TEMPLATE`               | what kritika sets on every runner Job, as a Job template spells it                                                                                                                                                                                                                                                                                |
| `runner.podTemplate`           | `KRITIKA_RUNNER_POD_TEMPLATE`               | what kritika sets on every runner pod, as a pod template spells it                                                                                                                                                                                                                                                                                |

A transcript can contain repository content that the agent read, and
every member of its account can read it. A review past the diff retention keeps
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

Finding resolution/restoration jobs use a dedicated `thread` queue, with
one worker per replica by default. They call the forge and update the
database. They do not launch runner pods or acquire model slots. The
default reserves capacity during model follow-ups, but permits less
parallelism than two idle follow-up workers. If queue-delay metrics show
that it is needed, increase `threadWorkers`. Database connections and
forge API limits remain shared with reviews, so raising worker counts
does not guarantee higher throughput.

Other keys in the file are `webUrl` (required public URL), `addr`,
`metricsAddr`, `gatewayAddr`, `gatewayUrl`, `executor`,
`databaseAppRole` and `databaseRunnerRole`. The chart derives all except
`webUrl` from deployment values. The `runner` section also accepts
`serviceAccount`, `databaseSecret` and `databaseSecretKey`. The chart
derives these from its runner and database values.

The runner image is `runner.podTemplate.spec.containers[0].image`. How
long a finished Job is kept is
`runner.jobTemplate.spec.ttlSecondsAfterFinished`. Database Secret names
are references, not DSNs.
