<div align="center">

# kritika

**Repository-aware AI pull request review for GitHub.**

[![CI](https://img.shields.io/github/actions/workflow/status/home-operations/kritika/ci.yaml?branch=main&label=ci)](https://github.com/home-operations/kritika/actions/workflows/ci.yaml)
[![Release](https://img.shields.io/github/actions/workflow/status/home-operations/kritika/release.yaml?branch=main&label=release)](https://github.com/home-operations/kritika/actions/workflows/release.yaml)
[![License](https://img.shields.io/github/license/home-operations/kritika)](https://github.com/home-operations/kritika/blob/main/LICENSE)

</div>

> [!WARNING]
> kritika is early and under active development: configuration and the APIs
> can still change from one release to the next, so read the
> [changelog](CHANGELOG.md) before upgrading. Schema changes arrive as
> migrations the leader applies.

kritika indexes a repository, reviews each pull request against that context,
posts one sticky summary comment plus inline findings and a commit status, and
answers follow-ups when the bot is @-mentioned. A pull request from a fork is
reviewed like any other unless the configuration excludes it. One deployment serves any
number of forge accounts, and every index, review and follow-up job runs in
its own Kubernetes Job pod that holds no provider key or App key.

📖 **Docs site: <https://kritika.home-operations.com/>**

## Features

- **Context beyond the diff.** Whole declarations the diff touches,
  definitions of identifiers on changed lines and callers of changed
  declarations, cut by tree-sitter, plus the most similar chunks from a
  VectorChord index of the default branch, and the issues the description
  says the pull request closes, which the review judges the change against.
- **An agent, not one prompt.** Each review is a bounded, read-only tool loop
  over the head commit, optionally with allowlisted commands (`gh`, `curl`,
  `fd`, `jq`, `rg`, `yq`) so it can read a dependency bump's release notes;
  `agent.steps: 1` makes it one call.
- **Fixes you can apply.** A finding offers its fix as a one-click suggestion,
  with a prompt a coding agent can apply it from.
- **Incremental reviews.** A later push is reviewed against what changed since
  the last review, an earlier finding it no longer finds has its thread
  resolved, `trigger.settle` folds a burst of force-pushes into one, and
  `trigger.limit` pauses a long-lived pull request's automatic reviews,
  as `@<bot> pause` does on request.
- **Follow-ups.** Someone with write access can @-mention the bot and get an
  answer in the thread, from an agent with a review's tools that reads the
  code and looks things up before it answers. Or reply `@<bot> dismiss <reason>` in a finding's
  thread to have it resolved and never raised again on that pull request;
  resolving the thread on the forge does the same, unless a push has
  changed the lines it was made on, which counts as fixed instead.
- **Reactions.** The pull request carries the bot's 👀 while a review
  runs and its 👍 once one is posted, and a mention the bot answers the
  same, so a list of pull requests shows which have been reviewed.
- **A confidence score, opt-in.** A second model scores each reviewed pull
  request from 0 to 5; a repository that gates on it has the commit status
  fail under its threshold, so it can be a required check.
- **Approvals, opt-in.** A repository or the instance can have a review that
  finds nothing at P0 or P1 approve the pull request, and a later
  review that does withdraw it. With a confidence score, a pull request is
  approved when its score and the risk of its change allow it.
- **Flow diagrams, opt-in.** The summary can draw the flow a change adds or
  alters as a Mermaid diagram, so a reviewer sees the path before reading
  the code.
- **Providers and limits.** OpenRouter, OpenAI, Anthropic, OpenCode and ChatGPT plan adapters, with
  per-account concurrency, daily review and monthly token caps. The provider
  credentials never enter a runner pod: the agent reaches its model through
  kritika's gateway.
- **Repository overrides.** A `.kritika.yaml`, read from the merge-base, can
  narrow the admin's settings and bring its own rules, context files and
  comment templates.
- **Skills.** A review is offered the [Agent Skills](https://agentskills.io)
  a repository keeps under `.agents/skills` and `.claude/skills`, by name and
  description, and reads one when it fits the pull request, or starts with
  one whose scope says it applies; they are read from the merge-base and
  grant no tool or command.
- **Configuration in git.** One YAML file holds the whole configuration,
  read at startup, and a change rolls the pods; secrets stay in Secrets,
  which reach kritika as environment variables.
- **Dashboard.** Sign-in, live review state, full model transcripts, the
  running configuration, repository on/off and an audit log.

## Installing

kritika ships as an OCI Helm chart, `oci://ghcr.io/home-operations/charts/kritika`.
The chart's [README](charts/kritika/README.md) lists every value, and
[Postgres with CloudNativePG](https://kritika.home-operations.com/database/)
sets up the database and its three roles. In short, it needs:

- a Postgres with [VectorChord](https://github.com/tensorchord/VectorChord)
  (and the pgvector it builds on) loaded, with an owner, an application
  and a runner role;
- the one public URL under `config.webUrl`, which the dashboard and
  GitHub's webhooks share;
- a way to sign in under `auth`;
- the configuration file under `configFile`.

It runs as one Deployment of `kritika serve`, one replica by default,
which creates a runner Job for each review, follow-up and index run; the
chart README's Topology section covers when to run two.

The [setup guide](https://kritika.home-operations.com/setup/) takes a
fresh instance through its GitHub App, model key and embedder to its first
review; the dashboard's setup checklist shows what is still missing.

### Security notes

- **Install kritika into a namespace of its own:** kritika's Role can
  create, patch and delete every Secret in the release namespace.
- **Turn on the chart's NetworkPolicy:** runner pods then reach the
  outside only through kritika's egress gateway.
- **Run runner Jobs under a sandboxed RuntimeClass** such as gVisor, since
  the pod parses untrusted content.

[Security](https://kritika.home-operations.com/security/) covers what a
runner holds, the gateway and the sandbox.

## Documentation

| Page                                                                                | What it covers                                                                              |
| ----------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------- |
| [Setup](https://kritika.home-operations.com/setup/)                                 | from install to the first review: the GitHub App, its permissions and its webhook           |
| [Configuration](https://kritika.home-operations.com/configuration/)                 | the configuration file: sign-in, GitHub Apps, repository settings, accounts and egress      |
| [Models](https://kritika.home-operations.com/models/)                               | providers and their keys, retries, fallback, the embedder, local models and OpenCode        |
| [`.kritika.yaml` reference](https://kritika.home-operations.com/repository-config/) | what a repository's own file can change                                                     |
| [Security](https://kritika.home-operations.com/security/)                           | hardening an install, what a runner holds, the egress gateway, runner tools and the sandbox |
| [Chart values](charts/kritika/README.md)                                            | every value of the Helm chart                                                               |
| [Reviews](https://kritika.home-operations.com/reviews/)                             | when a review runs, what it posts, and the commands that steer it                           |
| [Confidence and approvals](https://kritika.home-operations.com/confidence/)         | the confidence score, the commit status it can gate, risk, and when kritika approves        |
| [Dashboard](https://kritika.home-operations.com/dashboard/)                         | the setup checklist, the Configuration page, repository on/off and actions                  |
| [Metrics](https://kritika.home-operations.com/metrics/)                             | what kritika exports to Prometheus, and the chart's alerts and Grafana dashboard            |
| [Development](https://kritika.home-operations.com/development/)                     | building, testing, evaluation and the cluster loop                                          |

## License

[AGPL-3.0](LICENSE)
