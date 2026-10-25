---
title: Git forges
---

kritika reviews pull requests on these forges:

- GitHub, through a GitHub App;
- a Forgejo instance, through an authorized integration of a bot user;
- a GitLab instance, through a service account;
- a Gitea instance, through a bot user's access tokens.

A connection is one App, integration, service account or bot user,
together with the accounts it serves. The
[configuration reference](/configuration/#apps) has every key. This page
says what each forge needs and what works on it.

## GitHub

A GitHub connection is a GitHub App, registered as the
[setup guide](/setup/#register-the-github-app) describes and declared
under `apps`. The App's webhook reaches kritika at `/hooks/<name>`.
Installation events keep the served repositories current, and kritika
mints a short-lived, read-only installation token for each run.

```yaml
apps:
  github:
    accounts: [org-1]
    clientId: Iv1.example
    privateKey: { env: GITHUB_APP_PRIVATE_KEY }
    webhookSecret: { env: GITHUB_APP_WEBHOOK_SECRET }
```

## Forgejo

A Forgejo connection is an
[authorized integration](https://forgejo.org/docs/latest/user/api/authorized-integrations/)
of a bot user on the instance, declared under `forgejo` with the
instance's host. For each call, kritika signs a short-lived token with a
private key of its own. Forgejo verifies the token against the keys
kritika publishes at `<webUrl>/forgejo/.well-known/keys`, so no long-lived
credential exists on either side. Forgejo 16 or later is needed. The
instance must reach kritika's URL over HTTPS, as it must for webhooks.

### The bot user and its integration

Create a user for kritika and give it access to the repositories it
reviews, for example through an organization team with write access. Two
things need more:

- To answer a mention, kritika checks the commenter's permission, which
  Forgejo reveals only to repository and site admins. Without admin
  access, reviews still run, but no mention or command is answered.
- Dismissing its own stale approval needs repository admin access, which
  Forgejo asks of anyone dismissing a review.

Then, in order:

1. Generate an RSA private key, for example with
   `openssl genrsa -out forgejo.pem 4096`. Deploy kritika with the
   connection declared, its `privateKey` referenced from a Secret, and no
   `audience` yet. kritika serves the key's public half at once.
2. Signed in as the bot user, open **Settings → Authorized Integrations**
   and create an integration of the **Generic JWT** type. Set its issuer
   to `<webUrl>/forgejo`, which is the dashboard's URL plus `/forgejo`.
   Forgejo fetches kritika's discovery document and keys as it saves.
   Grant the capabilities `read:user`, `write:repository` and
   `write:issue` on the repositories that the connection's accounts hold.
   An optional claim rule on `sub` with the value `kritika` pins the
   tokens' subject.
3. Once the integration is saved, Forgejo shows its **Audience**. Copy it
   into the connection's `audience` and roll kritika out again.

A second integration, with `read:repository` alone and its audience given
as `runnerAudience`, is what runner pods fetch with. The pod that reads
untrusted pull request content then never holds a token that can write.
Without one, runners fetch as the first integration.

To rotate the key, publish the new one first:

1. Deploy the new key as `previousKey`. kritika serves that key but signs
   nothing with it.
2. Wait for Forgejo's cache of the key set to turn over, ten minutes by
   default. Forgejo does not refetch the set on an unknown key.
3. Swap the two fields, so the new key signs and the old one is still
   served.
4. Drop `previousKey` once the last token the old key signed has expired
   and the cache has turned over again. A runner's token lasts an hour.

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
    bots: [renovate-bot]
```

`host` is the instance's hostname alone, reached over HTTPS. Runner pods
are allowed to reach it through the
[egress gateway](/security/#the-egress-gateway), as they are allowed to
reach GitHub for an App. A name belongs to one connection only, under
`apps`, `forgejo`, `gitlab` or `gitea`. An account is served by one
connection on its forge.

`bots` lists the logins on the instance that are bots, such as a Renovate
user. Forgejo marks no account as a bot and allows no `[bot]` in a login.
Without the list, kritika treats every pull request and comment as a
person's:

- the summary of a dependency bump gets the praise that a bot's summary
  leaves out;
- a bot's unchanged patch is reviewed again;
- a bot's mention is answered.

A pull request recorded before its author was listed is marked as a bot's
the next time kritika records it, on a push or an edit. Closing or merging
it does not mark it.

### Add one webhook per account

Add a webhook to each organization, or user, in the connection's
`accounts`. Each covers every repository its owner has, including ones
added later.

- **Target URL:** `https://<kritika host>/hooks/<name>`, method POST, with
  the JSON content type.
- **Secret:** the connection's `webhookSecret`.
- **Trigger on:** custom events. Choose Push, and under pull request
  events, choose Modification, Synchronized and Comments. Add Repository so
  a new repository is known before its first pull request.

The dashboard's Connections section shows whether a verified webhook has
arrived. Polling keeps pull requests current without one, but mentions
need webhooks.

### Sign in with Forgejo

People can sign in to the dashboard with their Forgejo account, through an
OAuth2 application on the instance. Under **Settings → Applications →
Manage OAuth2 applications** of a user or of the site, create one as a
confidential client with the redirect URI
`<webUrl>/auth/callback/forgejo`. Then declare it under `auth`:

```yaml
auth:
  forgejo:
    host: codeberg.org
    clientId: 0a1b2c3d-…
    clientSecret: { env: FORGEJO_CLIENT_SECRET }
```

kritika asks for the `read:user` and `read:organization` scopes alone.
Among the accounts that the instance's connections serve, a person reads
their own and any organization they are a member of. A
[role mapping](/configuration/#role-mappings) can grant more.

## GitLab

A GitLab connection is a service account on a GitLab instance, version 19
or later, declared under `gitlab` with the instance's host. GitLab has no
short-lived credential that kritika could mint, so the account's personal
access tokens are the credential, held in a Secret.

### The service account and its tokens

Create a service account, either an instance one or one of the top-level
group kritika serves. Add it with the Developer role to each group or user
namespace in the connection's `accounts`. That role is what kritika needs
to post findings, resolve their threads, approve and set a commit status.
The access check on a mention reads the commenter's membership, which any
member can read.

Then create two personal access tokens of the account:

1. One with the `api` scope, the connection's `token`, which kritika calls
   the API with.
2. One with the `read_repository` scope alone, the connection's
   `runnerToken`, which runner pods fetch with. The pod that reads
   untrusted merge request content then never holds a token that can
   write. Without one, runners fetch with the first.

GitLab sets each token's expiry: a year at most, unless the instance
allows longer. Rotate a token before it expires: create its successor,
update the Secret and roll kritika out, then revoke the old one.

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

`host` is the instance's hostname alone, reached over HTTPS. Runner pods
may reach it through the [egress gateway](/security/#the-egress-gateway).
An account is a top-level group or a user. For a group, kritika serves the
projects of its subgroups too. A project in a subgroup is named by its
full path, such as `platform/backend/api`.

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
arrived. Polling keeps merge requests current without one, and it finds
new and renamed projects. kritika does not act on GitLab's project events,
so leave them off. Mentions need webhooks.

### Sign in with GitLab

People can sign in to the dashboard with their GitLab account, through an
OAuth application on the instance. The application can be:

- a user's, under **Edit profile → Access → Applications**;
- a group's, under its **Settings → Applications**;
- the instance's, under **Admin → Applications**.

Give it the redirect URI `<webUrl>/auth/callback/gitlab` and the `openid`
and `email` scopes alone. Declare it under `auth` with its
**Application ID** and **Secret**:

```yaml
auth:
  gitlab:
    host: gitlab.example.com
    clientId: 5f0c…
    clientSecret: { env: GITLAB_CLIENT_SECRET }
```

kritika reads who the person is, their primary email and their groups from
the instance's OpenID Connect userinfo endpoint, so the token it holds can
read nothing through the API. Among the accounts that the instance's
connections serve, a person reads their own and any top-level group they
are a member of, directly or through an ancestor group. A
[role mapping](/configuration/#role-mappings) can grant more.

## Gitea

A Gitea connection is a bot user on a Gitea instance, version 28 or later,
declared under `gitea` with the instance's host. Gitea has nothing like
Forgejo's authorized integrations, so the user's access tokens are the
credential, held in a Secret.

### The bot user and its tokens

Create a user for kritika and give it access to the repositories it
reviews, for example through an organization team with write access. That
access is what kritika needs to post findings, resolve their threads,
approve and set a commit status. Two things need more, as on Forgejo:

- To answer a mention, kritika checks the commenter's permission, which
  Gitea reveals only to repository and site admins. Without admin access,
  reviews still run, but no mention or command is answered.
- Dismissing its own stale approval needs repository admin access.

Then, signed in as the user, open **Settings → Applications** and generate
two access tokens with **All** repository and organization access:

1. One with **Read** on `user` and **Read and Write** on `repository` and
   `issue`, which are the scopes `read:user`, `write:repository` and
   `write:issue`. This is the connection's `token`, which kritika calls the
   API with.
2. One with **Read** on `repository` alone, the connection's
   `runnerToken`, which runner pods fetch with. The pod that reads
   untrusted pull request content then never holds a token that can write.
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
    bots: [renovate]
```

`host` is the instance's hostname alone, reached over HTTPS. Runner pods
may reach it through the [egress gateway](/security/#the-egress-gateway).
Gitea marks a bot user as a bot. `bots` lists any other login on the
instance that a bot runs as, such as an ordinary account that Renovate
uses. kritika then treats that login as a bot, as on
[Forgejo](#configure-the-connection).

### Add one webhook per account

Add a Gitea webhook to each organization, or user, in the connection's
`accounts`. Each covers every repository its owner has, including ones
added later.

- **Target URL:** `https://<kritika host>/hooks/<name>`, HTTP method POST,
  with the `application/json` content type. Gitea signs the JSON, so a
  form-encoded delivery fails the check.
- **Secret:** the connection's `webhookSecret`.
- **Trigger on:** custom events. Choose Push and Repository, and under
  pull request events, choose Pull Request, Pull Request Synchronized and
  Pull Request Comment. Repository makes a new or renamed repository known
  before its first pull request.

The dashboard's Connections section shows whether a verified webhook has
arrived. Polling keeps pull requests current without one, but mentions
need webhooks.

### Sign in with Gitea

People can sign in to the dashboard with their Gitea account, as on
Forgejo. Under **Settings → Applications → Manage OAuth2 Applications** of
a user or of the site, create an application as a confidential client with
the redirect URI `<webUrl>/auth/callback/gitea`. Declare it as
`auth.gitea` with the instance's `host`, `clientId` and `clientSecret`.
kritika asks for the `read:user` and `read:organization` scopes alone, and
places people as a [Forgejo sign-in](#sign-in-with-forgejo) does.

## What works where

The review itself is the same on every forge: its findings, summary,
suggested fixes, commit status, confidence score and approval. The
differences are in what the forge's API lets kritika do around them.

| Feature                                           |  GitHub   |     Forgejo     |                GitLab                 |      Gitea      | Notes                                                                                                                                                                                                                                       |
| ------------------------------------------------- | :-------: | :-------------: | :-----------------------------------: | :-------------: | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Reviews on open, push and reopen                  |     ✓     |        ✓        |                   ✓                   |        ✓        |                                                                                                                                                                                                                                             |
| Inline findings with suggested fixes              |     ✓     |        ✓        |                   ✓                   |        ✓        | Where a finding on a range sits, and how its fix shows, differ by forge; see below.                                                                                                                                                         |
| Sticky summary comment                            |     ✓     |        ✓        |                   ✓                   |        ✓        |                                                                                                                                                                                                                                             |
| Commit status, confidence gate                    |     ✓     |        ✓        |                   ✓                   |        ✓        | GitLab: the status is a job of an external pipeline on the commit, and a review that reached no verdict reports canceled.                                                                                                                   |
| Approval and its dismissal                        |     ✓     |        ✓        |                   ✓                   |        ✓        | Forgejo and Gitea: dismissing needs the bot to be a repository admin. GitLab: the approval names the head it approves. Its message and a dismissal's are posted as comments, since a GitLab approval carries none.                          |
| Reactions on the pull request and mentions        |     ✓     |        ✓        |                   ✓                   |        ✓        |                                                                                                                                                                                                                                             |
| Mentions and commands on the conversation         |     ✓     |        ✓        |                   ✓                   |        ✓        | Forgejo and Gitea: the bot needs admin access to check who asked.                                                                                                                                                                           |
| Mentions and `dismiss` in a finding's thread      |     ✓     |        ✓        |                   ✓                   |        ✓        | Forgejo and Gitea: a reply in a code conversation is answered. Code comments submitted with a review carry no event, so a mention there goes unanswered.                                                                                    |
| Replies in finding threads without a mention      |     ✓     |        ✗        |                   ✗                   |        ✗        | Forgejo's and Gitea's comment event does not say which comment a reply answers. GitLab's names its discussion but not the note it answers. So a reply that does not mention the bot is not read.                                            |
| Resolving a fixed finding's thread                |     ✓     |        ✗        |                   ✓                   |        ✓        | Forgejo leaves the conversation to be resolved by hand, and Gitea can still show it open; see below.                                                                                                                                        |
| Dismissing a finding by resolving its thread      |     ✓     |        ✗        |                   ✗                   |        ✗        | No forge but GitHub sends an event when a thread is resolved. Use `@<bot> dismiss <reason>` in the thread instead.                                                                                                                          |
| Outdated thread detection                         |     ✓     |        ✗        |                   ✓                   |        ✗        | Forgejo and Gitea do not say whether a code comment's lines have changed since.                                                                                                                                                             |
| 👍 and 👎 counts on findings in the dashboard     |     ✓     |        ✗        |                   ✗                   |        ✗        | Forgejo lists no reactions with a code comment; GitLab and Gitea list them per comment, a request each, which kritika does not make.                                                                                                        |
| `gh` or `glab` for the agent                      |     ✓     |        ✗        |                   ✗                   |        ✗        | `gh` speaks to GitHub alone, and GitLab's run token can only fetch, so no other forge gets a CLI. The agent still has `curl` through the gateway and `fetch_repo`.                                                                          |
| Repositories discovered from the forge            |     ✓     |        ✓        |                   ✓                   |        ✓        | Every repository the bot can see under the account; a private one needs the bot to have access. GitLab: the projects of a group's subgroups too.                                                                                            |
| New and renamed repositories by webhook           |     ✓     |        ✓        |                   ✗                   |        ✓        | kritika does not act on GitLab's project events, so polling finds them.                                                                                                                                                                     |
| A review of a head the pull request moved on from |     ✓     |        ✗        |                   ✓                   |        ✗        | Forgejo and Gitea report a pull request's merge base and diff as they stand. A review queued before a push fails, and the push's review takes over. GitLab keeps the diff of every push.                                                    |
| Webhook per account                               | the App's | one per account | one per group, or per project on Free | one per account | Added by hand.                                                                                                                                                                                                                              |
| Short-lived, read-only token for runs             |     ✓     |        ✓        |                   ✗                   |        ✗        | Forgejo: with a second, read-only integration as `runnerAudience`; a run's token lasts an hour. GitLab and Gitea: `runnerToken` is read-only, but a GitLab token lasts until it expires or is revoked, and a Gitea one until it is deleted. |
| Dashboard sign-in                                 |     ✓     |        ✓        |                   ✓                   |        ✓        | Through `auth.github`, `auth.forgejo`, `auth.gitea` and `auth.gitlab`; a sign-in on an instance places people on that instance's accounts alone.                                                                                            |

For inline findings with suggested fixes:

- Forgejo: a range is sent as Forgejo takes one. An instance without range
  comments anchors the finding on the range's first line. An instance that
  renders no suggestion fences shows the fix as a code block.
- GitLab: a finding on a range sits on its last line, and its fix covers
  the range.
- Gitea: a comment is on one line, so a finding on a range sits on its
  first line. Its fix shows as a code block, since Gitea renders no
  suggestion fences.

For resolving a fixed finding's thread:

- Forgejo's API cannot resolve a conversation. kritika marks the finding
  fixed in its summary and leaves the conversation to be resolved by hand.
- Gitea's diff view shows every review's comments on a line as one
  conversation, in the state of the oldest. So a thread kritika resolves
  still shows open there when an earlier review commented on the same
  line.

A bot account on Forgejo is not marked as a bot. A pull request opened by
one, such as a Renovate user, is treated as a person's unless the
connection lists its login under `bots`. GitLab marks its bot and service
accounts, and kritika reads the mark from the API. A GitLab webhook does
not mark them, so there kritika goes by the `[bot]` suffix and the names
GitLab gives access token users. Gitea marks a bot user as GitHub does, in
the API and webhooks alike. A Gitea connection's `bots` adds any other
account that a bot runs as.

## Accounts a connection does not serve

Anyone can install a public GitHub App. GitHub then sends kritika that
installation's events, signed with the App's own secret. kritika ignores
them and does not list, poll or review anything in that account. The
installation stays until someone removes it. The dashboard's Connections
section lists it as not served, with an Uninstall action.

A private App, which only the account that owns it can install, avoids
this. The [setup guide](/setup/#register-the-github-app) says when to
choose one. An App is declared by one `apps` entry. It has one webhook, so
a second entry's accounts would be strangers on the first entry's hook
path.

On Forgejo, GitLab and Gitea there is nothing to install. Anyone can add
kritika's bot user or service account to their own repositories. kritika
still lists and polls only the accounts a connection serves, and it
refuses a webhook that is not signed with the connection's
`webhookSecret`. Being added does give kritika's credential access to
their repositories. On Forgejo, grant the integration's capabilities only
on the repositories that the connection's accounts hold, so its tokens
reach no others. Gitea's tokens and GitLab's reach whatever the bot is
added to.
