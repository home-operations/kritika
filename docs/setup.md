# Setup

kritika reviews pull requests on github.com through a GitHub App. Events
reach kritika through the App's own webhook, which covers every repository
the App is installed on. No repository needs a file: a
[`.kritika.yaml`](repository-config.md) is optional.

This guide takes a new instance from nothing to its first review:

1. [Register the GitHub App](#register-the-github-app).
2. [Deploy kritika](#deploy) with the App in its configuration file.
3. [Install the App](#install-the-app) on the accounts it serves.
4. [Sign in](#sign-in) and [check that it works](#check-that-it-works).

## Register the GitHub App

An entry of `apps` is one GitHub App that kritika serves accounts through.
It serves the users and organizations its `accounts` lists, and each of
them is a kritika account, `github/<name>`, that this App alone serves.

Register the App under the account whose repositories kritika reviews (a
personal account's or an organization's Developer settings).

**Webhook:** Active, with the URL `<config.webUrl>/hooks/<app name>` and a
random secret. This one webhook receives the events of every repository
the App is installed on.

**Permissions:**

| Permission             | Access                       | What kritika uses it for                                                                            |
| ---------------------- | ---------------------------- | --------------------------------------------------------------------------------------------------- |
| Contents               | read-only, or read and write | branches, files, comparisons and fetching the code; with write, resolving its finding threads       |
| Metadata               | read-only                    | required by GitHub                                                                                  |
| Pull requests          | read and write               | reviews, inline comments and replies, and conversation comments                                     |
| Issues                 | read-only, or read and write | conversation comments, which GitHub delivers as issue comments; with write, reactions               |
| Commit statuses        | read and write               | the `Kritika / Review` status                                                                       |
| Members (organization) | read-only                    | only for signing in with GitHub through this App, whose role mapping reads a person's organizations |

- **Contents: read and write** has one use: GitHub lets an App resolve a
  review thread only with write access to the contents. With read-only,
  kritika leaves its finding threads open, both those a later review finds
  fixed and those `dismiss` closes, for a person to resolve; the summary
  and the dashboard record them either way. Write also lets the App push
  to the repository, which kritika never does.
- **Issues: read and write** has one use: the reactions kritika leaves, a
  👀 while it works and a 👍 once it has answered. GitHub documents a
  reaction on a pull request itself, or on a conversation comment, as
  needing write access to issues. With read-only, those may go without
  them; a mention in an inline thread gets them either way, under the pull
  requests permission.
- A permission changed on an App already installed takes effect once each
  account it is installed on approves it. kritika picks it up within the
  hour, or at once when restarted.

**Events:**

| Event                       | What kritika uses it for                                                |
| --------------------------- | ----------------------------------------------------------------------- |
| Pull request                | reviews when a pull request opens, is pushed to or is labelled; closing |
| Pull request review comment | mentions in inline threads                                              |
| Pull request review thread  | a resolved finding thread dismisses its finding                         |
| Issue comment               | mentions in the conversation                                            |
| Push                        | keeping the index of the default branch current                         |
| Repository                  | a repository created, archived or unarchived                            |

Installation events arrive without subscribing.

**Where it can be installed:** only on this account, unless it should
serve several. A public App can be installed on many organizations: list
each one kritika should review in the App's `accounts`. A delivery for any
account not listed is accepted and ignored, so nobody else who installs
the App gets reviews.

Then generate a private key and note the App's client ID. Comments mention
the bot as `@<app slug>` ([commands](reviews.md#commands)). To sign in with
GitHub through the same App, also generate a client secret on its settings
page.

## Deploy

Install the chart as its
[README](https://github.com/home-operations/kritika/blob/main/charts/kritika/README.md)
shows, with:

- **A database:** Postgres with VectorChord and kritika's three roles,
  under `database` ([Postgres with CloudNativePG](database.md)).
- **`config.webUrl`**, the one public URL. The dashboard is served at it,
  and GitHub delivers each App's webhook under it, to `/hooks/<app name>`,
  both from one port. The chart's `ingress` or `httpRoute` routes the URL
  there; nothing else needs to be public.
- **A way to sign in:** `config.authAdminPassword`, from an existing
  Secret, for the local admin, or OIDC or GitHub with a role mapping that
  makes someone an admin ([`auth`](configuration.md#auth)). For GitHub
  through the App, set its client ID and secret as the
  `KRITIKA_AUTH_GITHUB_*` variables.
- **The [configuration file](configuration.md)** as `configFile`, or an
  existing ConfigMap. Its secrets name variables, which `env` or `envFrom`
  set from existing Secrets: put the App's private key and webhook secret
  in a Secret and set a variable from each.

A minimal file declares the GitHub App, a model key and the default review
model:

```yaml
apps:
  github:
    accounts: [org-1]
    clientId: Iv1.example
    privateKey: { env: GITHUB_APP_PRIVATE_KEY }
    webhookSecret: { env: GITHUB_APP_WEBHOOK_SECRET }
providers:
  openrouter:
    type: openrouter
    apiKey: { env: OPENROUTER_API_KEY }
review:
  model: openrouter/vendor/large-model
```

Add `embedding` to index each repository for similar code, `repositories`
entries for an account's or a repository's own settings and rules, and
`accounts` for an account's limits ([configuration](configuration.md)). The
App can come from the environment instead ([`apps`](configuration.md#apps)).

The file lives in git with the rest of the deployment. kritika reads it at
startup, and the chart rolls the pods when `configFile` changes. A changed
ConfigMap or Secret does not roll them: restart them after changing one,
or have stakater's Reloader do it with
`deploymentAnnotations: { reloader.stakater.com/auto: "true" }`. Runner
Jobs read their Secret as each one starts.

## Install the App

Install the App on each account in `accounts`, for all repositories or
selected ones. Each pull request in them is then reviewed when it opens
and after each push, under its settings in the configuration file, in
every repository that runs
([which repositories run](configuration.md#which-repositories-run)).

Anyone can install a public App by its slug. The Configuration page's
GitHub Apps panel lists every account each App is installed on, marks
those it does not serve, and uninstalls the App from any of them. kritika
reviews nothing on an account the App's entry does not list, whether or
not the App is installed there.

## Sign in

Sign in as an admin. Until the instance can review, a banner says what is
missing and leads to the Configuration page, a tab of the instance, whose
Setup checklist names each step and what to set for it
([first run](dashboard.md#first-run)).

## Check that it works

GitHub keeps the App webhook's recent deliveries with kritika's response:

| Response | Meaning                                       |
| -------- | --------------------------------------------- |
| 204      | a ping                                        |
| 202      | accepted                                      |
| 400, 413 | a delivery kritika cannot parse, or too large |
| 401      | the secrets differ, or the App has none       |
| 404      | the path names no App                         |
| 500      | kritika could not queue the delivery          |

The account overview's Connection panel shows when its App last had a
delivery, explains where the webhook goes while none has, and says to set
the App's webhook secret when its deliveries arrive from GitHub with no
signature, which the Configuration page's GitHub Apps panel marks
`unsigned`. `kritika_webhooks_total{connection,outcome}` counts deliveries
by outcome.

## Without webhooks

When the forge cannot reach the listener, polling alone still reviews:
every `KRITIKA_POLL_INTERVAL` (10 minutes unless set, `0s` turns it off),
the leader lists the open pull requests and reviews those updated since
the last poll. It is a backstop, not a substitute:

- A review waits for the next poll.
- No mention is answered, since only a webhook delivers one.
- A pull request kritika holds open that the forge no longer lists is
  asked for by number and recorded closed, so a `closed` event that was
  never delivered does not leave it open for good; at most 50 are asked
  for per repository and poll.
- The index catches up with the default branch at the next poll, not on
  each push: while no webhook has reached an App within
  `KRITIKA_POLL_LOOKBACK`, each poll also checks its indexed repositories'
  default branches.
- Each poll costs the App a listing of the repositories it reaches, about
  one request per repository polled, one more per hundred open pull
  requests, and a read of the reactions on up to 30 recently reviewed pull
  requests per account. So the interval bounds how many repositories a
  connection can poll within GitHub's rate limit; a poll that outlasts its
  interval is cut there, and the next covers the rest.
