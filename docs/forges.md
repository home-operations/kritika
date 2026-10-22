# Git forges

kritika reviews pull requests on GitHub, through a GitHub App, and on a
Forgejo instance, through a bot account. A connection is one App or one
bot and the accounts it serves; the [configuration reference](configuration.md#apps)
has every key. This page says what each forge needs and what works on it.

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

A bot user on the Forgejo instance, declared under `forgejo` with the
instance's host. kritika posts reviews, comments and statuses as this
user.

### The bot account

Create a user for kritika and give it access to the repositories it
reviews, for example through an organization team with write access. Two
things need more:

- Answering a mention checks the commenter's permission, which Forgejo
  reveals only to repository and site admins: without admin access,
  reviews still run, but no mention or command is answered.
- Dismissing a stale approval of its own needs repository admin access,
  which Forgejo asks of anyone dismissing a review.

Create an access token for the bot with these scopes:

- `read:user`, to learn its own login.
- `write:repository`, for pull requests, reviews, statuses, files and the
  listing of each account's repositories.
- `write:issue`, for conversation comments and reactions.

No organization scope is needed: repositories are listed through the
account's user listing, which covers organizations.

A second token with `read:repository` alone, given as `gitToken`, is
what runner pods fetch with, so the token that can write never enters the
pod that reads untrusted pull request content. Without one, runners fetch
with the bot's token.

### Configure the connection

```yaml
forgejo:
  codeberg:
    host: codeberg.org
    accounts: [org-1]
    token: { env: FORGEJO_TOKEN }
    gitToken: { env: FORGEJO_READ_TOKEN }
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

## What works where

The review itself, its findings, summary, suggested fixes, commit status,
confidence score and approval are the same on both forges. The
differences are in what the forge's API lets kritika do around them.

| Feature                                      | GitHub | Forgejo | On Forgejo                                                                                                                                     |
| -------------------------------------------- | :----: | :-----: | ---------------------------------------------------------------------------------------------------------------------------------------------- |
| Reviews on open, push and reopen             |   ✓    |    ✓    |                                                                                                                                                |
| Inline findings with suggested fixes         |   ✓    |    ✓    | A range is sent as Forgejo takes one. An instance without range comments anchors the finding on the range's first line; one that renders suggestion fences applies the fix one-click, older ones show it as a code block. |
| Sticky summary comment                       |   ✓    |    ✓    |                                                                                                                                                |
| Commit status, confidence gate               |   ✓    |    ✓    |                                                                                                                                                |
| Approval and its dismissal                   |   ✓    |    ✓    | Dismissing needs the bot to be a repository admin.                                                                                             |
| Reactions on the pull request and mentions   |   ✓    |    ✓    |                                                                                                                                                |
| Mentions and commands on the conversation    |   ✓    |    ✓    | The bot needs admin access to check who asked.                                                                                                 |
| Mentions and `dismiss` in a finding's thread |   ✓    |    ✓    | A reply in a code conversation is answered; code comments submitted with a review carry no event, so a mention there goes unanswered.         |
| Replies in finding threads without a mention |   ✓    |    ✗    | Forgejo delivers a code reply as a plain comment, which kritika cannot tell from one on the conversation without asking the forge, so a reply that does not mention the bot is not read. |
| Resolving a fixed finding's thread           |   ✓    |    ✗    | Forgejo's API cannot resolve a conversation. kritika marks the finding fixed in its summary and leaves the conversation to be resolved by hand. |
| Dismissing a finding by resolving its thread |   ✓    |    ✗    | Forgejo sends no event when a conversation is resolved. Use `@<bot> dismiss <reason>` in the thread instead.                                   |
| Outdated thread detection                    |   ✓    |    ✗    | Forgejo does not say whether a code comment's lines have changed since.                                                                        |
| 👍 and 👎 counts on findings in the dashboard |   ✓    |    ✗    | Forgejo lists no reactions with a code comment.                                                                                                |
| `gh` for the agent                           |   ✓    |    ✗    | The run's token is the bot's, so `gh` is not offered; `curl` through the gateway and `fetch_repo` still are.                                  |
| Repositories discovered from the forge       |   ✓    |    ✓    | Every repository the bot can see under the account; a private one needs the bot to have access.                                                |
| A review of a head the pull request moved on from |   ✓    |    ✗    | Forgejo reports a pull request's merge base and diff as they stand, so a review queued before a push fails and the push's review takes over.    |
| Webhook per account                          | the App's |  one per account | Added by hand on each organization or user.                                                                                           |
| Short-lived, read-only token for runs        |   ✓    |    ✗    | Give the bot a read-only `gitToken` instead; it is static.                                                                                      |

A bot account on Forgejo is not marked as a bot, so a pull request opened
by one, such as a Renovate user, is treated as a person's unless its
login ends in `[bot]`.
