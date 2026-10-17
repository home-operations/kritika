# Configuration

The YAML configuration file is the primary interface for application settings:
sign-in, GitHub Apps, providers, review policy, logging, workers, polling and
retention. `KRITIKA_CONFIG_FILE` names the file and is required by `serve` and `chatgpt login`.
Helm's `config` uses exactly that document structure and renders it into a
ConfigMap.

- **Secrets** are references such as `{ env: ADMIN_PASSWORD }`, never literal
  values in the file. Helm's `env` and `envFrom` supply those variables from
  existing Secrets. kritika removes secret variables from its environment
  after reading them. Database connection strings remain bootstrap variables:
  `KRITIKA_DATABASE_URL`, `KRITIKA_DATABASE_OWNER_URL`, and, for the local
  executor, `KRITIKA_RUNNER_DATABASE_URL`.
- **Deployment settings** belong in Helm's `runner`, `database`, `service` and
  other Kubernetes values. The chart derives the corresponding file settings
  and rejects duplicates under `config`. In particular, use `runner.runtimeClass`
  and `runner.deadline` for runner Jobs.
- **Existing ConfigMaps** can supply `config.yaml` through `existingConfigMap`
  instead of inline `config`. Setting both is an error. The external file
  supplies `webUrl` and application settings; the chart supplies deployment
  settings through environment variables. Omit chart-owned keys from that file:
  `addr`, `metricsAddr`, `gatewayAddr`, `gatewayUrl`, `databaseAppRole`,
  `databaseRunnerRole`, `executor`, and `runner`.
- **Operational settings** support either file or environment: logging, listen
  addresses, workers, polling, retention, database role names, executor and
  runner settings. A setting cannot appear in both sources, even with the same
  value. Conflicts fail startup with the relevant names. Omitted settings use
  application defaults; explicit `false` and `0` remain explicit values.
- **Application declarations and policy** use the file: `auth`, `apps`,
  `providers`, `embedding`, review policy, account and repository settings.
  Environment variables only supply values explicitly referenced by these
  sections. Unreferenced auth, app, provider and policy env variables fail startup with
  migration guidance. `KRITIKA_REVIEW_WORKERS` remains an operational setting.
- **Startup** reads and validates the document at startup; changes require
  a restart. Unknown keys and invalid values fail startup. Runtime validation
  reports all failing bounds together, with file paths and environment
  variable names. Durations use strings such as `10m` or `0s`.
  The chart rolls pods
  when its rendered configuration changes. External ConfigMap and Secret changes
  require a restart or a controller such as Reloader.

The dashboard controls whether each repository is enabled. A repository's own
[`.kritika.yaml`](repository-config.md) narrows the configured policy.
CEL expression keys end in `Expr`, or use `expr` within a condition.

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

| Variable | Purpose |
| --- | --- |
| `KRITIKA_CONFIG_FILE` | Configuration file path, required by `serve` and `chatgpt login` |
| `KRITIKA_DATABASE_URL` | Application DSN for `serve`; runner DSN for `run` |
| `KRITIKA_DATABASE_OWNER_URL` | Owner DSN for migrations and leader work |
| `KRITIKA_RUNNER_DATABASE_URL` | Runner DSN for the local executor |
| `KRITIKA_RUN_SPEC_FILE` | Per-run job document path |
| `KRITIKA_GIT_TOKEN` | Per-run Git credential |
| `KRITIKA_GATEWAY_TOKEN` | Per-run model gateway credential |
| `KRITIKA_DATABASE_TOKEN` | Per-run database credential |
| `KRITIKA_POD_NAME`, `KRITIKA_POD_UID` | The serve pod's name and UID, from the downward API, which its [Kubernetes Events](metrics.md#kubernetes-events) name; the chart sets both |

The service supplies per-run inputs to runner Jobs. Secret references such as
`{ env: OPENROUTER_API_KEY }` can use any environment variable name; the file
defines how that value is used.

## Migrating Helm values

Move the contents of `configFile` into `config`; `configFile` is rejected with
migration guidance. Move flat policy keys into their YAML sections, for example
`config.reviewModel` becomes `config.review.model`, and
`config.authAdminPassword` becomes `config.auth.admin.password: { env: ADMIN_PASSWORD }`.
Move the old password's `valueFrom` into `env.ADMIN_PASSWORD`.
Move `config.runnerRuntimeClass` to `runner.runtimeClass` and
`config.runnerDeadline` to `runner.deadline`. Runtime keys such as
`config.logLevel`, `config.reviewWorkers` and `config.pollInterval` keep their names.
Move automatic auth, app, provider and policy env configuration into the file;
those env names no longer configure the application implicitly. Existing secret
variable names can still be used in explicit `{ env: NAME }` references.
Remove old empty-string duration placeholders such as `config.pollInterval: ""`;
omit the key to use its default. The chart rejects empty duration strings.
Operational env settings remain supported. Remove their corresponding variables
from `env` or `envFrom` when moving them into the file, to avoid duplicates.

