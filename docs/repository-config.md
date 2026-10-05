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
repository entries take, in the same groups. It narrows what an
admin allows, adds to the review's rules and context, and replaces the
rest:

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
    whenExpr: pr.headRef.startsWith("renovate/")
context:
  - { path: ARCHITECTURE.md, description: how the services fit together }
```

- `enabled: false`: stops reviews, follow-ups and indexing for the
  repository. It cannot turn a disabled repository back on.
- `review.model` / `review.fallback`: a `<provider>/<model>` of a
  provider the instance or the repository's account declares, used for
  the review and for follow-ups. A model of any other provider is
  dropped; the account's limits bound what a choice can cost. A review
  whose model fails goes on with the fallback, on the same provider or
  another; a follow-up uses a fallback on its own provider alone.
- `review.feedback`: how much the review says, replacing the
  admin's.

| `review.feedback`    | What the review reports                                                                                                                                                                |
| -------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `detailed` (default) | every line a maintainer could act on, smaller improvements, missing tests and questions included, each inline, with a one-click suggestion wherever the fix changes those lines or adds lines after them |
| `standard`           | the same review, with nits in the summary rather than inline                                                                                                                           |
| `minimal`            | only what would stop the review: correctness, security and reliability findings; a finding of another category is dropped before it is posted                                        |

  A `blocking` finding is always posted inline, and the summary lists
  every finding. Every finding carries a category beside its severity,
  what kind of problem it is: `correctness`, `security`, `performance`,
  `reliability`, `maintainability` or `tests`; the comments show it, the
  dashboard filters by it, and `kritika_findings_total` counts by it.

- `comments.inline: false`: posts the summary alone, without inline
  comments.
- `comments.summary` / `comments.finding`: paths to Go
  [text/template](https://pkg.go.dev/text/template) templates that replace
  kritika's built-in summary and inline comment templates; an empty path
  restores the built-in one where the admin set a template. They use the
  [sprout](https://github.com/go-sprout/sprout) helpers tuppr and chaski
  expose (std, strings, conversion, encoding, numeric, slices, maps, regex,
  time, semver and reflect; not env, filesystem, network, random, uniqueid
  or checksum, and not `set` or `unset`). The `template`, `define` and
  `block` actions are refused, so a template cannot read any file or call
  any other template. The summary template's dot is the review (`.Number`,
  `.HeadSHA`, `.HeadURL`, `.Model`, `.AuthorIsBot`, `.Result.Summary.Headline`, `.Result.Summary.Take`,
  `.Result.Summary.Praise`, `.Result.Findings`,
  `.Counts.Blocking`/`.Important`/`.Nit`, `.Unanchored`, the findings on
  lines the diff does not show, `.Notes`, `.Incremental`, `.PriorHeadSHA`,
  `.PriorHeadURL`, `.Prior`, the last review's findings this review did
  not report again, each with `.Resolved`, and the dismissed ones each with
  `.Dismissed` and `.DismissReason`, `.Sources`, `.Incomplete`, `.Confidence`, nil unless the review was
  scored, with its `.Score`, `.Threshold`, `.Passed`, `.Risk`, `.Reason` and `.Model`, and `.WebURL` and `.PullURL`, the
  dashboard's origin and the pull request's page on it, where the built-in
  template's re-run badge points). The inline template's dot is
  one finding (`.Path`, `.Line`, `.EndLine`, `.Severity`, `.Category`, `.Title`,
  `.Explanation`, `.SuggestedFix`, `.Replacement`, `.AgentPrompt`, `.Rules`,
  the ids of the rules it enforces, `.URL`, a link to the lines at the head
  commit, and `.ThreadURL`, a link to its inline comment thread once one
  is posted). Rendering is bounded (loop iterations, bytes per function
  call, output size, a deadline), so a template cannot hang or exhaust
  memory; one that exceeds a bound falls back to the default with a note
  in the comment.
- `review.fixes: true`: findings must include a suggested fix. The
  file can turn the requirement on, never off.
- `review.approve: true`: a review that finds nothing blocking or important
  approves the pull request, as a review pinned to the head it saw; nits
  alone do not withhold it. Where a confidence score is asked for, the
  score decides instead: the pull request is approved when its score
  reaches `confidence.threshold` and its risk is within `confidence.risk`,
  and a review left unscored approves nothing. A later review of the same
  pull request whose verdict no longer allows it dismisses kritika's
  approval, as does a reviewer who stands as requesting changes; a head
  that moved while it was reviewed is left to its own review. It replaces
  the admin's, in either direction: a repository turns it on where the
  instance leaves it off. Off unless set.
- `confidence.model` / `confidence.threshold`: the model that scores a
  reviewed pull request from 0 to 5, a `<provider>/<model>` held to the
  same providers as `review.model`, and the score its commit status needs
  to pass; each replaces the admin's, the threshold in either direction.
  `confidence.risk` is the highest risk a change may be rated and still be
  approved, and may only lower the admin's; a higher one is dropped.
  See [the configuration](configuration.md#repository-settings-and-repositories)
  for how a score is reached.
- `trigger.include` / `trigger.exclude`: conditions on the pull request,
  each `{ expr }` with an optional `name`. A pull request is reviewed when
  one `include` holds, or there are none, and no `exclude` holds. The
  lists are passed beside the admin's own: a pull request must pass both.
  A condition under a name one of the admin's has is dropped, and the
  review's summary says so. Each expression is compiled and smoke-tested
  against a sample pull request when the file is parsed, so a broken one
  is rejected rather than silently skipping every review. A review the
  lists keep out ends before any runner starts, and its commit status
  names the exclusion that held when it has a name. See
  [the recipes](#include-and-exclude-recipes).
- `trigger.ignore`: path globs added to the admin's own ignore list, for
  reviews and indexing alike. A pull request whose every changed path is
  ignored, by these, the admin's globs or kritika's defaults (vendored
  trees, lockfiles and generated code), is skipped.
- `rules`: checks the review makes, added after the admin's. Each has an `id` (lowercase letters,
  digits and hyphens, at most 64 characters) that findings cite it by,
  and either the `rule` itself (at most 2000 characters) or a `file`,
  read from the same merge-base tree, whose content is the check; optional `paths`
  globs apply it only when a changed path matches one, so checks for one
  part of the repository do not spend the room on changes elsewhere. An
  optional `whenExpr`, a CEL expression over the `pr` that a
  `trigger.include` condition sees, applies it only to a pull request it is true of, such as
  `pr.headRef.startsWith("renovate/")` for Renovate's; it is compiled and
  smoke-tested like a condition, and a rule whose `whenExpr` fails to
  evaluate is left out. A rule whose `id` an admin's rule has is
  dropped, and the review's summary says so. The rules a change matches
  are listed by id in the system prompt (and a follow-up's), a file rule
  under a heading of its own, within 16 KiB of rule text and 32 KiB of
  rule files, and a finding lists the ids of the rules it enforces,
  keeping only ones its review was given.
- `context`: files that explain the code, each a `path` with a
  `description` and optional `paths` globs, added after the admin's. The
  review is pointed at each file to read it with its own tools. A file
  with `paths` applies only when a changed path matches one of them.

A review also adds to its instructions the repository's agent files: the
`AGENTS.md` of the root and of each directory above a changed path, or a
directory's `CLAUDE.md` where it has no `AGENTS.md`, read from the merge
base, within 32 KiB. They follow the rules in the prompt.

A value the file may not take, such as an unknown feedback level or a
model of an undeclared provider, is dropped: the admin's value applies for
that field, a note in the review's summary says which field was dropped
and what it may be, and the rest of the file still applies. `agent`,
`trigger.settle`, `trigger.limit`, `trigger.lines`,
`review.incremental`, `confidence.instructions`, `limits` and `runner`
are the admin's alone; a file naming one of them, or any other unknown key, does not
parse.

## Include and exclude recipes

The `expr` of a `trigger.include` or `trigger.exclude` condition, like a
rule's `whenExpr`, is a [CEL](https://cel.dev) expression over `pr`, which has the pull request's `number`, `title`,
`body`, `author`, `state`, `open`, `merged`, `draft`, `fork`, `headRef`,
`headSha`, `baseRef`, `url`, `createdAt` and `labels` (each with a `name`
and a `color`), and `event`, what started the review: `opened`,
`reopened`, `ready_for_review`, `synchronize` (a push), `poll` (a push
kritika found without its webhook), `labeled` or `unlabeled` (a label
added or removed) or `manual` (a re-run from the dashboard).

A label change starts a review only of a head that has none yet: one the
lists kept out, or whose review failed. So removing `skip-review`, or
adding the label a condition asks for, has the pull request reviewed
without waiting for its next push.

Some conditions, each under `trigger`:

- Skip drafts:

  ```yaml
  exclude:
    - expr: pr.draft
  ```

- Skip anything labelled `skip-review`:

  ```yaml
  exclude:
    - { name: skip-label, expr: 'pr.labels.exists(l, l.name == "skip-review")' }
  ```

- Skip Renovate's pull requests:

  ```yaml
  exclude:
    - expr: pr.author.startsWith("renovate")
  ```

- Skip pull requests from forks:

  ```yaml
  exclude:
    - { name: forks, expr: pr.fork }
  ```

- Skip when the description asks to:

  ```yaml
  exclude:
    - expr: pr.body.contains("[skip-review]")
  ```

- Review only pull requests into `main`:

  ```yaml
  include:
    - expr: pr.baseRef == "main"
  ```

- Review when a pull request opens or is re-run, not on every push:

  ```yaml
  include:
    - expr: pr.event in ["opened", "reopened", "ready_for_review", "manual"]
  ```

A named exclusion shows in the skipped review's commit status:
`kritika: skipped (filtered by .kritika.yaml: skip-label)`.

## Limits

A file that fails to parse is ignored as a whole, and noted rather than
failing the review. Every referenced file, plus `.kritika.yaml` itself, is
capped at 256 KiB, and 1 MiB in total; a file over either limit is skipped
and noted rather than failing the review.
