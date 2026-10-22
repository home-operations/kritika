# Git forges

kritika reviews pull requests on GitHub, through a GitHub App, on a
Forgejo instance, through an authorized integration of a bot user, on a
GitLab instance, through a service account, and on a Gitea instance,
through a bot user's access tokens. A connection is one App, integration,
service account or bot user and the accounts it serves; the
[configuration reference](configuration.md#apps) has every key. This page
says what each forge needs and what works on it.

## GitHub

A GitHub App, registered as the [setup guide](setup.md#register-the-github-app)
describes and declared under `apps`. The App's webhook reaches kritika at
`/hooks/<name>`; installation events keep the served repositories current,
and kritika mints a short-lived, read-only installation token for each
run.

```yaml
apps:
  github:
    accounts: [org-1]
    clientId: Iv1.example
    privateKey: { env: GITHUB_APP_PRIVATE_KEY }
    webhookSecret: { env: GITHUB_APP_WEBHOOK_SECRET }
```

## Forgejo

An [authorized integration](https://forgejo.org/docs/latest/user/api/authorized-integrations/)
of a bot user on the instance, declared under `forgejo` with the
instance's host. kritika signs a short-lived token for each call with a
private key of its own, and Forgejo verifies it against the keys kritika
publishes at `<webUrl>/forgejo/.well-known/keys`, so no long-lived
credential exists on either side. Forgejo 16 or later is needed; the
instance must reach kritika's URL over HTTPS, as it must for webhooks.

### The bot user and its integration

Create a user for kritika and give it access to the repositories it
reviews, for example through an organization team with write access. Two
things need more:

- Answering a mention checks the commenter's permission, which Forgejo
  reveals only to repository and site admins: without admin access,
  reviews still run, but no mention or command is answered.
- Dismissing a stale approval of its own needs repository admin access,
  which Forgejo asks of anyone dismissing a review.

Then, in order:

1. Generate an RSA private key, for example with
   `openssl genrsa -out forgejo.pem 4096`, and deploy kritika with the
   connection declared, its `privateKey` referenced from a Secret and no
   `audience` yet. kritika serves the key's public half at once.
2. Signed in as the bot user, open **Settings → Authorized Integrations**
   and create one of the **Generic JWT** type with the issuer
   `<webUrl>/forgejo`, the dashboard's URL plus `/forgejo`. Forgejo fetches
   kritika's discovery document and keys as it saves. Grant the capabilities
   `read:user`, `write:repository` and `write:issue`, on the repositories
   the connection's accounts hold. An optional claim rule on `sub` with the
   value `kritika` pins the tokens' subject.
3. Copy the **Audience** Forgejo shows once the integration is saved into
   the connection's `audience` and roll kritika out again.

A second integration with `read:repository` alone, its audience given as
`runnerAudience`, is what runner pods fetch with, so the pod that reads
untrusted pull request content never holds a token that can write.
Without one, runners fetch as the first integration.

To rotate the key, publish the new one first: deploy it as `previousKey`,
which kritika serves and signs nothing with, and wait for Forgejo's cache
of the key set to turn over, ten minutes by default, since Forgejo does
not refetch the set on an unknown key. Then swap the two fields, so the
new key signs and the old is still served, and drop `previousKey` once
the last token the old key signed has expired, an hour for a runner's,
and the cache has turned over again.

### Configure the connection

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

`host` is the instance's hostname alone, reached over HTTPS; runner pods
are allowed to reach it through the [egress gateway](security.md#the-egress-gateway)
as they are GitHub for an App. A name is one connection's, under `apps`
or `forgejo`, and an account is served by one connection on its forge.

### Add one webhook per account

Add a webhook to each organization, or user, in the connection's
`accounts`. Each covers every repository its owner has, including ones
added later.

- **Target URL:** `https://<kritika host>/hooks/<name>`, method POST, with
  the JSON content type.
- **Secret:** the connection's `webhookSecret`.
- **Trigger on:** custom events: Push, and under pull request events,
  Modification, Synchronized and Comments. Add Repository so a new
  repository is known before its first pull request.

The dashboard's Connections section shows whether a verified webhook has
arrived. Polling keeps pull requests current without one, but mentions
need webhooks.

## GitLab

A service account on a GitLab instance, version 19 or later, declared
under `gitlab` with the instance's host. GitLab has no short-lived
credential kritika could mint, so the account's personal access tokens are
the credential, held in a Secret.

### The service account and its tokens

Create a service account, an instance one or one of the top-level group
kritika serves, and add it to each group or user namespace in the
connection's `accounts` with the Developer role. That role is what posting
findings, resolving their threads, approving and setting a commit status
take; the access check on a mention reads the commenter's membership,
which any member can.

Then create two personal access tokens of the account:

1. One with the `api` scope, the connection's `token`, which kritika calls
   the API with.
2. One with the `read_repository` scope alone, the connection's
   `runnerToken`, which runner pods fetch with, so the pod that reads
   untrusted merge request content never holds a token that can write.
   Without one, runners fetch with the first.

GitLab sets each token's expiry, a year at most unless the instance allows
longer. Rotate a token before it expires: create its successor, update the
Secret and roll kritika out, then revoke the old one.

### Configure the connection

```yaml
gitlab:
  work:
    host: gitlab.example.com
    accounts: [platform]
    token: { env: GITLAB_TOKEN }
    runnerToken: { env: GITLAB_RUNNER_TOKEN }
    webhookSecret: { env: GITLAB_WEBHOOK_SIGNING_TOKEN }
```

`host` is the instance's hostname alone, reached over HTTPS, and runner
pods may reach it through the [egress gateway](security.md#the-egress-gateway).
An account is a top-level group, whose subgroups' projects it serves too,
or a user. A project in a subgroup is named by its full path, so a
repository entry for it is keyed `platform/backend/api`.

### Add the webhooks

A group webhook covers every project of the group and its subgroups,
including ones added later, but needs GitLab Premium or Ultimate. On the
Free tier, add the webhook to each project instead.

- **URL:** `https://<kritika host>/hooks/<name>`.
- **Signing token:** the connection's `webhookSecret`, which begins
  `whsec_`. kritika checks the signature over each delivery and refuses
  one whose timestamp is more than five minutes from its own clock. A
  webhook with only a secret token, which GitLab sends in plain text,
  shows as unsigned in the dashboard and is not acted on.
- **Trigger:** Push events, Comments and Merge request events.

The dashboard's Connections section shows whether a verified webhook has
arrived. Polling keeps merge requests current without one, and finds new
and renamed projects: kritika does not act on GitLab's project events, so
leave them off. Mentions need webhooks.

## Gitea

A bot user on a Gitea instance, version 28 or later, declared under
`gitea` with the instance's host. Gitea has nothing like Forgejo's
authorized integrations, so the user's access tokens are the credential,
held in a Secret.

### The bot user and its tokens

Create a user for kritika and give it access to the repositories it
reviews, for example through an organization team with write access. That
is what posting findings, resolving their threads, approving and setting
a commit status take. Two things need more, as on Forgejo:

- Answering a mention checks the commenter's permission, which Gitea
  reveals only to repository and site admins: without admin access,
  reviews still run, but no mention or command is answered.
- Dismissing a stale approval of its own needs repository admin access.

Then, signed in as the user, open **Settings → Applications** and
generate two access tokens with **All** repository and organization
access:

1. One with **Read** on `user` and **Read and Write** on `repository` and
   `issue`, the scopes `read:user`, `write:repository` and `write:issue`:
   the connection's `token`, which kritika calls the API with.
2. One with **Read** on `repository` alone, the connection's
   `runnerToken`, which runner pods fetch with, so the pod that reads
   untrusted pull request content never holds a token that can write.
   Without one, runners fetch with the first.

Gitea's access tokens do not expire. Rotate one by generating its
successor, updating the Secret and rolling kritika out, then deleting the
old one.

### Configure the connection

```yaml
gitea:
  home:
    host: gitea.example.com
    accounts: [org-1]
    token: { env: GITEA_TOKEN }
    runnerToken: { env: GITEA_RUNNER_TOKEN }
    webhookSecret: { env: GITEA_WEBHOOK_SECRET }
```

`host` is the instance's hostname alone, reached over HTTPS, and runner
pods may reach it through the [egress gateway](security.md#the-egress-gateway).

### Add one webhook per account

Add a Gitea webhook to each organization, or user, in the connection's
`accounts`. Each covers every repository its owner has, including ones
added later.

- **Target URL:** `https://<kritika host>/hooks/<name>`, HTTP method POST,
  with the `application/json` content type: Gitea signs the JSON, so a
  form-encoded delivery fails the check.
- **Secret:** the connection's `webhookSecret`.
- **Trigger on:** custom events: Push and Repository, and under pull
  request events, Pull Request, Pull Request Synchronized and Pull Request
  Comment. Repository makes a new or renamed repository known before its
  first pull request.

The dashboard's Connections section shows whether a verified webhook has
arrived. Polling keeps pull requests current without one, but mentions
need webhooks.

## What works where

The review itself, its findings, summary, suggested fixes, commit status,
confidence score and approval are the same on every forge. The
differences are in what the forge's API lets kritika do around them.

| Feature                                           |  GitHub   |     Forgejo     |                GitLab                 |      Gitea      | Notes                                                                                                                                                                                                                                                                                                                                                                                                                                               |
| ------------------------------------------------- | :-------: | :-------------: | :-----------------------------------: | :-------------: | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Reviews on open, push and reopen                  |     ✓     |        ✓        |                   ✓                   |        ✓        |                                                                                                                                                                                                                                                                                                                                                                                                                                                     |
| Inline findings with suggested fixes              |     ✓     |        ✓        |                   ✓                   |        ✓        | Forgejo: a range is sent as Forgejo takes one; an instance without range comments anchors the finding on the range's first line, and one that renders no suggestion fences shows the fix as a code block. GitLab: a finding on a range sits on its last line and its fix covers the range. Gitea: a comment is on one line, so a finding on a range sits on its first, and its fix shows as a code block, since Gitea renders no suggestion fences. |
| Sticky summary comment                            |     ✓     |        ✓        |                   ✓                   |        ✓        |                                                                                                                                                                                                                                                                                                                                                                                                                                                     |
| Commit status, confidence gate                    |     ✓     |        ✓        |                   ✓                   |        ✓        | GitLab: the status is a job of an external pipeline on the commit, and a review that reached no verdict reports canceled.                                                                                                                                                                                                                                                                                                                           |
| Approval and its dismissal                        |     ✓     |        ✓        |                   ✓                   |        ✓        | Forgejo and Gitea: dismissing needs the bot to be a repository admin. GitLab: the approval names the head it approves, and its message and a dismissal's are posted as comments, since a GitLab approval carries none.                                                                                                                                                                                                                              |
| Reactions on the pull request and mentions        |     ✓     |        ✓        |                   ✓                   |        ✓        |                                                                                                                                                                                                                                                                                                                                                                                                                                                     |
| Mentions and commands on the conversation         |     ✓     |        ✓        |                   ✓                   |        ✓        | Forgejo and Gitea: the bot needs admin access to check who asked.                                                                                                                                                                                                                                                                                                                                                                                   |
| Mentions and `dismiss` in a finding's thread      |     ✓     |        ✓        |                   ✓                   |        ✓        | Forgejo and Gitea: a reply in a code conversation is answered; code comments submitted with a review carry no event, so a mention there goes unanswered.                                                                                                                                                                                                                                                                                            |
| Replies in finding threads without a mention      |     ✓     |        ✗        |                   ✗                   |        ✗        | Forgejo's and Gitea's comment event does not say which comment a reply answers, and GitLab's names its discussion but not the note it answers, so a reply that does not mention the bot is not read.                                                                                                                                                                                                                                                |
| Resolving a fixed finding's thread                |     ✓     |        ✗        |                   ✓                   |        ✓        | Forgejo's API cannot resolve a conversation: kritika marks the finding fixed in its summary and leaves the conversation to be resolved by hand. Gitea's diff view shows every review's comments on a line as one conversation, in the state of the oldest, so a thread kritika resolves still shows open there when an earlier review commented on the same line.                                                                                   |
| Dismissing a finding by resolving its thread      |     ✓     |        ✗        |                   ✗                   |        ✗        | No forge but GitHub sends an event when a thread is resolved. Use `@<bot> dismiss <reason>` in the thread instead.                                                                                                                                                                                                                                                                                                                                  |
| Outdated thread detection                         |     ✓     |        ✗        |                   ✓                   |        ✗        | Forgejo and Gitea do not say whether a code comment's lines have changed since.                                                                                                                                                                                                                                                                                                                                                                     |
| 👍 and 👎 counts on findings in the dashboard      |     ✓     |        ✗        |                   ✗                   |        ✗        | Forgejo lists no reactions with a code comment; GitLab and Gitea list them per comment, a request each, which kritika does not make.                                                                                                                                                                                                                                                                                                                |
| `gh` or `glab` for the agent                      |     ✓     |        ✗        |                   ✗                   |        ✗        | `gh` speaks to GitHub alone, and GitLab's run token can only fetch, so no other forge gets a CLI; `curl` through the gateway and `fetch_repo` still are.                                                                                                                                                                                                                                                                                            |
| Repositories discovered from the forge            |     ✓     |        ✓        |                   ✓                   |        ✓        | Every repository the bot can see under the account; a private one needs the bot to have access. GitLab: a group's subgroups' projects too.                                                                                                                                                                                                                                                                                                          |
| New and renamed repositories by webhook           |     ✓     |        ✓        |                   ✗                   |        ✓        | kritika does not act on GitLab's project events, so polling finds them.                                                                                                                                                                                                                                                                                                                                                                             |
| A review of a head the pull request moved on from |     ✓     |        ✗        |                   ✓                   |        ✗        | Forgejo and Gitea report a pull request's merge base and diff as they stand, so a review queued before a push fails and the push's review takes over. GitLab keeps the diff of every push.                                                                                                                                                                                                                                                          |
| Webhook per account                               | the App's | one per account | one per group, or per project on Free | one per account | Added by hand.                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| Short-lived, read-only token for runs             |     ✓     |        ✓        |                   ✗                   |        ✗        | Forgejo: with a second, read-only integration as `runnerAudience`; a run's token lasts an hour. GitLab and Gitea: `runnerToken` is read-only, but a personal access token lasts until it expires or is deleted.                                                                                                                                                                                                                                     |

A bot account on Forgejo is not marked as a bot, so a pull request opened
by one, such as a Renovate user, is treated as a person's unless its
login ends in `[bot]`. GitLab marks its bot and service accounts, which
kritika reads from the API; a webhook does not, so there it goes by the
`[bot]` suffix and the names GitLab gives access token users. Gitea marks
a bot user as GitHub does, in the API and webhooks alike.
