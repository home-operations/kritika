---
title: Repository settings
---

A repository works without any configuration file. Add `.kritika.yaml` at its
root when you want custom review rules, ignored paths or trigger conditions.

Start small:

```yaml
# yaml-language-server: $schema=https://kritika.home-operations.com/kritika.schema.json
ignore: ["web/src/generated/**"]
rules:
  - id: wrap-errors
    rule: 'Wrap errors with fmt.Errorf("<package>: %w", err).'
    paths: ["**/*.go"]
```

kritika reads this file from the **merge base**, the shared ancestor of the pull
request and its target branch. A pull request cannot change its own review rules.
Merge a configuration change before expecting it to apply to later reviews.
Indexing uses the configuration at the commit being indexed.

The file contains no credentials. Models must use a provider the admin configured.
The [JSON Schema](https://kritika.home-operations.com/kritika.schema.json) provides editor completion and validation.
For custom tools, see [repository tools](#repository-tools).

## What it may set

Use only the settings you need. This table shows how each interacts with the
admin's policy; detailed examples follow.

| Key                                                          | What it sets                                                       | Against the admin's value                                                                                 |
| ------------------------------------------------------------ | ------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------- |
| `enabled`                                                    | `false` stops reviews, follow-ups and indexing                     | can only turn the repository off                                                                          |
| [`review.model`, `review.fallback`](#models)                 | the models the review and follow-ups run on                        | replaces                                                                                                  |
| [`review.effort`](#models)                                   | how hard they reason                                               | replaces                                                                                                  |
| `review.fixes`                                               | `true` requires a suggested fix on every finding                   | can only turn it on                                                                                       |
| [`review.approve`](#approvals)                               | `true` approves a pull request the review allows, `when` only some | replaces; its `when` holds beside the admin's                                                             |
| [`review.diagram`](#flow-diagrams)                           | `true` draws the change's flow in the summary                      | replaces, either way                                                                                      |
| [`confidence.*`](#confidence)                                | the score that judges a review, and the models that give it        | replaces; `risk` may only be lowered, `when` holds beside the admin's                                     |
| [`trigger.include`, `trigger.exclude`](#trigger-conditions)  | which pull requests are reviewed                                   | judged beside the admin's lists                                                                           |
| `comments.inline`                                            | `false` posts the summary alone, without inline comments           | replaces                                                                                                  |
| [`comments.summary`, `comments.finding`](#comment-templates) | templates for the summary and inline comments                      | replaces                                                                                                  |
| [`ignore`](#ignored-paths)                                   | paths the review and the index leave out                           | added                                                                                                     |
| [`rules`](#rules)                                            | checks the review makes                                            | added after; one under an admin's id is dropped                                                           |
| [`context`](#context)                                        | files that explain the code                                        | added after                                                                                               |
| [`skills`](#skills)                                          | where the repository's Agent Skills live, and when each is offered | `paths` replaces; `scope` adds up                                                                         |
| [`tools`](#repository-tools)                                 | the tools the reviews may run, beside the image's commands         | added; one providing an image command replaces it                                                         |
| [`review.agents`](#agents)                                   | agents that review beside the default pass, or replace it          | specialists added, one under an admin's name taking only rules and skills; the others replace the admin's |

A value the file may not take, such as a model of an undeclared provider,
is dropped. The admin's value applies for that field, and a note in the
review's summary says which field was dropped and what it may be. The rest
of the file still applies.

These keys are the admin's alone: `agent`, `trigger.limit`,
`review.incremental`, `review.cost`, `review.replies`, `comments.dashboard`,
`confidence.instructions`, `confidence.floors` and `limits`. A file that
names one of them, or any other unknown key, does not parse.

### Models

`review.model` and `review.fallback` are each a `<provider>/<model>`, where
the provider is one that the instance or the repository's account declares.
They are used for the review and for follow-ups. A model of any other
provider is dropped, and the account's limits bound what a choice can cost.

`review.model` replaces the admin's model and the admin's [agents that
replace the default pass](/models/#agents-that-replace-the-default-pass).
The file may write its own such agents. When a review's model fails, the
review goes on with the fallback, on the same provider or another. A
follow-up does the same.

`review.effort` sets how hard the review model reasons, and
`confidence.effort` sets how hard the scorer reasons. Each covers its
fallback too. The value is one of `none`, `minimal`, `low`, `medium`,
`high`, `xhigh` or `max` ([effort](/models/#effort)). Each setting
replaces the admin's. `""` restores the provider's default, and a value
that is not a level is dropped.

### Approvals

`review.approve: true` has kritika approve a pull request that its review
allows. That is one with no findings at P0 or P1, or, with a confidence
score, one whose score and risk allow it. The setting replaces the admin's
in either direction, so a repository can turn it on where the instance
leaves it off ([approvals](/confidence/#approvals)).

A block with `when` conditions, like a [rule's](#rules), approves only a
pull request that one of them holds for:

```yaml
review:
  approve:
    when:
      - name: small bumps
        expr: pr.bot && pr.labels.exists(l, l.name in ["type/patch", "type/minor", "type/digest"])
```

The block turns approval on, and its conditions hold beside the admin's. A
pull request must pass both, so the file cannot approve what the admin's
conditions keep out. `approve: true` also keeps the admin's conditions.

### Flow diagrams

With `review.diagram: true`, the summary draws the flow that the change adds
or alters: a request path, a data flow or a state machine. It is a Mermaid
flowchart or sequence diagram that the forge renders, and its nodes are
plain-language steps rather than function names. The diagram is off unless
set, since it costs output tokens on every review. The setting replaces the
admin's in either direction.

- The model leaves it out when the change has no such flow, as for a
  version bump or a documentation change.
- kritika keeps only a flowchart, graph or sequence diagram under 4 KiB.
- A re-review of the commits since the last review is shown that review's
  diagram. It returns the diagram as it is, redrawn where the new commits
  alter the flow, or empty once the flow is gone. When it answers with no
  diagram at all, kritika keeps the last one.

### Confidence

`confidence.model`, `confidence.fallback`, `confidence.effort`,
`confidence.threshold` and `confidence.gate` replace the admin's. The
threshold and the gate do so in either direction. The models are held
to the same providers as `review.model` ([models](#models)). The fallback
takes the scorer's call when the confidence model fails. `confidence.risk`
may only lower the admin's, and a higher one is dropped.

`confidence.gate` may be a block of `when` conditions, as `review.approve`
may. It then gates only a pull request that one of them holds for. It
replaces the admin's, conditions and all.

`confidence.when` lists conditions, as a rule's `when` does. One of them
must hold for a pull request to be scored, and the admin's conditions apply
as well. A pull request that the file's conditions leave unscored is
reviewed without a score. Where the admin asks for a score, that pull
request is also not approved, so the file cannot lift the admin's risk
policy by narrowing scoring.

`confidence.instructions` and `confidence.floors` are the admin's alone
([confidence and approvals](/confidence/)).

### Trigger conditions

`trigger.include` and `trigger.exclude` are conditions on the pull
request. Each condition is an `expr`, `paths` globs, or both, with an
optional `name`. `paths` globs hold when a changed path matches one. With
both, both must hold. A pull request is reviewed when no `exclude` holds
and either one `include` holds or there are none.

- The lists are judged beside the admin's own, and a pull request must
  pass both. A condition with the same name as one of the admin's is
  dropped, and the review's summary says so.
- Each expression is compiled and smoke-tested against a sample pull
  request when the file is parsed, so a broken one is rejected rather than
  silently skipping every review.
- A condition on the pull request alone is decided before any runner
  starts. One with `paths` or `pr.lines` is decided once the pull request
  is fetched, before any model is called. When the lists keep a review
  out, its commit status names the exclusion that held, if that exclusion
  has a name.

See [the recipes](#include-and-exclude-recipes).

### Ignored paths

`ignore` adds path globs to the admin's own ignore list, for reviews and
indexing alike. A pull request is skipped when every changed path is
ignored, whether by these globs, the admin's globs or kritika's defaults.
The defaults cover vendored trees, lockfiles, generated and minified code,
source maps and logs.

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
- The rules a change matches are listed by id in the system prompt, and
  in a follow-up's, within the [limits](#limits). A file rule goes under a
  heading of its own. A finding lists the ids of the rules it enforces,
  keeping only the ones its review was given.

### Context

`context` lists files that explain the code, added after the admin's. Each
entry is a `path` with a `description` and optional `paths` globs. The
review or follow-up is pointed at each file to read it with its own tools,
from the **pull request's head**. A pull request can change or delete that
content. These are optional references, not instructions. Requirements
that must survive changes in the pull request belong in [rules](#rules).
A file with `paths` applies only when a changed path matches one of them.
If the admin already lists the same path, the admin's description and
scope win, and the repository's entry is dropped with a summary note.
Paths are not checked for existence when offered. A missing file produces
a tool error if the reviewer tries to read it.

Reference pointers reserve up to a quarter of the user prompt budget.
An oversized list is shortened with a summary note; the prompt also says
when pointers were omitted, including in follow-ups.

### Skills

`skills` are the [Agent Skills](https://agentskills.io) the repository
keeps for its reviews. A skill is a folder that holds a `SKILL.md`, with
any files the skill needs beside it. The `SKILL.md` has YAML frontmatter,
then instructions. The frontmatter has a `name` and a `description`. The
`name` uses lowercase letters, digits and hyphens, at most 64 characters,
and is the folder's name when unset. Each folder directly under
`.agents/skills` and `.claude/skills` that holds a `SKILL.md` is a skill,
with nothing to configure.

| Key                   | What                                                                                                                                  |
| --------------------- | ------------------------------------------------------------------------------------------------------------------------------------- |
| `skills.paths`        | the directories whose folders are skills, replacing the admin's; `paths: []` looks nowhere, which turns skills off for the repository |
| `skills.scope.<name>` | narrows when the skill of that name is offered; added to the admin's scopes, and a skill named in both takes this file's              |

`skills.scope.<name>`: its `paths` are globs, and a changed path must match
one of them. Its `when` lists conditions as a rule's does, and one of them
must hold. With both, both must hold.

When a scope has `paths` or `when`, a review the skill applies to gets the
skill's instructions in the system prompt, rather than its name to read.
`load: false` offers the skill by name instead. With `load: true`, a skill
whose scope has neither `paths` nor `when` is given whole to every review.

- **Read from the merge base**, as this file and the rules are, so a pull
  request cannot add or rewrite a skill to steer its own review.
- **Offered by name, or given whole:** the system prompt lists each skill's
  name and description. When a skill fits the pull request, the review
  reads its instructions, or a file in its folder, with its `load_skill`
  tool, from the merge base too. A skill whose scope has `paths` or `when`
  skips that step, since the scope already decides where it applies. So
  does a skill whose scope says `load: true`. When such a skill's `paths`
  and `when` hold, the review starts with its instructions in the system
  prompt, and `load_skill` still serves the files in its folder. Some
  scopes narrow without deciding, such as every Go change for a reference
  skill that only sometimes helps there. Such a scope says `load: false`
  to keep the skill offered by name.
- **Left out, not fatal:** a skill past a [limit](#limits) is left out and
  noted rather than failing the review. So is a skill whose `SKILL.md` has
  no frontmatter, no description or a name another skill has.
- **No tools granted:** `allowed-tools` and every other frontmatter key is
  ignored. A skill guides a review but grants it no tool or command. The
  review skips a step that needs one it was not given.
- **Reported:** the summary's collapsed **Review context** block counts
  the skills the review was offered and names the ones it read. For
  example, it shows `1/2 skills` on the block's line and
  `Skills read: review-renovate-pr` inside it. Each read also shows in the
  review's transcript, and a skill given whole counts as read. Commands
  offered to a review through the run tool are counted the same way beside
  the skills, such as `1/2 commands` and `Commands run: helm`.
- **Follow-ups** are answered by an agent with the review's tools and
  commands, under the same `agent` limits, in a runner of its own. That
  agent is offered no skills.
- **A worked example:** Sentry's
  [`gha-security-review`](https://github.com/getsentry/skills/blob/main/skills/gha-security-review/SKILL.md)
  skill reviews GitHub Actions workflows with an explicit threat model and
  a table of safe patterns not to flag. It requires a concrete exploit for
  every finding. Its `references/` load through `load_skill`, as any file
  in the folder does. Scope it with
  `skills.scope.gha-security-review.paths: [".github/workflows/**"]`, and
  check its exceptions against your own policy. It leaves first-party
  actions on version tags alone, which a repository that pins every action
  to a digest would not.

Rules, context and skills compared:

|                  | In the prompt                                                                  | Cited by findings |
| ---------------- | ------------------------------------------------------------------------------ | ----------------- |
| A rule           | whole, wherever it applies                                                     | yes, by id        |
| A `context` file | a pointer, with the configuration's description                                | no                |
| A skill          | its own description, until the review reads it; whole, when its scope loads it | no                |

### Agents

Every review has a **default pass**: kritika's own instructions, on
`review.model`, with the repository's `rules` and `skills`.
`review.agents` is a list of agents beside it, each by its `name`:

```yaml
review:
  model: anthropic/claude-sonnet-5-5 # the default pass
  fallback: anthropic/claude-haiku-5-5
  effort: medium
  agents:
    - name: security # limited to categories: runs beside the others
      model: anthropic/claude-opus-5-5
      categories: [security]
      instructions: Review as an application security engineer.
      when: [{ name: auth, paths: ["**/auth/**", ".github/workflows/**"] }]
    - name: deep # every category: replaces the default pass
      model: anthropic/claude-opus-5-5
      effort: high
      when:
        - { name: migrations, paths: ["db/migrations/**"] }
        - { name: large, expr: pr.lines > 800 }
```

- **An agent with `categories` is a specialist.** It reviews the change,
  or the files its `paths` cover, for those categories alone. It runs as
  an agent of its own beside the default pass, when one of its `when`
  conditions holds, or always if it has none.
- **An agent without `categories` replaces the default pass** when its
  conditions hold. The first such agent whose conditions hold picks the
  review's model, as [agents that replace the default
  pass](/models/#agents-that-replace-the-default-pass) describes. It
  takes only `name`, `model`, `fallback`, `effort`, `when` and `enabled`.

A specialist takes:

| Key                           | What                                                                                                                                                                         |
| ----------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `name`                        | lowercase letters, digits and hyphens, at most 64 characters, not `default`; the comments credit a finding to it                                                             |
| `categories`                  | the only finding categories it may report; its schema and its submission check refuse the rest                                                                               |
| `instructions`                | what it reviews for, at most 8000 characters, added after kritika's own instructions                                                                                         |
| `file`                        | instead of `instructions`, a file read from the merge base whose content they are; the first 32 KiB is used                                                                  |
| `model`, `fallback`, `effort` | as `review.*`, for this agent; where unset, the default pass's, as the agents picked it                                                                                      |
| `paths`                       | the changed files it reads, every one when unset; a pull request that changes none of them is not reviewed by it                                                             |
| `when`                        | conditions as a trigger's, `{name, expr}` or `{name, paths}` with `pr.lines` usable: it runs only on a pull request one of them holds for                                    |
| `rules`                       | its checks, in place of the repository's, which are the default pass's                                                                                                       |
| `skills.scope`                | skill scopes laid over the repository's for this agent, so a skill can be given whole to it alone                                                                            |
| `agent`                       | `steps`, `tokens` and `timeout` (at least `1s` and at most `2h35m`) for its agent, or each of its parts; they may lower the repository's `agent` bounds and never raise them |
| `enabled`                     | `false` turns it off                                                                                                                                                         |

- **One review.** The agents' answers are merged into one summary
  comment, one commit status and one confidence score. Each inline finding
  names the agent that raised it, as in `P1 · Open redirect · security ·
  security`. The summary's **Review context** block lists the agents that
  ran. A review by the default pass alone names no agent, in that block or
  on its findings.
- **Duplicates merge.** When two agents raise the same thing (one path,
  overlapping lines, the same category or title), one comment is posted:
  the more severe finding, crediting both.
- **Split by agent.** A large diff is split into parts within each agent,
  and the run is sized and budgeted for every agent's parts. An agent
  whose parts did not all submit leaves the review
  [incomplete](/reviews/#the-commit-status). Its findings are posted, and
  the summary names the agent. A retry of the same job reuses the parts
  that did submit.
- **Judged over the whole pull request.** Which agents run, and on which
  model, is decided from the whole change before any model is called. So
  a pull request keeps its agents from push to push, and its author cannot
  talk one away.
- **Incremental per agent.** A re-review builds on the last one only for
  the agents the last one ran. An agent it did not run, say one whose
  `when` first holds on a later push, reviews its files whole, and none of
  its findings is held back.
- **Follow-ups.** A reply or mention in a finding's thread is answered by
  the specialist that raised the finding, with its instructions, rules and
  model, while that specialist is on. Otherwise the default pass answers.
- **On demand.** `@<bot> review <name>` runs that specialist alone,
  whatever its `paths` and `when`, and carries the other agents' findings
  on unchecked. `@<bot> review default` runs the default pass alone. For
  an agent without categories, `@<bot> review <name>` runs the default
  pass on that agent's model. A name that matches no agent that is on
  runs every agent that applies, with a note.
- **The admin's agents.** From this file, a specialist the admin defines
  can only gain rules, under ids of their own, and skill scopes, for
  skills it does not scope. Anything else written for it is dropped with
  a note. The file's agents without categories replace the admin's whole,
  as its `review.model` does, even when every one of them is off. They
  are dropped whole when one of them names a model of a provider the
  account may not use. They are also dropped whole when, together with
  the admin's specialists, they pass the [limit](#limits) on agents. An
  agent named `default` is dropped with a note, and the rest of the file
  applies. So is an agent under the name of an admin's agent of the other
  kind: a specialist where the admin's agent replaces the default pass,
  or the other way round.
- **Shared tools.** A specialist runs the repository's
  [tools](#repository-tools) and commands, as the default pass does. There
  is no per-agent tool list.
- **Model slots.** A review holds a slot of the account's
  [`limits.concurrency`](/configuration/#accounts) on the model of each
  specialist it runs, as it does on the default pass's model. It waits for
  those slots as it waits for the default pass's.

Each specialist spends tokens of its own: with two, a review costs about
three times as much. The dashboard's review timeline shows each part's
agent and cost, with a total by agent.

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

Context is reference material, not instructions. Put requirements every
applicable review must enforce into [rules](#rules), and review procedures into
[skills](#skills). A rule can name a file containing review requirements;
keep coding-agent workflows such as committing and opening pull requests
out of that file. Skills are available to reviews only, not follow-up
answers; put requirements shared by both in rules. An agent file that is
not listed is not offered as context, and no review warns that it is
missing.

### Comment templates

`comments.summary` and `comments.finding` are paths to Go
[text/template](https://pkg.go.dev/text/template) templates that replace
kritika's built-in summary and inline comment templates. Where the admin
set a template, an empty path restores the built-in one.

- **Helpers:** the [sprout](https://github.com/go-sprout/sprout) helpers
  that tuppr and chaski expose: std, strings, conversion, encoding, numeric,
  slices, maps, regex, time, semver and reflect. Not env, filesystem,
  network, random, uniqueid, checksum or crypto, and not `set` or `unset`.
- **Refused actions:** `template`, `define` and `block`, so a template
  cannot read any file or call any other template.
- **Bounds:** rendering is bounded (loop iterations, bytes per function
  call, output size, a deadline), so a template cannot hang or exhaust
  memory. A template that exceeds a bound falls back to the default, with
  a note in the summary.

The summary template's dot is the review:

| Field                                                                        | What                                                                                                                                                                                                          |
| ---------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `.Number`                                                                    | the pull request's number                                                                                                                                                                                     |
| `.HeadSHA`, `.HeadURL`                                                       | the head commit, and a link to it                                                                                                                                                                             |
| `.HeadSubject`                                                               | the head commit's subject line, cut to 40 characters and escaped for Markdown                                                                                                                                 |
| `.Reviews`                                                                   | how many reviews of the pull request this one makes                                                                                                                                                           |
| `.Model`                                                                     | the review model                                                                                                                                                                                              |
| `.Effort`                                                                    | the effort it reasoned at, "" for the provider's default                                                                                                                                                      |
| `.Route`                                                                     | the [agent that replaced the default pass](/models/#agents-that-replace-the-default-pass) and the condition that held, escaped for Markdown, "" for the default                                               |
| `.Cost`                                                                      | what the pull request's reviews have cost, "" unless the admin's `review.cost` is on                                                                                                                          |
| `.AuthorIsBot`                                                               | whether a bot opened the pull request                                                                                                                                                                         |
| `.Result.Summary.Headline`, `.Result.Summary.Take`, `.Result.Summary.Praise` | the review's headline, its take, and what it found good                                                                                                                                                       |
| `.Result.Summary.Diagram`                                                    | Mermaid source for the flow the change adds or alters, "" unless `review.diagram` is on and the change has one                                                                                                |
| `.Result.Findings`                                                           | the findings, each as the inline template sees one; an earlier finding that an incremental re-review carried on unchecked has `.CarriedFrom`: the head it was last reported at, whose lines its `.URL` links  |
| `.Counts.P0`, `.Counts.P1`, `.Counts.P2`, `.Counts.Total`                    | the findings by severity and all of them, those outside the diff and those carried on unchecked included                                                                                                      |
| `.Unanchored`                                                                | the findings on lines the diff does not show                                                                                                                                                                  |
| `.HeldBack`                                                                  | an incremental re-review's new findings more than 3 lines from what changed since the last review, not counted                                                                                                |
| `.Notes`                                                                     | the review's notes                                                                                                                                                                                            |
| `.Incremental`, `.PriorHeadSHA`, `.PriorHeadURL`                             | whether the review covered only what changed since the last one, and that review's head                                                                                                                       |
| `.Prior`                                                                     | the last review's findings this review listed as resolved, with `.Resolved`, then those dismissed, with `.Dismissed` and `.DismissReason`                                                                     |
| `.Sources`                                                                   | links to what the review's commands fetched                                                                                                                                                                   |
| `.Agents`, `.NamedAgents`                                                    | the [agents](#agents) the review ran, in order, and whether a specialist ran, alone or beside the default pass                                                                                                |
| `.Skills`, `.Commands`                                                       | the repository's skills and the run tool's commands the review was offered                                                                                                                                    |
| `.Incomplete`                                                                | why the head was not fully reviewed, "" otherwise                                                                                                                                                             |
| `.Confidence`                                                                | nil unless the review was scored                                                                                                                                                                              |
| `.Approval`                                                                  | nil unless `review.approve` is on: `.Approved` and `.Reason`, "" when a confidence score approved it                                                                                                          |
| `.WebURL`, `.PullURL`                                                        | the dashboard's origin, which serves the built-in template's severity badges, and the pull request's page on it, where its View and Re-run badges point; both "" when the admin's `comments.dashboard` is off |

`.Skills` and `.Commands` each have:

- `.Names`: every one offered
- `.Used`: the ones the review read or ran
- `.Named`: `.Used` sorted and cut to the first 10
- `.More`: how many `.Named` leaves out

`.Confidence` has:

- `.Score`, `.Threshold`, `.Passed` and `.Risk`
- `.Reason`: the scorer's own
- `.Explanation`: the reason, then how a floor held the risk when one did
- `.Rated` and `.Held`: the scorer's rating and the path that held it, ""
  when none did
- `.Model`

The inline template's dot is one finding, with the dashboard's origin
beside it:

| Field                        | What                                                                                                                                                                                        |
| ---------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `.Path`, `.Line`, `.EndLine` | where it is                                                                                                                                                                                 |
| `.Severity`, `.Category`     | how much it matters, and what kind of problem it is; `.Severity.Label` is its badge text, P0 to P2, and `.Severity.Badge .WebURL` the badge image, or the label in bold without a dashboard |
| `.WebURL`                    | the dashboard's origin, "" when the admin's `comments.dashboard` is off                                                                                                                     |
| `.Title`, `.Explanation`     | what it is                                                                                                                                                                                  |
| `.Agents`, `.AgentNames`     | the [agents](#agents) that raised it, none for the default pass alone, and their names joined for a comment, "" for the default pass alone                                                  |
| `.SuggestedFix`              | the fix, in prose                                                                                                                                                                           |
| `.Replacement`               | the lines that replace the finding's, for a one-click suggestion                                                                                                                            |
| `.AgentPrompt`               | a prompt a coding agent can apply the fix from                                                                                                                                              |
| `.AgentPromptFence`          | a code fence longer than any in the prompt                                                                                                                                                  |
| `.Rules`                     | the ids of the rules it enforces                                                                                                                                                            |
| `.URL`                       | a link to the lines at the head commit                                                                                                                                                      |
| `.ThreadURL`                 | a link to its inline comment thread, once one is posted                                                                                                                                     |

## Repository tools

A repository adds tools for its own reviews under `tools`. The key is read
from the merge base like the rest of the file, so a pull request that
changes it uses the previously merged version for its own review.

The key is mise's `[tools]` table. Each entry is a tool, named by its
[registry](https://mise.jdx.dev/registry.html) short name or by a backend
such as `github:owner/repo`. Its value is the version to run, or a table of
the version and the options that select, verify and extract its download.

The repository's tools add to the image's commands: `curl`, `find`, `gh`,
`grep` and `jq`. A tool that provides one of those commands replaces it,
`gh` excepted, while the others remain available. Reading the tools needs
a runner image with mise. On an image without it, the review summary says
the tools were not offered.

```yaml
tools:
  helm: "4.3.1"
  "github:home-operations/flate":
    version: "0.6.5"
    filter_bins: flate
```

A tool installs at the version given, on the first call that needs it,
which can take a moment. A failed install fails that tool call without
stopping the review. Release hosts need the admin's `egress.allow`
permission, and so do the hosts a backend verifies against
([runner tools](/security/#runner-tools)). Once the admin gives the
gateway a download cache, later runs reuse what a run locked
([the gateway's cache](/security/#the-gateways-download-cache)).

An entry may take these options: `os`, `filter_bins`, `asset_pattern`,
`additional_asset_patterns`, `matching`, `matching_regex`, `no_app`,
`version_prefix`, `url`, `format`, `strip_components`, `bin_path`, `bin`,
`rename_exe`, `exe`, `extract_all`, `symlink_bins`, `checksum`, `size`
and `platforms`. The per-platform tables of `platforms` (`linux-x64`,
`linux-arm64`) take the same options. Each option is typed as mise reads
it: a string, a list of strings, a boolean or a whole number, as the
[schema](https://kritika.home-operations.com/kritika.schema.json) spells
out. `url` is an `https://` URL.

An install only downloads and unpacks, so these fail the file:

- every other option, among them `postinstall`, `install_env`, `depends`,
  `api_url` and `headers`
- a value holding a template
- a name carrying options or a version (`tool[exe=x]`, `tool@4`)

For the same reason, only the `aqua`, `github`, `gitlab`, `http` and `ubi`
backends and the core tools are offered. A backend that builds or runs a
package manager or plugin (`go:`, `cargo:`, `npm:`, `pipx:`, `gem:`,
`dotnet:`, `spm:`, `asdf:`, `vfox:`) fails the file by name. A registry
short name that mise resolves to such a backend (`yarn`, for example) is
not offered, and a summary note says so.

The `http` backend is offered only by a registry short name whose commands
the registry names, such as `nomad-pack`. An `http:` name fails the file.
A tool declared by its backend names its commands with `filter_bins`, and
mise refuses `filter_bins` on `http`.

A core tool that would build from source when no prebuilt release fits
(`node`, `python`, `ruby` or `erlang`) fails to install instead.

mise `[settings]`, `[env]`, tasks and hooks have no place in the file. The
settings are the admin's, and the runner container's `env` in the chart's
`runner.podTemplate` sets the commands' variables.

Give each tool the number of the version to run, after an optional `v`.
Every other request is refused, since mise would resolve it anew on each
install and a review's tool would change without a commit. That includes
`latest`, `system`, `lts` and `lts/iron`, and the `prefix:`, `sub-`,
`path:`, `ref:`, `tag:`, `branch:` and `rev:` forms. A version such as
`"4"` is accepted but resolves to the newest match, so pin the full
version.

No lock is committed. Each run locks the tool itself before installing it,
then installs against that lock. The checksum comes from what the backend
publishes (a GitHub release's API digest or checksum asset, or an aqua
recipe's checksum file), or else from hashing the asset. Installing against
the lock also lets the gateway's download cache serve the next run. Pin a
download yourself with `checksum`, per platform where the asset differs.
mise refuses a download that does not match:

```yaml
tools:
  "github:home-operations/flate":
    version: "0.6.5"
    filter_bins: flate
    platforms:
      linux-x64: { checksum: "sha256:…" }
      linux-arm64: { checksum: "sha256:…" }
```

### Updating tools with Renovate

Renovate's [mise manager](https://docs.renovatebot.com/modules/manager/mise/)
reads mise's own files, not `.kritika.yaml`, so a
[custom regex manager](https://docs.renovatebot.com/modules/manager/regex/)
offers the updates instead. On the line above each tool, annotate the
datasource and package Renovate should follow. Do this for the plain form
and the table form alike:

```yaml
tools:
  # renovate: datasource=github-releases depName=helm/helm
  helm: "4.3.1"
  # renovate: datasource=github-releases depName=home-operations/flate
  "github:home-operations/flate":
    version: "0.6.5"
    filter_bins: flate
```

Then give Renovate the manager. It takes the version from the plain form,
a table's `version` line and an inline table alike, and compares it with
the release tag less its `v`:

```json
{
  "customManagers": [
    {
      "customType": "regex",
      "managerFilePatterns": ["/^\\.kritika\\.yaml$/"],
      "matchStrings": [
        "# renovate: datasource=(?<datasource>\\S+) depName=(?<depName>\\S+)(?: versioning=(?<versioning>\\S+))?\\n\\s*\"?[^\"\\n]+\"?:[ \\t]*(?:\\{[ \\t]*)?(?:\\n\\s+version:[ \\t]*)?\"?(?<currentValue>[^\"\\s,}]+)\"?"
      ],
      "versioningTemplate": "{{#if versioning}}{{{versioning}}}{{else}}semver{{/if}}",
      "extractVersionTemplate": "^v?(?<version>.+)$"
    }
  ]
}
```

A registry short name's package is the one the
[registry](https://mise.jdx.dev/registry.html) lists for it, and `mise
registry <name>` prints it. A tool pinned with `checksum` needs the
checksum updated with the version, which the manager does not do. Keep
such tools few, or update them by hand.

## Include and exclude recipes

The `expr` of a `trigger.include` or `trigger.exclude` condition, like a
rule's `when` conditions, is a [CEL](https://cel.dev) expression over
`pr`:

| Field                                      | What                                                                                                                                                     |
| ------------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `number`, `title`, `body`, `author`, `url` | the pull request                                                                                                                                         |
| `state`, `open`, `merged`, `draft`, `fork` | its state                                                                                                                                                |
| `bot`                                      | whether a bot opened it, as the forge marks one, or a Forgejo or Gitea connection lists its login under `bots`                                           |
| `headRef`, `headSha`, `baseRef`            | its branches and head commit                                                                                                                             |
| `createdAt`                                | when it was opened                                                                                                                                       |
| `labels`                                   | its labels, each with a `name` and a `color`                                                                                                             |
| `event`                                    | what started the review (below)                                                                                                                          |
| `lines`                                    | the lines its diff adds and removes, paths the `ignore` globs match left out; trigger conditions and a `review.agents` entry's `when` only, not a rule's |

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
all. `ignore` leaves the paths out of the review, and skips only a pull
request that changes nothing else.

A label change starts a review only for a head that has none yet: one that
the lists kept out, or one whose review failed. So removing `skip-review`,
or adding the label a condition asks for, gets the pull request reviewed
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
rather than failing the review. The configuration and prompt limits below
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
| agents                                               | 8, the admin's and the file's together    |
| an agent's instructions                              | 8000 characters, or 32 KiB of its file    |

Context file contents are read on demand by `read_file`, outside the
configuration-file limits above. Each read is capped at `agent.output`
(32 KiB by default), and blobs over 1 MiB are refused. A cut or refusal
is reported in the tool response, not as a summary note. Line ranges let
the reviewer read more of a file within the blob limit.