Example Helm values:

```yaml
config:
  webUrl: https://kritika.example.com
  logLevel: info
  reviewWorkers: 2
  pollInterval: 10m
  auth:
    admin:
      password: { env: ADMIN_PASSWORD }
  providers:
    openrouter:
      type: openrouter
      apiKey: { env: OPENROUTER_API_KEY }
  review:
    model: openrouter/vendor/large-model
runner:
  runtimeClass: gvisor
  deadline: 20m
env:
  ADMIN_PASSWORD:
    valueFrom:
      secretKeyRef: { name: kritika-auth, key: password }
  OPENROUTER_API_KEY:
    valueFrom:
      secretKeyRef: { name: kritika-models, key: openrouter }
```

## `auth`

`auth` sets how people sign in and what each may do. In Helm these keys
belong under `config.auth`; secret references read variables supplied from
existing Kubernetes Secrets.

| Key | Purpose |
| --- | --- |
| `sessionTTL` | Dashboard session lifetime. |
| `admin.user` | Local administrator username. |
| `admin.password` | Environment reference for the administrator password. |
| `oidc.name` | Sign-in button label. |
| `oidc.issuer` | OpenID Connect issuer URL. |
| `oidc.clientId` | Client ID, inline or an environment reference. |
| `oidc.clientSecret` | Environment reference for the client secret. |
| `oidc.scopes` | Requested scopes as a YAML list. |
| `oidc.rolesClaim` | Claim containing the user's roles. |
| `oidc.roleMappingExpr` | Expression mapping identity claims to a dashboard role. |
| `oidc.defaultRole` | Role when no mapping applies. |
| `github.clientId` | Client ID, inline or an environment reference. |
| `github.clientSecret` | Environment reference for the client secret. |
| `github.roleMappingExpr` | Expression mapping GitHub identity to a dashboard role. |

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
```

- `admin` is the local admin. It signs in on the sign-in page with a
  username, `admin` unless `user` sets another, and `password`. It exists
  only while a password is set: it is the way into a fresh instance, and a
  way in when every provider is down. Ten failed attempts from one address
  within 15 minutes lock that address out until the window passes; each
  replica counts its own.
- `oidc` signs in through any OpenID Connect issuer, an `https` URL. The
  sign-in page labels it `name`, or "SSO" when unset.
- `github` signs in on github.com with an OAuth App's client, or a GitHub
  App's own: the App kritika reviews through can serve both, with a client
  secret generated on its settings page (see [setup](setup.md)).
- `sessionTTL` is how long a dashboard session lasts, between 5 minutes and
  30 days. It defaults to 12 hours.

A provider must allow the callback URL `<KRITIKA_WEB_URL>/auth/callback/oidc`
or `<KRITIKA_WEB_URL>/auth/callback/github`. The dashboard refuses to start
with no way to sign in. The configuration is refused when nothing could
make an admin: set an admin password, or a `roleMappingExpr` on a
provider.

### Roles

| Role   | What it may do                                                                                                                                                                                                   |
| ------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Admin  | runs the instance from the dashboard: turns repositories on and off, queues re-runs, cancels and reindexes, and reads every account, the audit log and the [Configuration page](dashboard.md#configuration-page) |
| Member | reads reviews, conversations and transcripts, with no write access: every account, or only the accounts serving the forge accounts their sign-in placed them on                                                  |

The Configuration page lists the instance settings read-only, each with
its source: the environment, the configuration file, or kritika's default.
A secret shows only whether it is set, and a URL's credentials are hidden.
Every admin write is audit-logged in the same transaction as the change it
makes.

A session holds the role its sign-in gave it. Editing a provider's role
mapping, or rotating the admin password, ends the sessions it granted, so
the next request signs in again under the new rules.

#### Role mappings

A `roleMappingExpr` is a [CEL](https://cel.dev) expression evaluated at
sign-in. It yields one of:

- a role for every account: `"admin"`, `"member"` or `""` for none;
- a map from forge account to `"member"`, which reads only the accounts
  serving those accounts, such as `{"github/org-1": "member"}`; `"*"` as a
  key stands for every account.

CEL gives both branches of a conditional one type, so an expression that
yields a role on one branch and a map on the other wraps one in `dyn()`.

| Provider | Variables the expression sees                                                                                                                                              |
| -------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| OIDC     | `claims`, the ID token's claims merged with the UserInfo response; `roles`, the values of the claim `rolesClaim` names, read from a list, a map's keys, or a single string |
| GitHub   | `login`; `email`; `orgs`, the organizations the user is an active member of; `teams`, each as `"<org>/<team>"`                                                             |

When the mapping places nobody:

- An OIDC sign-in is refused, unless `defaultRole: member` lets it read
  every account. `defaultRole` defaults to `none`, so a wrong mapping fails
  closed.
- A GitHub sign-in reads the accounts serving the user's own account, or an
  organization they are an active member of, and is refused when there are
  none. Accounts a mapping names are added to those.

A mapping that fails to evaluate refuses the sign-in.

## `apps`

`apps` declares the GitHub Apps kritika serves accounts through. The
[setup guide](setup.md) covers creating one.

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
| `clientId`      | the App's client ID, inline or, like the secrets, as a reference                                               |
| `privateKey`    | the App's private key                                                                                          |
| `webhookSecret` | the same value as the App's webhook secret                                                                     |

## `providers` and `embedding`

`providers` are the instance's model keys, and `embedding` the embedder
that builds each repository's similar-code index from one of them.
[Models](models.md) describes both, with retries, fallback, local models
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

## Repository settings and `repositories`

The repository settings are written at the file's root and apply to every
repository, and `repositories` holds the entries that change them for
some: `owner/*` for every repository of an account, and `owner/name` for
one. They come in five groups, with a few keys beside them:

| Group        | Keys                                                                                | What they set                              |
| ------------ | ----------------------------------------------------------------------------------- | ------------------------------------------ |
| `review`     | `model`, `fallback`, `effort`, `fixes`, `approve`, `incremental`, `diagram`, `cost` | what a review runs on and what it says     |
| `confidence` | `model`, `fallback`, `effort`, `threshold`, `gate`, `risk`, `instructions`          | how a review is judged                     |
| `trigger`    | `include`, `exclude`, `limit`                                                       | which pull requests are reviewed, and when |
| `comments`   | `inline`, `summary`, `finding`, `dashboard`                                         | what is posted                             |
| `agent`      | `steps`, `output`, `tokens`, `prompt`, `parts`, `timeout`, `commandTimeout`, `fetches` | the bounds of a review's tool loop   |
| beside them  | `enabled`, `rules`, `context`, `skills`, `ignore`, and `limits` at the root alone   |                                            |

`ignore` lists globs of paths left out of a review's initial diff and
context, the index, and the agent's searches and checkout, on top of
kritika's own (vendored trees, lockfiles, generated and minified code,
source maps and logs). A pull request that changes nothing else is skipped.
The agent can still read an ignored path with `read_file` or its diff with
`read_diff`; `ignore` is not an access restriction. Changing the effective
ignore rules triggers a full rebuild on the next index run, including files
whose contents have not changed. Editing instance configuration does not
queue index jobs by itself; reindex an idle repository from the dashboard
to apply new ignore rules immediately.

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
repositories:
  org-1/*:
    review: { model: org-1-key/vendor/large-model }
    rules:
      - {
          id: wrap-errors,
          rule: 'Wrap errors with fmt.Errorf("<package>: %w", err).',
          paths: ["**/*.go"],
        }
  org-1/repo-1:
    review: { approve: true }
    trigger:
      include:
        - expr: pr.baseRef == "main"
    agent: { commandTimeout: 1m }
