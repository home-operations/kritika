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
`gatewayAddr`, `gatewayUrl`, `databaseAppRole`, `executor` and `runner`.

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
| `KRITIKA_DATABASE_URL`                | Application DSN (database connection string) for `serve`                                                                                        |
| `KRITIKA_DATABASE_OWNER_URL`          | Owner DSN for migrations and leader work. The leader is the replica, one running kritika pod, that holds the leader lock                        |
| `KRITIKA_GATEWAY_CACHE_S3_ACCESS_KEY` | S3 access key for serve only. Paired with the secret key                                                                                        |
| `KRITIKA_GATEWAY_CACHE_S3_SECRET_KEY` | S3 secret key for serve only. When both keys are absent, serve uses IAM credentials                                                             |
| `KRITIKA_RUN_SPEC_FILE`               | Per-run job document path                                                                                                                       |
| `KRITIKA_GIT_TOKEN`                   | Per-run Git credential                                                                                                                          |
| `KRITIKA_GATEWAY_TOKEN`               | Per-run model gateway credential                                                                                                                |
| `KRITIKA_REPORT_TOKEN`                | Per-run report credential                                                                                                                       |
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

| Group        | Keys                                                                                          | What they set                              |
| ------------ | --------------------------------------------------------------------------------------------- | ------------------------------------------ |
| `review`     | `model`, `fallback`, `effort`, `fixes`, `approve`, `incremental`, `diagram`, `cost`, `agents` | what a review runs on and what it says     |
| `confidence` | `model`, `fallback`, `effort`, `threshold`, `gate`, `risk`, `instructions`, `floors`, `when`  | how a review is judged                     |
| `trigger`    | `include`, `exclude`, `limit`                                                                 | which pull requests are reviewed, and when |
| `comments`   | `inline`, `summary`, `finding`, `dashboard`                                                   | what is posted                             |
| `agent`      | `steps`, `output`, `tokens`, `prompt`, `parts`, `timeout`, `commandTimeout`, `fetches`        | the bounds of a review's tool loop         |
| beside them  | `enabled`, `rules`, `context`, `skills`, `ignore`, and `limits` at the root alone             |                                            |

`ignore` lists globs of paths left out of a review's initial diff and
context, the index, and the agent's searches and checkout. These add to
kritika's own: vendored trees, lockfiles, generated and minified code,
source maps and logs. A pull request that changes nothing else is skipped.
The index also leaves out files that SOPS encrypted (`*.sops.*`) and
generated protobuf and Dart code, which a review still reads.
The agent can still read an ignored path with `read_file`, or its diff
with `read_diff`. `ignore` is not an access restriction.

