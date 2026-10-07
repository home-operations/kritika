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
  feedback: standard
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
```

| Key                                                          | What it sets                                                       | Against the admin's value                       |
| ------------------------------------------------------------ | ------------------------------------------------------------------ | ----------------------------------------------- |
| `enabled`                                                    | `false` stops reviews, follow-ups and indexing                     | can only turn the repository off                |
| [`review.model`, `review.fallback`](#models)                 | the models the review and follow-ups run on                        | replaces                                        |
| [`review.feedback`](#feedback)                               | how much the review says                                           | replaces                                        |
| `review.fixes`                                               | `true` requires a suggested fix on every finding                   | can only turn it on                             |
| [`review.approve`](#approvals)                               | `true` approves a pull request the review allows                   | replaces, either way                            |
| [`review.diagram`](#flow-diagrams)                           | `true` draws the change's flow in the summary                      | replaces, either way                            |
| [`confidence.*`](#confidence)                                | the score that judges a review                                     | replaces; `risk` may only be lowered            |
| [`trigger.include`, `trigger.exclude`](#trigger-conditions)  | which pull requests are reviewed                                   | judged beside the admin's lists                 |
| `comments.inline`                                            | `false` posts the summary alone, without inline comments           | replaces                                        |
| [`comments.summary`, `comments.finding`](#comment-templates) | templates for the summary and inline comments                      | replaces                                        |
| [`ignore`](#ignored-paths)                                   | paths the review and the index leave out                           | added                                           |
| [`rules`](#rules)                                            | checks the review makes                                            | added after; one under an admin's id is dropped |
| [`context`](#context)                                        | files that explain the code                                        | added after                                     |
| [`skills`](#skills)                                          | where the repository's Agent Skills live, and when each is offered | `paths` replaces; `scope` adds up               |

A value the file may not take, such as an unknown feedback level or a
model of an undeclared provider, is dropped: the admin's value applies for
that field, a note in the review's summary says which field was dropped
and what it may be, and the rest of the file still applies. `agent`,
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

### Feedback

| `review.feedback`    | What the review reports                                                                                                                                                                                  |
| -------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `detailed` (default) | every line a maintainer could act on, smaller improvements, missing tests and questions included, each inline, with a one-click suggestion wherever the fix changes those lines or adds lines after them |
| `standard`           | the same review, with nits in the summary rather than inline                                                                                                                                             |
| `minimal`            | only what would stop the review: correctness, security and reliability findings; a finding of another category is dropped before it is posted                                                            |

Only nits move between levels: with `comments.inline` on, a blocking or
important finding a level keeps is posted inline wherever the diff shows
its line, and the summary lists every finding posted.

Every finding carries a category beside its severity, what kind of problem
it is: `correctness`, `security`, `performance`, `reliability`,
`maintainability` or `tests`. The comments show it, the dashboard filters
by it, and `kritika_findings_total` counts by it.

### Approvals

`review.approve: true` has kritika approve a pull request its review
allows: one with nothing blocking or important, or, with a confidence
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

`confidence.model`, `confidence.threshold` and `confidence.gate` replace
the admin's, the threshold and the gate in either direction;
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
review is pointed at each file to read it with its own tools. A file with
`paths` applies only when a changed path matches one of them.

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
| `skills.scope.<name>` | narrows when the skill of that name is offered: its `paths` are globs, one of which a changed path must match, and its `when` conditions as a rule's, one of which must hold; with both, both must. Added to the admin's scopes, a skill named in both taking this file's |

- **Read from the merge base**, as this file and the rules are, so a pull
  request cannot add or rewrite a skill to steer its own review.
- **Offered by name:** the system prompt lists only each skill's name and
  description; the review reads a skill's instructions, or a file in its
  folder, with its `load_skill` tool when the skill fits the pull request,
  from the merge base too.
- **Left out, not fatal:** a skill past a [limit](#limits), or whose
  `SKILL.md` has no frontmatter, no description or a name another skill
  has, is left out and noted rather than failing the review.
- **No tools granted:** `allowed-tools` and every other frontmatter key is
  ignored. A skill guides a review and grants it no tool or command, and
  the review skips a step that needs one it was not given.
- **Reported:** the review's summary carries a note of the skills it was
  offered and the ones it read, such as
  `Skills offered: review-renovate-pr, go-style; read: review-renovate-pr`,
  and each read shows in its transcript. A review offered commands through
  the run tool carries a note of the same shape beside it, such as
  `Commands offered: gh, helm; run: helm`.
- **Follow-ups** are answered by an agent with the review's tools and
  commands, under the same `agent` limits, in a runner of its own; it is
  offered no skills.

Rules, context and skills compared:

|                  | In the prompt                                   | Cited by findings |
| ---------------- | ----------------------------------------------- | ----------------- |
| A rule           | whole, wherever it applies                      | yes, by id        |
| A `context` file | a pointer, with the configuration's description | no                |
| A skill          | its own description, until the review reads it  | no                |

### Agent files

A review also adds to its instructions the repository's agent files: the
`AGENTS.md` of the root and of each directory above a changed path, or a
directory's `CLAUDE.md` where it has no `AGENTS.md`, read from the merge
base, within the [limits](#limits). They follow the rules in the prompt.

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
| `.Cost`                                                                      | what the pull request's reviews have cost, "" unless the admin's `review.cost` is on                                     |
| `.AuthorIsBot`                                                               | whether a bot opened the pull request                                                                                    |
| `.Result.Summary.Headline`, `.Result.Summary.Take`, `.Result.Summary.Praise` | the review's headline, its take, and what it found good                                                                  |
| `.Result.Summary.Diagram`                                                    | Mermaid source for the flow the change adds or alters, "" unless `review.diagram` is on and the change has one           |
| `.Result.Findings`                                                           | the findings, each as the inline template sees one                                                                       |
| `.Counts.Blocking`, `.Counts.Important`, `.Counts.Nit`                       | the findings by severity                                                                                                 |
| `.Unanchored`                                                                | the findings on lines the diff does not show                                                                             |
| `.Notes`                                                                     | the review's notes                                                                                                       |
| `.Incremental`, `.PriorHeadSHA`, `.PriorHeadURL`                             | whether the review covered only what changed since the last one, and that review's head                                  |
| `.Prior`                                                                     | the last review's findings this review did not report again, each with `.Resolved`, or `.Dismissed` and `.DismissReason` |
| `.Sources`                                                                   | links to what the review's commands fetched                                                                              |
| `.Incomplete`                                                                | why the head was not fully reviewed, "" otherwise                                                                        |
| `.Confidence`                                                                | nil unless the review was scored: `.Score`, `.Threshold`, `.Passed`, `.Risk`, `.Reason` and `.Model`                     |
| `.Approval`                                                                  | nil unless `review.approve` is on: `.Approved` and `.Reason`, "" when a confidence score approved it                     |
| `.WebURL`, `.PullURL`                                                        | the dashboard's origin, and the pull request's page on it, where the built-in template's re-run badge points             |

The inline template's dot is one finding:

| Field                        | What                                                             |
| ---------------------------- | ---------------------------------------------------------------- |
| `.Path`, `.Line`, `.EndLine` | where it is                                                      |
| `.Severity`, `.Category`     | how much it matters, and what kind of problem it is              |
| `.Title`, `.Explanation`     | what it is                                                       |
| `.SuggestedFix`              | the fix, in prose                                                |
| `.Replacement`               | the lines that replace the finding's, for a one-click suggestion |
| `.AgentPrompt`               | a prompt a coding agent can apply the fix from                   |
| `.AgentPromptFence`          | a code fence longer than any in the prompt                       |
| `.Rules`                     | the ids of the rules it enforces                                 |
| `.URL`                       | a link to the lines at the head commit                           |
| `.ThreadURL`                 | a link to its inline comment thread, once one is posted          |

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

A file that fails to parse is ignored as a whole, and noted rather than
failing the review. Past any other limit, kritika leaves out, or cuts,
what does not fit, and notes it in the summary:

| What                                             | Limit                                     |
| ------------------------------------------------ | ----------------------------------------- |
| `.kritika.yaml`, and each file it references     | 256 KiB                                   |
| all of them together                             | 1 MiB                                     |
| the rules in the prompt                          | 16 KiB of rule text, 32 KiB of rule files |
| skills                                           | 50 read                                   |
| a skill's description                            | 1024 characters                           |
| the skills' names and descriptions in the prompt | 4 KiB                                     |
| agent files                                      | 32 KiB                                    |
