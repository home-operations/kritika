# Configuration

kritika takes its settings from three places, each for what it suits:

| Where                                                                                                                                                                                          | What                                                                                                                                                              | Changed by                         |
| ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------- |
| The configuration file, and its `KRITIKA_AUTH_*`, `KRITIKA_APPS_*`, `KRITIKA_PROVIDERS_*`, `KRITIKA_REVIEW_*`, `KRITIKA_CONFIDENCE_*`, `KRITIKA_TRIGGER_*` and `KRITIKA_EMBEDDING_*` variables | What is reviewed and how: sign-in, the GitHub Apps, model providers, the embedder, egress, the repository settings, the repository entries and the accounts       | a restart                          |
| The other `KRITIKA_*` variables                                                                                                                                                                | How kritika runs: its addresses, the database, `KRITIKA_WEB_URL`, logging, polling, onboarding, retention and runner Jobs ([how kritika runs](#how-kritika-runs)) | a restart                          |
| The dashboard                                                                                                                                                                                  | Whether each repository is on or off                                                                                                                              | an admin, on the Repositories page |

A repository's own [`.kritika.yaml`](repository-config.md) narrows what the
configuration sets for it, from its own git.

- **The file** is optional: `KRITIKA_CONFIG_FILE` names it, and the chart's
  `configFile` renders it, so the configuration lives in git with the rest
  of the deployment.
- **A secret** in the file is `{ env: NAME }`, the variable holding it,
  never the value itself; the chart's `env` and `envFrom` set such a
  variable from an existing Secret. Once the configuration is read,
  kritika removes every variable a secret came from from its own
  environment.
- **Variables** also set sign-in, one app, one provider, the review
  models, some review, confidence and trigger settings, and the embedder.
  A variable wins over the file and carries a secret itself, so a small
  deployment can be configured from the environment alone. A variable
  under one of the prefixes above that names no key is refused at startup
  rather than ignored; `KRITIKA_REVIEW_WORKERS`, a process setting, is the
  one exception.
- **The chart's `config`** has a key for every `KRITIKA_*` variable it
  does not derive from its other values, its name without the prefix in
  camelCase (`KRITIKA_AUTH_OIDC_ISSUER` is `authOidcIssuer`); a secret's
  key takes a `valueFrom` from a Secret.
- **An expression:** a key whose value is a [CEL](https://cel.dev)
  expression ends in `Expr`, or is the `expr` of a condition:
  `roleMappingExpr`, and each item of a rule's `when` and of
  `trigger.include` and `trigger.exclude`.
- **When it is read:** once, at startup, with its variables. A change
  takes a restart, and the chart rolls the pods when its `configFile`
  changes. Content that does not load, or that would leave the dashboard
  no way to sign in, fails startup, so a rolling update leaves the old
  pods serving.

## `auth`

`auth` sets how people sign in and what each may do. Its variables are
keys of the chart's `config`, the secrets among them from existing Secrets.

| Key                      | Environment variable                        |
| ------------------------ | ------------------------------------------- |
| `sessionTTL`             | `KRITIKA_AUTH_SESSION_TTL`                  |
| `admin.user`             | `KRITIKA_AUTH_ADMIN_USER`                   |
| `admin.password`         | `KRITIKA_AUTH_ADMIN_PASSWORD`               |
| `oidc.name`              | `KRITIKA_AUTH_OIDC_NAME`                    |
| `oidc.issuer`            | `KRITIKA_AUTH_OIDC_ISSUER`                  |
| `oidc.clientId`          | `KRITIKA_AUTH_OIDC_CLIENT_ID`               |
| `oidc.clientSecret`      | `KRITIKA_AUTH_OIDC_CLIENT_SECRET`           |
| `oidc.scopes`            | `KRITIKA_AUTH_OIDC_SCOPES`, comma-separated |
| `oidc.rolesClaim`        | `KRITIKA_AUTH_OIDC_ROLES_CLAIM`             |
| `oidc.roleMappingExpr`   | `KRITIKA_AUTH_OIDC_ROLE_MAPPING_EXPR`       |
| `oidc.defaultRole`       | `KRITIKA_AUTH_OIDC_DEFAULT_ROLE`            |
| `github.clientId`        | `KRITIKA_AUTH_GITHUB_CLIENT_ID`             |
| `github.clientSecret`    | `KRITIKA_AUTH_GITHUB_CLIENT_SECRET`         |
| `github.roleMappingExpr` | `KRITIKA_AUTH_GITHUB_ROLE_MAPPING_EXPR`     |

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

The same app can come from the environment instead:

| Variable                      | Key                          |
| ----------------------------- | ---------------------------- |
| `KRITIKA_APPS_NAME`           | the key, `github` unless set |
| `KRITIKA_APPS_ACCOUNTS`       | `accounts`, comma-separated  |
| `KRITIKA_APPS_CLIENT_ID`      | `clientId`                   |
| `KRITIKA_APPS_PRIVATE_KEY`    | `privateKey`                 |
| `KRITIKA_APPS_WEBHOOK_SECRET` | `webhookSecret`              |

The environment declares at most one app. It replaces the file's app of
the same name whole, or is added to the file's when none has that name.

## `providers` and `embedding`

`providers` are the instance's model keys, and `embedding` the embedder
that builds each repository's similar-code index from one of them.

```yaml
providers:
  openrouter:
    type: openrouter
    apiKey: { env: OPENROUTER_API_KEY }
embedding:
  model: openrouter/voyageai/voyage-code-4
  dims: 1024
```

A model is named `<provider>/<model>`, on a provider the file declares. A
provider takes:

| Key       | What                                                                                              |
| --------- | ------------------------------------------------------------------------------------------------- |
| `type`    | `openrouter`, `openai`, `anthropic` or `opencode`                                                 |
| `apiKey`  | its key, required                                                                                 |
| `baseUrl` | its API's URL, the type's default unless set                                                      |
| `pricing` | per model id, the prices of a provider that reports no cost ([local models](#local-models))       |
| `retries` | how many more times a failed model step is tried, from 0 to 5; 0 unless set ([retries](#retries)) |

### Retries

`retries` is how many more times a review's model step is tried when the
provider fails it in a way another attempt may get past: a 5xx, a 429, a
timeout or a cut connection.

- The gateway waits up to a second, then up to twice as long each time, to
  at most 30 seconds, and waits out a longer `Retry-After` the provider
  sends, up to a minute.
- It never retries a refusal of the request itself, such as a prompt over
  the model's input limit, or a spent budget.
- A request the provider has not answered in five minutes counts as a
  timeout.
- The provider's client sends nothing again on its own, so `retries` is
  every attempt a step gets. A step's attempts, the waits between them and
  its [fallback](#fallback) share 12 minutes, so long timeouts end the
  retries early.

A routing proxy that picks a model per request is where it earns its keep:
a step the proxy routed badly is answered on the next attempt. A
follow-up's steps are retried as a review's are; the embedder's client
sends a failed request again twice on its own.

A step that still fails that way once the provider's `retries` and the
fallback are spent does not end the review. The runner keeps the
conversation and sends the step again after 30 seconds, then after one,
two and four minutes, while the agent's `timeout` allows; only a step
that fails after the last of those ends the review as failed.

### The embedder

| Key             | Default | What                                                                                                                    |
| --------------- | ------- | ----------------------------------------------------------------------------------------------------------------------- |
| `model`         |         | a model of an `openrouter` or `openai` provider of the instance, whose endpoint, or the type's default, and key it uses |
| `dims`          |         | the model's dimension, at most 4000; it must be what the model returns                                                  |
| `maxBatch`      | 64      | inputs in one request                                                                                                   |
| `maxBatchChars` | 200,000 | characters in one request                                                                                               |
| `maxItemChars`  | 16,000  | characters in one input                                                                                                 |
| `similarFloor`  | 0.5     | the cosine similarity a chunk needs to be offered as similar code                                                       |

With an embedder, a review's prompt carries the index's chunks nearest the
change, and the agent gets a `search_code` tool over the same index; both
keep only chunks at or above `similarFloor`. Where useful matches part
from noise depends on the model and the repository: a small embedder on a
repository of similar files can score nearly everything above 0.5, and a
floor of about 0.7 keeps the matches that mean something.

Without an embedder, indexing is off and reviews run without similar code.
The index holds one model and dimension: a configuration that changes
either drops every repository's index, and the leader builds each again, a
few at a time, as `KRITIKA_ONBOARD_WINDOW` paces them. Removing the
embedder keeps the index, and adding back the same model and dimension
uses it again.

### Local models

A model served in your own network is a provider of type `openai` whose
`baseUrl` is the server's OpenAI-compatible API, such as vLLM, Ollama or
llama.cpp's server, or of type `anthropic` for a server that speaks the
Anthropic API. It can serve reviews, the embedder, or both:

```yaml
providers:
  local:
    type: openai
    baseUrl: http://llm.example.svc.cluster.local:8000/v1
    apiKey: { env: LOCAL_LLM_KEY }
    pricing:
      large-model: { input: 0.1, output: 0.4 }
review:
  model: local/large-model
embedding:
  model: local/embed-model
  dims: 768
```

- `apiKey` is required: a server that takes no key still needs a
  reference, to a variable holding any value.
- The model must support tool calls: a review works through tools and
  submits its findings as a call to `submit_review`, which its last step
  tells it to make, and a follow-up's reply is a tool call too.
- The kritika pods call the server; a review's runner reaches it only
  through their gateway. With the chart's `networkPolicy.enabled`,
  add the server's port to `networkPolicy.egressPorts`, which allows only
  443 unless set.
- A server that reports no cost makes every call cost nothing unless
  `pricing` gives the model's prices, in dollars per million tokens of
  `input`, `output`, `cacheRead` and `cacheWrite`, keyed by the model's
  id on the server. Tokens count against an account's `limits` either
  way.

### OpenCode Go and Zen

OpenCode Go and OpenCode Zen are one gateway with an OpenAI-compatible
chat completions API that routes requests, and caches prompts, by a
per-conversation header, `x-opencode-session`, and refuses a request
without one. A provider of type `opencode` sends it: a review's steps
name their run, and a follow-up names its mention. Its `baseUrl` is Go's,
`https://opencode.ai/zen/go/v1`, unless set; Zen is the same type at
`https://opencode.ai/zen/v1`.

```yaml
providers:
  opencode:
    type: opencode
    apiKey: { env: OPENCODE_API_KEY }
  zen:
    type: opencode
    baseUrl: https://opencode.ai/zen/v1
    apiKey: { env: OPENCODE_API_KEY }
review: { model: opencode/glm-5.3, fallback: zen/qwen3.8-max }
```

- Only the models the gateway serves on `/v1/chat/completions` can be
  used; its endpoint tables say which. Models it serves on
  `/v1/responses` or `/v1/messages` cannot.
- A response that reports no cost makes the call cost nothing unless
  `pricing` gives the model's prices, as for a local model.

## Repository settings and `repositories`

The repository settings are written at the file's root and apply to every
repository, and `repositories` holds the entries that change them for
some: `owner/*` for every repository of an account, and `owner/name` for
one. They come in five groups, with a few keys beside them:

| Group        | Keys                                                                                  | What they set                              |
| ------------ | ------------------------------------------------------------------------------------- | ------------------------------------------ |
| `review`     | `model`, `fallback`, `feedback`, `fixes`, `approve`, `incremental`, `diagram`, `cost` | what a review runs on and what it says     |
| `confidence` | `model`, `threshold`, `gate`, `risk`, `instructions`                                  | how a review is judged                     |
| `trigger`    | `include`, `exclude`, `settle`, `limit`                                               | which pull requests are reviewed, and when |
| `comments`   | `inline`, `summary`, `finding`                                                        | what is posted                             |
| `agent`      | `steps`, `output`, `tokens`, `prompt`, `timeout`, `commands`, `commandTimeout`        | the bounds of a review's tool loop         |
| beside them  | `enabled`, `rules`, `context`, `skills`, `ignore`, and `limits` at the root alone     |                                            |

`ignore` lists globs of the paths kritika never looks at: they are left out
of a review's context and of the index, on top of kritika's own (vendored
trees, lockfiles, generated and minified code, source maps and logs), and a
pull request that changes nothing else is skipped.

```yaml
review:
  model: openrouter/vendor/large-model
  fallback: openrouter/vendor/small-model
  feedback: detailed
trigger:
  settle: 30s
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
    review: { feedback: minimal }
    trigger:
      include:
        - expr: pr.baseRef == "main"
    agent: { commands: [gh, curl] }
```

### What only the admin sets

The root and each entry take the keys a repository's own `.kritika.yaml`
takes, which the [`.kritika.yaml` reference](repository-config.md)
describes, and these, which are the admin's alone:

| Key                       | What                                                                                                                                                                                                                                                                                |
| ------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `confidence.instructions` | guidance to the scorer on rating risk ([confidence and risk](#confidence-and-risk))                                                                                                                                                                                                 |
| `review.cost`             | `true` ends the summary's footer with what the pull request's reviews have cost together, every model call charged to one of them. Off unless set: the comment is public, on a fork's pull request too, and the figure is the instance's spend                                      |
| `review.incremental`      | how many files of the change may move since the last review before a re-review covers the whole pull request again; 25 unless set ([incremental reviews](#incremental-reviews))                                                                                                     |
| `trigger.settle`          | how long a new head waits before its review starts, so a burst of pushes is reviewed once; no wait unless set                                                                                                                                                                       |
| `trigger.limit`           | how many automatic reviews a pull request gets before kritika pauses them, so a long-lived pull request stops spending on every push; unlimited unless set. The summary of the last one says so, and `@<app slug> review` and `resume` still work ([commands](reviews.md#commands)) |
| `agent`                   | the bounds of a review's tool loop ([the agent](#the-agent))                                                                                                                                                                                                                        |
| `limits`                  | at the root alone: every account's limits ([`accounts`](#accounts))                                                                                                                                                                                                                 |
| `enabled`                 | at the root and `owner/*` only: where repositories start ([which repositories run](#which-repositories-run)). A repository's `.kritika.yaml` can only set `enabled: false`, which turns its own repository off                                                                      |

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

### Fallback

A `review.fallback` on the review model's provider is handed to the
provider with the request, as OpenRouter's server-side fallback is, and
the provider or the adapter tries it when the review model fails. A
fallback on another provider is tried by the gateway itself: once a
review's step has failed on the review model, and its provider's
`retries` are spent, the same step goes to the fallback, with that
provider's own `retries`, and the review carries on there. The step's
usage is recorded under the model that answered. A follow-up's steps fall
back the same way.

### Confidence and risk

With a `confidence.model`, a second model scores every reviewed pull
request from 0 to 5: how ready it is to merge, from the diff and the
findings the review reported. A different vendor's model than the review's
makes it a second opinion.

| Key                       | Default                 | What                                                                              |
| ------------------------- | ----------------------- | --------------------------------------------------------------------------------- |
| `confidence.model`        | none: nothing is scored | the model that scores, a `<provider>/<model>`                                     |
| `confidence.threshold`    | 5                       | the score approvals need, and a gated commit status                               |
| `confidence.gate`         | off                     | `true` fails the commit status under the threshold, so it can be a required check |
| `confidence.risk`         | `low`                   | the highest risk a change may be rated and still be approved                      |
| `confidence.instructions` |                         | the admin's guidance to the scorer on rating risk in your code                    |

How the score is reached and used:

- The findings set the most a pull request can score, however the scorer
  reads them: 2 with a blocking finding, 3 with an important one; nits
  take nothing off.
- Without the gate, the commit status reports the score and passes
  whatever it is: a score blocks a merge only where someone asked it to.
  A gated review the scorer did not answer for reports an error on the
  commit, not a pass.
- With no `confidence.model`, a review that ran reports success whatever
  it found.
- A dismissed finding stops counting at the next review, which a push or
  `@<app slug> review` starts.
- A bot's rebase that leaves its patch unchanged is skipped when its last
  review was scored, and keeps that score; one whose last review has no
  score is reviewed again.
- The scorer's call counts towards the account's `tokensPerMonth`, and
  shows in the review's transcript. It is sent a prompt of `agent.prompt`
  tokens: a `confidence.model` with a smaller context window than the
  review's refuses one it cannot take, and the review then has no score.

The same call rates the change's risk, how much damage it could do if the
review missed something, from what the change does and not from where its
files live:

| Risk       | What the change is                                                                                      |
| ---------- | ------------------------------------------------------------------------------------------------------- |
| `low`      | documentation, tests, formatting, comments, and small changes with no effect on behavior that matters   |
| `medium`   | ordinary application or business logic                                                                  |
| `high`     | build or runtime configuration, modules much else depends on                                            |
| `critical` | authentication, authorization, secrets, billing, data migrations, infrastructure, CI, public interfaces |

A dependency update is rated by what the dependency does and how far its
version moves, not as a class of its own. A move of the version's first
non-zero component is a major update: 1.x to 2.x, but also 0.1 to 0.2 and
0.0.1 to 0.0.2, since a version under 1.0 promises nothing from one such
step to the next.

| Update         | Risk                                                                                                                                                             |
| -------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| major          | at least `high`; `critical` when the dependency is one the critical row names                                                                                    |
| minor or patch | `low` for an application nothing else depends on, `medium` for one that other things do, `high` for a module much else depends on or that the critical row names |

`confidence.risk` bears on approvals alone, never on the commit status: a
risky change that scores well passes its check and waits for a person.
With `review.approve` on, a pull request is approved when its score
reaches `confidence.threshold` and its risk is within `confidence.risk`,
so one threshold decides the approval and, where gated, the check: a
repository can have kritika approve what scores well without ever failing
a check, or fail the check and leave approving to people. A bot's
unchanged rebase, skipped with the score it carries, has that score decide
its approval the same way.

`confidence.instructions` is plain guidance to the scorer on rating risk
in your code, such as "the media apps under `kubernetes/apps/default` are
low whatever moves" or "anything under `db/migrations` is critical"; it
refines the table above and changes nothing else about the score. It is
the admin's alone, so a pull request cannot talk its own risk down.

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
rebase, and does not count. A re-run at the head the last review saw, or a
rebase that leaves the change as it was, always covers the whole pull
request.

### The agent

`agent` bounds a review's tool loop, and a follow-up's:

| Key              | Default              | What                                                                           |
| ---------------- | -------------------- | ------------------------------------------------------------------------------ |
| `steps`          | 60                   | model calls the loop may make                                                  |
| `output`         | 32768                | bytes one tool call may return                                                 |
| `tokens`         | 4,000,000            | prompt and output tokens the review may spend across its steps                 |
| `prompt`         | 24000, at least 8000 | tokens of the prompt a review, its confidence score and a follow-up start from |
| `timeout`        | 20m, at most 2h35m   | the loop's wall time                                                           |
| `commands`       | none                 | the programs its run tool may execute                                          |
| `commandTimeout` | 30s, at least 1s     | one command's wall time                                                        |

- **Submitting:** the last step is told to submit, as is any step once the
  review has spent nine tenths of its `tokens`. A step told to submit that
  does not, because the submission was rejected, it answered in prose or
  it called another tool, which is refused, is told again, up to twice, so
  the steps spent reading are not lost to one slip at the end. `steps: 1`
  is the cheapest review: one step, which must submit the findings, over
  the same prompt.
- **The prompt** holds the system prompt, the pull request, as much of the
  diff as fits, whole files only, and then the context. A file left out is
  named in the review's notes, and a review's or a follow-up's agent reads
  it with its tools; the scorer, which has none, never sees it. A model
  with a larger context window can take more, so a large file is reviewed
  from the start. Every step sends the prompt again and counts it against
  `tokens`, so a larger `prompt` leaves the agent fewer steps unless
  `tokens` is raised with it, and one near `tokens` leaves it none.
- **Commands:** the runner's `-tools` image has `gh`, `curl`, `fd`, `jq`,
  `rg` and `yq`. The agent is told to use `gh` for GitHub, which signs in
  with a token minted for the run that can only read the repository under
  review and public repositories. [Runner tools](security.md#runner-tools)
  says what bounds a command.

### Environment variables

The provider, some of the root's settings and the embedder can come from
the environment:

| Variable                       | Key                                                             |
| ------------------------------ | --------------------------------------------------------------- |
| `KRITIKA_PROVIDERS_NAME`       | the provider's name, `openrouter` unless set                    |
| `KRITIKA_PROVIDERS_TYPE`       | `type`, which defaults to the name when that is a provider type |
| `KRITIKA_PROVIDERS_BASE_URL`   | `baseUrl`                                                       |
| `KRITIKA_PROVIDERS_API_KEY`    | `apiKey`                                                        |
| `KRITIKA_PROVIDERS_RETRIES`    | `retries`                                                       |
| `KRITIKA_REVIEW_MODEL`         | `review.model`                                                  |
| `KRITIKA_REVIEW_FALLBACK`      | `review.fallback`                                               |
| `KRITIKA_REVIEW_FEEDBACK`      | `review.feedback`                                               |
| `KRITIKA_REVIEW_APPROVE`       | `review.approve`, `true` or `false`                             |
| `KRITIKA_REVIEW_FIXES`         | `review.fixes`, `true` or `false`                               |
| `KRITIKA_REVIEW_INCREMENTAL`   | `review.incremental`, a whole number of files                   |
| `KRITIKA_REVIEW_DIAGRAM`       | `review.diagram`, `true` or `false`                             |
| `KRITIKA_REVIEW_COST`          | `review.cost`, `true` or `false`                                |
| `KRITIKA_CONFIDENCE_MODEL`     | `confidence.model`                                              |
| `KRITIKA_CONFIDENCE_THRESHOLD` | `confidence.threshold`, a whole number from 0 to 5              |
| `KRITIKA_CONFIDENCE_GATE`      | `confidence.gate`, `true` or `false`                            |
| `KRITIKA_CONFIDENCE_RISK`      | `confidence.risk`, `low`, `medium`, `high` or `critical`        |
| `KRITIKA_TRIGGER_SETTLE`       | `trigger.settle`, a duration such as `30s`                      |
| `KRITIKA_TRIGGER_LIMIT`        | `trigger.limit`, a whole number of reviews                      |
| `KRITIKA_EMBEDDING_MODEL`      | `embedding.model`                                               |
| `KRITIKA_EMBEDDING_DIMS`       | `embedding.dims`                                                |

The environment declares at most one provider, which replaces the file's
of the same name whole or is added to the file's. The embedding variables
set the file's embedder key by key. The Configuration page lists what the
file and the environment set under "Instance settings", with where each
comes from.

### Which repositories run

kritika registers every repository each App reaches, once the
configuration is applied and again on every poll, and "Resync from GitHub"
on the Repositories page does the same at once. Whether one runs is
decided in this order:

1. an archived repository never runs: unarchive it on GitHub, then
   resync;
2. one an admin turned on or off on the Repositories page runs as they
   chose;
3. a fork does not run, since an account can reach many forks it never
   meant to review;
4. any other runs as `enabled` says, at the root or `owner/*`: on unless
   one sets `enabled: false`.

An `owner/name` entry may not set `enabled`: the dashboard owns a
repository's on or off, and the configuration only says where one starts.
A repository that is off is neither reviewed, polled nor indexed. One an
admin turned off, or that the App no longer reaches, has its index dropped
once `KRITIKA_INDEX_GRACE` has passed.

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

`providers` are its own model keys. A model named `<key name>/<model>` in
its `owner/*` or `owner/name` entries, or in one of its repositories'
`.kritika.yaml`, runs on that key and the account pays for it; a key's
name may not be one the instance's providers use.

An account runs while an app serves it. An entry for an account no app
serves is kept, but not run, and the Configuration page lists it as not
served.

## `egress`

`egress` is what runner pods may reach through kritika's gateway beyond
`github.com` and `api.github.com`, which an app allows: `allow` and `deny`
list hosts, IP addresses and CIDRs, and `credentials` names hosts the
gateway adds a token to. [Security](security.md#the-egress-gateway)
describes them with the gateway.

```yaml
egress:
  allow: ["*", 10.10.0.5]
  deny: ["*.pastebin.com", 203.0.113.0/24]
  credentials:
    api.github.com: { env: GITHUB_TOKEN }
```

## How kritika runs

These come from the environment rather than the file, each a camelCased
key of the chart's `config` (`KRITIKA_POLL_INTERVAL` is `pollInterval`)
except where noted; a restart changes them. The chart sets the rest of
the process's variables, its addresses, database URLs and roles and the
runner image, from its other values.

| Variable                           | What                                                                                                                                                                                                                             |
| ---------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `KRITIKA_POLL_INTERVAL`            | how often the leader lists each app's open pull requests, its backstop for missed webhooks; `0s` turns it off; 10m unless set                                                                                                    |
| `KRITIKA_POLL_LOOKBACK`            | how far back a first or long-idle poll looks; 24h unless set                                                                                                                                                                     |
| `KRITIKA_ONBOARD_WINDOW`           | how many onboarding index jobs the leader keeps queued or running at once; 4 unless set                                                                                                                                          |
| `KRITIKA_INDEX_GRACE`              | how long the index of a repository that stopped running is kept; 720h unless set                                                                                                                                                 |
| `KRITIKA_TRANSCRIPT_RETENTION`     | how long a review's full model transcript is kept, at least 24h; 720h unless set                                                                                                                                                 |
| `KRITIKA_DIFF_RETENTION`           | how long a review keeps the diff it was made from, the context it read and the repository files it named, at least 24h; 720h unless set                                                                                          |
| `KRITIKA_REVIEW_WORKERS`           | review jobs one replica runs at once; 2 unless set                                                                                                                                                                               |
| `KRITIKA_INDEX_WORKERS`            | index jobs one replica runs at once; 1 unless set                                                                                                                                                                                |
| `KRITIKA_LEADER_RETRY_INTERVAL`    | how often a replica retries the leader lock, and the holder checks it still has it; 15s unless set                                                                                                                               |
| `KRITIKA_GATEWAY_TOKEN_TTL`        | how long a run's gateway token outlives its Job's deadline, in case the replica that minted it dies first; 1h unless set                                                                                                         |
| `KRITIKA_LOG_LEVEL`                | `debug`, `info`, `warn` or `error`; `info` unless set                                                                                                                                                                            |
| `KRITIKA_LOG_FORMAT`               | `json` or `text`; `json` unless set                                                                                                                                                                                              |
| `KRITIKA_RUNNER_DEADLINE`          | a runner Job's deadline, at most 2h; 15m unless set                                                                                                                                                                              |
| `KRITIKA_RUNNER_RUNTIME_CLASS`     | the RuntimeClass of runner Jobs, e.g. `gvisor`; the cluster default unless set                                                                                                                                                   |
| `KRITIKA_RUNNER_IMAGE_PULL_POLICY` | the runner container's imagePullPolicy, `Always`, `IfNotPresent` or `Never`; the chart's `runner.image.pullPolicy` renders it                                                                                                    |
| `KRITIKA_RUNNER_RESOURCES`         | a runner pod's resources, as JSON; the chart's `runner.resources` renders it                                                                                                                                                     |
| `KRITIKA_RUNNER_TOOLS`             | command-line tools a runner pod mounts from an image for the agent's run tool, as JSON: each a `name`, a digest-pinned `image`, the `path` of its binaries and the `commands` it provides; the chart's `runner.tools` renders it |

A transcript may contain repository content the agent read, and every
member of its account can read it. A review past the diff retention keeps
its findings, summary and what it read by name and size; the dashboard's
diff and raw views say the bodies were not kept.