```

### What only the admin sets

The root and each entry take the keys a repository's own `.kritika.yaml`
takes, which the [`.kritika.yaml` reference](repository-config.md)
describes, and these, which are the admin's alone:

| Key                       | What                                                                                                                                                                                                                                                                                |
| ------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `confidence.instructions` | guidance to the scorer on rating risk ([risk](confidence.md#risk))                                                                                                                                                                                                                  |
| `review.cost`             | `true` ends the summary's footer with what the pull request's reviews have cost together, every model call charged to one of them. Off unless set: the comment is public, on a fork's pull request too, and the figure is the instance's spend                                      |
| `comments.dashboard`      | `false` keeps the dashboard's URL out of the comments, which anyone can read on a public repository: the summary has no re-run badge, and each severity is its label in text rather than an image the dashboard serves. `@<app slug> review` still re-runs a review ([commands](reviews.md#commands)). On unless set |
| `review.incremental`      | how many files of the change may move since the last review before a re-review covers the whole pull request again; 25 unless set ([incremental reviews](#incremental-reviews))                                                                                                     |
| `trigger.limit`           | how many automatic reviews a pull request gets before kritika pauses them, so a long-lived pull request stops spending on every push; unlimited unless set. The summary of the last one says so, and `@<app slug> review` and `resume` still work ([commands](reviews.md#commands)) |
| `agent`                   | the bounds of a review's tool loop ([the agent](#the-agent))                                                                                                                                                                                                                        |
| `limits`                  | at the root alone: every account's limits ([`accounts`](#accounts))                                                                                                                                                                                                                 |
| `enabled`                 | at the root and `owner/*` only: the default for existing or explicitly listed repositories without a dashboard choice ([which repositories run](#which-repositories-run)). New discoveries wait for UI enablement. A repository's `.kritika.yaml` can only set `enabled: false`, which turns its own repository off |

A repository adds tools of its own through
[`.kritika/mise.toml`](repository-config.md#repository-tools); it cannot
supply mise `[settings]`, which stay the admin's.

`trigger.settle` is no longer a key, at the root or in an entry: a file
that still sets it does not load, so remove it before upgrading. A new
head is reviewed as soon as a slot is free, and a review that starts
cancels the pull request's older active reviews
([overlapping reviews](reviews.md#overlapping-reviews)).

### Precedence

A value applies in this order: kritika's default, the file's root,
`owner/*`, `owner/name`, and the repository's `.kritika.yaml`. A narrower
value replaces the broader one's, except:

| Key                                  | Across the root, `owner/*` and `owner/name`                              | From a `.kritika.yaml`                                            |
| ------------------------------------ | ------------------------------------------------------------------------ | ----------------------------------------------------------------- |
| `ignore`                             | globs add up                                                             | globs add up                                                      |
| `rules`                              | add up by id                                                             | added; a rule under an admin's id is dropped                      |
| `context`                            | replaces                                                                 | added                                                             |
| `trigger.include`, `trigger.exclude` | add up by name ([trigger conditions](#which-pull-requests-are-reviewed)) | judged beside the admin's lists; a name an admin's has is dropped |
| `skills.paths`                       | replaces                                                                 | replaces                                                          |
| `skills.scope`                       | adds up by skill name                                                    | adds up by skill name, the file's winning                         |
| `review.fixes`                       | replaces                                                                 | can only turn it on                                               |
| `confidence.risk`                    | replaces                                                                 | can only lower it                                                 |
| `enabled`                            | replaces                                                                 | can only turn it off                                              |

### Confidence and risk

`confidence` sets a second model's score of each reviewed pull request,
whether the commit status is gated on it, and how risky a change may be
and still be approved. [Confidence and approvals](confidence.md) describes
the keys, how a score is reached, the risk tables and how approvals follow
them.

### Which pull requests are reviewed

`trigger.include` and `trigger.exclude` decide which pull requests are
reviewed: one is reviewed when one `include` condition holds, or there are
none, and no `exclude` condition holds. A condition has:

| Key     | What                                                                                                           |
| ------- | -------------------------------------------------------------------------------------------------------------- |
| `expr`  | a CEL expression over the pull request ([the `pr` variable](repository-config.md#include-and-exclude-recipes)) |
| `paths` | globs that hold when a changed path matches one of them                                                        |
| `name`  | optional; names the condition in a skipped review's commit status, and lets a narrower scope replace it        |

A condition takes an `expr`, `paths` or both, when both must hold.

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
and removes, paths the `ignore` globs match left out. A size limit is an
exclusion on it:

```yaml
trigger:
  exclude: [{ name: too-large, expr: pr.lines > 2000 }]
```

- **When it is decided:** a condition on the pull request alone is decided
  at once, and a pull request it keeps out leaves no review behind. A
  condition with `paths` or `pr.lines` is decided once the pull request is
  fetched, before any model is called, and a pull request it keeps out
  shows as a skipped review; its commit status names the exclusion that
  held when it has a name. The lists are then judged with the first 64 KiB
  of the description.
- **Across scopes:** the lists add up across the root, `owner/*` and
  `owner/name`, and a named condition replaces the broader scope's of that
  name where it stands. A name may not be given twice in one list.
- **A repository's lists:** a `.kritika.yaml` has lists of its own,
  judged beside the admin's: a pull request must pass both, and a
  condition in the file under a name an admin's has is dropped.
- **Checked at startup:** an expression is compiled and smoke-tested
  against a sample pull request, so a broken one fails startup.
- **Forks:** a pull request from a fork is reviewed like any other unless
  excluded, such as with `exclude: [{ name: forks, expr: pr.fork }]`.
- **Asked-for reviews:** a review someone asks for, with
  `@<app slug> review` or a re-run from the dashboard, passes the admin's
  lists whatever they say; a `.kritika.yaml`'s lists still apply to it.

### Incremental reviews

A later push is reviewed against what changed since the last review, as
long as no more than `review.incremental` files of the change moved; past
that, the re-review covers the whole pull request again. What moves
between the two heads outside the change's own paths is the base, under a
merge or a rebase, and does not count; nor does what the base gained in
the change's own files since the last review, as long as the merge base
that review was made against can still be fetched. A re-run at the head
the last review saw, a re-run someone asked for with `@<app slug> review`
or from the dashboard, or a merge or rebase that leaves the change as it
was, always covers the whole pull request. A re-review that covers the
whole pull request is still shown the last review's findings, on the diff
or off it, to report again those that still hold, except
a re-run at the head the last review saw, or one someone asked for, which
look afresh. Findings those fresh reviews omit stay open and counted,
marked as not rechecked. A finding reported again keeps the inline thread
its first report opened, however it is worded now. Each review
also leaves notes, which no comment shows, on what it checked beyond the
diff and found sound, and the next re-review is shown them beside the
findings. Prior merge-impact notes are also offered for reassessment,
within the remaining prompt budget after the diff and earlier findings;
any omitted notes are marked. They are not copied forward automatically.
A continued conversation receives the latest review's impact explicitly,
so older submissions do not define the current list. Split reviewers
establish impact from their own assigned changes instead of inheriting
whole-review notes.

An incremental re-review carries on the last review's conversation
instead when it can, so the model keeps what it read and the provider
serves the earlier part of the conversation from its prompt cache. It is
told the head moved, shown the diff since, and asked for the whole review
again. That takes all of these:

- the merge base has not moved since the last review, so the diff since
  shows everything that changed;
- the last review's conversation was kept on the same review model less
  than 25 minutes before, or 4 for an Anthropic model, whose cache lasts
  5 minutes;
- it is under 100,000 tokens, past which every step's reading it again
  costs more than starting afresh, and under a quarter of the review's
  `agent.tokens`, or what the monthly cap leaves of them;
- the review would send the same system prompt and tools, and the pull
  request's title, description and linked issues are as they were.

Otherwise the re-review starts afresh, with the last review's findings
and notes. On the dashboard, a review that carried on names the review
before on its Timeline, and its Conversation starts where that review's
left off, linking to it.

An incremental re-review raises as new only what the new commits brought,
at P0 or P1, and reports again each of the last review's findings that
still holds, whatever its severity. Every review shown earlier findings,
including a full re-review, names the ones that no longer hold,
which the summary lists as resolved and whose threads it resolves. One it
neither reports again nor names stays open: the summary lists it among the
findings as not rechecked since the head a review last reported it at, it
counts toward the score, the status and the approval, and the next
re-review is asked about it again. A finding
in the diff that the last review did not make, more than 3 lines from
every line changed since, is held back: the summary lists it folded away,
with no inline comment, and it counts toward nothing, the score, the
status and the approval included. A file where one of the last review's
findings went unreported is spared, since that finding may be back under
a new title.

### The agent

`agent` bounds a review's tool loop, and a follow-up's:

| Key              | Default              | What                                                                                         |
| ---------------- | -------------------- | -------------------------------------------------------------------------------------------- |
| `steps`          | 60                   | model calls the loop may make                                                                |
| `output`         | 32768                | bytes one tool call may return; a command's output cut to it is kept whole beside the checkout |
| `tokens`         | 4,000,000            | prompt and output tokens the review, or each part of a split one, may spend across its steps |
| `prompt`         | 24000, at least 8000 | tokens of the prompt a review, its confidence score and a follow-up start from               |
| `parts`          | 8                    | the most parts a review over 64 KiB of diff is split into; 1 never splits                    |
| `timeout`        | 20m, at most 2h35m   | the loop's wall time, each part's in a split review                                          |
| `commandTimeout` | 30s, at least 1s     | one command's wall time                                                                      |
| `fetches`        | 8                    | repositories `fetch_repo` may fetch for a review, a split one's parts between them           |

- **Submitting:** the last step is told to submit, as is any step once the
  review has spent nine tenths of its `tokens`. A step told to submit that
  does not, because the submission was rejected, it answered in prose or
  it called another tool, which is refused, is told again, up to twice, so
  the steps spent reading are not lost to one slip at the end. `steps: 1`
  is the cheapest review: one step, which must submit the findings, over
  the same prompt.
- **Output per step:** a step may produce 16384 tokens, and a model
  reasoning at a high [`review.effort`](models.md#effort) can spend them
  all before it calls a tool. Such a step is told to go on rather than to
  submit, up to twice in a row; a third in a row is taken as an answer in
  prose.
- **The prompt** holds the system prompt, the pull request, as much of the
  diff as fits, whole files only, and then the context. A re-review's
  earlier findings and notes take what the diff leaves, and the diff gives
  way to them up to a quarter of the prompt. A file left out is
  named in the review's notes, and a review's or a follow-up's agent reads
  its part of the diff with `read_diff`, which numbers each line as the
  head has it; the scorer, which has no tools, never sees it. The list of
  changed files marks each one the change adds, deletes or renames, and a
  review's findings may point at any line of the pull request's diff, in a
  file left out as in one shown. A model with a larger context window can
  take more, so a large file is reviewed from the start. Every step sends
  the prompt again and counts it against `tokens`, so a larger `prompt`
  leaves the agent fewer steps unless `tokens` is raised with it, and one
  near `tokens` leaves it none.
- **Large reviews:** a review whose diff is over 64 KiB, counting only the
  files no `ignore` glob matches, is split into parts of about 64 KiB each,
  cut at directory boundaries where it can, at most `parts` of them. Each
  part is reviewed by an agent of its own with the bounds above, from its
  own files' diff and the context built from it, and reports findings only
  on its own files; it can still read any other file. The parts run as
  many at once as the review model has [`concurrency`](#accounts) slots
  free when the review starts, its own and those no other review holds,
  and the rest as those end; the parts' tool calls run one at a time. They
  are published as one review, its summary written from theirs by one more
  call to the review model, which is tried again and falls back as a
  review's step is ([retries](models.md#retries)) and counts towards
  `tokensPerMonth`; when that call still fails or the cap is reached, the
  summary joins the parts' own.
  A part that ends before it submits leaves its files unreviewed: the
  summary names them, and the review counts as incomplete for the commit
  status, approves nothing, withdrawing an approval given before, and is no
  review a later one builds on. An incremental re-review splits only the
  diff since the last review, when that is over 64 KiB. The token grant and
  the Job deadline are sized for the parts from the pull request's size as
  the forge reports it, the deadline for running them in the slots taken.
- **Commands:** the runner's `-tools` image offers `curl`, `fd`, `gh`,
  `jq`, `rg` and `yq`, and a repository's
  [`.kritika/mise.toml`](repository-config.md#repository-tools) and lock,
  read from the merge base, add any other tool mise installs, such as
  `crane`, for that repository. Every command those tools provide is
  offered; `mise` itself never is. On a
  custom image without mise, the six default commands are offered when
  found on `PATH`, and a repository's mise config is not read, which the
  review summary says. Tool install and command resolution failures also
  appear in the summary.
  The agent is told to use `gh` for GitHub,
  which signs in with a token minted for the run that can only read the
  repository under review and public repositories. With `gh` or `curl`
  the agent can also fetch another repository's files, or list its tags,
  with `fetch_repo`, such as the upstream of a dependency on any host.
  [Runner tools](security.md#runner-tools) says what bounds a command and
  a fetch.

### Configuration sources

App and provider declarations, authentication settings, review policy, and
embedding settings come from the file. For these sections, environment variables
supply values only where the file explicitly references them, such as
`auth.admin.password: { env: ADMIN_PASSWORD }` or
`providers.openrouter.apiKey: { env: OPENROUTER_API_KEY }`.

### Which repositories run

kritika registers every repository each App reaches, once the
configuration is applied and again on every poll, and "Resync from GitHub"
on the Repositories page does the same at once. Each also disables the
repositories the App no longer reaches, such as one deleted, renamed or
transferred on GitHub, unless the configuration lists them; a listing that
fails or comes back empty disables nothing. Newly discovered repositories
start **off**, even when the root or `owner/*` sets `enabled: true`. Turn
them on from the Repositories page to begin polling, indexing and reviews.
Resyncs and webhooks preserve that choice. Existing repositories keep their
current state. Explicit configuration entries use its defaults unless an
admin has made a choice in the UI, even if discovery registered the
repository first. A rename webhook within the same account preserves
enablement. A transfer to another account requires that account's admin to
enable the repository. If a rename webhook is missed, a sync cannot link
the old and new names, so the new name waits for enablement.
Whether one runs is decided in this order:

1. an archived repository never runs: unarchive it on GitHub, then
   resync, then turn it on in the UI if it is off;
2. a newly discovered repository stays off until an admin turns it on;
   one an admin turned on or off on the Repositories page runs as they chose;
3. a fork does not run, since an account can reach many forks it never
   meant to review;
4. any other runs as `enabled` says, at the root or `owner/*`: on unless
   one sets `enabled: false`.

An `owner/name` entry may not set `enabled`: the dashboard owns a
repository's on or off. The root and `owner/*` defaults apply only to
existing or explicitly listed repositories without a dashboard choice.
A repository that is off, that the App no longer reaches, or whose account
no App serves is neither reviewed, polled nor indexed. Its index is
dropped once it has been off for `KRITIKA_INDEX_GRACE`, counted from the
leader's hourly sweep that first finds it off; one that runs again before
then keeps its index.

## `accounts`

An account is a user or organization an app serves, `github/<name>`. Its
entry under `accounts`, keyed by its name, holds what is the account's
alone:

```yaml
accounts:
  org-1:
    limits: { reviewsPerDay: 50, tokensPerMonth: 20000000 }
    providers:
      org-1-key: { type: openrouter, apiKey: { env: ORG_1_OPENROUTER_API_KEY } }
```

`limits` caps the account; the root's `limits` sets every account's, and
an account's entry replaces it key by key:

| Key              | Default   | What                                                                                                                                                       |
| ---------------- | --------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `concurrency`    | 2         | how many model calls it runs at once; index runs hold all but one while they embed, so a review's similar-code lookup has a slot unless `concurrency` is 1 |
| `reviewsPerDay`  | unlimited | reviews in a calendar day                                                                                                                                  |
| `tokensPerMonth` | unlimited | input and output tokens in a calendar month                                                                                                                |

`providers` are its own model keys, which its repositories' models can
name ([an account's own keys](models.md#an-accounts-own-keys)).

An account runs while an app serves it. An entry for an account no app
serves is kept, but not run, and the Configuration page lists it as not
served.

## `egress`

`egress` is what runner pods may reach through kritika's gateway beyond
`github.com` and `api.github.com`, which an app allows: `allow` and `deny`
list hosts, IP addresses and CIDRs, `credentials` names hosts the
gateway adds a token to, and `cache` names hosts whose downloads the
gateway keeps for every install, in `gatewayCacheDir`, beyond what a
repository's lock pins, which it keeps once `gatewayCacheDir` is set.
[Security](security.md#the-egress-gateway) describes them with the
gateway, and the cache with [runner tools](security.md#the-gateways-download-cache).

```yaml
# The chart's gatewayCache.enabled sets this one.
gatewayCacheDir: /var/cache/kritika
egress:
  allow: ["*", 10.10.0.5]
  deny: ["*.pastebin.com", 203.0.113.0/24]
  credentials:
    api.github.com: { env: GITHUB_TOKEN }
  cache: [get.helm.sh, github.com]
```

## How kritika runs

The following keys live at the file's root unless they start with `runner.`.
Under Helm, root keys go under `config`, while runner settings go under the
chart's top-level `runner`. Operational environment variables remain supported.

| File key | Environment variable                           | What                                                                                                                                                                                                                             |
| --- | ---------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `pollInterval` | `KRITIKA_POLL_INTERVAL`            | how often the leader lists each app's open pull requests, its backstop for missed webhooks; `0s` turns it off; 10m unless set                                                                                                    |
| `pollLookback` | `KRITIKA_POLL_LOOKBACK`            | how far back a first or long-idle poll looks; 24h unless set                                                                                                                                                                     |
| `onboardWindow` | `KRITIKA_ONBOARD_WINDOW`           | how many onboarding index jobs the leader keeps queued or running at once; 4 unless set                                                                                                                                          |
| `indexGrace` | `KRITIKA_INDEX_GRACE`              | how long the index of a repository that stopped running is kept; 720h unless set                                                                                                                                                 |
| `transcriptRetention` | `KRITIKA_TRANSCRIPT_RETENTION`     | how long a review's full model transcript is kept, and the hosts the gateway refused its runs, at least 24h; 720h unless set                                                                                                     |
| `diffRetention` | `KRITIKA_DIFF_RETENTION`           | how long a review keeps the diff it was made from, the context it read and the repository files it named, at least 24h; 720h unless set                                                                                          |
| `reviewWorkers` | `KRITIKA_REVIEW_WORKERS`           | jobs one replica runs at once in each of the separate review and follow-up queues; 2 per queue unless set |
| `threadWorkers` | `KRITIKA_THREAD_WORKERS` | finding resolution/restoration jobs one replica runs at once, independently of model follow-ups; 1 unless set |
| `indexWorkers` | `KRITIKA_INDEX_WORKERS`            | index jobs one replica runs at once; 1 unless set                                                                                                                                                                                |
| `leaderRetryInterval` | `KRITIKA_LEADER_RETRY_INTERVAL`    | how often a replica retries the leader lock, and the holder checks it still has it; 15s unless set                                                                                                                               |
| `gatewayTokenTtl` | `KRITIKA_GATEWAY_TOKEN_TTL`        | how long a run's gateway token outlives its Job's deadline, in case the replica that minted it dies first; 1h unless set                                                                                                         |
| `gatewayCacheDir` | `KRITIKA_GATEWAY_CACHE_DIR` | where the gateway keeps downloads, a directory of its own: what a repository's lock pins, from any host the egress rules allow, and the downloads of the hosts `egress.cache` names; nothing is kept unless set; the chart's `gatewayCache.enabled` renders it ([the gateway's cache](security.md#the-gateways-download-cache)) |
| `gatewayCacheMaxSize` | `KRITIKA_GATEWAY_CACHE_MAX_SIZE` | the most the gateway's cache keeps, in bytes or with `Ki`, `Mi`, `Gi` or `Ti`, the least recently used downloads going first, and apart from that the most its fetches under way may write; 2Gi unless set |
| `gatewayCacheMaxAge` | `KRITIKA_GATEWAY_CACHE_MAX_AGE` | how long the gateway serves a kept download before it fetches it again; 24h unless set |
| `logLevel` | `KRITIKA_LOG_LEVEL`                | `debug`, `info`, `warn` or `error`; `info` unless set                                                                                                                                                                            |
| `logFormat` | `KRITIKA_LOG_FORMAT`               | `json` or `text`; `json` unless set                                                                                                                                                                                              |
| `runner.deadline` | `KRITIKA_RUNNER_DEADLINE`          | a runner Job's deadline, at most 2h; 15m unless set                                                                                                                                                                              |
| `runner.runtimeClass` | `KRITIKA_RUNNER_RUNTIME_CLASS`     | the RuntimeClass of runner Jobs, e.g. `gvisor`; the cluster default unless set                                                                                                                                                   |
| `runner.imagePullPolicy` | `KRITIKA_RUNNER_IMAGE_PULL_POLICY` | the runner container's imagePullPolicy, `Always`, `IfNotPresent` or `Never`; the chart's `runner.image.pullPolicy` renders it                                                                                                    |
| `runner.resources` | `KRITIKA_RUNNER_RESOURCES`         | a runner pod's resources, as JSON in env or a YAML mapping in the file; the chart's `runner.resources` renders it                                                                                                                                                     |
| `runner.miseUrlReplacements` | `KRITIKA_RUNNER_MISE_URL_REPLACEMENTS` | rewrites of the URLs runner tools download from, such as to a mirror: mise's `url_replacements`, a list of `from` and `to` pairs tried in order, as a JSON array in env or a YAML list in the file; the chart's `runner.mise.urlReplacements` renders it ([more tools](security.md#more-tools)). Replaces `runner.miseConfig` ([migrating](security.md#migrating-from-the-instance-mise-config)). |

A transcript may contain repository content the agent read, and every
member of its account can read it. A review past the diff retention keeps
its findings, summary and what it read by name and size; the dashboard's
diff and raw views say the bodies were not kept.

### Thread workers

Finding resolution/restoration jobs use a dedicated `thread` queue, with one
worker per replica by default. They call the forge and update the database;
they do not launch runner pods or acquire model slots. The default reserves
capacity during model follow-ups, but permits less parallelism than two idle
follow-up workers. Increase `threadWorkers` if queue-delay metrics show
it is needed. Database connections and forge API limits remain shared with
reviews, so raising worker counts does not guarantee higher throughput.

See [queue upgrades and rollbacks](database.md#queue-upgrades-and-rollbacks)
before switching between releases that use different queue names.

Other file settings are `webUrl` (required public URL), `addr`, `metricsAddr`,
`gatewayAddr`, `gatewayUrl`, `executor`, `databaseAppRole` and
`databaseRunnerRole`. The chart derives all except `webUrl` from deployment
values. The `runner` section also accepts `image`, `serviceAccount`,
`databaseSecret`, `databaseSecretKey` and `ttl`; the chart derives these from
its runner and database values. Database Secret names are references, not DSNs.
