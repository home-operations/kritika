---
title: Models and providers
---

You need a provider credential and a `review.model` to start reviewing.
A model name has the form `<provider>/<model>`: the first part matches a key
under `providers`, and the rest is the provider's model ID.

```yaml
providers:
  openrouter:
    type: openrouter
    apiKey: { env: OPENROUTER_API_KEY }
review:
  model: openrouter/vendor/large-model
```

Replace `vendor/large-model` with the model you want to use. In Helm, put this
configuration under `config`. Supply the key from a Secret as shown in [Setup](/setup/#deploy).

Everything else is optional:

| I want to…                                  | Setting and details                                                           |
| ------------------------------------------- | ----------------------------------------------------------------------------- |
| Try another model when a call fails         | [`review.fallback`](#fallback)                                                |
| Retry temporary failures                    | Provider [`retries`](#retries)                                                |
| Adjust reasoning effort                     | [`review.effort`](#effort)                                                    |
| Use a stronger model for some pull requests | [Agents that replace the default pass](#agents-that-replace-the-default-pass) |
| Score reviews or approve pull requests      | [`confidence.model`](/confidence/)                                            |
| Search for similar code                     | [`embedding.model`](#the-embedder)                                            |
| Use a different model for one repository    | [Repository settings](/repository-config/#models)                             |

## Providers

| Key           | What                                                                                                                                          |
| ------------- | --------------------------------------------------------------------------------------------------------------------------------------------- |
| `type`        | `openrouter`, `openai`, `anthropic`, `opencode` or `chatgpt`                                                                                  |
| `apiKey`      | its key, required except for `chatgpt`, which uses a stored sign-in                                                                           |
| `baseUrl`     | its API's URL, the type's default unless set                                                                                                  |
| `pricing`     | the prices of a provider that reports no cost, keyed by model id or [floating alias](#floating-model-aliases) ([local models](#local-models)) |
| `retries`     | how many more times a failed model step is tried, from 0 to 5, and 0 unless set ([retries](#retries))                                         |
| `concurrency` | how many calls run at once on each of its models, per account, and 2 unless set ([concurrency](#concurrency))                                 |
| `timeout`     | how long one request to it can take, up to 30m, and 5m unless set ([timeouts](#timeouts))                                                     |
| `headers`     | headers sent with every request, keyed by name. Each value is inline or a reference like `apiKey` ([headers](#headers))                       |

Provider credentials never enter a runner pod: the agent reaches its model
through kritika's gateway ([the model endpoint](/security/#the-model-endpoint)).

### Concurrency

`concurrency` is how many calls can run at once on each model of the
provider, for each account. A review holds one of the review model's slots
from the time it is admitted to the time it ends, and one on the model of
each [agent](#agents-that-replace-the-default-pass) it runs. A follow-up,
the scorer's call and a split review's summary call hold one of their
model's for the call. A review that finds every slot held waits on the
queue for one. A slot that comes free wakes the oldest waiting review of a
pull request that is ready. The review of a draft takes a slot only when
the model has more slots free than reviews of ready pull requests that
wait for one, and each of those reviews has started. A review
that is split into parts ([large reviews](/configuration/#the-agent)) also
takes the slots that no other review holds. While another review waits for
its first slot, it gives those extra slots back as its parts end, and it
keeps its own slot until it ends. A review also takes one of the replica's
`reviewWorkers` ([how kritika runs](/configuration/#how-kritika-runs)) for
as long as it runs, so a review cannot take a free slot while every one of
them is busy.
Index runs hold all but one of the embedder's slots while
they embed, so a review's similar-code lookup has a slot unless
`concurrency` is 1. `concurrency` is one value for the provider; an
account that needs another on the same endpoint declares a provider of
its own under [`accounts`](/configuration/#accounts).

A [fallback](#fallback) on another provider has slots of its own: a step
that goes to it takes one of the fallback model's for the step, and waits
for one within the step's own time. So a self-hosted fallback with less
capacity than the review model sets its provider's `concurrency` to what
it serves at once, and an outage of the review model's provider moves at
most that many steps onto it, while the review model keeps its own
parallelism. The slots that reviews hold count: when an agent of a review
runs on the fallback model, each running review holds one of its slots for
the whole review, and a fallback step waits for a slot beyond all of them.
So such a provider needs a `concurrency` above what its agents hold at
once, or fallback steps on it time out while they are all held.

### Cost accounting

Review, follow-up and confidence calls use the provider's reported cost
when there is one, and otherwise the model's configured `pricing`. A
reported cost is either a number or the `total_cost` of an object, which
is how Bifrost passes on a provider's cost. Any other shape counts as no
reported cost.

Without a reported cost or a configured price, kritika warns on the first
unpriced call for each account, provider and selected model in each
process. The warning names the requested model reference and the
selected ID. This also covers fallbacks, repository overrides and new IDs
selected by floating aliases.
The APIs of `anthropic` and `openai` providers report no cost. At startup,
kritika also warns for each model that the configuration file sets on such
a provider. It warns when `pricing` has no price under the model as written.

The dashboard labels these calls **Unpriced** and marks totals that
contain them as incomplete. The summary footer does the same when
`review.cost` is on, and `kritika_model_unpriced_calls_total` counts them
([metrics](/metrics/)). Per-review averages include only the reviews
whose cost is known. An explicit zero price or an upstream-reported zero
remains $0, and ChatGPT calls remain included in the plan. Existing
history is not reclassified. Tokens count against an account's `limits`
either way.

### An account's own keys

An account's entry under [`accounts`](/configuration/#accounts) can
declare providers of its own:

```yaml
accounts:
  org-1:
    providers:
      org-1-key: { type: openrouter, apiKey: { env: ORG_1_OPENROUTER_API_KEY } }
```

A model named `<key name>/<model>` in the `.kritika.yaml` of one of the
account's repositories runs on that key and the account pays for it:

```yaml
review: { model: org-1-key/vendor/large-model }
```

A key's name cannot be one that the instance's providers use. The
instance's own configuration cannot name an account's key.

### Headers

A provider's `headers` are sent with every request to it, and they
replace kritika's own headers of the same name. A value is either inline
or, for a secret, a reference like `apiKey`'s. kritika masks a secret
value as it masks the key.

`{session}` in a value stands for the conversation a step belongs to.
It is the same conversation a provider of type `opencode` sends
([OpenCode Go and Zen](#opencode-go-and-zen)). A header with `{session}`
is sent only with a conversation's steps. Any other request carries
kritika's own header of that name, if it has one. That way, a gateway
that keys a cache or a route on its own header gets the conversation.
Bifrost, for one, forwards a header only under the `x-bf-eh-` prefix,
which it strips:

```yaml
providers:
  bifrost:
    type: openai
    baseUrl: http://bifrost.ai.svc.cluster.local:8080/v1
    apiKey: { env: BIFROST_API_KEY }
    headers:
      x-bf-eh-x-opencode-session: "{session}"
      x-team: platform
```

- `{session}` is the only placeholder, and goes in an inline value.
- These headers are refused:
  - `Authorization`, `Host`, `Accept`, `Accept-Encoding`, `Content-Type`,
    `Content-Length`, `Content-Encoding`, `Transfer-Encoding` and
    `Connection`, which `apiKey`, the SDKs or Go's HTTP client set
  - `X-Api-Key`, which will otherwise send a second key
  - `x-opencode-session` on a provider of type `opencode`, which sends
    its own
- A `chatgpt` provider takes no `headers`, and the
  [embedder](#the-embedder) sends none.

### Floating model aliases

Providers of type `anthropic`, `openai` and `chatgpt` accept
`<provider>/~<family>-latest` in these configuration keys:

- `review.model` and `review.fallback`
- `confidence.model` and `confidence.fallback`
- the `model` and `fallback` of a `review.agents` entry

For these provider types, the `~` makes the alias a kritika lookup. kritika
sends model IDs without it unchanged. This includes a provider's own
`*-latest` aliases.

OpenRouter resolves its own [aliases](#openrouter),
`~<author>/<family>-latest`, on the server. OpenCode takes no floating
aliases.

The first kritika alias request after startup fetches the provider's catalog
and waits for it. Concurrent requests share one fetch. Each provider uses its
catalog for five minutes. After that, the next alias request gets the
cached catalog at once and starts a refresh. While refreshing fails,
kritika keeps the cached catalog and tries again a minute later. There is
no background polling.

A failed first fetch, or a family with no model available, uses the usual
[retries](#retries) and [fallback](#fallback).

A review or follow-up resolves its kritika aliases once, when its run starts.
Every step of the run goes to the models they selected, whichever replica
serves it. A review carries on the last review's conversation only when
its alias still selects the same model. An alias that cannot be resolved
when the run starts is resolved again at each step.

Usage records the selected model ID. `pricing` prices that model under its
own ID. If the model has no entry of its own, `pricing` uses the entry
under the alias. So an entry keyed `~opus-latest` also prices models
released later. With a [reasoning effort](#effort) set, an Anthropic alias
skips models whose catalog entry lacks that level. The model an OpenAI or
ChatGPT alias selects must support the level.

Use an explicit model ID to pin a version. Floating aliases, both
kritika's and OpenRouter's, apply to review and confidence calls. Use
explicit IDs for embedding models, since an alias rollover does not
trigger an index rebuild. Loading the configuration fails on:

- An alias in `embedding.model`
- An `anthropic`, `openai` or `chatgpt` alias that is not `~<family>-latest`
- An `openrouter` alias that is not `~<author>/<family>-latest`
- An alias for an `opencode` provider

If you configured a ChatGPT alias such as `plan/sol-latest`, change it to
`plan/~sol-latest` to keep kritika's floating selection.

#### OpenRouter

OpenRouter's [native aliases](https://openrouter.ai/docs/guides/routing/routers/latest-resolution)
use `~<author>/<family>-latest`:

```yaml
review:
  model: openrouter/~anthropic/claude-sonnet-latest
  fallback: openrouter/~google/gemini-flash-latest
```

kritika passes these aliases to OpenRouter without fetching a catalog or
pinning a version. OpenRouter chooses the model on each request, so the
version can change between steps of a review. Usage records the concrete
model returned by OpenRouter. A review carries on the last review's
conversation while the alias is unchanged, even after OpenRouter moves it
to a newer model. The newer model has none of that conversation cached.

#### Anthropic

A provider of type `anthropic` accepts aliases such as `~opus-latest`,
`~sonnet-latest` and `~haiku-latest`:

```yaml
providers:
  anthropic:
    type: anthropic
    apiKey: { env: ANTHROPIC_API_KEY }
review:
  model: anthropic/~opus-latest
  fallback: anthropic/~sonnet-latest
  effort: high
confidence:
  model: anthropic/~haiku-latest
```

An alias selects the first active model of the requested family in that
API key's [model catalog](https://platform.claude.com/docs/en/api/models/list).
It uses the catalog's `line` and `lifecycle` fields. The catalog lists newer
models first. kritika does not compare release dates, since the catalog
does not always know one. The selection needs no mapping of model versions or families in
kritika. Models with no family, or marked deprecated or retired, are
excluded.

kritika reads all catalog pages before choosing a model. To support these
aliases, a gateway serving this provider must implement the Models API,
list newer models first and return those fields. A catalog that lists a
model twice, as one that ignores its page cursor does, fails the request.

#### OpenAI

A provider of type `openai` accepts aliases such as `~sol-latest` and
`~astra-latest`:

```yaml
providers:
  openai:
    type: openai
    apiKey: { env: OPENAI_API_KEY }
review:
  model: openai/~sol-latest
  fallback: openai/~astra-latest
  effort: high
```

The adapter lists models available to that API key through the
[Models API](https://developers.openai.com/api/reference/resources/models/methods/list/),
then uses the same numeric version selection as [ChatGPT](#chatgpt-plans).
For example, `gpt-6.10-sol` ranks above `sol-6.9`. IDs with preview,
date or other nonnumeric suffixes are excluded. Any matching family works
without a mapping in kritika. An OpenAI-compatible server must expose
`/models` with versioned IDs to support these aliases. The selected model
must support the Chat Completions API this provider uses.

### ChatGPT plans

A provider of type `chatgpt` uses a ChatGPT Plus or Pro plan through
[Sign in with ChatGPT](https://developers.openai.com/siwc/token-sharing-open-source/sign-in).
Configure it without `apiKey`, and choose a model the plan serves:

```yaml
providers:
  plan:
    type: chatgpt
  openrouter:
    type: openrouter
    apiKey: { env: OPENROUTER_API_KEY }
review:
  model: plan/<model-id>
  fallback: openrouter/<model-id>
```

After deploying this configuration, select a running kritika pod. Replace
`POD_NAME` in both commands below with its name. On the machine with your
browser, forward port 1455 from that pod:

```sh
kubectl -n kritika get pods
kubectl -n kritika port-forward pod/POD_NAME 1455:1455 --address 127.0.0.1
```

Leave the forward running. In another terminal, run the login command in
the same pod. Then open the URL that it prints in your browser:

```sh
kubectl -n kritika exec -it POD_NAME -- /kritika chatgpt login plan
```

For a provider declared under an account, include its forge and account
name before the provider name, for example:

```sh
kubectl -n kritika exec -it POD_NAME -- /kritika chatgpt login github/acme plan
```

The browser returns to `http://127.0.0.1:1455/auth/callback`. The forward
delivers it to the command in the pod. Wait for the terminal to say that
the provider is connected. Then stop the forward. No chart service or
ingress needs to expose the callback port. If the pod exits, repeat both
commands against another pod.

The command saves the registration and tokens in Postgres. Each replica
reads the current access token and reuses it for up to a minute, or until
it is due for renewal. Only the elected leader renews the rotating refresh
token. A runner holds no way to read these credentials.

A terminal refresh error clears the unusable tokens. Repeat the login command to
reconnect with the saved client and host IDs. The client ID is saved as
soon as the browser returns, so a first sign-in that fails after that is
repeated as the same client. A returning sign-in must use the same ChatGPT
account.

Use a concrete model ID to pin a version. A family alias such as
`plan/~sol-latest` or `plan/~astra-latest` selects the newest visible
version in that account's authenticated model catalog. Aliases compare
numeric versions in `gpt-<version>-<family>` or `<family>-<version>` IDs
and exclude preview suffixes. They work for any matching family without a
mapping in kritika.

The catalog follows the
[floating alias cache](#floating-model-aliases) and also refreshes when
the access token changes. If no matching model is available, the request
fails and a configured fallback can take over. Usage records the concrete
model selected. Model and reasoning effort use the same configuration
fields as other providers. The selected model must support the requested
effort.

The adapter uses streamed Responses requests with `store: false` and the
full conversation, and keys the prompt cache by that conversation. The
plan route does not accept an output-token cap, so the adapter omits it.
As with other providers, an answer cut off by the model's own cap is
returned as cut off. A server error or rate limit reported after the
stream opened is retried like a 5xx or 429. This provider cannot serve
embeddings.

Calls report token usage and are marked as covered by a ChatGPT plan. The
dashboard shows "ChatGPT plan" and "Included in plan" alongside their
token counts. `pricing` is not accepted for this provider. API spend
totals exclude subscription fees, but calls made through a paid fallback
still contribute their API costs.

The account's Usage page shows ChatGPT controls only when a ChatGPT provider
is configured for that account. It can filter to plan calls and group token
usage by hour or week. Review usage also totals ChatGPT calls and tokens by
run. Usage outside a run, such as the embedding of a review's similar code,
is labeled "Run not recorded". Token counts do not measure the percentage of
a plan allowance consumed.

When OpenAI supplies quota headers or rate-limit events on an inference
response, the dashboard shows the reported allowance windows. It also shows
the remaining percentages, reset times and observation time. Windows keep
their upstream duration, which can differ from one hour. These snapshots
update with model calls.

A passed reset time is shown as awaiting an update, and connections
without quota data show "Allowance data unavailable". The sign-in route
does not always expose this data. kritika does not estimate allowances
from tokens.

After a plan usage limit, each replica pauses that provider for 15 minutes and a
configured fallback can take over. This delay is a retry pause, not a
prediction of when the plan's allowance resets. See your allowance in
[ChatGPT Settings → Usage](https://chatgpt.com/settings/usage).

#### Tradeoffs and limitations

A ChatGPT plan can cover review calls within its included allowance. Compared
with API-key providers, consider these tradeoffs:

- Capacity is shared. Reviews share the plan's allowance with other apps
  that use that plan. Available capacity depends on the plan, model and
  task. kritika does not receive a separate allowance. See OpenAI's
  [current plan limits](https://learn.chatgpt.com/docs/pricing) and
  [app usage settings](https://developers.openai.com/siwc/token-sharing-open-source/profiles-and-sessions#tracking-usage).
- Sign-in needs maintenance. Initial setup requires a browser login. Tokens
  refresh automatically, but a revoked or unusable session requires an
  admin to repeat the login command. API keys are simpler to provision for
  an unattended service.
- Model availability is limited. Only models in the signed-in account's
  catalog are available. A family alias selects among those models. It
  cannot make an unavailable model accessible. Other vendors need another
  provider.
- Request controls are limited. The plan route does not accept an
  output-token cap, and this provider cannot serve embeddings. OpenAI
  documents further
  [preview limitations](https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations).
- Usage accounting has limits. Per-review and per-run token counts cannot
  reliably predict how much plan allowance a review consumes. Allowance
  snapshots depend on quota data that OpenAI supplies and can be unavailable
  or stale. API spend totals exclude the subscription fee.

Configure `review.fallback` with an API-key provider so it can take over
when the plan is exhausted or unavailable. If the scorer uses ChatGPT,
configure `confidence.fallback` the same way. Paid fallback calls still
incur API charges.
For sustained team workloads, consider an API-key provider as the primary.

You declare providers and the embedding configuration in the configuration
file. API keys use environment references such as
`{ env: OPENROUTER_API_KEY }`. Setting an environment variable alone does not
declare a provider or embedder.

## Retries

`retries` is how many more times a call is tried when the provider fails
it in a way that another attempt can get past. It applies to a review's model
step, the scorer's call and a split review's summary call. The failures
it covers are a 5xx, a 429, a timeout, a cut connection, and a 402 that
carries a `Retry-After`. That 402 is OpenRouter's answer when the
account's in-flight requests, with this request, exceed its balance.

- kritika waits up to a second, then up to twice as long each time, to at
  most 30 seconds. It waits out a longer `Retry-After` the provider sends,
  up to a minute.
- It never retries a refusal of the request itself, such as a prompt over
  the model's input limit, or a spent budget.
- A request that the provider does not answer in its [`timeout`](#timeouts)
  counts as a timeout.
- The provider's client sends nothing again on its own, so `retries` is
  every attempt a step gets.
- A step's attempts, the waits between them and its [fallback](#fallback)
  share the step's budget, so long timeouts end the retries early. With a
  fallback on another provider, the review model's attempts get at most
  its provider's share of that time, so the fallback always gets a turn.
- The scorer's call has the two minutes a score gets
  ([confidence](/confidence/#the-score)), shared the same way.
- A split review's summary call has 100 seconds, and the review model's
  attempts can take all of them. That is about one slow answer. Half of the
  100 seconds cuts such an answer short.

`retries` earns its keep behind a routing proxy that picks a model per
request: a step the proxy routed badly is answered on the next attempt. A
follow-up's steps are retried as a review's are. The embedder's client
sends a failed request again twice on its own.

A step that still fails that way after the provider's `retries` and the
fallback are spent does not end the review. The runner keeps the
conversation and sends the step again after 30 seconds, then after one,
two and four minutes. It does this while the agent's
[`timeout`](/configuration/#the-agent) allows. Only a step that fails
after the last of those ends the review as failed.

## Timeouts

`timeout` is how long one request to the provider can take. A request it
has not answered by then fails as a timeout, which its `retries` cover. It
is 5 minutes unless set, and at most 30 minutes. A self-hosted model that
takes longer than 5 minutes over a large prompt sets what its slowest
answer takes. Without that, kritika abandons the request at 5 minutes and
sends the step again while the server is still at work on the first one,
which doubles the load on a server that was slow to begin with.

A step's budget is one request to the review model's provider and one to
the fallback's, plus 2 minutes for the waits between attempts: 12 minutes
with both at their defaults. A fallback on the review model's own
provider, or none, counts the review model's `timeout` twice. The step's
attempts on the review model get the review model's share of the budget,
the `timeout`'s part of the two together, before a fallback on another
provider takes over. A runner's request to the gateway for a step
outlasts the longest budget among the run's models by 5 minutes, and the
runner names that wait on each step, so a gateway with a newer
configuration budgets the step within it. A longer
`timeout` does not change the agent's [`timeout`](/configuration/#the-agent)
for the whole loop, which bounds how many slow steps a review can take.

## Fallback

A `review.fallback` on the review model's provider is handed to the
provider with the request, as OpenRouter's server-side fallback is. The
provider or the adapter tries it when the review model fails.

The gateway itself tries a fallback on another provider. The same step
goes to the fallback once both of these are true: a review's step failed
on the review model, and its provider's `retries` or its share of the
step's budget ([timeouts](#timeouts)) are spent. The step first takes a slot of the fallback model's
provider's [`concurrency`](#concurrency), waiting for one within the time
the step has left; a step that gets none fails as it would with no
fallback. The fallback gets that provider's own `retries`, and the review
carries on there. The step's usage is recorded under the model that
answered. A follow-up's steps fall back the same way, and so does a split
review's summary call ([large reviews](/configuration/#the-agent)).

`confidence.fallback` does the same for the scorer's call. On the
confidence model's provider, it goes to the provider with the call. On
another provider, it gets the call once the confidence model's attempts
are spent. Those attempts get at most the confidence model's share of the
time that remains for the score. The call is recorded, charged and counted under the model that
answered.

`confidence.fallback` is set wherever `confidence.model` is.
This includes a repository's `.kritika.yaml`, under the same provider
rules. `confidence.effort` applies to it too.

## Effort

`review.effort` and `confidence.effort` set how hard a model reasons, as
the providers' reasoning effort. The levels, lowest first, are `none`,
`minimal`, `low`, `medium`, `high`, `xhigh` and `max`.

`review.effort` sets the review model's level, for reviews and follow-ups
alike, and the fallback that takes a step reasons as hard.
`confidence.effort` sets the level of the scorer and its fallback. When
unset, each is the provider's default. The review is where reasoning pays
off, and the score is a short call that a lower level answers for less:

```yaml
review:
  model: openrouter/vendor/large-model
  effort: high
confidence:
  model: openrouter/vendor/small-model
  effort: low
```

| Where                                | What it sets                                                                                                              |
| ------------------------------------ | ------------------------------------------------------------------------------------------------------------------------- |
| nothing                              | the provider's default, for both                                                                                          |
| the file's root or a `.kritika.yaml` | its level, like any [repository setting](/configuration/#repository-settings), and a `.kritika.yaml` replaces the admin's |
| a `.kritika.yaml`, `effort: ""`      | the provider's default again, where the admin set a level                                                                 |

Each provider type gets the level in its own form:

| Type                 | Sent as                | Notes                                                                                               |
| -------------------- | ---------------------- | --------------------------------------------------------------------------------------------------- |
| `openrouter`         | `reasoning.effort`     | OpenRouter maps a level a model lacks to the nearest it takes, for each model in the request's list |
| `openai`, `opencode` | `reasoning_effort`     | a model that takes no reasoning effort refuses the request. Leave its effort unset                  |
| `chatgpt`            | `reasoning.effort`     | the model must support the chosen level                                                             |
| `anthropic`          | `output_config.effort` | the Messages API runs from `low` to `max`, so `none` and `minimal` go out as `low`. Unset as above  |

Both are set in the configuration file. In Helm, use
`config.review.effort` and `config.confidence.effort`. The dashboard's
repository page shows each repository's effective levels beside its
models.

## Agents that replace the default pass

`review.model` is the default pass's model. An agent of `review.agents`
without `categories` replaces it for the pull requests its conditions hold
for. The first such agent that holds picks the review's model. A pull
request that no agent picks gets the default:

```yaml
review:
  model: anthropic/claude-sonnet-5-5 # the default pass
  fallback: anthropic/claude-haiku-5-5
  effort: medium
  agents:
    - name: deep # every category: replaces the default pass
      model: anthropic/claude-opus-5-5
      effort: high
      when:
        - { name: migrations, paths: ["db/migrations/**"] }
        - { name: large, expr: pr.lines > 800 }
```

An agent with `categories` reviews beside the default pass instead. See
[agents](/repository-config/#agents).

- An agent's `when` takes the same conditions as `trigger.include`
  ([trigger conditions](/configuration/#which-pull-requests-are-reviewed)).
  Each condition is an `expr`, `paths` or both, with an optional `name`.
  When a condition has both, both must hold. One condition holding is
  enough, and `paths` holds when one changed file matches, whatever else
  the pull request changes. An agent without `when` always replaces the
  default pass, which is how an admin takes it over for every pull
  request.
- The conditions are judged as the review starts, over the whole pull
  request against its base rather than its last push. So a pull request
  keeps its agent while it still changes what picked it, and its reviews'
  conversation and findings stay on one model.
- A condition with `paths` or `pr.lines` reads the pull request's diff
  from the forge before any model is called. If the forge will not give
  the whole diff, those conditions do not hold. The others are judged as
  usual.
- Such an agent takes only `name`, `model`, `fallback`, `effort`, `when`
  and `enabled`. Its `effort` and `fallback` replace `review.effort` and
  `review.fallback` where it sets them.
- The summary's footer names the agent and the condition that picked the
  model, as in `via deep (migrations)`. `@<bot> review deep` runs the
  default pass on that agent's model, whatever its conditions.
- A repository's `.kritika.yaml` that writes `review.model`, or agents of
  this kind, replaces the instance's agents of this kind as a whole. Every
  model they name must be of a provider that the account can use, or they
  are all dropped ([repository models](/repository-config/#models)).
- An agent's model waits for its own [`concurrency`](#concurrency)
  slots, as a repository's own model does. Follow-ups run on the default.

Since `paths` holds when any changed file matches, these agents suit
picking a stronger model for what a change touches. A pull request that
no agent picks gets the default, as it does without them. You cannot write
an agent for pull requests that change only some paths, such as
documentation alone.

## The embedder

| Key              | Default | What                                                                                                                                               |
| ---------------- | ------- | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| `model`          |         | a model of one of the instance's `openrouter` or `openai` providers. The embedder uses that provider's key and its endpoint, or the type's default |
| `dims`           |         | the model's dimension, at most 4000. It must be what the model returns                                                                             |
| `maxBatch`       | 64      | inputs in one request                                                                                                                              |
| `maxBatchChars`  | 200,000 | characters in one request                                                                                                                          |
| `maxItemChars`   | 16,000  | characters in one input                                                                                                                            |
| `similarFloor`   | 0.5     | the least cosine similarity a chunk needs to be offered as similar code. Required with a [prompt](#prompts)                                        |
| `similarMargin`  | 0.1     | how far below the best match of its query a chunk can score and still be offered                                                                   |
| `queryPrompt`    |         | text put, exactly as written, in front of each similar-code query of a review's diff                                                               |
| `searchPrompt`   |         | text put, exactly as written, in front of each `search_code` query. Unset, `queryPrompt`                                                           |
| `documentPrompt` |         | text put, exactly as written, in front of each indexed chunk                                                                                       |
| `lexical`        | true    | also search the index by name, and fuse the two searches. See [search by name](#search-by-name)                                                    |
| `rerank`         |         | a reranker that orders each query's candidates. See [reranking](#reranking)                                                                        |

An embedder builds each repository's similar-code index. With an embedder,
a review's prompt carries the index's chunks (pieces of files) nearest the
change. The agent gets a `search_code` tool over the same index. Both
keep a chunk only when it scores at least `similarFloor` and no more than
`similarMargin` below the best match of its query. A query is either a
hunk of the diff or a `search_code` query. Of the chunks kept, an answer
takes each query's best match before any query's second, at most two
chunks of one file and ten in all. The review's prompt also leaves out a
chunk more than half of whose lines another context stage already shows.

The index is of the default branch, which can be ahead of or behind the
pull request. Both show a chunk as the pull request's head has it: found
by its text, at its lines in the head, which differ from the index's when
code above it moved. A chunk whose text or file the head no longer has
is left out, as is one found at lines that a chunk before it mostly
shows.

How high matches score depends on the model and on the code a query is
near. A repository of similar files scores nearly everything high, and
one of varied code scores lower. The hunks of one diff differ too. The
margin follows each query's own best match, so one setting holds across
repositories where a floor cannot. Keep the floor low, under the weakest
match worth showing anywhere, and narrow the margin to keep fewer.

Indexing is off without an embedder, and reviews run without similar code.
The index holds one model, dimension and document prompt. A configuration
that changes any of them drops every repository's index. The leader then
builds each index again, a few at a time, as `KRITIKA_ONBOARD_WINDOW`
paces them. Removing the embedder keeps the index, and adding back the
same model, dimension and document prompt uses it again.

Any rebuild of a repository's index, and any update for a push, sends the
embedder only the chunks whose text its index does not already hold for
the same embedder. The others take the vector already stored, so the
provider is not paid twice for code that did not change.

A kritika release can also change how it chunks code or what it embeds
for each chunk: the index format. A new format drops no index. Each
repository's index keeps answering similar-code lookups, and pushes keep
it current, while the leader rebuilds it in the new format, a few at a
time as `KRITIKA_ONBOARD_WINDOW` paces them. During a rollout, index jobs
wait for a replica of the release that the leader runs. A replica of a
release from before index formats does not wait: it can update an index
that the new release built with chunks of its own format. The leader then
rebuilds that index too, as it does one of another format.

A runner of the new release can also report to a replica of the release
before it. That replica refuses a report with a field that only the new
release sends, such as the signature of a declaration's window. The runner
sends the report again for up to three minutes, as it does one that a
replica without the route refuses, by which time a rollout has usually
replaced the old replicas. A runner that still fails costs its index job one
of three attempts. When a job spends them all, the next poll queues the
push again, and the onboarding feeder offers an index not yet built again.

### Prompts

Many embedding models are trained with a task prompt in front of what
they embed, and their documentation recommends sending it. The prompt
goes on queries, and for some models on documents too.

The two kinds of query ask different things. A review's query is code: the
changed lines of a hunk with a few lines around them, without the file's
path, matched against code like it. A `search_code` query is mostly a
description of code. `queryPrompt` goes in front of every query of a
review's diff, and `searchPrompt` in front of every `search_code` query.
Unset, `searchPrompt` is `queryPrompt`. `documentPrompt` goes in front of
every indexed chunk, ahead of the path and symbol the chunk starts with.
Each is used exactly as written: quote it so YAML keeps a trailing space,
and write a newline as `\n` inside double quotes. When a prompt is unset,
its inputs go as they are. In Helm, all three go under
`config.embedding`.

The model's documentation gives its prompts. These suit a review, where
the documents are code:

```yaml
# Voyage code models, such as voyageai/voyage-code-4: what Voyage itself
# adds for an input_type of query and of document, for either kind of query
queryPrompt: "Represent the query for retrieving supporting documents: "
documentPrompt: "Represent the document for retrieval: "

# Gemini Embedding 2 and EmbeddingGemma 2: the symmetric similarity
# task, the same on both sides
queryPrompt: "task: sentence similarity | query: "
documentPrompt: "task: sentence similarity | query: "

# Qwen3-Embedding: a one-sentence instruction for each task, on queries
# only, as "Instruct: {task}\nQuery:"
queryPrompt: "Instruct: Given a code change, retrieve code that implements similar logic\nQuery:"
searchPrompt: "Instruct: Given a description of code, retrieve the code it describes\nQuery:"

# jina-code-embeddings: its code-to-code task for a review's hunks and its
# text-to-code task for search_code, whose documents take the same prefix
queryPrompt: "Find an equivalent code snippet given the following code snippet:\n"
searchPrompt: "Find the most relevant code snippet given the following query:\n"
documentPrompt: "Candidate code snippet:\n"

# nomic-embed-code: a prefix on a query that searches for code, and none
# on code, so a review's hunks go as they are
searchPrompt: "Represent this query for searching relevant code: "
```

The similarity task fits how a review compares a change with code like
it. The same models' code-search pair, `task: code retrieval | query: `
with `title: none | text: `, is for searching code with a query. That is
closer to what `search_code` does, but the index holds chunks embedded
with one `documentPrompt`, so the similarity pair serves both kinds.
OpenAI's `text-embedding-3` models and Mistral's `codestral-embed` take
no prompt.

Prompts move every score, up for some models and down for others. So a
floor tuned without them stops separating the same chunks, and the
configuration refuses a prompt without `similarFloor`. Prompts can also
widen or narrow the gaps between matches, which `similarMargin` measures.
A review's **Run details** conversation lists each similar chunk with its
score, as in `(similarity 0.42)`. Set the floor under the weakest chunk
worth keeping, and set the margin to the gap that still holds the chunks
worth keeping. A prompt counts toward `maxItemChars`, and at the defaults
it leaves every query and chunk whole.

A new `queryPrompt` or `searchPrompt` applies once kritika restarts with it, as any
configuration change does, and needs no new index. `documentPrompt`
shapes the stored vectors, so changing it rebuilds every repository's
index, as a new model does.

### Search by name

A change's code is full of names: the functions it calls, the types and
fields it uses, the settings it reads. An embedder can score low the code
that shares those names but does something else with them, which is often
the code a reviewer most needs to see. So, unless `lexical` is `false`,
each similar-code query also searches the index by name, and the two
searches' results are fused.

When a chunk is indexed, its path, symbol and text are split into words
the way code is written: each identifier whole, and its parts at case
changes, underscores, hyphens and digits. `parseHTTPRequest` and
`parse_http_request` both give `parsehttprequest`, `parse`, `http` and
`request`. Language keywords, names nearly every function uses (`err`,
`ctx`, `self`), English stop words and numbers are left out. A review's
query is split the same way, and a `search_code` query's words match the
parts of the identifiers they make up. The longest 32 words of a query
are searched for. A word weighs what BM25 gives a word in that many of
the repository's chunks, so a name few chunks hold counts for more than one
many do. A word in more than one chunk in a hundred, at least 20 and at
most 200 chunks, is too common to search for at all. Postgres's own
full-text ranking is not used, as it knows nothing of the other chunks,
and its parsers split code where it is not split.

Each search keeps a chunk on its own terms. The vector search keeps one
at or above `similarFloor` and within `similarMargin` of its query's best,
as before. The search by name keeps one that holds at least half the
weight of its query's best match by name. A chunk either search keeps is
kept. Each query's chunks are ordered by reciprocal rank fusion: a chunk
scores 1 / (60 + its rank) in the answer of each search that found it, so
one that both find comes first. Of those, an answer takes four for each
query, and the answer's other bounds hold as before. A chunk the vector
search keeps shows its similarity, as in `(similarity 0.81)`. One kept by
name alone shows the names it shares with the query instead, as in
`(shares AcquireLease, Lease)`, as its similarity sits under the floor and
would read as a poor match.

The words are stored with each chunk as a Postgres `tsvector`, so no
extension beyond VectorChord is needed. A request's queries are searched
for together, in one pass over the words of the generation's chunks in its
[partition](/database/#the-similar-code-index), which no index can
shorten under row-level security. A chunk indexed by a release without
them matches no query by name until its repository's index is rebuilt.
`kritika_similar_hits_total` counts each chunk by the search that found
it: `vector`, `lexical` or `both`.

### Reranking

A reranker reads each query beside each of its candidates and scores how
well the candidate answers it, which an embedding of each alone cannot.
With `rerank` set, both searches look for `candidates` chunks for each
query rather than four. The candidates that the floor, the margin and the
weight by name keep, at most `candidates` of each query, the best fused
first, are sent to the reranker, and its scores order them in place of the
fusion's. The answer then takes each query's best four as before.

| Key          | Default | What                                                                                                                        |
| ------------ | ------- | --------------------------------------------------------------------------------------------------------------------------- |
| `model`      |         | a model of one of the instance's `openai` providers whose `baseUrl` is the reranker's API root. It uses that provider's key |
| `api`        |         | the API's shape: `cohere`, `jina`, `voyage` or `tei`                                                                        |
| `candidates` | 50      | the candidates of each query sent to be reranked, at most 100                                                               |
| `timeout`    | 20s     | how long the reranking of one similar-code request, all its queries, can take, at most 45s                                  |

The reranker's provider is of type `openai`, which stands for any
endpoint that takes a bearer key, and it sets `baseUrl`: kritika posts to
`{baseUrl}/rerank`. The four APIs differ in their requests and answers:

| `api`    | Request                                                    | Answer                                     | Usage recorded                   |
| -------- | ---------------------------------------------------------- | ------------------------------------------ | -------------------------------- |
| `cohere` | `model`, `query`, `documents`, `top_n`                     | `results` of `index` and `relevance_score` | `meta.billed_units.input_tokens` |
| `jina`   | as `cohere`, and `return_documents: false`                 | as `cohere`                                | `usage.total_tokens`             |
| `voyage` | `model`, `query`, `documents`, `top_k`, `truncation: true` | `data` of `index` and `relevance_score`    | `usage.total_tokens`             |
| `tei`    | `query`, `texts`, `truncate: true`                         | a list of `index` and `score`              | the `x-compute-tokens` header    |

```yaml
providers:
  # Cohere's v2 API
  cohere:
    type: openai
    baseUrl: https://api.cohere.com/v2
    apiKey: { env: COHERE_API_KEY }
  # Jina AI
  jina:
    type: openai
    baseUrl: https://api.jina.ai/v1
    apiKey: { env: JINA_API_KEY }
  # Voyage AI
  voyage:
    type: openai
    baseUrl: https://api.voyageai.com/v1
    apiKey: { env: VOYAGE_API_KEY }
  # Hugging Face Text Embeddings Inference serving a reranker, such as
  # BAAI/bge-reranker-base, in the cluster
  tei:
    type: openai
    baseUrl: http://reranker.ai.svc.cluster.local
    apiKey: { env: UNUSED_KEY }
embedding:
  model: openrouter/voyageai/voyage-code-3
  dims: 1024
  rerank:
    model: cohere/rerank-v3.5
    api: cohere
    # or: { model: jina/jina-reranker-v2-base-multilingual, api: jina }
    # or: { model: voyage/rerank-2.5, api: voyage }
    # or: { model: tei/bge-reranker-base, api: tei }
```

A reranker fails open. A query that it does not answer, with an error or
past `timeout`, keeps the fused order, and the failure is logged as a
warning and counted under `kritika_model_calls_total` with role `rerank`
and outcome `error`. A request reranks its queries one at a time, holding
one of the reranker's slots, which the provider's `concurrency` sets, as
the embedder's does. The tokens that the provider reports are recorded as
usage with role `rerank`, and count against the account's `limits`. They
are not charged to the run's own budget. TEI takes no more texts in one
request than its `--max-client-batch-size`, 32 unless it is raised, so a
TEI reranker sets `candidates` under it or the server raises it. The
kritika pods call the reranker, so the chart's NetworkPolicies must let
them reach its port, as for a [local model](#local-models).

## Local models

A model served in your own network is a provider of type `openai`. Its
`baseUrl` is the server's OpenAI-compatible API, such as vLLM, Ollama or
llama.cpp's server. For a server that speaks the Anthropic API, it is a
provider of type `anthropic`. Either type can serve reviews, but only an
`openai` one can serve the embedder:

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

- `apiKey` is required. A server that takes no key still needs a
  reference to a variable holding any value.
- The model must support tool calls. A review works through tools and
  submits its findings as a call to `submit_review`, which its last step
  tells it to make. A follow-up's reply is a tool call too.
- The kritika pods call the server, and a review's runner reaches it only
  through their gateway. The chart's NetworkPolicies are on by default
  and allow only port 443 unless `networkPolicy.egressPorts` says
  otherwise. Add the port of a server outside the cluster there. For a
  server in the cluster, allow its pods in `networkPolicy.extraEgress`,
  on the port they listen on rather than its Service's port.
- A server that takes more than 5 minutes to answer a large prompt sets
  the provider's [`timeout`](#timeouts) to what its slowest answer takes.
- A server that reports no cost leaves calls unpriced unless `pricing`
  gives the model's prices. Prices are in dollars per million tokens of
  `input`, `output`, `cacheRead` and `cacheWrite`, keyed by the model's
  id on the server. Tokens count against an account's `limits` either
  way.

## OpenCode Go and Zen

OpenCode Go and OpenCode Zen are one gateway with an OpenAI-compatible
chat completions API. It routes requests and caches prompts by a
per-conversation header, `x-opencode-session`, and refuses a request
without one. A provider of type `opencode` sends that header. In it, a
review's steps name their run, or the run whose conversation they carry
on ([incremental reviews](/configuration/#incremental-reviews)). A
follow-up names its mention.

The provider's `baseUrl` is Go's,
`https://opencode.ai/zen/go/v1`, unless set. Zen is the same type at
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

Only the models that the gateway serves on `/v1/chat/completions` can be
used. Its endpoint tables say which. Models that it serves on
`/v1/responses` or `/v1/messages` cannot be used.

A response that reports no cost leaves the call unpriced unless
`pricing` gives the model's prices, as for a local model.
