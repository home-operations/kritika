# Models

kritika calls models through the providers the configuration file
declares, each with its own credentials. A model is named `<provider>/<model>`, on
a provider the file declares:

| Setting            | The model that                                                                                  |
| ------------------ | ----------------------------------------------------------------------------------------------- |
| `review.model`     | reviews pull requests and answers follow-ups                                                    |
| `review.fallback`  | takes over a review's or a follow-up's step when the review model fails ([fallback](#fallback)) |
| `confidence.model` | scores a reviewed pull request ([confidence and approvals](confidence.md))                      |
| `confidence.fallback` | takes over the scorer's call when the confidence model fails ([fallback](#fallback))         |
| `embedding.model`  | builds each repository's similar-code index ([the embedder](#the-embedder))                     |

The review models can differ by repository, in the admin's `repositories`
entries or a repository's own [`.kritika.yaml`](repository-config.md#models),
which may name only a provider the instance or the repository's account
declares. `review.effort` and `confidence.effort` set how hard each model
reasons ([effort](#effort)).

## Providers

```yaml
providers:
  openrouter:
    type: openrouter
    apiKey: { env: OPENROUTER_API_KEY }
review:
  model: openrouter/vendor/large-model
embedding:
  model: openrouter/voyageai/voyage-code-4
  dims: 1024
```

| Key       | What                                                                                                                                     |
| --------- | ---------------------------------------------------------------------------------------------------------------------------------------- |
| `type`    | `openrouter`, `openai`, `anthropic`, `opencode` or `chatgpt`                                                                             |
| `apiKey`  | its key, required except for `chatgpt`, which uses a stored sign-in                                                                      |
| `baseUrl` | its API's URL, the type's default unless set                                                                                             |
| `pricing` | per model id or [floating alias](#floating-model-aliases), the prices of a provider that reports no cost ([local models](#local-models)) |
| `retries` | how many more times a failed model step is tried, from 0 to 5; 0 unless set ([retries](#retries))                                        |

Provider credentials never enter a runner pod: the agent reaches its model
through kritika's gateway ([the model endpoint](security.md#the-model-endpoint)).

### An account's own keys

An account's entry under [`accounts`](configuration.md#accounts) can
declare providers of its own:

```yaml
accounts:
  org-1:
    providers:
      org-1-key: { type: openrouter, apiKey: { env: ORG_1_OPENROUTER_API_KEY } }
repositories:
  org-1/*:
    review: { model: org-1-key/vendor/large-model }
```

A model named `<key name>/<model>` in the account's `owner/*` or
`owner/name` entries, or in one of its repositories' `.kritika.yaml`, runs
on that key and the account pays for it. A key's name may not be one the
instance's providers use.

### Floating model aliases

Providers of type `anthropic`, `openai` and `chatgpt` accept
`<provider>/~<family>-latest` in `review.model`, `review.fallback`,
`confidence.model` and `confidence.fallback`. For these provider types,
the `~` makes the alias a kritika lookup. Model IDs without it, including
a provider's own `*-latest` aliases, are sent unchanged.

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
own ID or, without an entry of its own, under the alias, so an entry keyed
`~opus-latest` also prices models released later. With a
[reasoning effort](#effort) set, an Anthropic alias skips models whose
catalog entry lacks that level; the model an OpenAI or ChatGPT alias
selects must support it.

Use an explicit model ID to pin a version. Floating aliases, kritika's and
OpenRouter's, apply to review and confidence calls; use explicit IDs for
embedding models, since an alias rollover does not trigger an index rebuild.
Loading the configuration fails on:

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
it to a newer model, which has none of that conversation cached.

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
which lists newer models first, using its `line` and `lifecycle` fields.
Release dates are not compared, since the catalog may not know one. It
needs no mapping of model versions or families in kritika. Models with no
family, or marked deprecated or retired, are excluded.

kritika reads all catalog pages before choosing a model. A gateway serving
this provider must implement the Models API, list newer models first and
return those fields to support these aliases. A catalog that lists a model
twice, as one that ignores its page cursor does, fails the request.

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
it is due for renewal; only the elected leader renews the rotating refresh
token. The runner database role cannot read these credentials. A terminal
refresh error clears the unusable tokens; repeat the login command to
reconnect with the saved client and host IDs. The client ID is saved as
soon as the browser returns, so a first sign-in that fails after that is
repeated as the same client. A returning sign-in must use the same ChatGPT
account.

Use a concrete model ID to pin a version, or a family alias such as
`plan/~sol-latest` or `plan/~astra-latest` to select the newest visible version
in that account's authenticated model catalog. Aliases compare numeric
versions in `gpt-<version>-<family>` or `<family>-<version>` IDs and exclude
preview suffixes. They work for any matching family without a mapping in
kritika. The catalog follows the [floating alias cache](#floating-model-aliases)
and also refreshes when the access token changes. If no matching model is
available, the request fails and a configured fallback can take over. Usage
records the concrete model selected. Model and reasoning effort use the
same configuration fields as other providers; the selected model must
support the requested effort.

The adapter uses streamed Responses requests with `store: false` and the
full conversation, keying the prompt cache by that conversation. The plan route
does not accept an output-token cap, so the adapter omits it; an answer
the model's own cap cuts off is returned as cut off, as with other
providers. A server error or rate limit reported after the stream opened
is retried like a 5xx or 429. It cannot serve embeddings. Calls report token
usage and are marked as covered by a ChatGPT plan. The dashboard shows
“ChatGPT plan” and “Included in plan” alongside their token counts. `pricing`
is not accepted for this provider. API spend totals exclude subscription
fees; calls made through a paid fallback still contribute their API costs.
The account's Usage page shows ChatGPT controls only when a ChatGPT provider
is configured for that account. It can filter to plan calls and group token
usage by hour or week. Review usage also totals ChatGPT calls and tokens by
run; older records without a run ID are labelled “Run not recorded”. Token
counts do not measure the percentage of a plan allowance consumed.

When OpenAI supplies quota headers or rate-limit events on an inference
response, the dashboard shows the reported allowance windows, remaining
percentages, reset times and observation time. Windows keep their upstream
duration, which can differ from one hour. These snapshots update with model
calls. A passed reset time is shown as awaiting an update, and connections
without quota data show “Allowance data unavailable”. The sign-in route may
not expose this data; kritika does not estimate allowances from tokens.

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

Configure `review.fallback`, and `confidence.fallback` if the scorer uses
ChatGPT, with an API-key provider so it can take over when the plan is
exhausted or unavailable. Paid fallback calls still incur API charges.
For sustained team workloads, consider an API-key provider as the primary.

### From the environment

One provider and the embedder can come from the environment instead:

| Variable                     | Key                                                             |
| ---------------------------- | --------------------------------------------------------------- |
| `KRITIKA_PROVIDERS_NAME`     | the provider's name, `openrouter` unless set                    |
| `KRITIKA_PROVIDERS_TYPE`     | `type`, which defaults to the name when that is a provider type |
| `KRITIKA_PROVIDERS_BASE_URL` | `baseUrl`                                                       |
| `KRITIKA_PROVIDERS_API_KEY`  | `apiKey`                                                        |
| `KRITIKA_PROVIDERS_RETRIES`  | `retries`                                                       |
| `KRITIKA_EMBEDDING_MODEL`    | `embedding.model`                                               |
| `KRITIKA_EMBEDDING_DIMS`     | `embedding.dims`                                                |

The environment declares at most one provider, which replaces the file's
of the same name whole or is added to the file's. The embedding variables
set the file's embedder key by key. The review and confidence models have
variables of their own
([environment variables](configuration.md#environment-variables)).

## Retries

`retries` is how many more times a review's model step, the scorer's call
or a split review's summary call is tried when the provider fails it in a
way another attempt may get past: a 5xx, a 429, a 402 that carries a
`Retry-After` (OpenRouter's answer while the account's in-flight requests
would exceed its balance), a timeout or a cut connection.

- kritika waits up to a second, then up to twice as long each time, to at
  most 30 seconds, and waits out a longer `Retry-After` the provider
  sends, up to a minute.
- It never retries a refusal of the request itself, such as a prompt over
  the model's input limit, or a spent budget.
- A request the provider has not answered in five minutes counts as a
  timeout.
- The provider's client sends nothing again on its own, so `retries` is
  every attempt a step gets. A step's attempts, the waits between them and
  its [fallback](#fallback) share 12 minutes, so long timeouts end the
  retries early; with a fallback on another provider, the review model's
  attempts get at most half of them, so the fallback always gets a turn.
  The scorer's call has the two minutes a score gets
  ([confidence](confidence.md#the-score)), shared the same way. A split
  review's summary call has 100 seconds, and the review model's attempts
  may take all of them: that is about one slow answer, which half of it
  would cut.

A routing proxy that picks a model per request is where it earns its keep:
a step the proxy routed badly is answered on the next attempt. A
follow-up's steps are retried as a review's are; the embedder's client
sends a failed request again twice on its own.

A step that still fails that way once the provider's `retries` and the
fallback are spent does not end the review. The runner keeps the
conversation and sends the step again after 30 seconds, then after one,
two and four minutes, while the agent's
[`timeout`](configuration.md#the-agent) allows; only a step that fails
after the last of those ends the review as failed.

## Fallback

A `review.fallback` on the review model's provider is handed to the
provider with the request, as OpenRouter's server-side fallback is, and
the provider or the adapter tries it when the review model fails. A
fallback on another provider is tried by the gateway itself: once a
review's step has failed on the review model, and its provider's
`retries` or half of the step's 12 minutes are spent, the same step goes
to the fallback, with that provider's own `retries`, and the review
carries on there. The step's usage is recorded under the model that
answered. A follow-up's steps fall back the same way, and so does a split
review's summary call ([large reviews](configuration.md#the-agent)).

`confidence.fallback` does the same for the scorer's call: on the
confidence model's provider it goes to the provider with the call, on
another provider it gets the call once the confidence model's attempts
are spent, which get at most half of the time the score has left, and the
call is recorded, charged and counted under the model that answered. It
is set wherever `confidence.model` is, a repository's `.kritika.yaml`
included, under the same provider rules, and `confidence.effort` applies
to it too.

## Effort

`review.effort` and `confidence.effort` set how hard a model reasons, as
the providers' reasoning effort: `none`, `minimal`, `low`, `medium`,
`high`, `xhigh` or `max`, lowest first. `review.effort` is the review
model's, for reviews and follow-ups alike, and the fallback that takes a
step reasons as hard; `confidence.effort` is the scorer's, and its
fallback's. Unset, each is
the provider's default. The review is where reasoning pays off, and the
score is a short call that a lower level answers for less:

```yaml
review:
  model: openrouter/vendor/large-model
  effort: high
confidence:
  model: openrouter/vendor/small-model
  effort: low
```

| Where                 | What it sets                                                                                                                            |
| --------------------- | --------------------------------------------------------------------------------------------------------------------------------------- |
| nothing               | the provider's default, for both                                                                                                        |
| a scope               | its level, like any [repository setting](configuration.md#repository-settings-and-repositories); a `.kritika.yaml` replaces the admin's |
| a scope, `effort: ""` | the provider's default again, where a broader scope set a level                                                                         |

Each provider type gets the level in its own form:

| Type                 | Sent as                | Notes                                                                                               |
| -------------------- | ---------------------- | --------------------------------------------------------------------------------------------------- |
| `openrouter`         | `reasoning.effort`     | OpenRouter maps a level a model lacks to the nearest it takes, for each model in the request's list |
| `openai`, `opencode` | `reasoning_effort`     | a model that takes no reasoning effort refuses the request; leave its effort unset                  |
| `chatgpt`           | `reasoning.effort`     | the model must support the chosen level                                                            |
| `anthropic`          | `output_config.effort` | the Messages API runs from `low` to `max`, so `none` and `minimal` go out as `low`; unset as above  |

The environment sets both (`KRITIKA_REVIEW_EFFORT`, `KRITIKA_CONFIDENCE_EFFORT`,
[environment variables](configuration.md#environment-variables)), as do
the chart's `reviewEffort` and `confidenceEffort`. The dashboard's
repository page shows each repository's effective levels beside its
models.

## The embedder

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

## Local models

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

## OpenCode Go and Zen

OpenCode Go and OpenCode Zen are one gateway with an OpenAI-compatible
chat completions API that routes requests, and caches prompts, by a
per-conversation header, `x-opencode-session`, and refuses a request
without one. A provider of type `opencode` sends it: a review's steps
name their run, or the run whose conversation they carry on
([incremental reviews](configuration.md#incremental-reviews)), and a
follow-up names its mention. Its `baseUrl` is Go's,
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
