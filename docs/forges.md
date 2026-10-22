# Git forges

kritika reviews pull requests on GitHub, through a GitHub App, and on a
Forgejo instance, through an authorized integration of a bot user. A
connection is one App or one integration and the accounts it serves; the
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
| `gh` for the agent                           |   ✓    |    ✗    | The run's token is the integration's, so `gh` is not offered; `curl` through the gateway and `fetch_repo` still are.                           |
| Repositories discovered from the forge       |   ✓    |    ✓    | Every repository the bot user can see under the account; a private one needs the bot to have access.                                           |
| A review of a head the pull request moved on from |   ✓    |    ✗    | Forgejo reports a pull request's merge base and diff as they stand, so a review queued before a push fails and the push's review takes over.    |
| Webhook per account                          | the App's |  one per account | Added by hand on each organization or user.                                                                                           |
| Short-lived, read-only token for runs        |   ✓    |    ✓    | With a second, read-only integration as `runnerAudience`; a run's token lasts an hour.                                                         |

A bot account on Forgejo is not marked as a bot, so a pull request opened
by one, such as a Renovate user, is treated as a person's unless its
login ends in `[bot]`.
