# Models

kritika calls models through the providers the configuration file
declares, each with its own key. A model is named `<provider>/<model>`, on
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

| Key       | What                                                                                              |
| --------- | ------------------------------------------------------------------------------------------------- |
| `type`    | `openrouter`, `openai`, `anthropic` or `opencode`                                                 |
| `apiKey`  | its key, required                                                                                 |
| `baseUrl` | its API's URL, the type's default unless set                                                      |
| `pricing` | per model id, the prices of a provider that reports no cost ([local models](#local-models))       |
| `retries` | how many more times a failed model step is tried, from 0 to 5; 0 unless set ([retries](#retries)) |

The provider key never enters a runner pod: the agent reaches its model
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

`retries` is how many more times a review's model step, or the scorer's
call, is tried when the provider fails it in a way another attempt may get
past: a 5xx, a 429, a timeout or a cut connection.

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
  ([confidence](confidence.md#the-score)), shared the same way.

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
answered. A follow-up's steps fall back the same way.

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