Changing the effective ignore rules triggers a full rebuild on the next
index run. The rebuild chunks every file again, but it sends the embedder
only the chunks whose text the repository's index does not already hold,
by the content hash that each chunk stores. A chunk indexed before kritika
stored that hash is embedded again (see
[the embedder](/models/#the-embedder)).
Editing instance configuration does not queue index jobs by itself. To
apply new ignore rules immediately, reindex an idle repository from the
dashboard. That reindex takes no vector from the index it replaces: it
embeds each distinct chunk text again, once.

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
| `review.cost`             | `true` ends the summary's footer with what the pull request's reviews cost together. Off unless set                                                                                                                                                            |
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

`comments.dashboard` set to `false` leaves the summary without a re-run
badge. It also shows each severity as its label in text rather than an
image that the dashboard serves. `@<app slug> review` still re-runs a
review ([commands](/reviews/#commands)).

### Badge images

GitHub fetches the images in a comment through its own proxy, from the
internet. As a result, the badges render only where `webUrl` is public.
For a dashboard that is not public, set `badgeUrl` at the file's root to a
public address that serves the images:

```yaml
webUrl: https://kritika.internal.example.com
badgeUrl: https://kritika-hooks.example.com
```

The comments then load their images from `<badgeUrl>/badges/`. The View
and Re-run links still go to `webUrl`, and sign-in still uses `webUrl`.

kritika serves the images on the listener that receives the webhooks, at
`/badges/` after `webUrl`'s path. On the public hostname, route the GET
requests whose path starts with `<badgeUrl path>/badges/` to the public
Service, for example as one more rule beside the one for `/hooks/`. If the
two paths differ, rewrite the prefix to `<webUrl path>/badges/`. For
example, with a `webUrl` that ends in `/kritika` and a `badgeUrl` with no
path, forward `/badges/view.svg` to `/kritika/badges/view.svg`. The images
are the same for everyone and need no sign-in.

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
| `confidence.floors`                  | globs add up, level by level. The higher level holds for a path                                                               |
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

  As many parts run at once as the review holds slots of the review
  model's provider [`concurrency`](/models/#concurrency). Those are the
  review's own slot and the slots that no other review holds, which it
  takes as they come free. The rest of the parts run as those end. A
  review that waits for its first slot of the model comes before those
  extra slots: as its parts end, the split review gives the extra slots
  back, down to its own, and it takes them again once no review waits. An
  [agent](/repository-config/#agents) on another model runs its parts in
  that model's slots, counted the same way, so a slower model's agent
  never takes the review model's slots or exceeds its own. The parts' tool
  calls run one at a time. The parts are published as one review.

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
  it. The deadline is sized for running the parts of each model one at a
  time, because a review can give every slot but its own to the reviews
  that wait.

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
repository that runs again before then keeps its index. For this, a
repository whose default branch's `.kritika.yaml` sets `enabled: false` is
off from the index job that finds it so until one finds it on again.

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
    linear:
      apiKey: { env: ORG_1_LINEAR_API_KEY }
```

`limits` caps the account. The root's `limits` sets every account's
limits, and an account's entry replaces them key by key:

| Key              | Default   | What                                        |
| ---------------- | --------- | ------------------------------------------- |
| `reviewsPerDay`  | unlimited | reviews in a calendar day                   |
| `tokensPerMonth` | unlimited | input and output tokens in a calendar month |

How many calls run at once on a model is its provider's
[`concurrency`](/models/#concurrency). The accounts share the slots of a
root provider, and an account's own provider has slots for that account
alone.

`providers` are its own model keys, which its repositories' models can
name ([an account's own keys](/models/#an-accounts-own-keys)).

`linear.apiKey` is a Linear personal API key. The reviews of the account's
repositories read the Linear issues that a pull request links with it
([Linear issues](/repository-config/#linear-issues)). A repository reads
no Linear issue until its `.kritika.yaml` lists the teams that it reads.
Create the key with the Read permission alone, and limit it to the teams
that the account's repositories can read. The key is the limit that a
repository cannot change. Several accounts can use the same key.

A person who can change `.kritika.yaml` on a branch of a repository can
list any team that the key reaches. If a team's issues must stay out of the
account's public repositories, leave that team out of the key. The
account's private repositories then cannot read that team either.

An account runs while a connection serves it. An entry for an account no
connection serves is kept, but not run, and the Accounts page lists
it as not served.

## `egress`

`egress` is what runner pods can reach through kritika's gateway, a
forward proxy, beyond the connections' forges. Those forges are
`github.com` and `api.github.com` for an app, and the instance's host
for a Forgejo, GitLab or Gitea connection. `allow` and `deny` list
hosts, IP addresses and CIDRs, and `credentials` names hosts that the
gateway signs requests in to, for the runs of the accounts that each
one lists. Once `gatewayCacheDir` is set, the gateway
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
    api.github.com: { token: { env: GITHUB_TOKEN }, accounts: ["*"] }
    git.example.com:
      {
        scheme: oci,
        username: kritika-bot,
        token: { env: GITEA_PACKAGES_TOKEN },
        accounts: [org-1],
      }
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
| `reviewWorkers`                | `KRITIKA_REVIEW_WORKERS`                    | jobs that one replica runs at once in each of the separate review and follow-up queues. 2 per queue unless set. A review holds one for as long as it runs, so a provider [`concurrency`](/models/#concurrency) above it needs this raised for more reviews to run at once                                                                         |
| `threadWorkers`                | `KRITIKA_THREAD_WORKERS`                    | finding resolution/restoration jobs that one replica runs at once, independently of model follow-ups. 1 unless set                                                                                                                                                                                                                                |
| `indexWorkers`                 | `KRITIKA_INDEX_WORKERS`                     | index jobs that one replica runs at once in each of the `index` (first indexes, forced rebuilds) and `index_update` queues. 1 per queue unless set                                                                                                                                                                                                |
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
| `badgeUrl`                     | `KRITIKA_BADGE_URL`                         | the public address that a review comment's badge images load from, for a dashboard that is not public ([badge images](#badge-images)). `webUrl` unless set                                                                                                                                                                                        |
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

### Tuning

The caps, budgets, timeouts, intervals, batch sizes and retry counts that kritika works within have defaults that suit most installations. [Tuning](/tuning/) says which few matter for a symptom you see, and what changing them costs. Each one can be changed under the file's `tuning` block, at `tuning.<area>.<name>`, or with its `KRITIKA_TUNING_<AREA>_<NAME>` environment variable. As with every other setting, use one source for each value: a value in both is refused at startup, and the chart refuses an `env` entry for a key that `config.tuning` sets. Under Helm, `tuning` goes under `config`.

```yaml
tuning:
  gateway:
    similarPerQuery: 6
```

or, in the environment:

```sh
KRITIKA_TUNING_GATEWAY_SIMILAR_PER_QUERY=6
```

Durations are written as Go spells them (`90s`, `2m`, `168h`), sizes as a whole number of bytes or with a `Ki`, `Mi`, `Gi` or `Ti` suffix, and lists of durations comma-separated in the environment and as a YAML list in the file.

kritika validates every value at startup, as `kritika config check` does. Each must be positive unless its row says 0 is allowed. Some values are bounded by others, as `webApi.defaultLimit` by `webApi.maxLimit`: a pair that breaks such an invariant is refused with both named. Every error names the value's file path and its variable.

serve hands the values that differ from the defaults, file-set ones included, to every runner Job it creates as `KRITIKA_TUNING_*` variables, so serve and its runners work within the same values. Set them on serve only. `runner.podTemplate`'s `env` is for the run tool's commands, not for the runner itself.

Some values need care:

- The `indexer`, `chunk` and `lexical` values, and the `contextPack` values that shape an index, change what an index build stores. A repository's existing index keeps the chunks it was built with until a reindex from the dashboard rebuilds it: a push updates only what it changes, and a tuning change rebuilds nothing on its own.
- On stopping, serve's `process.queueDrain`, `process.queueStopHeadroom` and `process.eventsDrain` run one after another, beside `gateway.drain`, and together must fit within the pod's `terminationGracePeriodSeconds`, 200 in the chart. Raise that too when raising one of them.
- A value whose description says it is the default when the configuration file does not set a key, such as `agent.defaultMaxSteps` for `agent.steps`, is only the fallback: a key that the file or a repository's settings set wins.
- Some values are mirrored where kritika cannot change them. The dashboard's API-token form offers the default `webApi.tokenLifetimes` (never, 7, 30, 90 and 365 days) and checks the default `webApi.tokenNameMax` of 100 characters, and some dashboard pages ask for a fixed number of items, which `webApi.maxLimit` clamps. The published [repository schema](https://kritika.home-operations.com/kritika.schema.json) states the default `configFile.maxReviewers` (8), `configFile.maxReviewerChars` (8000) and `configFile.maxRuleChars` (2000). Changing those values does not update the dashboard or the schema.
- `review.maxRenderBytes` cannot exceed 65536, the longest comment GitHub takes.

The reference below is generated from kritika's source.

<!-- tuning:begin: generated from internal/tuning by TestDocs; rewrite with go test ./internal/tuning/ -run Docs -update -->

#### `tuning.gateway`

Gateway holds the tunable values of package gateway.

| Setting                               | Environment variable                            | Default | Description                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         |
| ------------------------------------- | ----------------------------------------------- | ------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `tuning.gateway.similarPerQuery`      | `KRITIKA_TUNING_GATEWAY_SIMILAR_PER_QUERY`      | `4`     | SimilarPerQuery is how many neighbours one query of a similar-code request keeps, unless a search of one query asks for more.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       |
| `tuning.gateway.similarPerFile`       | `KRITIKA_TUNING_GATEWAY_SIMILAR_PER_FILE`       | `2`     | SimilarPerFile is how many chunks of one file an answer keeps, so that one file's neighbouring declarations do not take every place.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| `tuning.gateway.similarMax`           | `KRITIKA_TUNING_GATEWAY_SIMILAR_MAX`            | `10`    | SimilarMax is how many chunks an answer keeps when its request asks for no other number.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            |
| `tuning.gateway.drain`                | `KRITIKA_TUNING_GATEWAY_DRAIN`                  | `2m`    | Drain is how long a stopping gateway lets model steps in flight finish. A step that it cuts is paid for and not recorded, and the runner's retry is paid for again. So Drain covers a long step, not the usual few seconds. The chart's grace period must outlast it.                                                                                                                                                                                                                                                                                                                                                               |
| `tuning.gateway.maxConversationBytes` | `KRITIKA_TUNING_GATEWAY_MAX_CONVERSATION_BYTES` | `16Mi`  | MaxConversationBytes bounds one model step's request, the whole conversation so far with every tool output in it capped, and so the runner's read of the conversation that it carries on.                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| `tuning.gateway.detachTimeout`        | `KRITIKA_TUNING_GATEWAY_DETACH_TIMEOUT`         | `2m`    | DetachTimeout bounds the writes that settle a step once the provider answers. These writes must land even if the request's context ends.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            |
| `tuning.gateway.refusedWriteTimeout`  | `KRITIKA_TUNING_GATEWAY_REFUSED_WRITE_TIMEOUT`  | `5s`    | RefusedWriteTimeout bounds the write that records a refused proxy destination, so that a looping agent's refusals are answered at the pace of the database and no slower.                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| `tuning.gateway.maxReportBody`        | `KRITIKA_TUNING_GATEWAY_MAX_REPORT_BODY`        | `64Mi`  | MaxReportBody bounds a run's context, agent and similar-exclusion reports. A record holds its context pack's two diffs, each of up to runner.maxPackDiffBytes, and its conversation, of up to maxConversationBytes, which JSON and base64 grow.                                                                                                                                                                                                                                                                                                                                                                                     |
| `tuning.gateway.maxCheckpointBody`    | `KRITIKA_TUNING_GATEWAY_MAX_CHECKPOINT_BODY`    | `4Mi`   | MaxCheckpointBody bounds a part checkpoint, which holds one part's submitted answer, far below it. One that it refuses only costs a retry that part's run again.                                                                                                                                                                                                                                                                                                                                                                                                                                                                    |
| `tuning.gateway.maxSmallReport`       | `KRITIKA_TUNING_GATEWAY_MAX_SMALL_REPORT`       | `64Ki`  | MaxSmallReport bounds a run's phase, failure and remaining-parts reports.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| `tuning.gateway.maxIndexBody`         | `KRITIKA_TUNING_GATEWAY_MAX_INDEX_BODY`         | `2Gi`   | MaxIndexBody bounds an index upload, which holds up to indexer.maxChunks chunks of indexer.maxChunkBytes, which JSON grows.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         |
| `tuning.gateway.maxIndexLine`         | `KRITIKA_TUNING_GATEWAY_MAX_INDEX_LINE`         | `32Mi`  | MaxIndexLine bounds one line of an index upload. Its opening line holds every changed path.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         |
| `tuning.gateway.indexBatchChunks`     | `KRITIKA_TUNING_GATEWAY_INDEX_BATCH_CHUNKS`     | `500`   | IndexBatchChunks and IndexBatchBytes are when an index upload's batch is written: as soon as it holds either, so an upload costs a batch and a line of memory however long it is.                                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| `tuning.gateway.indexBatchBytes`      | `KRITIKA_TUNING_GATEWAY_INDEX_BATCH_BYTES`      | `8Mi`   | IndexBatchChunks and IndexBatchBytes are when an index upload's batch is written: as soon as it holds either, so an upload costs a batch and a line of memory however long it is.                                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| `tuning.gateway.aquaRegistryTimeout`  | `KRITIKA_TUNING_GATEWAY_AQUA_REGISTRY_TIMEOUT`  | `2m`    | AquaRegistryTimeout bounds one fetch of the aqua registry that the gateway keeps for runners.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       |
| `tuning.gateway.aquaRegistryRetry`    | `KRITIKA_TUNING_GATEWAY_AQUA_REGISTRY_RETRY`    | `1m`    | AquaRegistryRetry is how soon a replica that has no registry yet asks again. So a replica that started while its source was down serves tools soon after the source is back.                                                                                                                                                                                                                                                                                                                                                                                                                                                        |
| `tuning.gateway.maxSimilarRequest`    | `KRITIKA_TUNING_GATEWAY_MAX_SIMILAR_REQUEST`    | `256Ki` | MaxSimilarRequest bounds a similar-code request: its queries, each cut to contextPack.similarQueryChars, with room for their encoding. The paths a search leaves out are the run's report, not the request's, so the bound holds however many files a change touches.                                                                                                                                                                                                                                                                                                                                                               |
| `tuning.gateway.similarWithin`        | `KRITIKA_TUNING_GATEWAY_SIMILAR_WITHIN`         | `50s`   | SimilarWithin is how long after a similar-code request arrives the vector search's looks past full files, its search by name and its reranking end at the latest, leaving the hits found until then, in the fused order, to the queries that they have not reached. A run waits runner.similarTimeout for its answer from before it sends the request, whatever the embedding and the searches took of it, so this is less. The rest is for the answer, its accounting, and the request's way to the gateway.                                                                                                                       |
| `tuning.gateway.rrfK`                 | `KRITIKA_TUNING_GATEWAY_RRF_K`                  | `60`    | RRFK is reciprocal rank fusion's constant: a candidate's score in its query is the sum, over the searches that found it, of 1 / (rrfK + its rank in that search's answer), counted from 1. The default is the value of the paper that introduced the fusion (Cormack, Clarke and Buettcher, 2009), which damps the weight of the very top ranks.                                                                                                                                                                                                                                                                                    |
| `tuning.gateway.lexicalShare`         | `KRITIKA_TUNING_GATEWAY_LEXICAL_SHARE`          | `0.5`   | LexicalShare is the least share of its query's best lexical weight that a hit needs to count as similar code by its names alone.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    |
| `tuning.gateway.similarNames`         | `KRITIKA_TUNING_GATEWAY_SIMILAR_NAMES`          | `3`     | SimilarNames is how many of the names a chunk shares with its query its label shows.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| `tuning.gateway.lexicalTimeout`       | `KRITIKA_TUNING_GATEWAY_LEXICAL_TIMEOUT`        | `5s`    | LexicalTimeout bounds the search by name, which a request can do without: its first statement and its looks past full files together, each statement's timeout what is left of it.                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| `tuning.gateway.lexicalTerms`         | `KRITIKA_TUNING_GATEWAY_LEXICAL_TERMS`          | `32`    | LexicalTerms is how many words of a query are searched for, the longest first.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| `tuning.gateway.lexicalCommonPer`     | `KRITIKA_TUNING_GATEWAY_LEXICAL_COMMON_PER`     | `100`   | LexicalCommonPer, LexicalCommonLeast and LexicalCommonShare bar the words too common to tell a generation's chunks apart: a word in a LexicalCommonPer-th of them, rounded down, or more is left out of the search, the cutoff raised to LexicalCommonLeast when it is less and lowered to store.LexicalCommonMax when it is more, and so is a word in more than 1/LexicalCommonShare of them. The share bars, in a small generation, a word that nearly all of its chunks hold: its weight is too small to say anything of a chunk, yet it would be its query's best by name, and every chunk holding it would clear lexicalShare. |
| `tuning.gateway.lexicalCommonLeast`   | `KRITIKA_TUNING_GATEWAY_LEXICAL_COMMON_LEAST`   | `20`    | LexicalCommonPer, LexicalCommonLeast and LexicalCommonShare bar the words too common to tell a generation's chunks apart: a word in a LexicalCommonPer-th of them, rounded down, or more is left out of the search, the cutoff raised to LexicalCommonLeast when it is less and lowered to store.LexicalCommonMax when it is more, and so is a word in more than 1/LexicalCommonShare of them. The share bars, in a small generation, a word that nearly all of its chunks hold: its weight is too small to say anything of a chunk, yet it would be its query's best by name, and every chunk holding it would clear lexicalShare. |
| `tuning.gateway.lexicalCommonShare`   | `KRITIKA_TUNING_GATEWAY_LEXICAL_COMMON_SHARE`   | `2`     | LexicalCommonPer, LexicalCommonLeast and LexicalCommonShare bar the words too common to tell a generation's chunks apart: a word in a LexicalCommonPer-th of them, rounded down, or more is left out of the search, the cutoff raised to LexicalCommonLeast when it is less and lowered to store.LexicalCommonMax when it is more, and so is a word in more than 1/LexicalCommonShare of them. The share bars, in a small generation, a word that nearly all of its chunks hold: its weight is too small to say anything of a chunk, yet it would be its query's best by name, and every chunk holding it would clear lexicalShare. |

#### `tuning.egress`

Egress holds the tunable values of package egress.

| Setting                         | Environment variable                      | Default | Description                                                                                                                                                                                                                 |
| ------------------------------- | ----------------------------------------- | ------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `tuning.egress.connectTimeout`  | `KRITIKA_TUNING_EGRESS_CONNECT_TIMEOUT`   | `15s`   | ConnectTimeout bounds opening a connection to a destination.                                                                                                                                                                |
| `tuning.egress.tunnelIdle`      | `KRITIKA_TUNING_EGRESS_TUNNEL_IDLE`       | `5m`    | TunnelIdle ends a CONNECT tunnel that neither side used for this long.                                                                                                                                                      |
| `tuning.egress.tunnelBuffer`    | `KRITIKA_TUNING_EGRESS_TUNNEL_BUFFER`     | `32Ki`  | TunnelBuffer is the buffer that each direction of a tunnel copies through.                                                                                                                                                  |
| `tuning.egress.upgradeTimeout`  | `KRITIKA_TUNING_EGRESS_UPGRADE_TIMEOUT`   | `2m`    | UpgradeTimeout bounds one upgraded request, response body included, and one cache fill. A partial cache file twice this old is a fill that died, and is swept.                                                              |
| `tuning.egress.maxEntryHeader`  | `KRITIKA_TUNING_EGRESS_MAX_ENTRY_HEADER`  | `16Ki`  | MaxEntryHeader bounds a cache entry's first line.                                                                                                                                                                           |
| `tuning.egress.maxRedirects`    | `KRITIKA_TUNING_EGRESS_MAX_REDIRECTS`     | `10`    | MaxRedirects bounds the redirects one cache fill follows.                                                                                                                                                                   |
| `tuning.egress.maxFills`        | `KRITIKA_TUNING_EGRESS_MAX_FILLS`         | `8`     | MaxFills bounds the cache fills under way at once, and each fill holds an upstream connection. A request past them is forwarded. A shared S3 cache sizes its local spool to this many entries.                              |
| `tuning.egress.s3LookupTimeout` | `KRITIKA_TUNING_EGRESS_S3_LOOKUP_TIMEOUT` | `2s`    | S3LookupTimeout bounds each of the S3 cache's lookups: dialling, the TLS handshake, a response's headers, a credential's discovery, and an entry's stat.                                                                    |
| `tuning.egress.s3KeepAlive`     | `KRITIKA_TUNING_EGRESS_S3_KEEP_ALIVE`     | `30s`   | S3KeepAlive is the keep-alive period of the S3 cache's connections.                                                                                                                                                         |
| `tuning.egress.s3ReadTimeout`   | `KRITIKA_TUNING_EGRESS_S3_READ_TIMEOUT`   | `10s`   | S3ReadTimeout bounds reading an entry from the S3 cache.                                                                                                                                                                    |
| `tuning.egress.s3UploadTimeout` | `KRITIKA_TUNING_EGRESS_S3_UPLOAD_TIMEOUT` | `30s`   | S3UploadTimeout is the fixed part of an upload's deadline, which S3UploadRate extends by the entry's size.                                                                                                                  |
| `tuning.egress.s3UploadRate`    | `KRITIKA_TUNING_EGRESS_S3_UPLOAD_RATE`    | `1Mi`   | S3UploadRate is the slowest upload allowed past the fixed budget, in bytes a second: at the default, a 256Mi entry has about five minutes and the 5Gi cap about ninety, instead of a deadline that no large entry can meet. |
| `tuning.egress.s3Failures`      | `KRITIKA_TUNING_EGRESS_S3_FAILURES`       | `3`     | S3Failures is how many failures in a row open a circuit of the S3 cache, which then serves from the origin alone for S3Cooldown before it probes again.                                                                     |
| `tuning.egress.s3Cooldown`      | `KRITIKA_TUNING_EGRESS_S3_COOLDOWN`       | `30s`   | S3Failures is how many failures in a row open a circuit of the S3 cache, which then serves from the origin alone for S3Cooldown before it probes again.                                                                     |
| `tuning.egress.s3UploadWorkers` | `KRITIKA_TUNING_EGRESS_S3_UPLOAD_WORKERS` | `2`     | S3UploadWorkers is how many uploads to the S3 cache run at once, and S3UploadQueue how many more wait. An upload past them is skipped.                                                                                      |
| `tuning.egress.s3UploadQueue`   | `KRITIKA_TUNING_EGRESS_S3_UPLOAD_QUEUE`   | `16`    | S3UploadWorkers is how many uploads to the S3 cache run at once, and S3UploadQueue how many more wait. An upload past them is skipped.                                                                                      |

#### `tuning.aqua`

Aqua holds the tunable values of package aqua.

| Setting                            | Environment variable                          | Default  | Description                                                                                                                                                                   |
| ---------------------------------- | --------------------------------------------- | -------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `tuning.aqua.maxRegistryBytes`     | `KRITIKA_TUNING_AQUA_MAX_REGISTRY_BYTES`      | `64Mi`   | MaxRegistryBytes bounds a fetch of the aqua registry, by the gateway from its source and by a runner from the gateway: many times its size.                                   |
| `tuning.aqua.maxDownloadBytes`     | `KRITIKA_TUNING_AQUA_MAX_DOWNLOAD_BYTES`      | `1Gi`    | MaxDownloadBytes bounds a package's download, and a minisign signature's message: the largest that the registry's tools ship is a few hundred MiB.                            |
| `tuning.aqua.maxChecksumFileBytes` | `KRITIKA_TUNING_AQUA_MAX_CHECKSUM_FILE_BYTES` | `4Mi`    | MaxChecksumFileBytes bounds a checksum file, which lists a release's assets.                                                                                                  |
| `tuning.aqua.maxReleaseBytes`      | `KRITIKA_TUNING_AQUA_MAX_RELEASE_BYTES`       | `16Mi`   | MaxReleaseBytes bounds the GitHub API's description of a release.                                                                                                             |
| `tuning.aqua.maxUnpackedBytes`     | `KRITIKA_TUNING_AQUA_MAX_UNPACKED_BYTES`      | `4Gi`    | MaxUnpackedBytes bounds what a download decompresses to, the entries it leaves out and its headers included, against an archive that expands without end.                     |
| `tuning.aqua.maxEntries`           | `KRITIKA_TUNING_AQUA_MAX_ENTRIES`             | `200000` | MaxEntries bounds how many entries an unpacked archive has.                                                                                                                   |
| `tuning.aqua.maxSignatureBytes`    | `KRITIKA_TUNING_AQUA_MAX_SIGNATURE_BYTES`     | `16Mi`   | MaxSignatureBytes bounds what a signature is read from: a bundle, a minisign signature, or the GitHub API's attestations of a download, and what a compressed one decodes to. |

#### `tuning.contextPack`

ContextPack holds the tunable values of package contextpack: the similar-code bounds that the gateway enforces on one request and a run keeps to, and the defaults of a context pack's Options.

| Setting                                 | Environment variable                               | Default  | Description                                                                                                                                                                                   |
| --------------------------------------- | -------------------------------------------------- | -------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `tuning.contextPack.similarQueries`     | `KRITIKA_TUNING_CONTEXT_PACK_SIMILAR_QUERIES`      | `12`     | SimilarQueries is how many texts one similar-code request embeds.                                                                                                                             |
| `tuning.contextPack.similarQueryChars`  | `KRITIKA_TUNING_CONTEXT_PACK_SIMILAR_QUERY_CHARS`  | `3000`   | SimilarQueryChars is how many bytes of each text are embedded.                                                                                                                                |
| `tuning.contextPack.similarLimit`       | `KRITIKA_TUNING_CONTEXT_PACK_SIMILAR_LIMIT`        | `16`     | SimilarLimit is the most chunks a request can ask the answer to hold. A run asks for more than its prompt shows, as it drops those that the head no longer has or that its other stages show. |
| `tuning.contextPack.maxDeclLines`       | `KRITIKA_TUNING_CONTEXT_PACK_MAX_DECL_LINES`       | `150`    | MaxDeclLines is the largest declaration used whole. A bigger one gets a window around the change instead.                                                                                     |
| `tuning.contextPack.windowRadius`       | `KRITIKA_TUNING_CONTEXT_PACK_WINDOW_RADIUS`        | `20`     | WindowRadius is the fixed-window fallback's reach on each side.                                                                                                                               |
| `tuning.contextPack.maxIdentifiers`     | `KRITIKA_TUNING_CONTEXT_PACK_MAX_IDENTIFIERS`      | `40`     | MaxIdentifiers caps the definition lookups of a review, most frequent first.                                                                                                                  |
| `tuning.contextPack.definitionsPerName` | `KRITIKA_TUNING_CONTEXT_PACK_DEFINITIONS_PER_NAME` | `2`      | DefinitionsPerName, the definitions of one identifier, and CallersPerSymbol cap each lookup.                                                                                                  |
| `tuning.contextPack.callersPerSymbol`   | `KRITIKA_TUNING_CONTEXT_PACK_CALLERS_PER_SYMBOL`   | `5`      | DefinitionsPerName, the definitions of one identifier, and CallersPerSymbol cap each lookup.                                                                                                  |
| `tuning.contextPack.maxDefinitions`     | `KRITIKA_TUNING_CONTEXT_PACK_MAX_DEFINITIONS`      | `30`     | MaxDefinitions and MaxCallers cap the definition and caller stages.                                                                                                                           |
| `tuning.contextPack.maxCallers`         | `KRITIKA_TUNING_CONTEXT_PACK_MAX_CALLERS`          | `30`     | MaxDefinitions and MaxCallers cap the definition and caller stages.                                                                                                                           |
| `tuning.contextPack.maxChunkBytes`      | `KRITIKA_TUNING_CONTEXT_PACK_MAX_CHUNK_BYTES`      | `6000`   | MaxChunkBytes truncates one chunk.                                                                                                                                                            |
| `tuning.contextPack.maxPackBytes`       | `KRITIKA_TUNING_CONTEXT_PACK_MAX_PACK_BYTES`       | `300000` | MaxPackBytes stops adding chunks: the default fits a budget of about 24k tokens with room for the diff.                                                                                       |
| `tuning.contextPack.maxScanFiles`       | `KRITIKA_TUNING_CONTEXT_PACK_MAX_SCAN_FILES`       | `20000`  | MaxScanFiles and MaxScanBytes bound the head tree's walk.                                                                                                                                     |
| `tuning.contextPack.maxScanBytes`       | `KRITIKA_TUNING_CONTEXT_PACK_MAX_SCAN_BYTES`       | `200Mi`  | MaxScanFiles and MaxScanBytes bound the head tree's walk.                                                                                                                                     |
| `tuning.contextPack.maxFileBytes`       | `KRITIKA_TUNING_CONTEXT_PACK_MAX_FILE_BYTES`       | `1Mi`    | MaxFileBytes skips files larger than this everywhere.                                                                                                                                         |

#### `tuning.indexer`

Indexer holds the tunable values of package indexer: the defaults of an index build's Options, and the bounds of a diff's similar-code queries. The chunks that a repository's index holds depend on them, so a change takes full effect as each repository is indexed again.

| Setting                            | Environment variable                         | Default  | Description                                                                                                                                                                                                                                                  |
| ---------------------------------- | -------------------------------------------- | -------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `tuning.indexer.maxDeclLines`      | `KRITIKA_TUNING_INDEXER_MAX_DECL_LINES`      | `150`    | MaxDeclLines is the largest declaration stored whole. Larger ones are represented by their nested declarations, or windowed.                                                                                                                                 |
| `tuning.indexer.windowLines`       | `KRITIKA_TUNING_INDEXER_WINDOW_LINES`        | `60`     | WindowLines and WindowOverlap cut files that have no declarations into overlapping windows of lines.                                                                                                                                                         |
| `tuning.indexer.windowOverlap`     | `KRITIKA_TUNING_INDEXER_WINDOW_OVERLAP`      | `10`     | WindowLines and WindowOverlap cut files that have no declarations into overlapping windows of lines. 0 is allowed.                                                                                                                                           |
| `tuning.indexer.maxChunkBytes`     | `KRITIKA_TUNING_INDEXER_MAX_CHUNK_BYTES`     | `6000`   | MaxChunkBytes truncates one chunk's text.                                                                                                                                                                                                                    |
| `tuning.indexer.maxFileBytes`      | `KRITIKA_TUNING_INDEXER_MAX_FILE_BYTES`      | `1Mi`    | MaxFileBytes skips larger files, in a build and in a diff's queries.                                                                                                                                                                                         |
| `tuning.indexer.maxFiles`          | `KRITIKA_TUNING_INDEXER_MAX_FILES`           | `50000`  | MaxFiles, MaxBytes and MaxChunks stop the walk.                                                                                                                                                                                                              |
| `tuning.indexer.maxBytes`          | `KRITIKA_TUNING_INDEXER_MAX_BYTES`           | `500Mi`  | MaxFiles, MaxBytes and MaxChunks stop the walk.                                                                                                                                                                                                              |
| `tuning.indexer.maxChunks`         | `KRITIKA_TUNING_INDEXER_MAX_CHUNKS`          | `100000` | MaxFiles, MaxBytes and MaxChunks stop the walk.                                                                                                                                                                                                              |
| `tuning.indexer.maxSignatureBytes` | `KRITIKA_TUNING_INDEXER_MAX_SIGNATURE_BYTES` | `200`    | MaxSignatureBytes bounds a chunk's signature, the line that names a windowed declaration, which a minified or generated declaration can make one long line.                                                                                                  |
| `tuning.indexer.minGapLines`       | `KRITIKA_TUNING_INDEXER_MIN_GAP_LINES`       | `3`      | MinGapLines is the fewest worded lines that a gap between an oversized declaration's nested ones needs to be stored by itself. A smaller gap, such as a field or a class's opening line, joins the nested declaration next to it when the two fit one piece. |
| `tuning.indexer.queryContext`      | `KRITIKA_TUNING_INDEXER_QUERY_CONTEXT`       | `3`      | QueryContext is how many unchanged lines a similar-code query keeps on each side of a hunk's changed lines. 0 is allowed.                                                                                                                                    |

#### `tuning.chunk`

Chunk holds the tunable values of package chunk. A parse has no timeout: every limit on it counts the parser's work, not time, so that a file is cut the same on a loaded machine as on an idle one.

| Setting                          | Environment variable                       | Default | Description                                                                                                                                                                                                                                                                             |
| -------------------------------- | ------------------------------------------ | ------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `tuning.chunk.maxFileBytes`      | `KRITIKA_TUNING_CHUNK_MAX_FILE_BYTES`      | `1Mi`   | MaxFileBytes is the largest file parsed when the caller sets no other limit.                                                                                                                                                                                                            |
| `tuning.chunk.parseMemoryBudget` | `KRITIKA_TUNING_CHUNK_PARSE_MEMORY_BUDGET` | `256Mi` | ParseMemoryBudget bounds one parse's arena and scratch memory. The library's default, the larger of 512 MiB and 512 bytes per input byte, is past a runner pod's memory limit for a file of the size that maxFileBytes lets through. A parse that it stops falls back to fixed windows. |
| `tuning.chunk.parseStackDepth`   | `KRITIKA_TUNING_CHUNK_PARSE_STACK_DEPTH`   | `4096`  | ParseStackDepth stops a parse whose stack runs deep, as a long run of open brackets drives it: the library recovers at the end of such a file for minutes otherwise.                                                                                                                    |

#### `tuning.lexical`

Lexical holds the tunable values of package lexical: bounds on what a document of the search by name stores. A longer run of letters is a key, a hash or encoded data, which no query names.

| Setting                             | Environment variable                          | Default | Description                                                |
| ----------------------------------- | --------------------------------------------- | ------- | ---------------------------------------------------------- |
| `tuning.lexical.maxDocumentLexemes` | `KRITIKA_TUNING_LEXICAL_MAX_DOCUMENT_LEXEMES` | `2048`  | MaxDocumentLexemes is how many lexemes one document keeps. |
| `tuning.lexical.maxLexemeBytes`     | `KRITIKA_TUNING_LEXICAL_MAX_LEXEME_BYTES`     | `64`    | MaxLexemeBytes is the longest lexeme kept.                 |

#### `tuning.runSpec`

RunSpec holds the tunable values of package runspec. A spec travels as a key of the run's Secret, which Kubernetes caps at 1 MiB with the credentials beside it, so what grows with a pull request is cut before it is encoded.

| Setting                             | Environment variable                           | Default | Description                                                                                                                                                                                                                                                                                                                                                                                                                           |
| ----------------------------------- | ---------------------------------------------- | ------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `tuning.runSpec.heartbeatInterval`  | `KRITIKA_TUNING_RUN_SPEC_HEARTBEAT_INTERVAL`   | `15s`   | HeartbeatInterval is how often a runner stamps runner_runs.heartbeat_at. The worker's staleness threshold is several of these.                                                                                                                                                                                                                                                                                                        |
| `tuning.runSpec.maxBytes`           | `KRITIKA_TUNING_RUN_SPEC_MAX_BYTES`            | `900Ki` | MaxBytes bounds an encoded spec, and it leaves the Secret room for the git and gateway tokens.                                                                                                                                                                                                                                                                                                                                        |
| `tuning.runSpec.maxPromptBytes`     | `KRITIKA_TUNING_RUN_SPEC_MAX_PROMPT_BYTES`     | `768Ki` | MaxPromptBytes is what an encoded prompt is trimmed to, under MaxBytes with room for the rest of the spec.                                                                                                                                                                                                                                                                                                                            |
| `tuning.runSpec.maxPriorFindings`   | `KRITIKA_TUNING_RUN_SPEC_MAX_PRIOR_FINDINGS`   | `200`   | MaxPriorFindings is how many of the last review's findings a spec carries, and how many dismissed ones.                                                                                                                                                                                                                                                                                                                               |
| `tuning.runSpec.maxBodyBytes`       | `KRITIKA_TUNING_RUN_SPEC_MAX_BODY_BYTES`       | `64Ki`  | MaxBodyBytes bounds the pull request body a spec carries. Encoding can grow it sixfold (each '&lt;' becomes &lt;), which MaxBytes still holds.                                                                                                                                                                                                                                                                                        |
| `tuning.runSpec.maxIssueBytes`      | `KRITIKA_TUNING_RUN_SPEC_MAX_ISSUE_BYTES`      | `16Ki`  | MaxIssueBytes bounds each linked issue's body a spec carries: the prompt shows a few thousand characters of one, and three worst-case issues must leave the body and the findings their room.                                                                                                                                                                                                                                         |
| `tuning.runSpec.reportRetryWindow`  | `KRITIKA_TUNING_RUN_SPEC_REPORT_RETRY_WINDOW`  | `3m`    | ReportRetryWindow is how long a runner starts new attempts at a report while the gateway is out of reach, and ReportTimeout how long one of them can then hang, since a replica that dies mid-request answers nothing. A replica restarts under the run in every rollout, and a lost report can be a model's answer already paid for. A heartbeat takes the same route, so the worker's patience with a silent run must outlast both. |
| `tuning.runSpec.reportTimeout`      | `KRITIKA_TUNING_RUN_SPEC_REPORT_TIMEOUT`       | `2m`    | ReportRetryWindow is how long a runner starts new attempts at a report while the gateway is out of reach, and ReportTimeout how long one of them can then hang, since a replica that dies mid-request answers nothing. A replica restarts under the run in every rollout, and a lost report can be a model's answer already paid for. A heartbeat takes the same route, so the worker's patience with a silent run must outlast both. |
| `tuning.runSpec.indexUploadTimeout` | `KRITIKA_TUNING_RUN_SPEC_INDEX_UPLOAD_TIMEOUT` | `30m`   | IndexUploadTimeout is how long one upload of a run's index can take. The indexer's limits allow hundreds of megabytes, so the gateway's usual bound on reading a request does not hold for it.                                                                                                                                                                                                                                        |

#### `tuning.executor`

Executor holds the tunable values of package executor.

| Setting                              | Environment variable                           | Default | Description                                                                                                                                                                                                                                                                                                                                     |
| ------------------------------------ | ---------------------------------------------- | ------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `tuning.executor.logTailBytes`       | `KRITIKA_TUNING_EXECUTOR_LOG_TAIL_BYTES`       | `64Ki`  | LogTailBytes is how much of a runner's output is kept: enough to diagnose, small enough to store per run.                                                                                                                                                                                                                                       |
| `tuning.executor.logTailLines`       | `KRITIKA_TUNING_EXECUTOR_LOG_TAIL_LINES`       | `5000`  | LogTailLines and LogReadBytes bound the read of a runner pod's log for its tail: enough lines to fill logTailBytes many times over, and a byte ceiling well above that.                                                                                                                                                                         |
| `tuning.executor.logReadBytes`       | `KRITIKA_TUNING_EXECUTOR_LOG_READ_BYTES`       | `4Mi`   | LogTailLines and LogReadBytes bound the read of a runner pod's log for its tail: enough lines to fill logTailBytes many times over, and a byte ceiling well above that.                                                                                                                                                                         |
| `tuning.executor.poll`               | `KRITIKA_TUNING_EXECUTOR_POLL`                 | `3s`    | Poll is how often a run's wait reads the watch again, for the grace that a stuck pod or a podless Job is given, and a new Job's way to the watch.                                                                                                                                                                                               |
| `tuning.executor.maxMissing`         | `KRITIKA_TUNING_EXECUTOR_MAX_MISSING`          | `10`    | MaxMissing is how many polls in a row a Job can be missing from the watch before it is given up on.                                                                                                                                                                                                                                             |
| `tuning.executor.startGrace`         | `KRITIKA_TUNING_EXECUTOR_START_GRACE`          | `3m`    | StartGrace is how long a runner pod can stay unable to start before the run is given up: uncreated, unscheduled, or with its container refused. The run is given up so that it does not hold its review's slot until the Job's deadline. StartGrace outlasts what clears on its own: a registry's hiccup, a node on its way from an autoscaler. |
| `tuning.executor.jobTtl`             | `KRITIKA_TUNING_EXECUTOR_JOB_TTL`              | `10m`   | JobTTL is how long a finished Job stays for kubectl when the jobTemplate sets no ttlSecondsAfterFinished. The run row keeps everything the Job knew.                                                                                                                                                                                            |
| `tuning.executor.apiTimeout`         | `KRITIKA_TUNING_EXECUTOR_API_TIMEOUT`          | `20s`   | APITimeout bounds one call to the Kubernetes API about a run's Job that must not wait on the run: listing its pods, deleting it, and the dry run of the Job at startup.                                                                                                                                                                         |
| `tuning.executor.finishTimeout`      | `KRITIKA_TUNING_EXECUTOR_FINISH_TIMEOUT`       | `20s`   | FinishTimeout bounds collecting a finished run's pod: its listing and the read of its log's tail.                                                                                                                                                                                                                                               |
| `tuning.executor.whyNoPodTimeout`    | `KRITIKA_TUNING_EXECUTOR_WHY_NO_POD_TIMEOUT`   | `10s`   | WhyNoPodTimeout bounds reading why the Job controller created no pod for a Job.                                                                                                                                                                                                                                                                 |
| `tuning.executor.watchSync`          | `KRITIKA_TUNING_EXECUTOR_WATCH_SYNC`           | `1m`    | WatchSync bounds how long the executor's start waits for its trial watches and its first lists.                                                                                                                                                                                                                                                 |
| `tuning.executor.eventInstanceBytes` | `KRITIKA_TUNING_EXECUTOR_EVENT_INSTANCE_BYTES` | `128`   | EventInstanceBytes is the longest reporting instance an Event names, past which it names the pod's UID.                                                                                                                                                                                                                                         |
| `tuning.executor.eventNoteBytes`     | `KRITIKA_TUNING_EXECUTOR_EVENT_NOTE_BYTES`     | `1Ki`   | EventNoteBytes is the longest note an Event holds.                                                                                                                                                                                                                                                                                              |
| `tuning.executor.eventTimeout`       | `KRITIKA_TUNING_EXECUTOR_EVENT_TIMEOUT`        | `10s`   | EventTimeout bounds an Event's write.                                                                                                                                                                                                                                                                                                           |

#### `tuning.runner`

Runner holds the tunable values of package runner.

| Setting                              | Environment variable                            | Default | Description                                                                                                                                                                                                                                                                                                                                      |
| ------------------------------------ | ----------------------------------------------- | ------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `tuning.runner.gatewayWait`          | `KRITIKA_TUNING_RUNNER_GATEWAY_WAIT`            | `90s`   | GatewayWait is how long a runner waits for its gateway to answer before it fetches. A runner started as the service comes up can beat the gateway's Service endpoints by the readiness probe's period and a little propagation. Much longer than that is an outage, and the run reports it and does not sit it out.                              |
| `tuning.runner.gatewayPoll`          | `KRITIKA_TUNING_RUNNER_GATEWAY_POLL`            | `2s`    | GatewayPoll is how often the wait tries again, and how long each try can take.                                                                                                                                                                                                                                                                   |
| `tuning.runner.fetchWriteBytes`      | `KRITIKA_TUNING_RUNNER_FETCH_WRITE_BYTES`       | `256Mi` | FetchWriteBytes is the most that a run's fetches write of their files, beside its checkout. The run tool's kept outputs share it.                                                                                                                                                                                                                |
| `tuning.runner.fetchWireBytes`       | `KRITIKA_TUNING_RUNNER_FETCH_WIRE_BYTES`        | `128Mi` | FetchWireBytes is the most that one fetch takes from the server, and FetchTimeout the most wall time.                                                                                                                                                                                                                                            |
| `tuning.runner.fetchTimeout`         | `KRITIKA_TUNING_RUNNER_FETCH_TIMEOUT`           | `2m`    | FetchWireBytes is the most that one fetch takes from the server, and FetchTimeout the most wall time.                                                                                                                                                                                                                                            |
| `tuning.runner.fetchDiffBytes`       | `KRITIKA_TUNING_RUNNER_FETCH_DIFF_BYTES`        | `4Mi`   | FetchDiffBytes is the most of a diff that a fetch keeps, the same as the run tool keeps of an output.                                                                                                                                                                                                                                            |
| `tuning.runner.maxTagListings`       | `KRITIKA_TUNING_RUNNER_MAX_TAG_LISTINGS`        | `8`     | MaxTagListings is how many times a run lists tags, by fetch_repo and fetch_oci together, the listing that a fetch naming a missing ref makes to name the closest included.                                                                                                                                                                       |
| `tuning.runner.tagsShown`            | `KRITIKA_TUNING_RUNNER_TAGS_SHOWN`              | `200`   | TagsShown is the most tag names a listing's answer shows, and ClosestTagsShown the most that an error for a missing ref does.                                                                                                                                                                                                                    |
| `tuning.runner.closestTagsShown`     | `KRITIKA_TUNING_RUNNER_CLOSEST_TAGS_SHOWN`      | `10`    | TagsShown is the most tag names a listing's answer shows, and ClosestTagsShown the most that an error for a missing ref does.                                                                                                                                                                                                                    |
| `tuning.runner.changedShown`         | `KRITIKA_TUNING_RUNNER_CHANGED_SHOWN`           | `100`   | ChangedShown is the most paths that a fetched diff's answer names before the patches.                                                                                                                                                                                                                                                            |
| `tuning.runner.maxDirNameBytes`      | `KRITIKA_TUNING_RUNNER_MAX_DIR_NAME_BYTES`      | `100`   | MaxDirNameBytes bounds the name of a fetch's directory, well inside a file system's 255 bytes.                                                                                                                                                                                                                                                   |
| `tuning.runner.ociWireBytes`         | `KRITIKA_TUNING_RUNNER_OCI_WIRE_BYTES`          | `16Mi`  | OCIWireBytes is the most that one fetch_oci call takes from the registry, and OCITimeout the most wall time.                                                                                                                                                                                                                                     |
| `tuning.runner.ociTimeout`           | `KRITIKA_TUNING_RUNNER_OCI_TIMEOUT`             | `1m`    | OCIWireBytes is the most that one fetch_oci call takes from the registry, and OCITimeout the most wall time.                                                                                                                                                                                                                                     |
| `tuning.runner.ociMetadataBytes`     | `KRITIKA_TUNING_RUNNER_OCI_METADATA_BYTES`      | `4Mi`   | OCIMetadataBytes is the largest manifest or config that fetch_oci reads.                                                                                                                                                                                                                                                                         |
| `tuning.runner.ociConfigShown`       | `KRITIKA_TUNING_RUNNER_OCI_CONFIG_SHOWN`        | `8Ki`   | OCIConfigShown is what fetch_oci shows of a config that is no image's.                                                                                                                                                                                                                                                                           |
| `tuning.runner.ociTagPages`          | `KRITIKA_TUNING_RUNNER_OCI_TAG_PAGES`           | `50`    | OCITagPages is how many pages long a tag listing of fetch_oci is.                                                                                                                                                                                                                                                                                |
| `tuning.runner.similarTimeout`       | `KRITIKA_TUNING_RUNNER_SIMILAR_TIMEOUT`         | `1m`    | SimilarTimeout bounds one similar-code request to the gateway, which can wait for the account's embedding slot.                                                                                                                                                                                                                                  |
| `tuning.runner.maxSimilarAnswerBody` | `KRITIKA_TUNING_RUNNER_MAX_SIMILAR_ANSWER_BODY` | `1Mi`   | MaxSimilarAnswerBody bounds the gateway's answer to a similar-code request: contextPack.similarLimit chunks.                                                                                                                                                                                                                                     |
| `tuning.runner.similarReportWindow`  | `KRITIKA_TUNING_RUNNER_SIMILAR_REPORT_WINDOW`   | `20s`   | SimilarReportWindow bounds what a run spends, before it searches, on learning whether its repository has an index and reporting its exclusions. Similar code is best effort, and the review waits for it.                                                                                                                                        |
| `tuning.runner.maxSimilarShown`      | `KRITIKA_TUNING_RUNNER_MAX_SIMILAR_SHOWN`       | `10`    | MaxSimilarShown is the most similar chunks a prompt shows.                                                                                                                                                                                                                                                                                       |
| `tuning.runner.searchCalls`          | `KRITIKA_TUNING_RUNNER_SEARCH_CALLS`            | `10`    | SearchCalls is how many times one run can call search_code.                                                                                                                                                                                                                                                                                      |
| `tuning.runner.searchShown`          | `KRITIKA_TUNING_RUNNER_SEARCH_SHOWN`            | `4`     | SearchShown is the most chunks one search_code answer shows. It asks the gateway for twice as many, as it leaves out those that the head no longer has or that the run ignores, and the gateway caps one query's answer at gateway.similarPerQuery unless asked for more.                                                                        |
| `tuning.runner.maxRuleFileDepth`     | `KRITIKA_TUNING_RUNNER_MAX_RULE_FILE_DEPTH`     | `8`     | MaxRuleFileDepth bounds how deep below a rule file directory files are looked for, against a tree that nests without end.                                                                                                                                                                                                                        |
| `tuning.runner.conversationTimeout`  | `KRITIKA_TUNING_RUNNER_CONVERSATION_TIMEOUT`    | `30s`   | ConversationTimeout bounds asking the gateway for the conversation to carry on.                                                                                                                                                                                                                                                                  |
| `tuning.runner.carryShare`           | `KRITIKA_TUNING_RUNNER_CARRY_SHARE`             | `4`     | CarryShare bounds the conversation a run carries on to 1/CarryShare of its token budget. Every step reads the whole of it again. Without this bound, a budget that the monthly cap cut short leaves the agent too few steps to work in.                                                                                                          |
| `tuning.runner.pageNoteRoom`         | `KRITIKA_TUNING_RUNNER_PAGE_NOTE_ROOM`          | `96`    | PageNoteRoom is kept free on a page of read_diff for the note that numbers it.                                                                                                                                                                                                                                                                   |
| `tuning.runner.maxPackDiffBytes`     | `KRITIKA_TUNING_RUNNER_MAX_PACK_DIFF_BYTES`     | `4Mi`   | MaxPackDiffBytes is how much of a diff, and of a delta diff, a context pack keeps: whole files in order, with a file left out if it would pass the limit. A pull request that regenerates a large text file otherwise puts the whole of it in the service's memory. The prompt's own budget is far smaller, so the agent is sent no less for it. |
| `tuning.runner.failureTimeout`       | `KRITIKA_TUNING_RUNNER_FAILURE_TIMEOUT`         | `10s`   | FailureTimeout bounds recording a run's error.                                                                                                                                                                                                                                                                                                   |
| `tuning.runner.maxFailureCause`      | `KRITIKA_TUNING_RUNNER_MAX_FAILURE_CAUSE`       | `8Ki`   | MaxFailureCause is the most of a run's error that its failure report carries. The run's row keeps less, so a git error of any length still fits the report.                                                                                                                                                                                      |
| `tuning.runner.reportBackoff`        | `KRITIKA_TUNING_RUNNER_REPORT_BACKOFF`          | `1s`    | ReportBackoff is the first wait between attempts at a report, which doubles up to ReportMaxBackoff.                                                                                                                                                                                                                                              |
| `tuning.runner.reportMaxBackoff`     | `KRITIKA_TUNING_RUNNER_REPORT_MAX_BACKOFF`      | `15s`   | ReportBackoff is the first wait between attempts at a report, which doubles up to ReportMaxBackoff.                                                                                                                                                                                                                                              |
| `tuning.runner.heartbeatTimeout`     | `KRITIKA_TUNING_RUNNER_HEARTBEAT_TIMEOUT`       | `10s`   | HeartbeatTimeout bounds one heartbeat, as runSpec.reportTimeout does a report. A heartbeat is tried once: another comes along soon.                                                                                                                                                                                                              |
| `tuning.runner.maxReportAnswer`      | `KRITIKA_TUNING_RUNNER_MAX_REPORT_ANSWER`       | `256Mi` | MaxReportAnswer bounds what the gateway's answer to a report is read to: the part checkpoints of a run, or an error.                                                                                                                                                                                                                             |
| `tuning.runner.minPartTime`          | `KRITIKA_TUNING_RUNNER_MIN_PART_TIME`           | `2m`    | MinPartTime is the least time a part of a split review starts with, or half its timeout when that is less. Less is not enough for an agent to read its files and answer. Its whole timeout is too much. The run's deadline leaves the last round no more than that, less what the run took to reach it.                                          |
| `tuning.runner.partSlotsPoll`        | `KRITIKA_TUNING_RUNNER_PART_SLOTS_POLL`         | `2s`    | PartSlotsPoll is how often a split review asks the worker for the slots its parts may run in, and PartSlotsTimeout how long one ask can take.                                                                                                                                                                                                    |
| `tuning.runner.partSlotsTimeout`     | `KRITIKA_TUNING_RUNNER_PART_SLOTS_TIMEOUT`      | `5s`    | PartSlotsPoll is how often a split review asks the worker for the slots its parts may run in, and PartSlotsTimeout how long one ask can take.                                                                                                                                                                                                    |
| `tuning.runner.installTimeout`       | `KRITIKA_TUNING_RUNNER_INSTALL_TIMEOUT`         | `2m`    | InstallTimeout bounds each step of installing a repository's tool: the registry's fetch, a checksum's lookup, and each attempt at the download, as fetchTimeout bounds a fetch.                                                                                                                                                                  |
| `tuning.runner.noteOmittedPaths`     | `KRITIKA_TUNING_RUNNER_NOTE_OMITTED_PATHS`      | `5`     | NoteOmittedPaths is how many omitted paths a review note names.                                                                                                                                                                                                                                                                                  |
| `tuning.runner.gatewayRetries`       | `KRITIKA_TUNING_RUNNER_GATEWAY_RETRIES`         | `2`     | GatewayRetries is how many times a step the gateway answered with a 500, its own database not answering, is sent again. The gateway marks every other refusal final, since it already retried the provider. 0 is allowed.                                                                                                                        |
| `tuning.runner.cancelWriteTimeout`   | `KRITIKA_TUNING_RUNNER_CANCEL_WRITE_TIMEOUT`    | `5s`    | CancelWriteTimeout bounds writing a cancelled agent's row, well inside a runner pod's termination grace period.                                                                                                                                                                                                                                  |

#### `tuning.worker`

Worker holds the tunable values of package worker.

| Setting                                   | Environment variable                                 | Default | Description                                                                                                                                                                                                                                                                                                                      |
| ----------------------------------------- | ---------------------------------------------------- | ------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `tuning.worker.minAgentTokens`            | `KRITIKA_TUNING_WORKER_MIN_AGENT_TOKENS`             | `50000` | MinAgentTokens is the least monthly headroom a review starts with: below it the agent cannot read the diff before running out, so the review is capped instead.                                                                                                                                                                  |
| `tuning.worker.diffBytesPerLine`          | `KRITIKA_TUNING_WORKER_DIFF_BYTES_PER_LINE`          | `64`    | DiffBytesPerLine estimates a unified diff's size from the forge's counts: a changed line with its share of the context around it.                                                                                                                                                                                                |
| `tuning.worker.diffBytesPerFile`          | `KRITIKA_TUNING_WORKER_DIFF_BYTES_PER_FILE`          | `256`   | DiffBytesPerFile is the estimate's share of a file's header.                                                                                                                                                                                                                                                                     |
| `tuning.worker.agentRowWait`              | `KRITIKA_TUNING_WORKER_AGENT_ROW_WAIT`               | `30s`   | AgentRowWait is how long a failed run's agent row is waited for: a runner pod's termination grace period.                                                                                                                                                                                                                        |
| `tuning.worker.agentRowPoll`              | `KRITIKA_TUNING_WORKER_AGENT_ROW_POLL`               | `1s`    | AgentRowPoll is how often the wait for a failed run's agent row looks again.                                                                                                                                                                                                                                                     |
| `tuning.worker.agentRowReadSlack`         | `KRITIKA_TUNING_WORKER_AGENT_ROW_READ_SLACK`         | `10s`   | AgentRowReadSlack is what the read of a run's agent row gets on top of AgentRowWait, for the reads around the wait.                                                                                                                                                                                                              |
| `tuning.worker.modelStepsReadTimeout`     | `KRITIKA_TUNING_WORKER_MODEL_STEPS_READ_TIMEOUT`     | `10s`   | ModelStepsReadTimeout bounds the read of whether a failed run began any model step, which runs on past the job's cancellation.                                                                                                                                                                                                   |
| `tuning.worker.maxAnswers`                | `KRITIKA_TUNING_WORKER_MAX_ANSWERS`                  | `4`     | MaxAnswers is how many of the latest replies in a finding's thread a re-review is shown.                                                                                                                                                                                                                                         |
| `tuning.worker.answerBytes`               | `KRITIKA_TUNING_WORKER_ANSWER_BYTES`                 | `600`   | AnswerBytes is how much of each of those replies it is shown.                                                                                                                                                                                                                                                                    |
| `tuning.worker.concessionReasonBytes`     | `KRITIKA_TUNING_WORKER_CONCESSION_REASON_BYTES`      | `200`   | ConcessionReasonBytes is how much of an answer, or of kritika's own reply, the reason of a dismissal that kritika concedes quotes. The store cuts the whole reason again, to Store.DismissalReasonChars.                                                                                                                         |
| `tuning.worker.ownPathsNamed`             | `KRITIKA_TUNING_WORKER_OWN_PATHS_NAMED`              | `3`     | OwnPathsNamed is how many of the changed files that configure kritika the reason it does not approve a change names before it counts the rest.                                                                                                                                                                                   |
| `tuning.worker.releaseTimeout`            | `KRITIKA_TUNING_WORKER_RELEASE_TIMEOUT`              | `10s`   | ReleaseTimeout bounds the lease release after the job's context is gone.                                                                                                                                                                                                                                                         |
| `tuning.worker.detachTimeout`             | `KRITIKA_TUNING_WORKER_DETACH_TIMEOUT`               | `2m`    | DetachTimeout bounds work that runs on past the job's context once the model answers: publishing, and ending a review whose job ended. It must still finish then, but a hung forge or database call must not hold the job forever.                                                                                               |
| `tuning.worker.chatGptRefreshInterval`    | `KRITIKA_TUNING_WORKER_CHAT_GPT_REFRESH_INTERVAL`    | `1m`    | ChatGPTRefreshInterval is how often the leader looks for ChatGPT sessions whose tokens are due for renewal.                                                                                                                                                                                                                      |
| `tuning.worker.chatGptRefreshTimeout`     | `KRITIKA_TUNING_WORKER_CHAT_GPT_REFRESH_TIMEOUT`     | `1m`    | ChatGPTRefreshTimeout bounds one session's refresh and the write that saves it, which run on past the leader's cancellation: once OpenAI answers, the refresh token it rotated is spent, and only the saved replacement can renew the session again.                                                                             |
| `tuning.worker.chatGptSaveAttempts`       | `KRITIKA_TUNING_WORKER_CHAT_GPT_SAVE_ATTEMPTS`       | `4`     | ChatGPTSaveAttempts is how many times a refreshed token set is written before it is given up.                                                                                                                                                                                                                                    |
| `tuning.worker.chatGptSaveBackoff`        | `KRITIKA_TUNING_WORKER_CHAT_GPT_SAVE_BACKOFF`        | `1s`    | ChatGPTSaveBackoff is the pause after the first failed write of a refreshed token set, and what each later pause adds to it.                                                                                                                                                                                                     |
| `tuning.worker.chatGptHttpTimeout`        | `KRITIKA_TUNING_WORKER_CHAT_GPT_HTTP_TIMEOUT`        | `30s`   | ChatGPTHTTPTimeout bounds one request to OpenAI's token endpoint.                                                                                                                                                                                                                                                                |
| `tuning.worker.awaitInterval`             | `KRITIKA_TUNING_WORKER_AWAIT_INTERVAL`               | `15s`   | AwaitInterval is how long the job of an answered review request sleeps between looks at whether the review ended.                                                                                                                                                                                                                |
| `tuning.worker.confidenceTimeout`         | `KRITIKA_TUNING_WORKER_CONFIDENCE_TIMEOUT`           | `2m`    | ConfidenceTimeout bounds scoring a review, the wait for a slot on the confidence model included. With the write-back's own bound it stays inside JobTimeout.PublishHeadroom, which Validate holds it to.                                                                                                                         |
| `tuning.worker.confidenceMaxOutputTokens` | `KRITIKA_TUNING_WORKER_CONFIDENCE_MAX_OUTPUT_TOKENS` | `4096`  | ConfidenceMaxOutputTokens bounds the scorer's answer: a score and a sentence or two, with room for a reasoning model's thinking.                                                                                                                                                                                                 |
| `tuning.worker.confidenceBudgetShare`     | `KRITIKA_TUNING_WORKER_CONFIDENCE_BUDGET_SHARE`      | `2`     | ConfidenceBudgetShare is how many times the agent's prompt budget the scorer's prompt can take, up to ConfidenceBudgetCap tokens and never less than the agent's. The scorer makes one call with no tool outputs to leave room for, and the diff is what its risk rating rests on.                                               |
| `tuning.worker.confidenceBudgetCap`       | `KRITIKA_TUNING_WORKER_CONFIDENCE_BUDGET_CAP`        | `96000` | ConfidenceBudgetCap bounds the scorer's prompt budget, so that an agent budget set for a large context does not double past what the scorer's model holds.                                                                                                                                                                       |
| `tuning.worker.continueWindow`            | `KRITIKA_TUNING_WORKER_CONTINUE_WINDOW`              | `25m`   | ContinueWindow is how recently a run must have kept a conversation for a review to carry it on. OpenAI keeps a prompt's prefix cached for about 30 minutes after its last use; the window leaves a runner the minutes it takes from the decision to its first step.                                                              |
| `tuning.worker.anthropicContinueWindow`   | `KRITIKA_TUNING_WORKER_ANTHROPIC_CONTINUE_WINDOW`    | `4m`    | AnthropicContinueWindow is ContinueWindow for an Anthropic model, whose prompt cache lasts about 5 minutes.                                                                                                                                                                                                                      |
| `tuning.worker.threadMessages`            | `KRITIKA_TUNING_WORKER_THREAD_MESSAGES`              | `20`    | ThreadMessages is how many of a thread's messages a follow-up's prompt keeps.                                                                                                                                                                                                                                                    |
| `tuning.worker.repliesPerThread`          | `KRITIKA_TUNING_WORKER_REPLIES_PER_THREAD`           | `3`     | RepliesPerThread is how many replies the bot makes, in one of its finding threads, to replies that did not address it. Past it, it leaves the thread to a mention.                                                                                                                                                               |
| `tuning.worker.forgeBuildTimeout`         | `KRITIKA_TUNING_WORKER_FORGE_BUILD_TIMEOUT`          | `1m`    | ForgeBuildTimeout bounds a forge client's build, which runs on no caller's context.                                                                                                                                                                                                                                              |
| `tuning.worker.embedBatch`                | `KRITIKA_TUNING_WORKER_EMBED_BATCH`                  | `128`   | EmbedBatch is how many staged chunks are embedded and inserted at once.                                                                                                                                                                                                                                                          |
| `tuning.worker.appliedBuildWait`          | `KRITIKA_TUNING_WORKER_APPLIED_BUILD_WAIT`           | `1m`    | AppliedBuildWait is how long an index job waits on a pod whose embedder or index format the leader did not apply, before it is tried again.                                                                                                                                                                                      |
| `tuning.worker.buildWaitWarn`             | `KRITIKA_TUNING_WORKER_BUILD_WAIT_WARN`              | `10`    | BuildWaitWarn is the wait for a pod on the applied build from which an index job logs each wait as a warning. A rollout's waits end within minutes; a leader that never applies the pods' build keeps every index job waiting until a restart.                                                                                   |
| `tuning.worker.tipCheckWait`              | `KRITIKA_TUNING_WORKER_TIP_CHECK_WAIT`               | `1m`    | TipCheckWait is how long an index job waits to ask the forge for the branch tip again after the forge did not give it, unless the forge names a later time.                                                                                                                                                                      |
| `tuning.worker.forgeWaitMin`              | `KRITIKA_TUNING_WORKER_FORGE_WAIT_MIN`               | `30s`   | ForgeWaitMin is an index job's first wait for a forge that failed transiently before its run starts. Each wait doubles it, up to ForgeWaitMax, ForgeWaits times: about an hour in all at the defaults. River's backoff would spend the job's few attempts within seconds and drop the push that queued it; a snooze spends none. |
| `tuning.worker.forgeWaitMax`              | `KRITIKA_TUNING_WORKER_FORGE_WAIT_MAX`               | `15m`   | ForgeWaitMax caps one of those waits.                                                                                                                                                                                                                                                                                            |
| `tuning.worker.forgeWaits`                | `KRITIKA_TUNING_WORKER_FORGE_WAITS`                  | `8`     | ForgeWaits is how many of those waits a job has before the failure spends an attempt.                                                                                                                                                                                                                                            |
| `tuning.worker.mergeTimeout`              | `KRITIKA_TUNING_WORKER_MERGE_TIMEOUT`                | `100s`  | MergeTimeout bounds writing a split review's summary, inside the time publishing reads the agent's run in: Validate holds it within JobTimeout.PublishHeadroom.                                                                                                                                                                  |
| `tuning.worker.mergeMaxOutputTokens`      | `KRITIKA_TUNING_WORKER_MERGE_MAX_OUTPUT_TOKENS`      | `16384` | MergeMaxOutputTokens bounds the merge call's answer: a summary, with room for the review model's reasoning at its effort.                                                                                                                                                                                                        |
| `tuning.worker.onboardInterval`           | `KRITIKA_TUNING_WORKER_ONBOARD_INTERVAL`             | `30s`   | OnboardInterval is how often the onboarding feeder tops its window up.                                                                                                                                                                                                                                                           |
| `tuning.worker.partSlotInterval`          | `KRITIKA_TUNING_WORKER_PART_SLOT_INTERVAL`           | `2s`    | PartSlotInterval is how often a split review looks for model slots for the parts still waiting for one.                                                                                                                                                                                                                          |
| `tuning.worker.maxNamedFiles`             | `KRITIKA_TUNING_WORKER_MAX_NAMED_FILES`              | `5`     | MaxNamedFiles is how many files a split review's note names before it counts the rest.                                                                                                                                                                                                                                           |
| `tuning.worker.rescueInterval`            | `KRITIKA_TUNING_WORKER_RESCUE_INTERVAL`              | `30s`   | RescueInterval is how often the leader looks for jobs and runs that a dead replica left.                                                                                                                                                                                                                                         |
| `tuning.worker.rescueBatch`               | `KRITIKA_TUNING_WORKER_RESCUE_BATCH`                 | `100`   | RescueBatch is how many jobs and runs one rescue pass takes on.                                                                                                                                                                                                                                                                  |
| `tuning.worker.retentionSweepInterval`    | `KRITIKA_TUNING_WORKER_RETENTION_SWEEP_INTERVAL`     | `1h`    | RetentionSweepInterval is how often the leader deletes what is past its retention window: model-call transcripts, egress refusals, settled model invocations, review diffs, agent conversations, stopped repositories' indexes, finished runs' similar-code exclusions and dashboard sessions.                                   |
| `tuning.worker.conversationRetention`     | `KRITIKA_TUNING_WORKER_CONVERSATION_RETENTION`       | `2h`    | ConversationRetention is how long an agent's conversation is kept for a re-review to carry on: well past any provider's cache window, after which carrying it on costs more than starting afresh.                                                                                                                                |
| `tuning.worker.lockRetry`                 | `KRITIKA_TUNING_WORKER_LOCK_RETRY`                   | `500ms` | LockRetry is how long a write under a pull request's publication lock waits before it tries the held lock again.                                                                                                                                                                                                                 |
| `tuning.worker.shutdownSaveGrace`         | `KRITIKA_TUNING_WORKER_SHUTDOWN_SAVE_GRACE`          | `10s`   | ShutdownSaveGrace is how long a completed runner's answer is still saved for once the replica shuts down.                                                                                                                                                                                                                        |
| `tuning.worker.snoozeMin`                 | `KRITIKA_TUNING_WORKER_SNOOZE_MIN`                   | `5s`    | SnoozeMin is the first snooze of a review that found every model slot held. Each further snooze doubles it, up to SnoozeMax. A slot let go sooner wakes it; the snooze bounds the wait when none is, as when a holder died and its lease expired.                                                                                |
| `tuning.worker.snoozeMax`                 | `KRITIKA_TUNING_WORKER_SNOOZE_MAX`                   | `1m`    | SnoozeMax caps that snooze.                                                                                                                                                                                                                                                                                                      |
| `tuning.worker.publishWait`               | `KRITIKA_TUNING_WORKER_PUBLISH_WAIT`                 | `5s`    | PublishWait is how long a review or thread change whose start found the pull request's publication lock held waits before it tries again: the older review is posting, which is brief.                                                                                                                                           |
| `tuning.worker.labelsMovedSnooze`         | `KRITIKA_TUNING_WORKER_LABELS_MOVED_SNOOZE`          | `1s`    | LabelsMovedSnooze is how long a review skipped by its filter while the pull request's labels moved waits to look again, without spending an attempt.                                                                                                                                                                             |
| `tuning.worker.stackTimeout`              | `KRITIKA_TUNING_WORKER_STACK_TIMEOUT`                | `10s`   | StackTimeout bounds the read of a pull request's stack. The prompt does without it, and the score's read spends the time the confidence model has to answer in.                                                                                                                                                                  |
| `tuning.worker.superviseInterval`         | `KRITIKA_TUNING_WORKER_SUPERVISE_INTERVAL`           | `10s`   | SuperviseInterval is how often supervision looks at a running run.                                                                                                                                                                                                                                                               |
| `tuning.worker.threadTimeout`             | `KRITIKA_TUNING_WORKER_THREAD_TIMEOUT`               | `5m`    | ThreadTimeout bounds a thread job: a few forge requests, each of which can wait out a rate limit for up to River's default job timeout.                                                                                                                                                                                          |
| `tuning.worker.minRetention`              | `KRITIKA_TUNING_WORKER_MIN_RETENTION`                | `24h`   | MinRetention is the shortest transcriptRetention and diffRetention that the configuration takes, so that a review's transcript and diff stay for at least a day to look into.                                                                                                                                                    |

#### `tuning.store`

Store holds the tunable values of package store.

| Setting                                | Environment variable                             | Default | Description                                                                                                                                                                                                                                                                                                                                                                                                                                                     |
| -------------------------------------- | ------------------------------------------------ | ------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `tuning.store.loginStateTtl`           | `KRITIKA_TUNING_STORE_LOGIN_STATE_TTL`           | `10m`   | LoginStateTTL is how long a sign-in can take between leaving for the provider and coming back.                                                                                                                                                                                                                                                                                                                                                                  |
| `tuning.store.maxLoginStates`          | `KRITIKA_TUNING_STORE_MAX_LOGIN_STATES`          | `10000` | MaxLoginStates caps the sign-ins in flight at once. Starting one needs no session, so without a cap anyone can grow login_states without bound for LoginStateTTL. The default is far more than any real dashboard's users start in that time.                                                                                                                                                                                                                   |
| `tuning.store.refusalsPerRun`          | `KRITIKA_TUNING_STORE_REFUSALS_PER_RUN`          | `100`   | RefusalsPerRun bounds the egress refusal rows that a run has, one per kind, host and reason. Past it, a run's further refusals are counted on the rows it has and new ones go unrecorded, so a looping agent cannot fill the table.                                                                                                                                                                                                                             |
| `tuning.store.lockYieldShare`          | `KRITIKA_TUNING_STORE_LOCK_YIELD_SHARE`          | `8`     | LockYieldShare is the share of deadlock_timeout, 1/LockYieldShare, that each of the index partition maintenance's lock waits gets, so that a publication's deadlock check never finds the cycle first and every search and build queued behind the wait waits briefly.                                                                                                                                                                                          |
| `tuning.store.partitionTries`          | `KRITIKA_TUNING_STORE_PARTITION_TRIES`           | `5`     | PartitionTries is how many times the partition maintenance tries a lock wait that it gave up, PartitionRetryPause apart. The next sweep tries again.                                                                                                                                                                                                                                                                                                            |
| `tuning.store.partitionRetryPause`     | `KRITIKA_TUNING_STORE_PARTITION_RETRY_PAUSE`     | `1s`    | PartitionRetryPause is the pause between those tries.                                                                                                                                                                                                                                                                                                                                                                                                           |
| `tuning.store.legacyDrainSlice`        | `KRITIKA_TUNING_STORE_LEGACY_DRAIN_SLICE`        | `5000`  | LegacyDrainSlice is how many chunks the drain of the legacy index partition moves or deletes at a time. It holds the repository's lock while it does, which bounds how long a publication or a webhook's write to the repository waits for it.                                                                                                                                                                                                                  |
| `tuning.store.lockTimeout`             | `KRITIKA_TUNING_STORE_LOCK_TIMEOUT`              | `5s`    | LockTimeout bounds one attempt at the leader lock and each ping of the held connection: a round trip each on a server that is up. It turns a server that is gone into a step-down or a retry instead of a wait on TCP retransmissions.                                                                                                                                                                                                                          |
| `tuning.store.sessionCloseTimeout`     | `KRITIKA_TUNING_STORE_SESSION_CLOSE_TIMEOUT`     | `5s`    | SessionCloseTimeout bounds the best-effort release of a dedicated connection: the leader lock's unlock, and the close of the listen and publication connections.                                                                                                                                                                                                                                                                                                |
| `tuning.store.leaseHeartbeat`          | `KRITIKA_TUNING_STORE_LEASE_HEARTBEAT`           | `30s`   | LeaseHeartbeat is how often a model lease's holder renews it.                                                                                                                                                                                                                                                                                                                                                                                                   |
| `tuning.store.leaseExpiry`             | `KRITIKA_TUNING_STORE_LEASE_EXPIRY`              | `2m`    | LeaseExpiry is the age past which a lease belongs to a process that died and is free to take. Validate holds it above LeaseHeartbeat.                                                                                                                                                                                                                                                                                                                           |
| `tuning.store.leasePollMin`            | `KRITIKA_TUNING_STORE_LEASE_POLL_MIN`            | `2s`    | LeasePollMin is the first wait between tries for a model slot. Each try doubles it, up to LeasePollMax.                                                                                                                                                                                                                                                                                                                                                         |
| `tuning.store.leasePollMax`            | `KRITIKA_TUNING_STORE_LEASE_POLL_MAX`            | `30s`   | LeasePollMax caps that wait.                                                                                                                                                                                                                                                                                                                                                                                                                                    |
| `tuning.store.indexWaitSetup`          | `KRITIKA_TUNING_STORE_INDEX_WAIT_SETUP`          | `5s`    | IndexWaitSetup bounds setting up the session of an index update's wait for a slot: its login and its wait lock.                                                                                                                                                                                                                                                                                                                                                 |
| `tuning.store.indexWaitClose`          | `KRITIKA_TUNING_STORE_INDEX_WAIT_CLOSE`          | `5s`    | IndexWaitClose bounds closing that session.                                                                                                                                                                                                                                                                                                                                                                                                                     |
| `tuning.store.listenBufferSize`        | `KRITIKA_TUNING_STORE_LISTEN_BUFFER_SIZE`        | `1024`  | ListenBufferSize bounds the queue of notifications waiting for their callbacks to run. It only needs to absorb a burst: in a steady state the consumer drains it as fast as Postgres delivers.                                                                                                                                                                                                                                                                  |
| `tuning.store.listenDropWarnInterval`  | `KRITIKA_TUNING_STORE_LISTEN_DROP_WARN_INTERVAL` | `1s`    | ListenDropWarnInterval is the least time between the warnings that the notification buffer is full, each of which counts the notifications dropped since the last.                                                                                                                                                                                                                                                                                              |
| `tuning.store.listenBackoffMin`        | `KRITIKA_TUNING_STORE_LISTEN_BACKOFF_MIN`        | `1s`    | ListenBackoffMin is the first reconnect delay of the notification listener. It doubles per failed attempt, up to ListenBackoffMax, and is jittered so that listeners that one failover disconnected do not all reconnect in lockstep.                                                                                                                                                                                                                           |
| `tuning.store.listenBackoffMax`        | `KRITIKA_TUNING_STORE_LISTEN_BACKOFF_MAX`        | `30s`   | ListenBackoffMax caps that delay.                                                                                                                                                                                                                                                                                                                                                                                                                               |
| `tuning.store.maxCheckpointRead`       | `KRITIKA_TUNING_STORE_MAX_CHECKPOINT_READ`       | `32Mi`  | MaxCheckpointRead bounds what one read of a run's part checkpoints holds in memory. What a runner saved decides the number and size of the rows, and the read runs in the service.                                                                                                                                                                                                                                                                              |
| `tuning.store.pollFailedReviews`       | `KRITIKA_TUNING_STORE_POLL_FAILED_REVIEWS`       | `2`     | PollFailedReviews is how many failed reviews of a head settle it for a poll. A failed review's own comment moves the pull request, so the next poll lists it again and makes up for a failure that does not repeat. One that does repeat is the review's own.                                                                                                                                                                                                   |
| `tuning.store.similarExcludeBatch`     | `KRITIKA_TUNING_STORE_SIMILAR_EXCLUDE_BATCH`     | `1000`  | SimilarExcludeBatch is the most paths that one statement recording a run's similar-code exclusions writes, so that a statement stays small however many paths a report holds.                                                                                                                                                                                                                                                                                   |
| `tuning.store.errorTextChars`          | `KRITIKA_TUNING_STORE_ERROR_TEXT_CHARS`          | `2000`  | ErrorTextChars is how much of an error its row keeps: a review's, a runner run's, an agent run's or an index run's.                                                                                                                                                                                                                                                                                                                                             |
| `tuning.store.dismissalReasonChars`    | `KRITIKA_TUNING_STORE_DISMISSAL_REASON_CHARS`    | `500`   | DismissalReasonChars is how much of a dismissal's reason its row keeps.                                                                                                                                                                                                                                                                                                                                                                                         |
| `tuning.store.followupReasonChars`     | `KRITIKA_TUNING_STORE_FOLLOWUP_REASON_CHARS`     | `500`   | FollowupReasonChars is how much of a follow-up's reason its row keeps.                                                                                                                                                                                                                                                                                                                                                                                          |
| `tuning.store.appPoolMaxConns`         | `KRITIKA_TUNING_STORE_APP_POOL_MAX_CONNS`        | `16`    | AppPoolMaxConns is the application pool's ceiling when its URI sets no pool_max_conns. pgx's own default is the node's CPU count, and a serve pod has no CPU limit, so on a large node two replicas alone could take most of Postgres's connections. Connections open on demand, so a ceiling costs an idle process nothing.                                                                                                                                    |
| `tuning.store.ownerPoolMaxConns`       | `KRITIKA_TUNING_STORE_OWNER_POOL_MAX_CONNS`      | `4`     | OwnerPoolMaxConns is the owner pool's, which carries only the leader's duties.                                                                                                                                                                                                                                                                                                                                                                                  |
| `tuning.store.keepaliveIdle`           | `KRITIKA_TUNING_STORE_KEEPALIVE_IDLE`            | `30s`   | KeepaliveIdle is how long a pool connection is idle before Postgres probes its client, KeepaliveInterval how long it waits between probes, and KeepaliveCount how many unanswered probes drop the session: so a client that died without closing its socket (a node lost, a pod killed) loses its session, and any advisory lock that it held, within about a minute at the defaults, rather than the kernel's two hours. Postgres takes them in whole seconds. |
| `tuning.store.keepaliveInterval`       | `KRITIKA_TUNING_STORE_KEEPALIVE_INTERVAL`        | `10s`   | KeepaliveIdle is how long a pool connection is idle before Postgres probes its client, KeepaliveInterval how long it waits between probes, and KeepaliveCount how many unanswered probes drop the session: so a client that died without closing its socket (a node lost, a pod killed) loses its session, and any advisory lock that it held, within about a minute at the defaults, rather than the kernel's two hours. Postgres takes them in whole seconds. |
| `tuning.store.keepaliveCount`          | `KRITIKA_TUNING_STORE_KEEPALIVE_COUNT`           | `3`     | KeepaliveIdle is how long a pool connection is idle before Postgres probes its client, KeepaliveInterval how long it waits between probes, and KeepaliveCount how many unanswered probes drop the session: so a client that died without closing its socket (a node lost, a pod killed) loses its session, and any advisory lock that it held, within about a minute at the defaults, rather than the kernel's two hours. Postgres takes them in whole seconds. |
| `tuning.store.appStatementTimeout`     | `KRITIKA_TUNING_STORE_APP_STATEMENT_TIMEOUT`     | `1m`    | AppStatementTimeout bounds every statement on the application pool. River, webhook ingest, the gateway and the dashboard share the pool, so an unbounded dashboard query over a large account could hold its connections and stall the rest.                                                                                                                                                                                                                    |
| `tuning.store.ownerStatementTimeout`   | `KRITIKA_TUNING_STORE_OWNER_STATEMENT_TIMEOUT`   | `10m`   | OwnerStatementTimeout bounds every statement on the owner pool. Migrations can build an index for minutes; anything longer is a hang worth breaking.                                                                                                                                                                                                                                                                                                            |
| `tuning.store.pingTimeout`             | `KRITIKA_TUNING_STORE_PING_TIMEOUT`              | `5s`    | PingTimeout bounds the ping a pool gives an idle connection before it hands it out. Without it the ping waits on the kernel's TCP retransmissions, many minutes, when the server died without closing the socket. A ping is an empty query.                                                                                                                                                                                                                     |
| `tuning.store.openRetry`               | `KRITIKA_TUNING_STORE_OPEN_RETRY`                | `5s`    | OpenRetry is how long the start waits to open the store again while the database does not answer, as in a fresh deployment whose Postgres is still bootstrapping.                                                                                                                                                                                                                                                                                               |
| `tuning.store.modelCallSweepBatch`     | `KRITIKA_TUNING_STORE_MODEL_CALL_SWEEP_BATCH`    | `5000`  | ModelCallSweepBatch is how many rows one statement of the model-call, egress refusal, model invocation and similar-code exclusion retention sweeps takes. A sweep is many such statements, each committed on its own, so a backlog too large for the owner connection's statement timeout is worked off rather than rolled back whole every time.                                                                                                               |
| `tuning.store.diffSweepBatch`          | `KRITIKA_TUNING_STORE_DIFF_SWEEP_BATCH`          | `200`   | DiffSweepBatch is the same for review diffs and conversations, which carry a whole diff or exchange each, so their batch is the smaller.                                                                                                                                                                                                                                                                                                                        |
| `tuning.store.summaryTop`              | `KRITIKA_TUNING_STORE_SUMMARY_TOP`               | `6`     | SummaryTop is how many groups of each kind the dashboard's finding summary keeps.                                                                                                                                                                                                                                                                                                                                                                               |
| `tuning.store.queueFinished`           | `KRITIKA_TUNING_STORE_QUEUE_FINISHED`            | `50`    | QueueFinished bounds how many finished jobs the queue view lists.                                                                                                                                                                                                                                                                                                                                                                                               |
| `tuning.store.queueActive`             | `KRITIKA_TUNING_STORE_QUEUE_ACTIVE`              | `500`   | QueueActive bounds how many unfinished jobs it lists, which a healthy queue keeps far below it.                                                                                                                                                                                                                                                                                                                                                                 |
| `tuning.store.recentFailureWindow`     | `KRITIKA_TUNING_STORE_RECENT_FAILURE_WINDOW`     | `24h`   | RecentFailureWindow is how long a discarded job shows in the queue view as a recent failure, unless a newer job of its kind for the same pull request or comment came after it.                                                                                                                                                                                                                                                                                 |
| `tuning.store.apiTokenUseInterval`     | `KRITIKA_TUNING_STORE_API_TOKEN_USE_INTERVAL`    | `1m`    | APITokenUseInterval is how often an API token's last use is written: the dashboard needs to know when it was used, not each use.                                                                                                                                                                                                                                                                                                                                |
| `tuning.store.webhookDeliveryInterval` | `KRITIKA_TUNING_STORE_WEBHOOK_DELIVERY_INTERVAL` | `1m`    | WebhookDeliveryInterval is how often a connection's last webhook delivery, signed or unsigned, is written: the dashboard needs to know that deliveries arrive, not to count them.                                                                                                                                                                                                                                                                               |

#### `tuning.jobs`

Jobs holds the tunable values of package jobs.

| Setting                         | Environment variable                      | Default | Description                                                                                                                                                                                                                                                                                                                                                                              |
| ------------------------------- | ----------------------------------------- | ------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `tuning.jobs.onboardRetryAfter` | `KRITIKA_TUNING_JOBS_ONBOARD_RETRY_AFTER` | `1h`    | OnboardRetryAfter is how long a repository whose onboarding job ended without building an index waits to be offered again: the feeder's pace, and the dashboard's, which shows where a repository waits for it. The poller waits as long to look again at the default branch of a repository whose index job ended without moving its index. River keeps finished jobs longer than that. |
| `tuning.jobs.indexAttempts`     | `KRITIKA_TUNING_JOBS_INDEX_ATTEMPTS`      | `3`     | IndexAttempts bounds an index job's tries: transient failures (a fetch timeout, a runner killed at its deadline) get River's backoff, and an onboarding that fails every try is offered again later.                                                                                                                                                                                     |
| `tuning.jobs.reviewAttempts`    | `KRITIKA_TUNING_JOBS_REVIEW_ATTEMPTS`     | `8`     | ReviewAttempts bounds a review, follow-up or thread job's tries. With River's backoff, the last of the default tries comes over an hour after the first, which outlasts a restart or a database failover. A job that fails every try fails on its input.                                                                                                                                 |

#### `tuning.jobTimeout`

JobTimeout holds the tunable values of package jobtimeout: the bounds of a River job's timeout. River cancels a job's context once its timeout passes, and supervision deletes the runner Job when that happens, so a job's timeout must cover everything that the worker does around the runner as well as the runner.

| Setting                                | Environment variable                              | Default | Description                                                                                                                                                                                                      |
| -------------------------------------- | ------------------------------------------------- | ------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `tuning.jobTimeout.heartbeatInterval`  | `KRITIKA_TUNING_JOB_TIMEOUT_HEARTBEAT_INTERVAL`   | `30s`   | HeartbeatInterval is the cadence of a replica's liveness beats on the River jobs it works.                                                                                                                       |
| `tuning.jobTimeout.abandonedBeats`     | `KRITIKA_TUNING_JOB_TIMEOUT_ABANDONED_BEATS`      | `6`     | AbandonedBeats is how many beats a job can miss before it counts as abandoned by a dead replica (HeartbeatAbandonedAfter), enough to allow for a slow database. A replica fences its job one beat before that.   |
| `tuning.jobTimeout.leaseWaitHeadroom`  | `KRITIKA_TUNING_JOB_TIMEOUT_LEASE_WAIT_HEADROOM`  | `15m`   | LeaseWaitHeadroom is the time a review or a follow-up can spend taking its model lease before its runner starts.                                                                                                 |
| `tuning.jobTimeout.publishHeadroom`    | `KRITIKA_TUNING_JOB_TIMEOUT_PUBLISH_HEADROOM`     | `5m`    | PublishHeadroom covers the worker's side of a review after the runner: reading the agent's run and the pack, scoring, merging, and the forge write-back.                                                         |
| `tuning.jobTimeout.indexWriteHeadroom` | `KRITIKA_TUNING_JOB_TIMEOUT_INDEX_WRITE_HEADROOM` | `60m`   | IndexWriteHeadroom covers embedding a repository's staged chunks and swapping the generation after the index runner ends. The embedding pass of a large repository is many model calls.                          |
| `tuning.jobTimeout.agentFetchHeadroom` | `KRITIKA_TUNING_JOB_TIMEOUT_AGENT_FETCH_HEADROOM` | `5m`    | AgentFetchHeadroom is the job time a review run keeps for fetching and the context pack on top of the agent's own timeout.                                                                                       |
| `tuning.jobTimeout.maxJobTimeout`      | `KRITIKA_TUNING_JOB_TIMEOUT_MAX_JOB_TIMEOUT`      | `3h`    | MaxJobTimeout bounds any review or index job, so that a runner deadline set absurdly high cannot hold a worker slot for longer than the stuck-job rescuer waits.                                                 |
| `tuning.jobTimeout.rescueGrace`        | `KRITIKA_TUNING_JOB_TIMEOUT_RESCUE_GRACE`         | `1h`    | RescueGrace is how much longer than MaxJobTimeout a running job is left alone before River treats it as abandoned by a dead worker (RescueStuckJobsAfter), so that a job still working never runs a second time. |
| `tuning.jobTimeout.runnerDeadline`     | `KRITIKA_TUNING_JOB_TIMEOUT_RUNNER_DEADLINE`      | `15m`   | RunnerDeadline bounds a runner Job whose jobTemplate sets no activeDeadlineSeconds. Validate holds it within MaxRunnerDeadline.                                                                                  |

#### `tuning.poller`

Poller holds the tunable values of package poller.

| Setting                            | Environment variable                          | Default | Description                                                                                                                                                                                                                                                                                                    |
| ---------------------------------- | --------------------------------------------- | ------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `tuning.poller.tipCheckInterval`   | `KRITIKA_TUNING_POLLER_TIP_CHECK_INTERVAL`    | `1h`    | TipCheckInterval is how often the poller looks at the default branches of an account whose connection's webhooks arrive. A look costs one request per indexed repository: at every poll it would double what polling costs a GitHub App installation, and a missed push webhook is rare while the rest arrive. |
| `tuning.poller.pollOffRecheck`     | `KRITIKA_TUNING_POLLER_POLL_OFF_RECHECK`      | `1m`    | PollOffRecheck is how often the poller looks again at a configuration that turns polling off, so that turning it back on takes effect without a restart.                                                                                                                                                       |
| `tuning.poller.reachTimeout`       | `KRITIKA_TUNING_POLLER_REACH_TIMEOUT`         | `1m`    | ReachTimeout bounds one connection's repository listing: the leader lists right after applying the configuration and applies no change until it is done, so a hung forge must not hold that up.                                                                                                                |
| `tuning.poller.maxCloseChecks`     | `KRITIKA_TUNING_POLLER_MAX_CLOSE_CHECKS`      | `50`    | MaxCloseChecks bounds how many pull requests one poll of a repository asks the forge about one by one. The rest wait for the next poll.                                                                                                                                                                        |
| `tuning.poller.reactionWindow`     | `KRITIKA_TUNING_POLLER_REACTION_WINDOW`       | `168h`  | ReactionWindow is how long after its latest review a pull request's reactions are read again: a finding is reacted to soon after it is posted, if at all.                                                                                                                                                      |
| `tuning.poller.reactionPulls`      | `KRITIKA_TUNING_POLLER_REACTION_PULLS`        | `30`    | ReactionPulls bounds the pull requests that one poll reads the reactions of, per account, the most recently reviewed first, so that an account with many open pull requests does not spend its API quota on them every poll.                                                                                   |
| `tuning.poller.forgeRateLimitWait` | `KRITIKA_TUNING_POLLER_FORGE_RATE_LIMIT_WAIT` | `1m`    | ForgeRateLimitWait is how long the looks at an account's default branches pause for a forge's 429 that names no wait.                                                                                                                                                                                          |

#### `tuning.ingest`

Ingest holds the tunable values of package ingest.

| Setting                         | Environment variable                     | Default | Description                                                                       |
| ------------------------------- | ---------------------------------------- | ------- | --------------------------------------------------------------------------------- |
| `tuning.ingest.dispatchTimeout` | `KRITIKA_TUNING_INGEST_DISPATCH_TIMEOUT` | `30s`   | DispatchTimeout bounds one webhook dispatch, which does not end with its request. |

#### `tuning.review`

Review holds the tunable values of package review.

| Setting                              | Environment variable                           | Default | Description                                                                                                                                                                                                                                                                                |
| ------------------------------------ | ---------------------------------------------- | ------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `tuning.review.heldBackLines`        | `KRITIKA_TUNING_REVIEW_HELD_BACK_LINES`        | `3`     | HeldBackLines is how far from every line changed since the last review an incremental re-review's new finding can point before it is held back.                                                                                                                                            |
| `tuning.review.maxLinkedIssues`      | `KRITIKA_TUNING_REVIEW_MAX_LINKED_ISSUES`      | `3`     | MaxLinkedIssues bounds the issues a review reads: a description that closes more is a batch, and the first few say what it is for.                                                                                                                                                         |
| `tuning.review.maxConfidenceFinding` | `KRITIKA_TUNING_REVIEW_MAX_CONFIDENCE_FINDING` | `1500`  | MaxConfidenceFinding bounds one finding in the scorer's prompt and the review's summary.                                                                                                                                                                                                   |
| `tuning.review.maxConfidenceSources` | `KRITIKA_TUNING_REVIEW_MAX_CONFIDENCE_SOURCES` | `20`    | MaxConfidenceSources bounds the sources the scorer's prompt lists.                                                                                                                                                                                                                         |
| `tuning.review.maxConfidenceReason`  | `KRITIKA_TUNING_REVIEW_MAX_CONFIDENCE_REASON`  | `500`   | MaxConfidenceReason bounds the reason kept of the scorer's answer.                                                                                                                                                                                                                         |
| `tuning.review.maxPraise`            | `KRITIKA_TUNING_REVIEW_MAX_PRAISE`             | `3`     | MaxPraise bounds a summary's praise. The schema says so and the parse enforces it.                                                                                                                                                                                                         |
| `tuning.review.maxChecked`           | `KRITIKA_TUNING_REVIEW_MAX_CHECKED`            | `10`    | MaxChecked bounds a summary's checked notes. The schema says so and the parse enforces it.                                                                                                                                                                                                 |
| `tuning.review.maxDiagramBytes`      | `KRITIKA_TUNING_REVIEW_MAX_DIAGRAM_BYTES`      | `4Ki`   | MaxDiagramBytes bounds a summary's diagram. A diagram over it is dropped rather than cut, since a cut one does not render.                                                                                                                                                                 |
| `tuning.review.maxMessageChars`      | `KRITIKA_TUNING_REVIEW_MAX_MESSAGE_CHARS`      | `2000`  | MaxMessageChars bounds one thread message in a follow-up's prompt.                                                                                                                                                                                                                         |
| `tuning.review.oneLineBytes`         | `KRITIKA_TUNING_REVIEW_ONE_LINE_BYTES`         | `200`   | OneLineBytes bounds a text that a prompt or a summary shows on one line: a title, a checked note, an answer, a reason.                                                                                                                                                                     |
| `tuning.review.maxVersionBytes`      | `KRITIKA_TUNING_REVIEW_MAX_VERSION_BYTES`      | `200`   | MaxVersionBytes is the longest token that reads as a version a change moves.                                                                                                                                                                                                               |
| `tuning.review.maxMergePaths`        | `KRITIKA_TUNING_REVIEW_MAX_MERGE_PATHS`        | `20`    | MaxMergePaths is how many of a part's files the merge call is shown before it counts the rest.                                                                                                                                                                                             |
| `tuning.review.maxMergeTake`         | `KRITIKA_TUNING_REVIEW_MAX_MERGE_TAKE`         | `1500`  | MaxMergeTake is how much of a part's take the merge call is shown.                                                                                                                                                                                                                         |
| `tuning.review.partBytes`            | `KRITIKA_TUNING_REVIEW_PART_BYTES`             | `64Ki`  | PartBytes is the most diff one part of a split review holds: about thirty files of an ordinary change at the default, few enough for one agent to read each with care. A review whose diff is larger is split into parts.                                                                  |
| `tuning.review.maxBumpsBytes`        | `KRITIKA_TUNING_REVIEW_MAX_BUMPS_BYTES`        | `2000`  | MaxBumpsBytes bounds the list of the versions a change moves, which is written before the budgeted sections: a digest pinned beside a tag takes a line of its own, and the diff shows the rest.                                                                                            |
| `tuning.review.defaultBudgetTokens`  | `KRITIKA_TUNING_REVIEW_DEFAULT_BUDGET_TOKENS`  | `64000` | DefaultBudgetTokens bounds a prompt's user message when nothing sets a budget, and is the agent's prompt budget when the configuration file sets none.                                                                                                                                     |
| `tuning.review.charsPerToken`        | `KRITIKA_TUNING_REVIEW_CHARS_PER_TOKEN`        | `4`     | CharsPerToken is the conservative approximation of a token's size used for budgeting.                                                                                                                                                                                                      |
| `tuning.review.bodyShare`            | `KRITIKA_TUNING_REVIEW_BODY_SHARE`             | `4`     | BodyShare divides the budget into the share that the description can take and the share that the linked issues can take together: 1/BodyShare each, which leaves at least half of it to the diff at the default. A body over its share is cut there, with a note of how much was left out. |
| `tuning.review.minUserBudget`        | `KRITIKA_TUNING_REVIEW_MIN_USER_BUDGET`        | `2000`  | MinUserBudget is the least a user message is given, in tokens, whatever the system prompt and what follows the diff take of the budget: room for the pull request's head and a small diff.                                                                                                 |
| `tuning.review.noteRoom`             | `KRITIKA_TUNING_REVIEW_NOTE_ROOM`              | `128`   | NoteRoom is kept free for the note on delta files or prior findings that did not fit.                                                                                                                                                                                                      |
| `tuning.review.omissionRoom`         | `KRITIKA_TUNING_REVIEW_OMISSION_ROOM`          | `512`   | OmissionRoom is kept free, before a diff is fitted, for the note on the files that did not fit, which the note keeps within.                                                                                                                                                               |
| `tuning.review.maxFocusRunes`        | `KRITIKA_TUNING_REVIEW_MAX_FOCUS_RUNES`        | `500`   | MaxFocusRunes bounds a review's focus: a line of a comment, not a brief.                                                                                                                                                                                                                   |
| `tuning.review.maxQuoteRefusals`     | `KRITIKA_TUNING_REVIEW_MAX_QUOTE_REFUSALS`     | `2`     | MaxQuoteRefusals is how many times a run's submission is sent back for its quotes before one is taken as it is, its misquoted findings dropped: a model that does not correct a quote twice over will not, and the rest of its review is worth more than the citations.                    |
| `tuning.review.maxRenderBytes`       | `KRITIKA_TUNING_REVIEW_MAX_RENDER_BYTES`       | `64Ki`  | MaxRenderBytes bounds a rendered comment, marker included. GitHub rejects comment bodies over 65,536 characters, and a character is at least a byte, so Validate refuses more.                                                                                                             |
| `tuning.review.renderTimeout`        | `KRITIKA_TUNING_REVIEW_RENDER_TIMEOUT`         | `2s`    | RenderTimeout bounds one render of a repository's template, parse included.                                                                                                                                                                                                                |
| `tuning.review.maxNamedOverlaps`     | `KRITIKA_TUNING_REVIEW_MAX_NAMED_OVERLAPS`     | `5`     | MaxNamedOverlaps is how many overlapping pull requests the default template names before it counts the rest: a monorepo can have many open pull requests on one directory, and a comment is not where to list them.                                                                        |
| `tuning.review.maxNamedShared`       | `KRITIKA_TUNING_REVIEW_MAX_NAMED_SHARED`       | `3`     | MaxNamedShared is how many shared paths of each it names.                                                                                                                                                                                                                                  |
| `tuning.review.maxNamedUsed`         | `KRITIKA_TUNING_REVIEW_MAX_NAMED_USED`         | `10`    | MaxNamedUsed is how many used names the default template writes before it counts the rest. A repository can offer far more commands than a comment needs to list.                                                                                                                          |
| `tuning.review.footerSubjectRunes`   | `KRITIKA_TUNING_REVIEW_FOOTER_SUBJECT_RUNES`   | `40`    | FooterSubjectRunes is how much of a commit subject the footer shows.                                                                                                                                                                                                                       |
| `tuning.review.maxIterations`        | `KRITIKA_TUNING_REVIEW_MAX_ITERATIONS`         | `20000` | MaxIterations is a template render's budget of loop iterations, charged in full when a loop starts, so that nested loops cannot multiply it.                                                                                                                                               |
| `tuning.review.maxMeasureDepth`      | `KRITIKA_TUNING_REVIEW_MAX_MEASURE_DEPTH`      | `64`    | MaxMeasureDepth bounds how deep the template sandbox measures a value. Anything deeper counts as one node.                                                                                                                                                                                 |

#### `tuning.transcript`

Transcript holds the tunable values of package transcript.

| Setting                           | Environment variable                        | Default | Description                                                                                                                                 |
| --------------------------------- | ------------------------------------------- | ------- | ------------------------------------------------------------------------------------------------------------------------------------------- |
| `tuning.transcript.toolResultCap` | `KRITIKA_TUNING_TRANSCRIPT_TOOL_RESULT_CAP` | `64Ki`  | ToolResultCap bounds one recorded tool result's content.                                                                                    |
| `tuning.transcript.rowCap`        | `KRITIKA_TUNING_TRANSCRIPT_ROW_CAP`         | `1Mi`   | RowCap bounds one row's encoded messages. A row over it keeps its messages' structure with a placeholder for every text, content and input. |
| `tuning.transcript.runCap`        | `KRITIKA_TUNING_TRANSCRIPT_RUN_CAP`         | `16Mi`  | RunCap bounds what a run records: once its rows exceed it, later rows keep only the response and usage.                                     |

#### `tuning.prFilter`

PRFilter holds the tunable values of package prfilter.

| Setting                         | Environment variable                       | Default   | Description                                                                                                                                                                                                                                                                                                                                                                                   |
| ------------------------------- | ------------------------------------------ | --------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `tuning.prFilter.evalCostLimit` | `KRITIKA_TUNING_PR_FILTER_EVAL_COST_LIMIT` | `1000000` | EvalCostLimit caps the CEL cost of a single filter evaluation. The expression is the admin's, and its inputs that an attacker sways (a title, labels) are bounded by the forge, so this is defense in depth against a pathological expression: a ceiling that no reasonable predicate approaches, which still bounds an accidental blow-up. CEL has no loops, so a finite cost always exists. |

#### `tuning.model`

Model holds the tunable values of package model.

| Setting                                  | Environment variable                                 | Default  | Description                                                                                                                                                                                                                                                                                                                                                                                                                              |
| ---------------------------------------- | ---------------------------------------------------- | -------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `tuning.model.anthropicMaxTokens`        | `KRITIKA_TUNING_MODEL_ANTHROPIC_MAX_TOKENS`          | `8192`   | AnthropicMaxTokens is the output cap an Anthropic Messages request gets when its step sets none: the API requires one.                                                                                                                                                                                                                                                                                                                   |
| `tuning.model.catalogTtl`                | `KRITIKA_TUNING_MODEL_CATALOG_TTL`                   | `5m`     | CatalogTTL is how long a provider's model catalog, which resolves a floating ~&lt;family>-latest alias, is used before it is read again.                                                                                                                                                                                                                                                                                                 |
| `tuning.model.catalogTimeout`            | `KRITIKA_TUNING_MODEL_CATALOG_TIMEOUT`               | `30s`    | CatalogTimeout bounds one read of a provider's model catalog.                                                                                                                                                                                                                                                                                                                                                                            |
| `tuning.model.catalogRetry`              | `KRITIKA_TUNING_MODEL_CATALOG_RETRY`                 | `1m`     | CatalogRetry is how long a catalog that failed to refresh stays in use before the next refresh: shorter than CatalogTTL, so a passing failure heals soon, and long enough not to hammer a provider that is down.                                                                                                                                                                                                                         |
| `tuning.model.defaultEmbedMaxBatch`      | `KRITIKA_TUNING_MODEL_DEFAULT_EMBED_MAX_BATCH`       | `64`     | DefaultEmbedMaxBatch is the most texts one embeddings request sends: the default when the configuration file does not set embedding.maxBatch. It and the two below are conservative enough for the common OpenAI-compatible servers, which differ widely in what they accept.                                                                                                                                                            |
| `tuning.model.defaultEmbedMaxBatchChars` | `KRITIKA_TUNING_MODEL_DEFAULT_EMBED_MAX_BATCH_CHARS` | `200000` | DefaultEmbedMaxBatchChars is the most characters one embeddings request sends in all: the default when the configuration file does not set embedding.maxBatchChars.                                                                                                                                                                                                                                                                      |
| `tuning.model.defaultEmbedMaxItemChars`  | `KRITIKA_TUNING_MODEL_DEFAULT_EMBED_MAX_ITEM_CHARS`  | `16000`  | DefaultEmbedMaxItemChars is the most characters of one text an embeddings request sends, the rest cut: the default when the configuration file does not set embedding.maxItemChars.                                                                                                                                                                                                                                                      |
| `tuning.model.embedTimeout`              | `KRITIKA_TUNING_MODEL_EMBED_TIMEOUT`                 | `1m`     | EmbedTimeout bounds one embeddings request, a batch of at most DefaultEmbedMaxBatch (or embedding.maxBatch) short texts.                                                                                                                                                                                                                                                                                                                 |
| `tuning.model.planPause`                 | `KRITIKA_TUNING_MODEL_PLAN_PAUSE`                    | `15m`    | PlanPause is how long the adapter sends nothing on a ChatGPT plan after the plan refused a step for its usage limit. The refusal says nothing of when the limit resets, and a step sent meanwhile costs the plan nothing but fails.                                                                                                                                                                                                      |
| `tuning.model.planErrorBodyBytes`        | `KRITIKA_TUNING_MODEL_PLAN_ERROR_BODY_BYTES`         | `4Ki`    | PlanErrorBodyBytes is how much of the body of a ChatGPT plan's refusal before the Responses layer, admission or routing, the error quotes.                                                                                                                                                                                                                                                                                               |
| `tuning.model.defaultStepTimeout`        | `KRITIKA_TUNING_MODEL_DEFAULT_STEP_TIMEOUT`          | `5m`     | DefaultStepTimeout bounds one request to a provider for a step: the default when the configuration file does not set providers.&lt;name>.timeout, and at most configFile.maxProviderTimeout. It is long enough for a step's whole output from a slow model. If a provider takes the request and never answers, the step fails as a timeout, which is Transient, so the gateway's retries and its fallback provider still get their turn. |
| `tuning.model.stepBudgetWaits`           | `KRITIKA_TUNING_MODEL_STEP_BUDGET_WAITS`             | `2m`     | StepBudgetWaits is what a step's budget (model.StepBudget) adds to its provider's and its fallback's request timeouts for the waits between their attempts.                                                                                                                                                                                                                                                                              |
| `tuning.model.gatewaySettle`             | `KRITIKA_TUNING_MODEL_GATEWAY_SETTLE`                | `5m`     | GatewaySettle is what the gateway can take to charge and record an answered step before it writes the answer, which the runner's request to the gateway waits for on top of the step's budget. It covers adapter.chargeTimeout, adapter.recordTimeout and adapter.reservationPad.                                                                                                                                                        |
| `tuning.model.retryWaitCeiling`          | `KRITIKA_TUNING_MODEL_RETRY_WAIT_CEILING`            | `1h`     | RetryWaitCeiling bounds the wait a provider asks for before it becomes a Duration, which a huge number of seconds overflows. Callers wait far less.                                                                                                                                                                                                                                                                                      |
| `tuning.model.maxRerankAnswer`           | `KRITIKA_TUNING_MODEL_MAX_RERANK_ANSWER`             | `8Mi`    | MaxRerankAnswer bounds a rerank answer, which carries a score, and with some APIs a document, for each document sent.                                                                                                                                                                                                                                                                                                                    |

#### `tuning.agent`

Agent holds the tunable values of package agent.

| Setting                                  | Environment variable                                 | Default        | Description                                                                                                                                                                                                                                                                                                                                                                                                                         |
| ---------------------------------------- | ---------------------------------------------------- | -------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `tuning.agent.continueTokens`            | `KRITIKA_TUNING_AGENT_CONTINUE_TOKENS`               | `100000`       | ContinueTokens is the most that a conversation can hold, in tokens, for a later Run to carry it on. Every step of that Run reads the whole of it again. So past this limit, a fresh Run with a handoff costs less, even with the provider's cache warm.                                                                                                                                                                             |
| `tuning.agent.maxCheckoutBytes`          | `KRITIKA_TUNING_AGENT_MAX_CHECKOUT_BYTES`            | `256Mi`        | MaxCheckoutBytes bounds a checkout's total size on the runner pod's scratch volume, beside the fetched pack.                                                                                                                                                                                                                                                                                                                        |
| `tuning.agent.binarySniffBytes`          | `KRITIKA_TUNING_AGENT_BINARY_SNIFF_BYTES`            | `8Ki`          | BinarySniffBytes is how many bytes at the start of a file the agent's tools search for a NUL byte to decide that the file is binary.                                                                                                                                                                                                                                                                                                |
| `tuning.agent.maxSources`                | `KRITIKA_TUNING_AGENT_MAX_SOURCES`                   | `50`           | MaxSources is how many URLs one run of the run tool records as the review's sources.                                                                                                                                                                                                                                                                                                                                                |
| `tuning.agent.maxSourceBytes`            | `KRITIKA_TUNING_AGENT_MAX_SOURCE_BYTES`              | `2Ki`          | MaxSourceBytes is how long a recorded source URL can be.                                                                                                                                                                                                                                                                                                                                                                            |
| `tuning.agent.setupErrorBytes`           | `KRITIKA_TUNING_AGENT_SETUP_ERROR_BYTES`             | `2Ki`          | SetupErrorBytes is how much of a command's setup failure, which the review summary shows, is kept.                                                                                                                                                                                                                                                                                                                                  |
| `tuning.agent.stderrShare`               | `KRITIKA_TUNING_AGENT_STDERR_SHARE`                  | `8`            | StderrShare divides a command's output limit for the end of its stderr that a cut output keeps: an error printed after a long output lands there.                                                                                                                                                                                                                                                                                   |
| `tuning.agent.stderrAppendShare`         | `KRITIKA_TUNING_AGENT_STDERR_APPEND_SHARE`           | `4`            | StderrAppendShare divides a command's output limit for the most that the kept end of stderr, with its lead, can take when it follows a cut output's notes. Longer, it is left out. At most StderrShare, so the end that StderrShare keeps fits.                                                                                                                                                                                     |
| `tuning.agent.commandWaitDelay`          | `KRITIKA_TUNING_AGENT_COMMAND_WAIT_DELAY`            | `1s`           | CommandWaitDelay is how long the run tool waits, once a command is killed, for a child that still holds its output open.                                                                                                                                                                                                                                                                                                            |
| `tuning.agent.defaultGrepMaxResults`     | `KRITIKA_TUNING_AGENT_DEFAULT_GREP_MAX_RESULTS`      | `100`          | DefaultGrepMaxResults is how many matches grep returns when its max_results input is unset or not positive. At most GrepMaxResultsCap.                                                                                                                                                                                                                                                                                              |
| `tuning.agent.grepMaxResultsCap`         | `KRITIKA_TUNING_AGENT_GREP_MAX_RESULTS_CAP`          | `500`          | GrepMaxResultsCap is the most matches grep returns: a larger max_results is clamped to it. The tool's description states it.                                                                                                                                                                                                                                                                                                        |
| `tuning.agent.grepContextLinesCap`       | `KRITIKA_TUNING_AGENT_GREP_CONTEXT_LINES_CAP`        | `10`           | GrepContextLinesCap is the most lines of context grep returns before and after a match. The tool's description states it.                                                                                                                                                                                                                                                                                                           |
| `tuning.agent.maxBlobBytes`              | `KRITIKA_TUNING_AGENT_MAX_BLOB_BYTES`                | `1Mi`          | MaxBlobBytes bounds how large a blob the agent reads: read_file rejects an oversized blob with a tool error, grep skips it and keeps walking, and a checkout leaves it out.                                                                                                                                                                                                                                                         |
| `tuning.agent.defaultMaxSteps`           | `KRITIKA_TUNING_AGENT_DEFAULT_MAX_STEPS`             | `60`           | DefaultMaxSteps is how many steps a Run takes: the default when the configuration file does not set agent.steps.                                                                                                                                                                                                                                                                                                                    |
| `tuning.agent.defaultMaxToolOutputBytes` | `KRITIKA_TUNING_AGENT_DEFAULT_MAX_TOOL_OUTPUT_BYTES` | `32Ki`         | DefaultMaxToolOutputBytes is how much of a tool's output a Run keeps: the default when the configuration file does not set agent.output.                                                                                                                                                                                                                                                                                            |
| `tuning.agent.defaultMaxTokens`          | `KRITIKA_TUNING_AGENT_DEFAULT_MAX_TOKENS`            | `6000000`      | DefaultMaxTokens is a Run's prompt and output token budget, which forces an early submission: the default when the configuration file does not set agent.tokens.                                                                                                                                                                                                                                                                    |
| `tuning.agent.maxOutputTokensPerStep`    | `KRITIKA_TUNING_AGENT_MAX_OUTPUT_TOKENS_PER_STEP`    | `16384`        | MaxOutputTokensPerStep is the most output tokens one step may produce, which the gateway also clamps a runner's request to.                                                                                                                                                                                                                                                                                                         |
| `tuning.agent.emptyRetries`              | `KRITIKA_TUNING_AGENT_EMPTY_RETRIES`                 | `2`            | EmptyRetries is how many steps in a row can end with neither text nor a tool call and each get a nudge. These nudges do not spend the once-per-Run nudge. A reasoning model on the ChatGPT route sometimes ends the step before its submission this way, and the nudge recovered every one. The once-per-Run nudge is kept for a turn that answers in prose. One more such step is taken as a turn with no tool call. 0 is allowed. |
| `tuning.agent.cutOffRetries`             | `KRITIKA_TUNING_AGENT_CUT_OFF_RETRIES`               | `2`            | CutOffRetries is how many steps in a row the output cap can cut off before any tool call. The model is still told to go on after each of them. One more is taken as a turn with no tool call: it is better to ask a model that cannot fit a step into the cap for what it has. 0 is allowed.                                                                                                                                        |
| `tuning.agent.forcedRetries`             | `KRITIKA_TUNING_AGENT_FORCED_RETRIES`                | `2`            | ForcedRetries is how many more steps a model gets after it was told to submit and did not. Each of these steps tells it again. A rejected submission goes back with its error, prose and other tool calls with a note that only the submission is available. The steps spent exploring are worth more than one slip at the end. The token budget bounds the retries where the step cap does not. 0 is allowed.                      |
| `tuning.agent.wrapUpFloor`               | `KRITIKA_TUNING_AGENT_WRAP_UP_FLOOR`                 | `2m`           | WrapUpFloor is the least time that a Run keeps, before its context's deadline, for the steps that it tells the model to submit on, unless the Run's whole time divided by WrapUpTotalShare is less. A submission writes the whole answer, so it can take longer than any step before it.                                                                                                                                            |
| `tuning.agent.wrapUpAnswers`             | `KRITIKA_TUNING_AGENT_WRAP_UP_ANSWERS`               | `2`            | WrapUpAnswers is how many of a Run's slowest answers so far it keeps time for before its deadline: one for the submission and one for a retry.                                                                                                                                                                                                                                                                                      |
| `tuning.agent.wrapUpTotalShare`          | `KRITIKA_TUNING_AGENT_WRAP_UP_TOTAL_SHARE`           | `4`            | WrapUpTotalShare divides a Run's whole time for the most that WrapUpFloor keeps of it, so a short Run is not told to submit at once.                                                                                                                                                                                                                                                                                                |
| `tuning.agent.wrapUpTokenShare`          | `KRITIKA_TUNING_AGENT_WRAP_UP_TOKEN_SHARE`           | `0.9`          | WrapUpTokenShare is the share of a Run's token budget whose spending tells the model to submit with what it has. At 1 or more, only the budget spent whole does.                                                                                                                                                                                                                                                                    |
| `tuning.agent.stepRetryWaits`            | `KRITIKA_TUNING_AGENT_STEP_RETRY_WAITS`              | `30s,1m,2m,4m` | StepRetryWaits are the waits before a step that failed transiently is sent again, one for each retry, while the Run's context lives. The Stepper has already retried on its own schedule, so these outlast a provider outage that schedule did not: the conversation so far is kept rather than the Run ending with its steps spent.                                                                                                |

#### `tuning.chatGpt`

ChatGPT holds the tunable values of package chatgpt.

| Setting                                    | Environment variable                                  | Default | Description                                                                                                                                                  |
| ------------------------------------------ | ----------------------------------------------------- | ------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `tuning.chatGpt.refreshLead`               | `KRITIKA_TUNING_CHATGPT_REFRESH_LEAD`                 | `5m`    | RefreshLead is how long before a ChatGPT plan's access token expires it is renewed. So a step's request never goes out with a token about to lapse under it. |
| `tuning.chatGpt.clientTimeout`             | `KRITIKA_TUNING_CHATGPT_CLIENT_TIMEOUT`               | `30s`   | ClientTimeout bounds one request of an HTTP client that the package makes itself, a sign-in's or a refresh's.                                                |
| `tuning.chatGpt.maxTokenBody`              | `KRITIKA_TUNING_CHATGPT_MAX_TOKEN_BODY`               | `1Mi`   | MaxTokenBody bounds the token endpoint's answer to a refresh.                                                                                                |
| `tuning.chatGpt.callbackReadHeaderTimeout` | `KRITIKA_TUNING_CHATGPT_CALLBACK_READ_HEADER_TIMEOUT` | `10s`   | CallbackReadHeaderTimeout bounds reading a request's headers on the sign-in's local callback server.                                                         |
| `tuning.chatGpt.loginTimeout`              | `KRITIKA_TUNING_CHATGPT_LOGIN_TIMEOUT`                | `10m`   | LoginTimeout bounds a kritika chatgpt login, the wait for the person to sign in included.                                                                    |
| `tuning.chatGpt.callbackCloseTimeout`      | `KRITIKA_TUNING_CHATGPT_CALLBACK_CLOSE_TIMEOUT`       | `1s`    | CallbackCloseTimeout bounds the callback server's graceful shutdown once the sign-in is over, after which it is closed.                                      |

#### `tuning.forge`

Forge holds the tunable values of package forge.

| Setting                     | Environment variable                  | Default | Description                                                                                                                                                                                                                                   |
| --------------------------- | ------------------------------------- | ------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `tuning.forge.maxFileBytes` | `KRITIKA_TUNING_FORGE_MAX_FILE_BYTES` | `1Mi`   | MaxFileBytes bounds what a forge client's FileAt reads of one file, the repository configuration's and a rule file's among them. GitHub's contents API inlines a file only up to 1 MiB, so GitHub reads no more than that whatever this says. |

#### `tuning.gitHub`

GitHub holds the tunable values of package forge/github.

| Setting                               | Environment variable                             | Default | Description                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          |
| ------------------------------------- | ------------------------------------------------ | ------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `tuning.gitHub.rateLimitMaxWait`      | `KRITIKA_TUNING_GITHUB_RATE_LIMIT_MAX_WAIT`      | `1m`    | RateLimitMaxWait bounds how long a request waits out a rate limit before it is sent again, and is the wait of a secondary limit that names none. A longer wait is not worth holding a worker for. The response goes back to go-github, which then refuses every request until the reset without asking GitHub. It also goes back to the caller, whose job River retries later.                                                                                                                                                                                       |
| `tuning.gitHub.responseHeaderTimeout` | `KRITIKA_TUNING_GITHUB_RESPONSE_HEADER_TIMEOUT`  | `1m`    | ResponseHeaderTimeout bounds how long a request waits for GitHub to start answering. The leader's poll runs on a context with no deadline, and a worker's job on one of hours. Otherwise, a server that accepts a request and never answers holds either for as long. The shared transport reads it once, on first use.                                                                                                                                                                                                                                              |
| `tuning.gitHub.errorBodyWait`         | `KRITIKA_TUNING_GITHUB_ERROR_BODY_WAIT`          | `10s`   | ErrorBodyWait bounds how long the body of an answer that is no success, or is a 202, is read for once its headers arrive. go-github reads such a body to its end to tell its errors apart, a 202's to build its AcceptedError, and a body that stalls would hold the request until its context ends: a worker's job hours on, the leader's poll never. An error's body is a few hundred bytes. Cut off, it leaves the status and the headers, by which a primary rate limit is still told apart; a secondary limit that only its body names is then a plain refusal. |
| `tuning.gitHub.maxRedirects`          | `KRITIKA_TUNING_GITHUB_MAX_REDIRECTS`            | `10`    | MaxRedirects is how many redirects, all on the API's origin, a request follows.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| `tuning.gitHub.appJwtLifetime`        | `KRITIKA_TUNING_GITHUB_APP_JWT_LIFETIME`         | `9m`    | AppJWTLifetime is how long an App JWT lives. At most GitHub's cap of ten minutes, and longer than TokenRefreshLead.                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| `tuning.gitHub.appJwtBackdate`        | `KRITIKA_TUNING_GITHUB_APP_JWT_BACKDATE`         | `30s`   | AppJWTBackdate is how far before now an App JWT says it was issued, to absorb minor clock skew.                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| `tuning.gitHub.tokenRefreshLead`      | `KRITIKA_TUNING_GITHUB_TOKEN_REFRESH_LEAD`       | `1m`    | TokenRefreshLead is how long before it expires an App JWT or an installation token is replaced, so a request never goes out with one about to lapse.                                                                                                                                                                                                                                                                                                                                                                                                                 |
| `tuning.gitHub.maxGraphQlBody`        | `KRITIKA_TUNING_GITHUB_MAX_GRAPH_QL_BODY`        | `4Mi`   | MaxGraphQLBody bounds a GraphQL answer.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              |
| `tuning.gitHub.maxGraphQlErrorBytes`  | `KRITIKA_TUNING_GITHUB_MAX_GRAPH_QL_ERROR_BYTES` | `1Ki`   | MaxGraphQLErrorBytes bounds how much of a refused GraphQL answer's body the error carries.                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| `tuning.gitHub.maxListedComments`     | `KRITIKA_TUNING_GITHUB_MAX_LISTED_COMMENTS`      | `5000`  | MaxListedComments is how many comments a listing of a pull request's conversation or inline comments returns, the oldest: a pull request with more is not read to its end into memory.                                                                                                                                                                                                                                                                                                                                                                               |

#### `tuning.gitLab`

GitLab holds the tunable values of package forge/gitlab.

| Setting                        | Environment variable                    | Default | Description                                                                                                           |
| ------------------------------ | --------------------------------------- | ------- | --------------------------------------------------------------------------------------------------------------------- |
| `tuning.gitLab.pageSize`       | `KRITIKA_TUNING_GITLAB_PAGE_SIZE`       | `100`   | PageSize is how many items a paginated listing asks for per page. GitLab serves at most 100, the default.             |
| `tuning.gitLab.maxDiffBytes`   | `KRITIKA_TUNING_GITLAB_MAX_DIFF_BYTES`  | `8Mi`   | MaxDiffBytes bounds a merge request's diff read whole.                                                                |
| `tuning.gitLab.discardTimeout` | `KRITIKA_TUNING_GITLAB_DISCARD_TIMEOUT` | `30s`   | DiscardTimeout bounds the discarding of a failed review's drafts, which runs on even after the review's context ends. |

#### `tuning.forgejo`

Forgejo holds the tunable values of package forge/forgejo.

| Setting                           | Environment variable                        | Default | Description                                                                                                                                                                        |
| --------------------------------- | ------------------------------------------- | ------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `tuning.forgejo.tokenTtl`         | `KRITIKA_TUNING_FORGEJO_TOKEN_TTL`          | `10m`   | TokenTTL is how long a token that kritika calls the API with lasts. Longer than TokenReuseMargin.                                                                                  |
| `tuning.forgejo.runnerTokenTtl`   | `KRITIKA_TUNING_FORGEJO_RUNNER_TOKEN_TTL`   | `1h`    | RunnerTokenTTL is how long a token that a runner fetches with lasts. That token is read once a run starts, but it can wait in a queue first.                                       |
| `tuning.forgejo.tokenSkew`        | `KRITIKA_TUNING_FORGEJO_TOKEN_SKEW`         | `1m`    | TokenSkew is how far a token's issue and not-before times are set back. Forgejo refuses a token issued in its future and allows no leeway.                                         |
| `tuning.forgejo.tokenReuseMargin` | `KRITIKA_TUNING_FORGEJO_TOKEN_REUSE_MARGIN` | `1m`    | TokenReuseMargin is how long a token must have left to be used again rather than replaced.                                                                                         |
| `tuning.forgejo.maxErrorBody`     | `KRITIKA_TUNING_FORGEJO_MAX_ERROR_BODY`     | `512`   | MaxErrorBody bounds how much of an error response an API error quotes.                                                                                                             |
| `tuning.forgejo.maxDiffBytes`     | `KRITIKA_TUNING_FORGEJO_MAX_DIFF_BYTES`     | `8Mi`   | MaxDiffBytes bounds a pull request's diff read whole.                                                                                                                              |
| `tuning.forgejo.pageSize`         | `KRITIKA_TUNING_FORGEJO_PAGE_SIZE`          | `50`    | PageSize is how many items a paginated listing asks for per page. A short page is the last: the page size is sent, so the server's own default never makes a full page look short. |
| `tuning.forgejo.maxRedirects`     | `KRITIKA_TUNING_FORGEJO_MAX_REDIRECTS`      | `10`    | MaxRedirects is how many redirects a request to the instance follows before it fails as a redirect loop.                                                                           |

#### `tuning.webhook`

Webhook holds the tunable values of package webhook.

| Setting                          | Environment variable                       | Default | Description                                                                                                                                     |
| -------------------------------- | ------------------------------------------ | ------- | ----------------------------------------------------------------------------------------------------------------------------------------------- |
| `tuning.webhook.maxBody`         | `KRITIKA_TUNING_WEBHOOK_MAX_BODY`          | `4Mi`   | MaxBody bounds a delivery's payload, read and parsed. GitHub caps deliveries at 25 MB. A pull request event is a few hundred kilobytes at most. |
| `tuning.webhook.gitLabTolerance` | `KRITIKA_TUNING_WEBHOOK_GIT_LAB_TOLERANCE` | `5m`    | GitLabTolerance is how far a GitLab delivery's timestamp can be from now: the signature covers it, so an older one is a replay.                 |

#### `tuning.webApi`

WebAPI holds the tunable values of package webapi.

| Setting                               | Environment variable                            | Default                    | Description                                                                                                                                                                                                                                                                                                                                      |
| ------------------------------------- | ----------------------------------------------- | -------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `tuning.webApi.maxBodyBytes`          | `KRITIKA_TUNING_WEB_API_MAX_BODY_BYTES`         | `1Mi`                      | MaxBodyBytes bounds a request body.                                                                                                                                                                                                                                                                                                              |
| `tuning.webApi.defaultLimit`          | `KRITIKA_TUNING_WEB_API_DEFAULT_LIMIT`          | `50`                       | DefaultLimit is how many items a list returns without ?limit=, or an MCP tool's list without limit. At most MaxLimit. The MCP tools' descriptions state it.                                                                                                                                                                                      |
| `tuning.webApi.maxLimit`              | `KRITIKA_TUNING_WEB_API_MAX_LIMIT`              | `200`                      | MaxLimit is the most items a list returns, whatever ?limit= asks.                                                                                                                                                                                                                                                                                |
| `tuning.webApi.heartbeatInterval`     | `KRITIKA_TUNING_WEB_API_HEARTBEAT_INTERVAL`     | `25s`                      | HeartbeatInterval is how often an idle event stream carries a comment, so that proxies and the browser keep it open.                                                                                                                                                                                                                             |
| `tuning.webApi.clientBuffer`          | `KRITIKA_TUNING_WEB_API_CLIENT_BUFFER`          | `64`                       | ClientBuffer is how many events a slow stream's client can fall behind before it is told to resync instead.                                                                                                                                                                                                                                      |
| `tuning.webApi.writeTimeout`          | `KRITIKA_TUNING_WEB_API_WRITE_TIMEOUT`          | `30s`                      | WriteTimeout bounds each write to an event stream, so a client that stops reading is dropped rather than holding its goroutine forever.                                                                                                                                                                                                          |
| `tuning.webApi.pullFollowups`         | `KRITIKA_TUNING_WEB_API_PULL_FOLLOWUPS`         | `200`                      | PullFollowups bounds the follow-ups a pull request's detail lists.                                                                                                                                                                                                                                                                               |
| `tuning.webApi.rulesRepoPage`         | `KRITIKA_TUNING_WEB_API_RULES_REPO_PAGE`        | `500`                      | RulesRepoPage is how many repositories one read of the rules list takes.                                                                                                                                                                                                                                                                         |
| `tuning.webApi.recentIndexRuns`       | `KRITIKA_TUNING_WEB_API_RECENT_INDEX_RUNS`      | `20`                       | RecentIndexRuns is how many index runs a repository's detail lists.                                                                                                                                                                                                                                                                              |
| `tuning.webApi.indexChunkLimit`       | `KRITIKA_TUNING_WEB_API_INDEX_CHUNK_LIMIT`      | `200`                      | IndexChunkLimit bounds the chunks one file's index listing returns.                                                                                                                                                                                                                                                                              |
| `tuning.webApi.analyticsRepos`        | `KRITIKA_TUNING_WEB_API_ANALYTICS_REPOS`        | `5`                        | AnalyticsRepos is how many repositories the analytics list.                                                                                                                                                                                                                                                                                      |
| `tuning.webApi.analyticsRefusals`     | `KRITIKA_TUNING_WEB_API_ANALYTICS_REFUSALS`     | `50`                       | AnalyticsRefusals is how many refused egress hosts the analytics list, each with a row per repository.                                                                                                                                                                                                                                           |
| `tuning.webApi.analyticsDependencies` | `KRITIKA_TUNING_WEB_API_ANALYTICS_DEPENDENCIES` | `50`                       | AnalyticsDependencies is how many drifting dependencies the analytics list.                                                                                                                                                                                                                                                                      |
| `tuning.webApi.topTools`              | `KRITIKA_TUNING_WEB_API_TOP_TOOLS`              | `10`                       | TopTools is how many of the agents' most run tools the tool usage lists.                                                                                                                                                                                                                                                                         |
| `tuning.webApi.defaultUsageWindow`    | `KRITIKA_TUNING_WEB_API_DEFAULT_USAGE_WINDOW`   | `720h`                     | DefaultUsageWindow is the span that a usage or analytics series covers without ?from=. At most MaxUsageWindow.                                                                                                                                                                                                                                   |
| `tuning.webApi.maxUsageWindow`        | `KRITIKA_TUNING_WEB_API_MAX_USAGE_WINDOW`       | `8784h`                    | MaxUsageWindow is the most that ?from= and ?to= can span. The series are read from every row in the span, and the analytics page reads the span before it too. So a span of years scans the account's history twice on one request.                                                                                                              |
| `tuning.webApi.tokenNameMax`          | `KRITIKA_TUNING_WEB_API_TOKEN_NAME_MAX`         | `100`                      | TokenNameMax is the longest name that a personal access token can have, in characters. The dashboard's token form checks the default, 100, on its own.                                                                                                                                                                                           |
| `tuning.webApi.tokenLifetimes`        | `KRITIKA_TUNING_WEB_API_TOKEN_LIFETIMES`        | `0s,168h,720h,2160h,8760h` | TokenLifetimes are the lifetimes that a personal access token can be created with, 0 for one that never expires. A request asks for whole days, so only a lifetime of whole days can be asked for. None turns the creation of tokens off. The dashboard's token form offers the default lifetimes on its own. An empty list, and 0, are allowed. |
| `tuning.webApi.exportLimit`           | `KRITIKA_TUNING_WEB_API_EXPORT_LIMIT`           | `1000`                     | ExportLimit is how many findings an export holds at most: the newest.                                                                                                                                                                                                                                                                            |

#### `tuning.server`

Server holds the tunable values of package server.

| Setting                           | Environment variable                        | Default | Description                                                                                                                                                                                                           |
| --------------------------------- | ------------------------------------------- | ------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `tuning.server.startingRetry`     | `KRITIKA_TUNING_SERVER_STARTING_RETRY`      | `5s`    | StartingRetry is how long a caller of a starting replica is told to wait, and how often its page reloads, in whole seconds: the store's own retry interval, so a browser's refresh lands just after the next attempt. |
| `tuning.server.shutdownTimeout`   | `KRITIKA_TUNING_SERVER_SHUTDOWN_TIMEOUT`    | `10s`   | ShutdownTimeout bounds how long an in-flight request to the management listener can hold up process exit once the root context is canceled.                                                                           |
| `tuning.server.readHeaderTimeout` | `KRITIKA_TUNING_SERVER_READ_HEADER_TIMEOUT` | `10s`   | ReadHeaderTimeout bounds reading one request's headers.                                                                                                                                                               |
| `tuning.server.readTimeout`       | `KRITIKA_TUNING_SERVER_READ_TIMEOUT`        | `1m`    | ReadTimeout bounds reading one request, body included, so a client that trickles a body does not hold its connection open. It does not bound the handler: an event stream or a model step runs on past it.            |
| `tuning.server.idleTimeout`       | `KRITIKA_TUNING_SERVER_IDLE_TIMEOUT`        | `2m`    | IdleTimeout is how long a keep-alive connection can wait for its next request.                                                                                                                                        |

#### `tuning.auth`

Auth holds the tunable values of package auth.

| Setting                          | Environment variable                        | Default | Description                                                                                                                                                                                                                                                                                   |
| -------------------------------- | ------------------------------------------- | ------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `tuning.auth.maxApiBody`         | `KRITIKA_TUNING_AUTH_MAX_API_BODY`          | `1Mi`   | MaxAPIBody bounds how much of a forge API response a sign-in reads.                                                                                                                                                                                                                           |
| `tuning.auth.gitHubMaxPages`     | `KRITIKA_TUNING_AUTH_GIT_HUB_MAX_PAGES`     | `10`    | GitHubMaxPages bounds how many pages of organizations or teams, a hundred each, a GitHub sign-in reads: far beyond any real account.                                                                                                                                                          |
| `tuning.auth.forgejoPageSize`    | `KRITIKA_TUNING_AUTH_FORGEJO_PAGE_SIZE`     | `50`    | ForgejoPageSize is the page size that a Forgejo sign-in's listing asks for, which is the instance's default cap.                                                                                                                                                                              |
| `tuning.auth.forgejoMaxPages`    | `KRITIKA_TUNING_AUTH_FORGEJO_MAX_PAGES`     | `20`    | ForgejoMaxPages bounds how many pages a Forgejo sign-in reads of one list. A longer list fails the sign-in rather than being cut: a role mapping must not decide on part of what the person belongs to.                                                                                       |
| `tuning.auth.failedBuildTtl`     | `KRITIKA_TUNING_AUTH_FAILED_BUILD_TTL`      | `30s`   | FailedBuildTTL is how long a failed build of a sign-in, that is, the discovery of an unreachable OIDC issuer, is remembered before a sign-in retries it. So a dead issuer does not cost a network round trip on every attempt.                                                                |
| `tuning.auth.maxFailedSignIns`   | `KRITIKA_TUNING_AUTH_MAX_FAILED_SIGN_INS`   | `10`    | MaxFailedSignIns is how many wrong passwords one client address can give in FailedSignInWindow before its password sign-ins are refused. Behind a proxy every client shares its address, so the limit is then the whole instance's: guessing slows for everyone, which is the side to err on. |
| `tuning.auth.failedSignInWindow` | `KRITIKA_TUNING_AUTH_FAILED_SIGN_IN_WINDOW` | `15m`   | FailedSignInWindow is the window that MaxFailedSignIns counts in, from a client's first wrong password.                                                                                                                                                                                       |
| `tuning.auth.maxTrackedClients`  | `KRITIKA_TUNING_AUTH_MAX_TRACKED_CLIENTS`   | `10000` | MaxTrackedClients bounds the password limiter's memory. At this bound, the limiter drops the expired windows. If that frees no room, it drops the oldest window. So a flood of addresses costs the displaced client its count, not the process its memory.                                    |
| `tuning.auth.maxLocalBody`       | `KRITIKA_TUNING_AUTH_MAX_LOCAL_BODY`        | `4Ki`   | MaxLocalBody bounds a password sign-in's request body.                                                                                                                                                                                                                                        |
| `tuning.auth.httpTimeout`        | `KRITIKA_TUNING_AUTH_HTTP_TIMEOUT`          | `15s`   | HTTPTimeout bounds each call to a sign-in provider, when the provider's client sets none.                                                                                                                                                                                                     |

#### `tuning.linear`

Linear holds the tunable values of package linear.

| Setting                          | Environment variable                       | Default | Description                                                                                                      |
| -------------------------------- | ------------------------------------------ | ------- | ---------------------------------------------------------------------------------------------------------------- |
| `tuning.linear.maxResponseBytes` | `KRITIKA_TUNING_LINEAR_MAX_RESPONSE_BYTES` | `1Mi`   | MaxResponseBytes bounds the answer read for one issue. The prompt keeps a few thousand bytes of its description. |
| `tuning.linear.httpTimeout`      | `KRITIKA_TUNING_LINEAR_HTTP_TIMEOUT`       | `15s`   | HTTPTimeout bounds one request to Linear's API, when the client sets no HTTP client of its own.                  |

#### `tuning.upstream`

Upstream holds the tunable values of package upstream.

| Setting                       | Environment variable                   | Default | Description                                                                                                                                                                                                                          |
| ----------------------------- | -------------------------------------- | ------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `tuning.upstream.nearMax`     | `KRITIKA_TUNING_UPSTREAM_NEAR_MAX`     | `5`     | NearMax bounds how many of the tree's paths that end in a missing path a fetch names, where a path given by its last segments is.                                                                                                    |
| `tuning.upstream.renameLimit` | `KRITIKA_TUNING_UPSTREAM_RENAME_LIMIT` | `50`    | RenameLimit is the most deleted or added files whose renames a fetch's diff finds by content: go-git scores each deleted file against each added one, reading the added one whole every time. Past it, only exact renames are found. |

#### `tuning.adapter`

Adapter holds the tunable values of package adapter.

| Setting                              | Environment variable                           | Default | Description                                                                                                                                                                                                                      |
| ------------------------------------ | ---------------------------------------------- | ------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `tuning.adapter.reservationPad`      | `KRITIKA_TUNING_ADAPTER_RESERVATION_PAD`       | `2m`    | ReservationPad is how long past a step's deadline its model invocation's reservation stays held before a sweep may release it, so a step that answered at its deadline is still charged against it. At most model.gatewaySettle. |
| `tuning.adapter.chargeTimeout`       | `KRITIKA_TUNING_ADAPTER_CHARGE_TIMEOUT`        | `2m`    | ChargeTimeout bounds saving an answered step's usage and settling it, on a context of its own, so a step whose caller is gone is still accounted for. With RecordTimeout, at most model.gatewaySettle.                           |
| `tuning.adapter.recordTimeout`       | `KRITIKA_TUNING_ADAPTER_RECORD_TIMEOUT`        | `2s`    | RecordTimeout bounds recording one model call. Recording is best effort: the transcript view loses a turn rather than a review, or a runner's step, that waits on it.                                                            |
| `tuning.adapter.leaseReleaseTimeout` | `KRITIKA_TUNING_ADAPTER_LEASE_RELEASE_TIMEOUT` | `10s`   | LeaseReleaseTimeout bounds letting a fallback's slot go once the step is over, whose context is usually done by then.                                                                                                            |
| `tuning.adapter.retryMin`            | `KRITIKA_TUNING_ADAPTER_RETRY_MIN`             | `1s`    | RetryMin is the wait before a step that failed in a way another attempt can avoid is tried again, doubled each time up to RetryMax.                                                                                              |
| `tuning.adapter.retryMax`            | `KRITIKA_TUNING_ADAPTER_RETRY_MAX`             | `30s`   | RetryMax is the longest of the doubling waits between a step's attempts. At least RetryMin.                                                                                                                                      |
| `tuning.adapter.retryAfterMax`       | `KRITIKA_TUNING_ADAPTER_RETRY_AFTER_MAX`       | `1m`    | RetryAfterMax is the longest Retry-After of a provider's that a step honors: a longer one sleeps the step past its caller's deadline with the remaining retries and the fallback untried.                                        |
| `tuning.adapter.planTokenTtl`        | `KRITIKA_TUNING_ADAPTER_PLAN_TOKEN_TTL`        | `1m`    | PlanTokenTTL bounds how long a replica keeps using a ChatGPT plan's access token that it read without reading it again. So a new sign-in reaches every replica within it.                                                        |

#### `tuning.metrics`

Metrics holds the tunable values of package metrics.

| Setting                              | Environment variable                           | Default | Description                                                                                                                 |
| ------------------------------------ | ---------------------------------------------- | ------- | --------------------------------------------------------------------------------------------------------------------------- |
| `tuning.metrics.queueCollectTimeout` | `KRITIKA_TUNING_METRICS_QUEUE_COLLECT_TIMEOUT` | `2s`    | QueueCollectTimeout bounds reading the job queue's statistics for one scrape, so a slow database does not stall the scrape. |

#### `tuning.roleMap`

RoleMap holds the tunable values of package rolemap.

| Setting                        | Environment variable                      | Default   | Description                                                                                                                                                                                                                                            |
| ------------------------------ | ----------------------------------------- | --------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `tuning.roleMap.evalCostLimit` | `KRITIKA_TUNING_ROLE_MAP_EVAL_COST_LIMIT` | `1000000` | EvalCostLimit bounds one evaluation of a role mapping, in CEL's cost units. The expression is the admin's and its inputs come from the identity provider. So this is a guard against an accident rather than an attack. No real mapping comes near it. |

#### `tuning.repoConfig`

RepoConfig holds the tunable values of package repoconfig. A repository configuration is meant to point at a handful of small instruction and template files, not embed arbitrary content. These caps bound how much of the merge-base tree ends up in a review prompt.

| Setting                                  | Environment variable                                 | Default | Description                                                                                                                  |
| ---------------------------------------- | ---------------------------------------------------- | ------- | ---------------------------------------------------------------------------------------------------------------------------- |
| `tuning.repoConfig.maxFileBytes`         | `KRITIKA_TUNING_REPO_CONFIG_MAX_FILE_BYTES`          | `256Ki` | MaxFileBytes is the largest repository file read for a review: the configuration, a referenced file, a rule file or a skill. |
| `tuning.repoConfig.maxTotalBytes`        | `KRITIKA_TUNING_REPO_CONFIG_MAX_TOTAL_BYTES`         | `1Mi`   | MaxTotalBytes bounds the referenced files read for a review in all.                                                          |
| `tuning.repoConfig.maxRulesBytes`        | `KRITIKA_TUNING_REPO_CONFIG_MAX_RULES_BYTES`         | `16Ki`  | MaxRulesBytes caps the rules a prompt lists by their ids and text.                                                           |
| `tuning.repoConfig.maxRuleFileBytes`     | `KRITIKA_TUNING_REPO_CONFIG_MAX_RULE_FILE_BYTES`     | `32Ki`  | MaxRuleFileBytes caps the content of a prompt's file rules, and of an agent's instructions file.                             |
| `tuning.repoConfig.maxRuleFiles`         | `KRITIKA_TUNING_REPO_CONFIG_MAX_RULE_FILES`          | `100`   | MaxRuleFiles bounds how many rule files are read for one review.                                                             |
| `tuning.repoConfig.maxSkills`            | `KRITIKA_TUNING_REPO_CONFIG_MAX_SKILLS`              | `50`    | MaxSkills bounds how many of a repository's skills are read.                                                                 |
| `tuning.repoConfig.maxSkillDescription`  | `KRITIKA_TUNING_REPO_CONFIG_MAX_SKILL_DESCRIPTION`   | `1024`  | MaxSkillDescription is how long a skill's description can be, in characters.                                                 |
| `tuning.repoConfig.maxSkillListingBytes` | `KRITIKA_TUNING_REPO_CONFIG_MAX_SKILL_LISTING_BYTES` | `4Ki`   | MaxSkillListingBytes bounds how much of the system prompt the listed skills' names and descriptions can take together.       |
| `tuning.repoConfig.maxSkillLoadedBytes`  | `KRITIKA_TUNING_REPO_CONFIG_MAX_SKILL_LOADED_BYTES`  | `32Ki`  | MaxSkillLoadedBytes bounds how much the instructions of the skills loaded whole can take together.                           |

#### `tuning.configFile`

ConfigFile holds the tunable values of package configfile: the defaults a configuration file's keys fall back to, which a key the file sets overrides, and the bounds that the file's values are checked against.

| Setting                                        | Environment variable                                       | Default | Description                                                                                                                                                                                                                                                                                                                                                                                                                          |
| ---------------------------------------------- | ---------------------------------------------------------- | ------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `tuning.configFile.defaultConcurrency`         | `KRITIKA_TUNING_CONFIG_FILE_DEFAULT_CONCURRENCY`           | `8`     | DefaultConcurrency is how many calls can run at once on each model of a provider of the instance, which every account shares: the default when the configuration file does not set providers.&lt;name>.concurrency.                                                                                                                                                                                                                  |
| `tuning.configFile.defaultAccountConcurrency`  | `KRITIKA_TUNING_CONFIG_FILE_DEFAULT_ACCOUNT_CONCURRENCY`   | `2`     | DefaultAccountConcurrency is how many calls can run at once on each model of an account's own provider: the default when the configuration file does not set that provider's concurrency.                                                                                                                                                                                                                                            |
| `tuning.configFile.maxProviderRetries`         | `KRITIKA_TUNING_CONFIG_FILE_MAX_PROVIDER_RETRIES`          | `5`     | MaxProviderRetries bounds a provider's retries: with backoff, five more attempts span about a minute, which a runner's deadline absorbs.                                                                                                                                                                                                                                                                                             |
| `tuning.configFile.maxProviderTimeout`         | `KRITIKA_TUNING_CONFIG_FILE_MAX_PROVIDER_TIMEOUT`          | `30m`   | MaxProviderTimeout bounds a provider's timeout. A step's budget is two of them, which the runner's request to the gateway and a run's deadline must outlast. At least model.defaultStepTimeout.                                                                                                                                                                                                                                      |
| `tuning.configFile.maxPushSettle`              | `KRITIKA_TUNING_CONFIG_FILE_MAX_PUSH_SETTLE`               | `1h`    | MaxPushSettle is the most that a push's review can wait (trigger.settle): a longer wait is a manual review in all but name.                                                                                                                                                                                                                                                                                                          |
| `tuning.configFile.defaultAgentTimeout`        | `KRITIKA_TUNING_CONFIG_FILE_DEFAULT_AGENT_TIMEOUT`         | `20m`   | DefaultAgentTimeout bounds a review's agent: the default when the configuration file does not set agent.timeout. At most the agent timeout that jobTimeout leaves.                                                                                                                                                                                                                                                                   |
| `tuning.configFile.defaultParts`               | `KRITIKA_TUNING_CONFIG_FILE_DEFAULT_PARTS`                 | `8`     | DefaultParts is the most parts a review is split into: the default when the configuration file does not set agent.parts. A 512 KiB diff at review.partBytes a part.                                                                                                                                                                                                                                                                  |
| `tuning.configFile.defaultFetches`             | `KRITIKA_TUNING_CONFIG_FILE_DEFAULT_FETCHES`               | `8`     | DefaultFetches is the most repositories fetch_repo fetches for a review: the default when the configuration file does not set agent.fetches. The upstream of a dependency bump takes one, a review of several a few more.                                                                                                                                                                                                            |
| `tuning.configFile.defaultCommandTimeout`      | `KRITIKA_TUNING_CONFIG_FILE_DEFAULT_COMMAND_TIMEOUT`       | `30s`   | DefaultCommandTimeout bounds one command of the run tool: the default when the configuration file does not set agent.commandTimeout.                                                                                                                                                                                                                                                                                                 |
| `tuning.configFile.minPromptTokens`            | `KRITIKA_TUNING_CONFIG_FILE_MIN_PROMPT_TOKENS`             | `8000`  | MinPromptTokens is the least agent.prompt takes: the system prompt, with its rules and skills, and the pull request's description are paid for out of the same budget. Below it, little of the diff will fit. At most review.defaultBudgetTokens.                                                                                                                                                                                    |
| `tuning.configFile.defaultMaxDeltaFiles`       | `KRITIKA_TUNING_CONFIG_FILE_DEFAULT_MAX_DELTA_FILES`       | `25`    | DefaultMaxDeltaFiles is the most files an incremental review takes: the default when a repository sets no review.incremental.                                                                                                                                                                                                                                                                                                        |
| `tuning.configFile.defaultSimilarFloor`        | `KRITIKA_TUNING_CONFIG_FILE_DEFAULT_SIMILAR_FLOOR`         | `0.5`   | DefaultSimilarFloor is the similar-code floor: the default when the configuration file does not set embedding.similarFloor.                                                                                                                                                                                                                                                                                                          |
| `tuning.configFile.defaultSimilarMargin`       | `KRITIKA_TUNING_CONFIG_FILE_DEFAULT_SIMILAR_MARGIN`        | `0.1`   | DefaultSimilarMargin is the similar-code margin: the default when the configuration file does not set embedding.similarMargin. Wide enough to keep the matches close to a query's best and drop only those well below it.                                                                                                                                                                                                            |
| `tuning.configFile.defaultRerankCandidates`    | `KRITIKA_TUNING_CONFIG_FILE_DEFAULT_RERANK_CANDIDATES`     | `50`    | DefaultRerankCandidates is how many of each query's candidates are sent to be reranked: the default when the configuration file does not set embedding.rerank.candidates, but for the tei API. At most MaxRerankCandidates.                                                                                                                                                                                                          |
| `tuning.configFile.maxRerankCandidates`        | `KRITIKA_TUNING_CONFIG_FILE_MAX_RERANK_CANDIDATES`         | `100`   | MaxRerankCandidates bounds embedding.rerank.candidates.                                                                                                                                                                                                                                                                                                                                                                              |
| `tuning.configFile.defaultTeiRerankCandidates` | `KRITIKA_TUNING_CONFIG_FILE_DEFAULT_TEI_RERANK_CANDIDATES` | `32`    | DefaultTEIRerankCandidates is the candidates sent to a Text Embeddings Inference reranker when the configuration file does not set embedding.rerank.candidates: the most that its --max-client-batch-size takes by default, which refuses a request of more with a 422. At most MaxRerankCandidates.                                                                                                                                 |
| `tuning.configFile.defaultRerankTimeout`       | `KRITIKA_TUNING_CONFIG_FILE_DEFAULT_RERANK_TIMEOUT`        | `15s`   | DefaultRerankTimeout bounds the reranking of one similar-code request: the default when the configuration file does not set embedding.rerank.timeout. It starts before the wait for a slot, so it covers that wait and the reranking of each of the request's queries in turn. A run waits a minute for its answer, of which the timeout leaves at least half to the embedding, its slot and the searches. At most MaxRerankTimeout. |
| `tuning.configFile.maxRerankTimeout`           | `KRITIKA_TUNING_CONFIG_FILE_MAX_RERANK_TIMEOUT`            | `30s`   | MaxRerankTimeout bounds embedding.rerank.timeout.                                                                                                                                                                                                                                                                                                                                                                                    |
| `tuning.configFile.defaultSessionTtl`          | `KRITIKA_TUNING_CONFIG_FILE_DEFAULT_SESSION_TTL`           | `12h`   | DefaultSessionTTL is how long a dashboard session lasts: the default when the configuration file does not set auth.sessionTTL. Within MinSessionTTL and MaxSessionTTL.                                                                                                                                                                                                                                                               |
| `tuning.configFile.minSessionTtl`              | `KRITIKA_TUNING_CONFIG_FILE_MIN_SESSION_TTL`               | `5m`    | MinSessionTTL is the shortest auth.sessionTTL the file can set.                                                                                                                                                                                                                                                                                                                                                                      |
| `tuning.configFile.maxSessionTtl`              | `KRITIKA_TUNING_CONFIG_FILE_MAX_SESSION_TTL`               | `720h`  | MaxSessionTTL is the longest auth.sessionTTL the file can set.                                                                                                                                                                                                                                                                                                                                                                       |
| `tuning.configFile.maxReviewerChars`           | `KRITIKA_TUNING_CONFIG_FILE_MAX_REVIEWER_CHARS`            | `8000`  | MaxReviewerChars bounds an agent's instructions written as text, in characters. The public schema, docs/public/kritika.schema.json, states the default.                                                                                                                                                                                                                                                                              |
| `tuning.configFile.maxReviewers`               | `KRITIKA_TUNING_CONFIG_FILE_MAX_REVIEWERS`                 | `8`     | MaxReviewers bounds how many agents a configuration, or a repository's file, writes: each runs an agent of its own over the change, or picks the one the default pass runs. It also sizes the largest review's job timeout. The public schema states the default.                                                                                                                                                                    |
| `tuning.configFile.maxRuleChars`               | `KRITIKA_TUNING_CONFIG_FILE_MAX_RULE_CHARS`                | `2000`  | MaxRuleChars bounds one rule's text, in characters. The public schema states the default.                                                                                                                                                                                                                                                                                                                                            |

#### `tuning.process`

Process holds the tunable values of the two mains, serve's and the runner's. On stopping, serve's queueDrain, queueStopHeadroom and eventsDrain run one after another, beside gateway.drain, and together must fit within the chart's terminationGracePeriodSeconds, 200s by default: raising one means raising that too.

| Setting                             | Environment variable                          | Default | Description                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     |
| ----------------------------------- | --------------------------------------------- | ------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `tuning.process.memLimitRefresh`    | `KRITIKA_TUNING_PROCESS_MEM_LIMIT_REFRESH`    | `1m`    | MemLimitRefresh is how often GOMEMLIMIT follows the cgroup memory limit, for a pod resized in place.                                                                                                                                                                                                                                                                                                                                                                                                                            |
| `tuning.process.queueDrain`         | `KRITIKA_TUNING_PROCESS_QUEUE_DRAIN`          | `100s`  | QueueDrain is how long a stopping serve lets running jobs finish. A review still running then is cut and retried.                                                                                                                                                                                                                                                                                                                                                                                                               |
| `tuning.process.queueStopHeadroom`  | `KRITIKA_TUNING_PROCESS_QUEUE_STOP_HEADROOM`  | `90s`   | QueueStopHeadroom covers what a cut review does before it hands its job back, each step at its own bound: deleting the runner Job and reading its pod (20s each), revoking its token (10s) and waiting for its agent row (40s). It is also River's stuck-job threshold for a job that its timeout cut. A job still at it when the process is killed is not recorded by River and waits on the rescuer instead. Completed runner answers are checkpointed before merge and confidence calls, so rescue can resume those answers. |
| `tuning.process.eventsDrain`        | `KRITIKA_TUNING_PROCESS_EVENTS_DRAIN`         | `5s`    | EventsDrain then lets the Kubernetes Events of the runs the stop cut be written.                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| `tuning.process.linger`             | `KRITIKA_TUNING_PROCESS_LINGER`               | `5s`    | Linger is how long the public listener and the gateway keep accepting connections once serve is told to stop. Kubernetes and the proxies in front of it take a moment to stop routing to a terminating pod. A connection refused meanwhile is a webhook lost, because GitHub does not redeliver on its own, or a runner's model step retried.                                                                                                                                                                                   |
| `tuning.process.publicDrain`        | `KRITIKA_TUNING_PROCESS_PUBLIC_DRAIN`         | `10s`   | PublicDrain is how long a stopping serve lets webhook and dashboard requests finish. Event streams end at once, when the API's Run returns.                                                                                                                                                                                                                                                                                                                                                                                     |
| `tuning.process.schemaPoll`         | `KRITIKA_TUNING_PROCESS_SCHEMA_POLL`          | `2s`    | SchemaPoll is how often serve looks for the database schema, which the leader migrates, before it works the queues.                                                                                                                                                                                                                                                                                                                                                                                                             |
| `tuning.process.queueStartRetry`    | `KRITIKA_TUNING_PROCESS_QUEUE_START_RETRY`    | `5s`    | QueueStartRetry is the wait between attempts to start the job queue.                                                                                                                                                                                                                                                                                                                                                                                                                                                            |
| `tuning.process.queueStartAttempts` | `KRITIKA_TUNING_PROCESS_QUEUE_START_ATTEMPTS` | `24`    | QueueStartAttempts is how many times serve tries to start the job queue before it fails: at the defaults, with QueueStartRetry, two minutes.                                                                                                                                                                                                                                                                                                                                                                                    |
| `tuning.process.driftInterval`      | `KRITIKA_TUNING_PROCESS_DRIFT_INTERVAL`       | `10s`   | DriftInterval is how often a replica compares its configuration with the one the leader applied, which differ while a rollout is part done.                                                                                                                                                                                                                                                                                                                                                                                     |

<!-- tuning:end -->

### Thread workers

Finding resolution/restoration jobs use a dedicated `thread` queue, with
one worker per replica by default. They call the forge and update the
database. They do not launch runner pods or acquire model slots. The
default reserves capacity during model follow-ups, but permits less
parallelism than two idle follow-up workers. If queue-delay metrics show
that it is needed, increase `threadWorkers`. Database connections and
forge API limits remain shared with reviews, so raising worker counts
does not guarantee higher throughput.

Index jobs that keep indexed repositories at their default branch use an
`index_update` queue of their own. An onboarding wave holds an `index`
worker for each repository's whole first build, so it can fill every
`index` worker in the fleet without taking one that an update needs. Updates
still share the embedder's slots with first builds, which hold one for a
batch of chunks at a time and leave the next to an update that waits. An
update that waits holds a database connection of its own until it gets the
slot ([connections](/database/#connections)), and comes first only once it
has opened that connection. Where it cannot, as when Postgres is at its
connection limit, it waits through the application pool without priority
over first builds. Each
replica runs up to `indexWorkers` jobs on each queue, each with a runner
pod. Updates queued on `index` by an earlier version finish there. A push to a repository
whose index is queued for a rebuild on `index`, as after an index format
change, joins that job and waits behind the onboarding wave with it.

A release without the `index_update` queue (0.0.122 and earlier) never works
it, and its pushes join the repository's job there rather than queue their
own, which leaves that repository's index where it is. Before starting
an older release, restore its configuration, or remove `searchPrompt`,
`lexical` and `rerank` from `embedding`: older releases reject these keys.
Once no replica of the newer release is left, move those jobs to `index`
as the database owner:

```sql
UPDATE river_job SET queue = 'index'
WHERE kind = 'index' AND queue = 'index_update'
  AND state IN ('available', 'pending', 'retryable', 'running', 'scheduled');
```

A job moved while `running` is handed back once the leader finds it stuck.

Other keys in the file are `webUrl` (required public URL), `addr`,
`metricsAddr`, `gatewayAddr`, `gatewayUrl`, `executor` and
`databaseAppRole`. The chart derives all except `webUrl` from deployment
values. The `runner` section also accepts `serviceAccount`. The chart
derives it from its runner values.

The runner image is `runner.podTemplate.spec.containers[0].image`. How
long a finished Job is kept is
`runner.jobTemplate.spec.ttlSecondsAfterFinished`.
