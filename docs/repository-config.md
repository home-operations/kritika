# `.kritika.yaml` reference

A repository may commit an optional `.kritika.yaml` at its root to tune how
kritika reviews it. It is read from the merge-base commit, never the pull
request's own tree, so a pull request cannot use its own copy to weaken the
review applied to it. kritika reads it before the review starts, and applies
it to follow-ups (from the pull request's merge base) and to indexing (from
the commit indexed) too.

The file holds nothing secret: no field takes a credential, a URL, a host
or a secret reference, and a model it names is a `<provider>/<model>` of a
provider an admin configured.

Repositories may also declare review tools in
[`.kritika/mise.toml`](#repository-tools), when an admin enables them.

[`kritika.schema.json`](kritika.schema.json) is its JSON Schema. An editor
using the YAML language server validates the file as it is written when
its first line names the schema:

```yaml
# yaml-language-server: $schema=https://kritika.home-operations.com/kritika.schema.json
```

## What it may set

The file takes the review keys the configuration file's root and its
repository entries take, in the same groups:

```yaml
review:
  model: openrouter/anthropic/claude-opus-5.5
trigger:
  exclude:
    - expr: pr.draft
    - { name: skip-label, expr: 'pr.labels.exists(l, l.name == "skip-review")' }
ignore: ["web/src/generated/**", "docs/**"]
comments: { inline: true }
rules:
  - {
      id: wrap-errors,
      rule: 'Wrap errors with fmt.Errorf("<package>: %w", err).',
      paths: ["**/*.go"],
    }
  - { id: house-style, file: .kritika/review.md }
  - id: renovate
    rule: Say what the update breaks, from the release notes in the body.
    when: [{ expr: pr.headRef.startsWith("renovate/") }]
context:
  - { path: ARCHITECTURE.md, description: how the services fit together }
skills:
  scope:
    review-renovate-pr:
      when: [{ expr: pr.headRef.startsWith("renovate/") }]
    migrations: { paths: ["db/migrations/**"] }
    go-reference: { paths: ["**/*.go"], load: false }
```

| Key                                                          | What it sets                                                       | Against the admin's value                       |
| ------------------------------------------------------------ | ------------------------------------------------------------------ | ----------------------------------------------- |
| `enabled`                                                    | `false` stops reviews, follow-ups and indexing                     | can only turn the repository off                |
| [`review.model`, `review.fallback`](#models)                 | the models the review and follow-ups run on                        | replaces                                        |
| [`review.effort`](#models)                                   | how hard they reason                                               | replaces                                        |
| `review.fixes`                                               | `true` requires a suggested fix on every finding                   | can only turn it on                             |
| [`review.approve`](#approvals)                               | `true` approves a pull request the review allows                   | replaces, either way                            |
| [`review.diagram`](#flow-diagrams)                           | `true` draws the change's flow in the summary                      | replaces, either way                            |
| [`confidence.*`](#confidence)                                | the score that judges a review, and the models that give it        | replaces; `risk` may only be lowered            |
| [`trigger.include`, `trigger.exclude`](#trigger-conditions)  | which pull requests are reviewed                                   | judged beside the admin's lists                 |
| `comments.inline`                                            | `false` posts the summary alone, without inline comments           | replaces                                        |
| [`comments.summary`, `comments.finding`](#comment-templates) | templates for the summary and inline comments                      | replaces                                        |
| [`ignore`](#ignored-paths)                                   | paths the review and the index leave out                           | added                                           |
| [`rules`](#rules)                                            | checks the review makes                                            | added after; one under an admin's id is dropped |
| [`context`](#context)                                        | files that explain the code                                        | added after                                     |
| [`skills`](#skills)                                          | where the repository's Agent Skills live, and when each is offered | `paths` replaces; `scope` adds up               |

A value the file may not take, such as a model of an undeclared provider,
is dropped: the admin's value applies for that field, a note in the
review's summary says which field was dropped and what it may be, and the
rest of the file still applies. `agent`,
`trigger.settle`, `trigger.limit`, `review.incremental`, `review.cost`,
`confidence.instructions` and `limits` are the admin's alone; a file
naming one of them, or any other unknown key, does not parse.

### Models

`review.model` and `review.fallback` are each a `<provider>/<model>` of a
provider the instance or the repository's account declares, used for the
review and for follow-ups. A model of any other provider is dropped; the
account's limits bound what a choice can cost. A review whose model fails
goes on with the fallback, on the same provider or another, and so does a
follow-up.

`review.effort` and `confidence.effort` set how hard the review model and
the scorer, each with its fallback, reason, one of `none`, `minimal`, `low`,
`medium`, `high`, `xhigh` or `max` ([effort](models.md#effort)). Each
replaces the admin's; `""` restores the provider's default, and a value
that is no level is dropped.

### Approvals

`review.approve: true` has kritika approve a pull request its review
allows: one with nothing at P0 or P1, or, with a confidence
score, one whose score and risk allow it. It replaces the admin's in
either direction, so a repository can turn it on where the instance
leaves it off ([approvals](confidence.md#approvals)).

### Flow diagrams

With `review.diagram: true`, the summary draws the flow the change adds or
alters, a request path, a data flow or a state machine, as a Mermaid
flowchart or sequence diagram the forge renders, its nodes plain-language
steps rather than function names. Off unless set, since it costs output
tokens on every review, and it replaces the admin's in either direction.

- The model leaves it out when the change has no such flow, as for a
  version bump or a documentation change.
- kritika keeps only a flowchart, graph or sequence diagram under 4 KiB.
- A re-review of the commits since the last review is shown that review's
  diagram, to return as it is, redrawn where the new commits alter the
  flow, or empty once the flow is gone; when it answers with no diagram at
  all, kritika keeps the last one.

### Confidence

`confidence.model`, `confidence.fallback`, `confidence.effort`,
`confidence.threshold` and `confidence.gate` replace the admin's, the
threshold and the gate in either direction; the models are held to the
providers `review.model` is ([models](#models)), and the fallback takes
the scorer's call when the confidence model fails;
`confidence.risk` may only lower the admin's, and a higher one is dropped.
`confidence.instructions` is the admin's alone
([confidence and approvals](confidence.md)).

### Trigger conditions

`trigger.include` and `trigger.exclude` are conditions on the pull
request, each an `expr`, `paths` globs that hold when a changed path
matches one, or both, when both must hold, with an optional `name`. A pull
request is reviewed when one `include` holds, or there are none, and no
`exclude` holds.

- The lists are judged beside the admin's own: a pull request must pass
  both. A condition under a name one of the admin's has is dropped, and
  the review's summary says so.
- Each expression is compiled and smoke-tested against a sample pull
  request when the file is parsed, so a broken one is rejected rather than
  silently skipping every review.
- A condition on the pull request alone is decided before any runner
  starts; one with `paths` or `pr.lines` is decided once the pull request
  is fetched, before any model is called. The commit status of a review
  the lists keep out names the exclusion that held when it has a name.

See [the recipes](#include-and-exclude-recipes).

### Ignored paths

`ignore` adds path globs to the admin's own ignore list, for reviews and
indexing alike. A pull request whose every changed path is ignored, by
these, the admin's globs or kritika's defaults (vendored trees, lockfiles,
generated and minified code, source maps and logs), is skipped.

### Rules

`rules` are checks the review makes, added after the admin's:

| Key     | What                                                                                                                                                                                                                    |
| ------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `id`    | lowercase letters, digits and hyphens, at most 64 characters; findings cite the rule by it                                                                                                                              |
| `rule`  | the check itself, at most 2000 characters                                                                                                                                                                               |
| `file`  | instead of `rule`, a file read from the same merge-base tree whose content is the check                                                                                                                                 |
| `paths` | optional globs: the rule applies only when a changed path matches one, so checks for one part of the repository do not spend the room on changes elsewhere                                                              |
| `when`  | optional conditions as `trigger.include` takes them, each an `expr` with an optional `name`; the rule applies only to a pull request one of them holds for, such as `pr.headRef.startsWith("renovate/")` for Renovate's |

- A `when` condition is compiled and smoke-tested like a trigger
  condition, and one that fails to evaluate does not hold.
- A rule whose `id` an admin's rule has is dropped, and the review's
  summary says so.
- The rules a change matches are listed by id in the system prompt (and a
  follow-up's), a file rule under a heading of its own, within the
  [limits](#limits). A finding lists the ids of the rules it enforces,
  keeping only ones its review was given.

### Context

`context` lists files that explain the code, each a `path` with a
`description` and optional `paths` globs, added after the admin's. The
review or follow-up is pointed at each file to read it with its own tools,
from the **pull request's head**. A pull request can change or delete that
content. These are optional references, not instructions; requirements
that must survive changes in the pull request belong in [rules](#rules).
A file with `paths` applies only when a changed path matches one of them.
If the admin already lists the same path, the admin's description and
scope win, and the repository's entry is dropped with a summary note.
Paths are not checked for existence when offered; a missing file produces
a tool error if the reviewer tries to read it.

Reference pointers reserve up to a quarter of the user prompt budget.
An oversized list is shortened with a summary note; the prompt also says
when pointers were omitted, including in follow-ups.

### Skills

`skills` are the [Agent Skills](https://agentskills.io) the repository
keeps for its reviews. A skill is a folder holding a `SKILL.md`: YAML
frontmatter with a `name` (lowercase letters, digits and hyphens, at most
64 characters; the folder's name when unset) and a `description`, then
instructions, with any files it needs beside it. Each folder directly
under `.agents/skills` and `.claude/skills` that holds a `SKILL.md` is
one, with nothing to configure.

| Key                   | What                                                                                                                                                                                                                                                                      |
| --------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `skills.paths`        | the directories whose folders are skills, replacing the admin's; `paths: []` looks nowhere, which turns skills off for the repository                                                                                                                                     |
| `skills.scope.<name>` | narrows when the skill of that name is offered: its `paths` are globs, one of which a changed path must match, and its `when` conditions as a rule's, one of which must hold; with both, both must. A scope with `paths` or `when` gives a review the skill applies to its instructions in the system prompt, rather than its name to read; `load: false` offers it by name instead, and `load: true` gives a skill whose scope has neither whole to every review. Added to the admin's scopes, a skill named in both taking this file's |

- **Read from the merge base**, as this file and the rules are, so a pull
  request cannot add or rewrite a skill to steer its own review.
- **Offered by name, or given whole:** the system prompt lists each skill's
  name and description; the review reads a skill's instructions, or a file
  in its folder, with its `load_skill` tool when the skill fits the pull
  request, from the merge base too. A skill whose scope has `paths` or
  `when`, already deciding where it applies, skips that step, as does one
  whose scope says `load: true`: when its `paths` and `when` hold, the
  review starts with its instructions in the system prompt, and
  `load_skill` still serves the files in its folder. A scope that narrows
  without deciding, such as every Go change for a reference skill that
  only sometimes helps there, says `load: false` to keep it offered by
  name.
- **Left out, not fatal:** a skill past a [limit](#limits), or whose
  `SKILL.md` has no frontmatter, no description or a name another skill
  has, is left out and noted rather than failing the review.
- **No tools granted:** `allowed-tools` and every other frontmatter key is
  ignored. A skill guides a review and grants it no tool or command, and
  the review skips a step that needs one it was not given.
- **Reported:** the review's summary carries a note of the skills it was
  offered and the ones it read, such as
  `Skills offered: review-renovate-pr, go-style; read: review-renovate-pr`,
  and each read shows in its transcript; a skill given whole counts as
  read. A review offered commands through
  the run tool carries a note of the same shape beside it, such as
  `Commands offered: gh, helm; run: helm`.
- **Follow-ups** are answered by an agent with the review's tools and
  commands, under the same `agent` limits, in a runner of its own; it is
  offered no skills.
- **A worked example:** Sentry's
  [`gha-security-review`](https://github.com/getsentry/skills/blob/main/skills/gha-security-review/SKILL.md)
  skill reviews GitHub Actions workflows with an explicit threat model, a
  table of safe patterns not to flag and a concrete exploit required of
  every finding; its `references/` load through `load_skill` as any file
  in the folder does. Scope it with
  `skills.scope.gha-security-review.paths: [".github/workflows/**"]`, and
  check its exceptions against your own policy: it leaves first-party
  actions on version tags alone, which a repository that pins every
  action to a digest would not.

Rules, context and skills compared:

|                  | In the prompt                                                                  | Cited by findings |
| ---------------- | ------------------------------------------------------------------------------ | ----------------- |
| A rule           | whole, wherever it applies                                                     | yes, by id        |
| A `context` file | a pointer, with the configuration's description                                | no                |
| A skill          | its own description, until the review reads it; whole, when its scope loads it | no                |

### Agent files

Reviews and follow-up answers do not automatically load `AGENTS.md` or
`CLAUDE.md`. To offer one as reference material, list it under `context`
in `.kritika.yaml`:

```yaml
context:
  - path: AGENTS.md
    description: Repository architecture and coding conventions
  - path: web/AGENTS.md
    description: Frontend architecture and conventions
    paths: ["web/**"]
```

These follow the [context semantics](#context), including reading from the
pull request's head. Directory-specific files must be listed explicitly;
`paths` limits when each reference is offered. Name the file containing the
actual text: `@AGENTS.md` imports and symlinks are not automatically
followed, and tool paths are relative to the repository root.

If your repository relied on automatic agent-file loading, add the
references it needs before upgrading. Move requirements every applicable
review must enforce into [rules](#rules), and review procedures into
[skills](#skills). A rule can name a file containing review requirements;
keep coding-agent workflows such as committing and opening pull requests
out of that file. Skills are available to reviews only, not follow-up
answers; put requirements shared by both in rules. Unlisted agent files
are not discovered or reported, so there is no per-review migration warning.

### Comment templates

`comments.summary` and `comments.finding` are paths to Go
[text/template](https://pkg.go.dev/text/template) templates that replace
kritika's built-in summary and inline comment templates; an empty path
restores the built-in one where the admin set a template.

- **Helpers:** the [sprout](https://github.com/go-sprout/sprout) helpers
  tuppr and chaski expose: std, strings, conversion, encoding, numeric,
  slices, maps, regex, time, semver and reflect. Not env, filesystem,
  network, random, uniqueid, checksum or crypto, and not `set` or `unset`.
- **Refused actions:** `template`, `define` and `block`, so a template
  cannot read any file or call any other template.
- **Bounds:** rendering is bounded (loop iterations, bytes per function
  call, output size, a deadline), so a template cannot hang or exhaust
  memory; one that exceeds a bound falls back to the default, with a note
  in the summary.

The summary template's dot is the review:

| Field                                                                        | What                                                                                                                     |
| ---------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------ |
| `.Number`                                                                    | the pull request's number                                                                                                |
| `.HeadSHA`, `.HeadURL`                                                       | the head commit, and a link to it                                                                                        |
| `.HeadSubject`                                                               | the head commit's subject line, cut to 40 characters and escaped for Markdown                                            |
| `.Reviews`                                                                   | how many reviews of the pull request this one makes                                                                      |
| `.Model`                                                                     | the review model                                                                                                         |
| `.Effort`                                                                    | the effort it reasoned at, "" for the provider's default                                                                 |
| `.Cost`                                                                      | what the pull request's reviews have cost, "" unless the admin's `review.cost` is on                                     |
| `.AuthorIsBot`                                                               | whether a bot opened the pull request                                                                                    |
| `.Result.Summary.Headline`, `.Result.Summary.Take`, `.Result.Summary.Praise` | the review's headline, its take, and what it found good                                                                  |
| `.Result.Summary.Diagram`                                                    | Mermaid source for the flow the change adds or alters, "" unless `review.diagram` is on and the change has one           |
| `.Result.Summary.Impact`                                                     | optional list of evidence-backed merge consequences and specific operational checks; empty when none are established       |
| `.Result.Findings`                                                           | the findings, each as the inline template sees one; an earlier one an incremental re-review carried on unchecked has `.CarriedFrom`, the head it was last reported at, whose lines its `.URL` links |
| `.Counts.P0`, `.Counts.P1`, `.Counts.P2`, `.Counts.Total`                   | the findings by severity and all of them, those outside the diff and those carried on unchecked included                 |
| `.Unanchored`                                                                | the findings on lines the diff does not show                                                                             |
| `.HeldBack`                                                                  | an incremental re-review's new findings more than 3 lines from what changed since the last review, not counted           |
| `.Notes`                                                                     | the review's notes                                                                                                       |
| `.Incremental`, `.PriorHeadSHA`, `.PriorHeadURL`                             | whether the review covered only what changed since the last one, and that review's head                                  |
| `.Prior`                                                                     | the last review's findings this review listed as resolved, with `.Resolved`, then those dismissed, with `.Dismissed` and `.DismissReason` |
| `.Sources`                                                                   | links to what the review's commands fetched                                                                              |
| `.Incomplete`                                                                | why the head was not fully reviewed, "" otherwise                                                                        |
| `.Confidence`                                                                | nil unless the review was scored: `.Score`, `.Threshold`, `.Passed`, `.Risk`, `.Reason` and `.Model`                     |
| `.Approval`                                                                  | nil unless `review.approve` is on: `.Approved` and `.Reason`, "" when a confidence score approved it                     |
| `.WebURL`, `.PullURL`                                                        | the dashboard's origin, which serves the built-in template's severity badges, and the pull request's page on it, where its re-run badge points |

The inline template's dot is one finding, with the dashboard's origin
beside it:

| Field                        | What                                                             |
| ---------------------------- | ---------------------------------------------------------------- |
| `.Path`, `.Line`, `.EndLine` | where it is                                                      |
| `.Severity`, `.Category`     | how much it matters, and what kind of problem it is; `.Severity.Label` is its badge text, P0 to P2, and `.Severity.Badge .WebURL` the badge image, or the label in bold without a dashboard |
| `.WebURL`                    | the dashboard's origin, "" when it has no public URL             |
| `.Title`, `.Explanation`     | what it is                                                       |
| `.SuggestedFix`              | the fix, in prose                                                |
| `.Replacement`               | the lines that replace the finding's, for a one-click suggestion |
| `.AgentPrompt`               | a prompt a coding agent can apply the fix from                   |
| `.AgentPromptFence`          | a code fence longer than any in the prompt                       |
| `.Rules`                     | the ids of the rules it enforces                                 |
| `.URL`                       | a link to the lines at the head commit                           |
| `.ThreadURL`                 | a link to its inline comment thread, once one is posted          |

## Repository tools

A repository adds tools for its own reviews with a mise config of its
own. kritika reads `.kritika/mise.toml` and `.kritika/mise.lock` from the
merge base, for reviews and follow-ups, so a pull request changing either
file uses the previously merged version for its own review. Reading them
needs a runner image with mise; on one without, the review summary says
the file was not read. The repository's tools layer over the instance's and the
image's: an entry replaces the same tool, including its options, while
unmentioned tools remain available.

For example, `.kritika/mise.toml` can contain:

```toml
[tools]
helm = "4.3.0"
"github:home-operations/flate" = { version = "0.6.5", filter_bins = "flate" }

[env]
NO_COLOR = "1"
```

kritika keeps each `[tools]` entry's version and the options that select,
verify and extract its download: `os`, `filter_bins`, `asset_pattern`,
`additional_asset_patterns`, `matching`, `matching_regex`, `no_app`,
`version_prefix`, `url`, `format`, `strip_components`, `bin_path`, `bin`,
`rename_exe`, `exe`, `extract_all`, `symlink_bins`, `checksum`, `size`
and `platforms`, whose per-platform tables keep the same options. Every
other option is dropped with a summary note, among them `postinstall`,
`install_env`, `depends`, `api_url` and `headers`, as is any option whose
value holds a template: an install only downloads and unpacks, and the
lock pins the version, so a `url` must be literal. It keeps `[env]` values
only when they are literal strings under non-reserved names. Templates,
`_.` directives, non-string values and the reserved names listed under
[runner tools](security.md#runner-tools) are dropped with summary notes.
`[settings]`, `[tasks]` and hooks are also dropped with notes; other
sections are ignored. Only the admin controls mise settings, including
mirrors, and the repository cannot expand network access.

Generate and commit the lock alongside the config:

```sh
mise -C .kritika lock
```

Include the runner's platforms when generating the lock, for example:

```sh
mise -C .kritika lock --platform linux-x64,linux-arm64
```

A repository tool needs a lock entry for its selected version with a URL
and checksum for the runner's platform, unless the runner image already
has that version installed, in which case nothing is downloaded. Without
one it is not offered, and the review summary says why. Tools that need
downloading install on first use with `mise install --locked`; a failed
install fails that tool call without stopping the review. Release hosts
still need the admin's `egress.allow` permission.

Renovate's [mise manager](https://docs.renovatebot.com/modules/manager/mise/)
recognizes this config and supports lock updates. A minimal Renovate
configuration enabling periodic lock maintenance is:

```json
{
  "extends": ["config:recommended"],
  "lockFileMaintenance": { "enabled": true }
}
```

Keep `.kritika/mise.lock` in the same update as `.kritika/mise.toml`; the
commands above also regenerate it manually. The runner writes the
filtered merge-base config and its lock into its isolated `HOME`, outside
the checkout. The checkout contains the head's `.kritika/mise.toml` unless
ignored, but excludes lockfiles by default; that copy does not configure
the runner's mise.

## Include and exclude recipes

The `expr` of a `trigger.include` or `trigger.exclude` condition, like a
rule's `when` conditions, is a [CEL](https://cel.dev) expression over
`pr`:

| Field                                      | What                                                                                                                       |
| ------------------------------------------ | -------------------------------------------------------------------------------------------------------------------------- |
| `number`, `title`, `body`, `author`, `url` | the pull request                                                                                                           |
| `state`, `open`, `merged`, `draft`, `fork` | its state                                                                                                                  |
| `headRef`, `headSha`, `baseRef`            | its branches and head commit                                                                                               |
| `createdAt`                                | when it was opened                                                                                                         |
| `labels`                                   | its labels, each with a `name` and a `color`                                                                               |
| `event`                                    | what started the review (below)                                                                                            |
| `lines`                                    | the lines its diff adds and removes, paths the `ignore` globs match left out; trigger conditions only, not a rule's `when` |

| `event`                                  | What started the review                           |
| ---------------------------------------- | ------------------------------------------------- |
| `opened`, `reopened`, `ready_for_review` | the pull request opened, reopened or became ready |
| `synchronize`                            | a push                                            |
| `poll`                                   | a push kritika found without its webhook          |
| `labeled`, `unlabeled`                   | a label added or removed                          |
| `manual`                                 | a re-run from the dashboard, or `@<bot> review`   |

A condition's `paths` are globs, as on a rule: it holds when a changed
path matches one of them. With an `expr` too, both must hold. An
exclusion with `paths` skips a pull request that touches the paths at
all; `ignore` leaves the paths out of the review, and skips only a pull
request that changes nothing else.

A label change starts a review only of a head that has none yet: one the
lists kept out, or whose review failed. So removing `skip-review`, or
adding the label a condition asks for, has the pull request reviewed
without waiting for its next push.

Some conditions, each under `trigger`:

| To                                                                    | Write                                                                                     |
| --------------------------------------------------------------------- | ----------------------------------------------------------------------------------------- |
| Skip drafts                                                           | `exclude: [{ expr: pr.draft }]`                                                           |
| Skip anything labelled `skip-review`                                  | `exclude: [{ name: skip-label, expr: 'pr.labels.exists(l, l.name == "skip-review")' }]`   |
| Skip Renovate's pull requests                                         | `exclude: [{ expr: pr.author.startsWith("renovate") }]`                                   |
| Skip pull requests from forks                                         | `exclude: [{ name: forks, expr: pr.fork }]`                                               |
| Skip when the description asks to                                     | `exclude: [{ expr: 'pr.body.contains("[skip-review]")' }]`                                |
| Review only pull requests into `main`                                 | `include: [{ expr: pr.baseRef == "main" }]`                                               |
| Review when a pull request opens or is re-run, not on every push      | `include: [{ expr: 'pr.event in ["opened", "reopened", "ready_for_review", "manual"]' }]` |
| Review only pull requests that touch `src/**`                         | `include: [{ name: source, paths: ["src/**"] }]`                                          |
| Skip pull requests over 2000 changed lines                            | `exclude: [{ name: too-large, expr: pr.lines > 2000 }]`                                   |
| Never review automatically a pull request touching `db/migrations/**` | `exclude: [{ name: migrations, paths: ["db/migrations/**"] }]`                            |

The last holds only for automatic reviews in the admin's configuration: a
review someone asks for still runs. In `.kritika.yaml` the exclusion holds
for that one too.

A named exclusion shows in the skipped review's commit status:
`kritika: skipped (filtered: skip-label)`.

## Limits

A configuration file that fails to parse is ignored as a whole, and noted
rather than failing the review. Configuration and prompt limits below
produce summary notes when content is omitted:

| What                                                 | Limit                                     |
| ---------------------------------------------------- | ----------------------------------------- |
| `.kritika.yaml`, each rule file and comment template | 256 KiB                                   |
| those configuration files together                   | 1 MiB                                     |
| the rules in the prompt                              | 16 KiB of rule text, 32 KiB of rule files |
| skills                                               | 50 read                                   |
| a skill's description                                | 1024 characters                           |
| the skills' names and descriptions in the prompt     | 4 KiB                                     |
| the skills given whole in the prompt                 | 32 KiB; one past it is offered by name    |

Context file contents are read on demand by `read_file`, outside the
configuration-file limits above. Each read is capped at `agent.output`
(32 KiB by default), and blobs over 1 MiB are refused. A cut or refusal
is reported in the tool response, not as a summary note; line ranges let
the reviewer read more of a file within the blob limit.
