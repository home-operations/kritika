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

Replace `vendor/large-model` with the model you want to use. In Helm, put these
settings under `config` and supply the key from a Secret as shown in [Setup](/setup/#deploy).

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

| Key       | What                                                                                                                                          |
| --------- | --------------------------------------------------------------------------------------------------------------------------------------------- |
| `type`    | `openrouter`, `openai`, `anthropic`, `opencode` or `chatgpt`                                                                                  |
| `apiKey`  | its key, required except for `chatgpt`, which uses a stored sign-in                                                                           |
| `baseUrl` | its API's URL, the type's default unless set                                                                                                  |
| `pricing` | the prices of a provider that reports no cost, keyed by model id or [floating alias](#floating-model-aliases) ([local models](#local-models)) |
| `retries` | how many more times a failed model step is tried, from 0 to 5; 0 unless set ([retries](#retries))                                             |
| `headers` | headers sent with every request, keyed by name; each value is inline or a reference like `apiKey` ([headers](#headers))                       |

Provider credentials never enter a runner pod: the agent reaches its model
through kritika's gateway ([the model endpoint](/security/#the-model-endpoint)).

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
kritika also warns for each model the configuration file sets on such a
provider when `pricing` has no price under the model as written.

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

A key's name may not be one the instance's providers use, and the
instance's own settings may not name an account's key.

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
  - `X-Api-Key`, which would send a second key
  - `x-opencode-session` on a provider of type `opencode`, which sends
    its own
- A `chatgpt` provider takes no `headers`, and the
  [embedder](#the-embedder) sends none.

### Floating model aliases

Providers of type `anthropic`, `openai` and `chatgpt` accept
`<provider>/~<family>-latest` in these settings:

- `review.model` and `review.fallback`
- `confidence.model` and `confidence.fallback`
- the `model` and `fallback` of a `review.agents` entry

For these provider types, the `~` makes the alias a kritika lookup. Model
IDs without it, including a provider's own `*-latest` aliases, are sent
unchanged.

OpenRouter resolves its own [aliases](#openrouter),
`~<author>/<family>-latest`, on the server. OpenCode takes no floating
aliases.

The first kritika alias request after startup fetches the provider's catalog
and waits for it; concurrent requests share one fetch. Each provider uses its
catalog for five minutes. After that, the next alias request gets the
cached catalog at once and starts a refresh. While refreshing fails,
kritika keeps the cached catalog and tries again a minute later. There is
no background polling. A failed first fetch, or a family with no model
available, uses the usual [retries](#retries) and [fallback](#fallback).

A review or follow-up resolves its kritika aliases once, when its run starts.
Every step of the run goes to the models they selected, whichever replica
serves it. A review carries on the last review's conversation only when
its alias still selects the same model. An alias that cannot be resolved
when the run starts is resolved again at each step.

Usage records the selected model ID. `pricing` prices that model under its
own ID. If the model has no entry of its own, `pricing` uses the entry
under the alias, so an entry keyed `~opus-latest` also prices models
released later. With a [reasoning effort](#effort) set, an Anthropic alias
skips models whose catalog entry lacks that level. The model an OpenAI or
ChatGPT alias selects must support the level.

Use an explicit model ID to pin a version. Floating aliases, both
kritika's and OpenRouter's, apply to review and confidence calls. Use
explicit IDs for embedding models, since an alias rollover does not
trigger an index rebuild. Loading the configuration fails on:

- an alias in `embedding.model`;
- an `anthropic`, `openai` or `chatgpt` alias that is not `~<family>-latest`;
- an `openrouter` alias that is not `~<author>/<family>-latest`;
- an alias for an `opencode` provider.

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

Kritika passes these aliases to OpenRouter without fetching a catalog or
pinning a version. OpenRouter chooses the model on each request, so the
version can change between steps of a review. Usage records the concrete
model returned by OpenRouter. A review carries on the last review's
conversation while the alias is unchanged, even after OpenRouter has moved
it to a newer model. The newer model has none of that conversation cached.

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
API key's [model catalog](https://platform.claude.com/docs/en/api/models/list),
using the catalog's `line` and `lifecycle` fields. The catalog lists newer
models first. Release dates are not compared, since the catalog may not
know one. The selection needs no mapping of model versions or families in
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
the **same pod** and open the URL it prints in your browser:

```sh
kubectl -n kritika exec -it POD_NAME -- /kritika chatgpt login plan
```

For a provider declared under an account, include its forge and account
name before the provider name, for example:

```sh
kubectl -n kritika exec -it POD_NAME -- /kritika chatgpt login github/acme plan
```

The browser returns to `http://127.0.0.1:1455/auth/callback`; the forward
delivers it to the command in the pod. Wait for the terminal to confirm
the provider is connected, then stop the forward. No chart service or
ingress needs to expose the callback port. If the pod exits, repeat both
commands against another pod.

The command saves the registration and tokens in Postgres. Each replica
reads the current access token and reuses it for up to a minute, or until
it is due for renewal. Only the elected leader renews the rotating refresh
token. The runner database role cannot read these credentials. A terminal
refresh error clears the unusable tokens. Repeat the login command to
reconnect with the saved client and host IDs. The client ID is saved as
soon as the browser returns, so a first sign-in that fails after that is
repeated as the same client. A returning sign-in must use the same ChatGPT
account.

Use a concrete model ID to pin a version. A family alias such as
`plan/~sol-latest` or `plan/~astra-latest` selects the newest visible
version in that account's authenticated model catalog. Aliases compare
numeric versions in `gpt-<version>-<family>` or `<family>-<version>` IDs
and exclude preview suffixes. They work for any matching family without a
mapping in kritika. The catalog follows the
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
still contribute their API costs. The account's Usage page shows ChatGPT
controls only when a ChatGPT provider is configured for that account. It
can filter to plan calls and group token usage by hour or week. Review
usage also totals ChatGPT calls and tokens by run. Usage outside a run,
such as the embedding of a review's similar code, is labelled "Run not
recorded". Token counts do not measure the
percentage of a plan allowance consumed.

When OpenAI supplies quota headers or rate-limit events on an inference
response, the dashboard shows the reported allowance windows, remaining
percentages, reset times and observation time. Windows keep their upstream
duration, which can differ from one hour. These snapshots update with model
calls. A passed reset time is shown as awaiting an update, and connections
without quota data show "Allowance data unavailable". The sign-in route may
not expose this data. kritika does not estimate allowances from tokens.

After a plan usage limit, each replica pauses that provider for 15 minutes and a
configured fallback can take over. This delay is a retry pause, not a
prediction of when the plan's allowance resets. Check your allowance in
[ChatGPT Settings → Usage](https://chatgpt.com/settings/usage).

#### Tradeoffs and limitations

A ChatGPT plan can cover review calls within its included allowance. Compared
with API-key providers, consider these tradeoffs:

- **Shared capacity.** Reviews share the plan's allowance with other apps
  using that plan. Available capacity depends on the plan, model and task;
  kritika does not receive a separate allowance. Check OpenAI's
  [current plan limits](https://learn.chatgpt.com/docs/pricing) and
  [app usage settings](https://developers.openai.com/siwc/token-sharing-open-source/profiles-and-sessions#tracking-usage).
- **Sign-in maintenance.** Initial setup requires a browser login. Tokens
  refresh automatically, but a revoked or unusable session requires an
  admin to repeat the login command. API keys are simpler to provision for
  an unattended service.
- **Model availability.** Only models in the signed-in account's catalog
  are available. A family alias selects among those models; it cannot make
  an unavailable model accessible. Other vendors need another provider.
- **Request controls.** The plan route does not accept an output-token
  cap, and this provider cannot serve embeddings. OpenAI documents further
  [preview limitations](https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations).
- **Usage accounting.** Per-review and per-run token counts cannot reliably
  predict how much plan allowance a review consumes. Allowance snapshots
  depend on quota data OpenAI supplies and may be unavailable or stale.
  API spend totals exclude the subscription fee.

Configure `review.fallback` with an API-key provider so it can take over
when the plan is exhausted or unavailable. If the scorer uses ChatGPT,
configure `confidence.fallback` the same way. Paid fallback calls still
incur API charges.
For sustained team workloads, consider an API-key provider as the primary.

Providers and embedding settings are declared in the configuration file.
API keys use environment references such as `{ env: OPENROUTER_API_KEY }`.
Setting an environment variable alone does not declare a provider or embedder.

## Retries

`retries` is how many more times a call is tried when the provider fails
it in a way another attempt may get past. It applies to a review's model
step, the scorer's call and a split review's summary call. The failures
it covers are a 5xx, a 429, a timeout, a cut connection, and a 402 that
carries a `Retry-After`. That 402 is OpenRouter's answer while the
account's in-flight requests would exceed its balance.

- kritika waits up to a second, then up to twice as long each time, to at
  most 30 seconds. It waits out a longer `Retry-After` the provider sends,
  up to a minute.
- It never retries a refusal of the request itself, such as a prompt over
  the model's input limit, or a spent budget.
- A request the provider has not answered in five minutes counts as a
  timeout.
- The provider's client sends nothing again on its own, so `retries` is
  every attempt a step gets.
- A step's attempts, the waits between them and its [fallback](#fallback)
  share 12 minutes, so long timeouts end the retries early. With a
  fallback on another provider, the review model's attempts get at most
  half of that time, so the fallback always gets a turn.
- The scorer's call has the two minutes a score gets
  ([confidence](/confidence/#the-score)), shared the same way.
- A split review's summary call has 100 seconds, and the review model's
  attempts may take all of them. That is about one slow answer, which
  half of the 100 seconds would cut short.

`retries` earns its keep behind a routing proxy that picks a model per
request: a step the proxy routed badly is answered on the next attempt. A
follow-up's steps are retried as a review's are. The embedder's client
sends a failed request again twice on its own.

A step that still fails that way after the provider's `retries` and the
fallback are spent does not end the review. The runner keeps the
conversation and sends the step again after 30 seconds, then after one,
two and four minutes, while the agent's
[`timeout`](/configuration/#the-agent) allows. Only a step that fails
after the last of those ends the review as failed.

## Fallback

A `review.fallback` on the review model's provider is handed to the
provider with the request, as OpenRouter's server-side fallback is. The
provider or the adapter tries it when the review model fails.

A fallback on another provider is tried by the gateway itself. Once a
review's step has failed on the review model, and its provider's
`retries` or half of the step's 12 minutes are spent, the same step goes
to the fallback. The fallback gets that provider's own `retries`, and the
review carries on there. The step's usage is recorded under the model
that answered. A follow-up's steps fall back the same way, and so does a
split review's summary call ([large reviews](/configuration/#the-agent)).

`confidence.fallback` does the same for the scorer's call. On the
confidence model's provider, it goes to the provider with the call. On
another provider, it gets the call once the confidence model's attempts
are spent, and those attempts get at most half of the time the score has
left. The call is recorded, charged and counted under the model that
answered. `confidence.fallback` is set wherever `confidence.model` is,
including a repository's `.kritika.yaml`, under the same provider rules.
`confidence.effort` applies to it too.

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

| Where                                | What it sets                                                                                                          |
| ------------------------------------ | --------------------------------------------------------------------------------------------------------------------- |
| nothing                              | the provider's default, for both                                                                                      |
| the file's root or a `.kritika.yaml` | its level, like any [repository setting](/configuration/#repository-settings); a `.kritika.yaml` replaces the admin's |
| a `.kritika.yaml`, `effort: ""`      | the provider's default again, where the admin set a level                                                             |

Each provider type gets the level in its own form:

| Type                 | Sent as                | Notes                                                                                               |
| -------------------- | ---------------------- | --------------------------------------------------------------------------------------------------- |
| `openrouter`         | `reasoning.effort`     | OpenRouter maps a level a model lacks to the nearest it takes, for each model in the request's list |
| `openai`, `opencode` | `reasoning_effort`     | a model that takes no reasoning effort refuses the request; leave its effort unset                  |
| `chatgpt`            | `reasoning.effort`     | the model must support the chosen level                                                             |
| `anthropic`          | `output_config.effort` | the Messages API runs from `low` to `max`, so `none` and `minimal` go out as `low`; unset as above  |

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

An agent with `categories` reviews beside the default pass instead; see
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
  model they name must be of a provider the account may use, or they are
  all dropped ([repository models](/repository-config/#models)).
- An agent's model waits for its own [`concurrency`](/configuration/#accounts)
  slots, as a repository's own model does. Follow-ups run on the default.

Since `paths` holds when any changed file matches, these agents suit
picking a stronger model for what a change touches. A pull request that
no agent picks gets the default, as it would without them. An agent for
pull requests that change only some paths, such as documentation alone,
cannot be written.

## The embedder

| Key              | Default | What                                                                                                                                               |
| ---------------- | ------- | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| `model`          |         | a model of one of the instance's `openrouter` or `openai` providers; the embedder uses that provider's key and its endpoint, or the type's default |
| `dims`           |         | the model's dimension, at most 4000; it must be what the model returns                                                                             |
| `maxBatch`       | 64      | inputs in one request                                                                                                                              |
| `maxBatchChars`  | 200,000 | characters in one request                                                                                                                          |
| `maxItemChars`   | 16,000  | characters in one input                                                                                                                            |
| `similarFloor`   | 0.5     | the least cosine similarity a chunk needs to be offered as similar code; required with a [prompt](#prompts)                                        |
| `similarMargin`  | 0.1     | how far below the best match of its query a chunk may score and still be offered                                                                   |
| `queryPrompt`    |         | text put, exactly as written, in front of each similar-code query                                                                                  |
| `documentPrompt` |         | text put, exactly as written, in front of each indexed chunk                                                                                       |

With an embedder, a review's prompt carries the index's chunks nearest the
change, and the agent gets a `search_code` tool over the same index. Both
keep a chunk only when it scores at least `similarFloor` and no more than
`similarMargin` below the best match of its query. A query is either a
hunk of the diff or a `search_code` query.

How high matches score depends on the model and on the code a query is
near. A repository of similar files scores nearly everything high, and
one of varied code scores lower. The hunks of one diff differ too. The
margin follows each query's own best match, so one setting holds across
repositories where a floor can't. Keep the floor low, under the weakest
match worth showing anywhere, and narrow the margin to keep fewer.

Without an embedder, indexing is off and reviews run without similar code.
The index holds one model, dimension and document prompt. A configuration
that changes any of them drops every repository's index. The leader then
builds each index again, a few at a time, as `KRITIKA_ONBOARD_WINDOW`
paces them. Removing the embedder keeps the index, and adding back the
same model, dimension and document prompt uses it again.

### Prompts

Many embedding models are trained with a task prompt in front of what
they embed, and their documentation recommends sending it. The prompt
goes on queries, and for some models on documents too.

`queryPrompt` goes in front of every similar-code query, both the
review's diff hunks and the agent's `search_code` queries.
`documentPrompt` goes in front of every indexed chunk, ahead of the path
and symbol the chunk starts with. Each is used exactly as written: quote
it so YAML keeps a trailing space, and write a newline as `\n` inside
double quotes. When a prompt is unset, its inputs go as they are. In
Helm, both go under `config.embedding`.

The model's documentation gives its prompts. These suit a review, where
both sides are code:

```yaml
# Voyage models, such as voyageai/voyage-code-4: what Voyage itself adds
# for an input_type of query and of document
queryPrompt: "Represent the query for retrieving supporting documents: "
documentPrompt: "Represent the document for retrieval: "

# Gemini Embedding 2 and EmbeddingGemma 2: the symmetric similarity
# task, the same on both sides
queryPrompt: "task: sentence similarity | query: "
documentPrompt: "task: sentence similarity | query: "

# Qwen3-Embedding: a one-sentence instruction, on queries only
queryPrompt: "Instruct: Given a code change, retrieve code that implements similar logic\nQuery:"
```

The similarity task fits how a review compares a change with code like
it. The same models' code-search pair, `task: code retrieval | query: `
with `title: none | text: `, is for searching code with a query. That is
closer to what `search_code` does, but kritika sends one pair for both.
OpenAI's `text-embedding-3` models and Mistral's `codestral-embed` take
no prompt.

Prompts move every score, up for some models and down for others. So a
floor tuned without them stops separating the same chunks, and the
configuration refuses a prompt without `similarFloor`. Prompts can also
widen or narrow the gaps between matches, which `similarMargin` measures.
A review's **Run details** conversation lists each similar chunk with its
score, as in `for similarity 0.42`. Set the floor under the weakest chunk
worth keeping, and set the margin to the gap that still holds the chunks
worth keeping. A prompt counts toward `maxItemChars`, and at the defaults
it leaves every query and chunk whole.

A new `queryPrompt` applies once kritika restarts with it, as any
configuration change does, and needs no new index. `documentPrompt`
shapes the stored vectors, so changing it rebuilds every repository's
index, as a new model does.

## Local models

A model served in your own network is a provider of type `openai` whose
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
follow-up names its mention. The provider's `baseUrl` is Go's,
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

- Only the models the gateway serves on `/v1/chat/completions` can be
  used; its endpoint tables say which. Models it serves on
  `/v1/responses` or `/v1/messages` cannot.
- A response that reports no cost leaves the call unpriced unless
  `pricing` gives the model's prices, as for a local model.
