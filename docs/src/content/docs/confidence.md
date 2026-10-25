---
title: Confidence and approvals
---

These features are optional and off by default:

- **Confidence scoring** asks a second model how ready a pull request is to merge.
- **Gating** fails the commit status when the score is too low.
- **Automatic approval** posts an approval on the forge when the review meets your policy.

Scoring can run without gating or approvals. Approvals can also work without
a score. This example enables all three: it requires at least 4 out of 5 and
approves changes rated at most medium risk:

```yaml
review:
  approve: true
confidence:
  model: openrouter/other-vendor/model
  threshold: 4
  gate: true
  risk: medium
```

Use a model from a provider you configured. These settings belong under Helm's
`config`, or at the root of the application configuration file.

## The score

The scorer reads:

- the pull request description and diff
- the versions the change moves
- the findings and the review summary
- kritika's record of the review's run: the sources it read, the commands it
  ran or could not run, and the parts of a split review that went unreviewed

It returns a score from **0 to 5** and a reason. A model from a different
vendor can provide a second opinion.

- A **P0** finding caps the score at **2**.
- A **P1** finding caps it at **3**.
- **P2** findings do not reduce it by themselves.
- Dismissed findings stop counting at the next review.

The scorer can raise its own concerns when evidence backs them. Two things do
not lower the score by themselves: what the summary says merging sets off,
and post-deployment checks.

Scoring uses tokens and adds cost. The scorer has no tools and sees only the
supplied prompt. Since there are no tool outputs to leave room for, the
prompt may take twice the agent's `agent.prompt` budget. It is capped at
96,000 tokens, but is never less than the `agent.prompt` budget itself. A
diff cut to fit names the files it does not show, and the scorer is told to
rate those files by their paths and say so.

If retries and `confidence.fallback` fail, the review remains unscored. The
scorer gets two minutes, including time waiting for a model slot. Once the
account's `tokensPerMonth` is spent, the scorer is not called and the review
remains unscored.

## The commit status

| Configuration             | Result after a completed review                             |
| ------------------------- | ----------------------------------------------------------- |
| No confidence model       | Success, regardless of findings.                            |
| Scoring enabled, gate off | Shows the score and passes.                                 |
| Scoring enabled, gate on  | Fails below the threshold; errors if no score is available. |

A pull request that [`confidence.when`](#only-some-pull-requests) leaves out
is reviewed as if no confidence model were set: it is not scored, and the
gate does not hold it. A `when` block in place of `gate: true` gates only
the pull requests that one of its conditions holds for. Any other pull
request shows its score and passes.

To block merging, also make **Kritika / Review** a required check on the forge.
Risk does not affect this check. A high-risk change can pass its score gate
without receiving an automatic approval.

## Approvals

Set `review.approve: true` to enable approvals.

| Configuration        | Approves when…                                                                |
| -------------------- | ----------------------------------------------------------------------------- |
| No confidence model  | There are no P0 or P1 findings.                                               |
| Confidence model set | The score meets `confidence.threshold` and risk is at most `confidence.risk`. |

These reviews do not approve:

- an unscored review, when a confidence model is configured
- a split review whose parts did not all get reviewed
- a review of a head that the pull request moved on from while the review
  ran

A later review that no longer qualifies withdraws kritika's approval. That
includes a review that finds a reviewer has requested changes. On its own, a
change request withdraws nothing until that later review. Reviews of merged
or closed pull requests neither approve nor withdraw an approval.

The summary says **Approved** or **Not approved**. **Not approved** gives a
reason, and so does **Approved** when no confidence score decided it. A
repository's `.kritika.yaml` can turn approvals on or off, and add
conditions of its own.

### Only some pull requests

These settings narrow scoring, the gate and approval to the pull requests
that one of their conditions holds for:

- `confidence.when` narrows scoring.
- A `when` block in place of `confidence.gate: true` narrows the gate.
- A `when` block in place of `review.approve: true` narrows approval.

Each is a list of conditions, as a [rule's](repository-config.md#rules) is:
an `expr` over [`pr`](repository-config.md#include-and-exclude-recipes) with
an optional `name`. To score every pull request, gate only Renovate's, and
approve only its small bumps:

```yaml
confidence:
  threshold: 4
  gate:
    when:
      - name: renovate
        expr: pr.headRef.startsWith("renovate/")
review:
  approve:
    when:
      - name: small bumps
        expr: pr.bot && pr.labels.exists(l, l.name in ["type/patch", "type/minor", "type/digest"])
```

To score Renovate's pull requests alone instead, saving the scorer's
calls on the rest, move the condition to `confidence.when` and write
`gate: true`.

- A condition is compiled and smoke-tested when the configuration loads.
  One that fails to evaluate does not hold. These conditions cannot read
  `pr.lines`; only trigger conditions and a `review.agents` entry's `when`
  can.
- A `gate` or `approve` block must list at least one condition: `true`
  gates or approves every pull request the review allows, and `false`
  none.
- The gate's conditions, like `confidence.when`, are judged as the review
  starts.
- Approval's conditions are judged as the review is posted, so a label
  added while the review ran counts. A pull request they do not hold for is
  never approved, whatever its review found. Any approval kritika gave it
  is dismissed; otherwise its summary says nothing about approval.
- `pr.bot` describes who opened the pull request, not who pushed to it. A
  commit that someone else pushes onto a bot's branch is approved as the
  bot's would be, when the review allows it.
- A repository's `confidence.gate` replaces the admin's, conditions and
  all, since a repository may already turn the gate off.
- A repository's `.kritika.yaml` adds conditions that must hold beside the
  admin's, and its `approve: true` keeps the admin's conditions. A pull
  request left unscored only by the file's `confidence.when` is not
  approved where a confidence model is set, even a model the file sets
  itself. The file may narrow scoring, but never lift the risk policy by
  doing so.
- If a pull request is closed by the time approval's conditions are judged,
  kritika neither approves it nor withdraws its approval.

## Risk

Risk describes the damage a change could cause if something was missed.
It controls automatic approval, independently of the confidence score.

| Risk       | Typical impact                                                                            |
| ---------- | ----------------------------------------------------------------------------------------- |
| `low`      | Documentation, tests, formatting or small changes that leave behavior alone.              |
| `medium`   | Routine application changes that a revert can undo.                                       |
| `high`     | Breaking changes to something other systems depend on, or expanded access.                |
| `critical` | Data loss, exposed secrets, administrative access or outages a revert cannot quickly fix. |

Dependency updates are rated by both the version step and the dependency's role.
Major changes and widely shared dependencies generally carry more risk. Breaking
release notes can raise that rating. Updates below 1.0 and unversioned tags need
extra care. Being bot-generated does not reduce an update's risk.

Admins can add local guidance with `confidence.instructions`, for example:

```yaml
confidence:
  instructions: anything under db/migrations is critical
```

Repository settings cannot change these instructions or raise the allowed risk.

The instructions guide the scorer; `confidence.floors` binds it. Each risk
level lists globs. A change that touches a matching path is rated at least
that level, whatever the scorer says. After the reason, the summary says
which path held the rating there:

```yaml
confidence:
  floors:
    critical: ["db/migrations/**"]
    high: ["charts/**", ".github/workflows/**"]
```

A floor never lowers a rating, and a change rated higher than its floor keeps
that rating. Floors apply to every path the change touches, deleted files
included. They apply again to the score an unchanged patch carries, so a
floor set after a review holds on the next rebase. Floors are the admin's
alone, as the instructions are.

A later push normally keeps the earlier risk rating unless the change's impact
changes. These trigger a fresh rating:

- manual re-runs
- re-runs of the same commit
- changes to scoring instructions
- a rating a floor held

## Settings reference

| Key                       | Default                            | What                                                                                                                                                                                                                 | From a `.kritika.yaml`                     |
| ------------------------- | ---------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------ |
| `confidence.model`        | none: nothing is scored            | the model that scores, a `<provider>/<model>` held to the same providers as `review.model`                                                                                                                           | replaces                                   |
| `confidence.fallback`     | none                               | the model that takes the scorer's call when the confidence model fails ([fallback](models.md#fallback))                                                                                                              | replaces                                   |
| `confidence.effort`       | the provider's                     | how hard the scorer, and the fallback that takes its call, reasons ([effort](models.md#effort))                                                                                                                      | replaces                                   |
| `confidence.threshold`    | 5                                  | the score approvals need, and a gated commit status                                                                                                                                                                  | replaces, either way                       |
| `confidence.gate`         | off                                | `true` fails the commit status under the threshold, so it can be a required check; a `when` block gates only the pull requests one of its conditions holds for ([only some pull requests](#only-some-pull-requests)) | replaces, either way                       |
| `confidence.when`         | none: every pull request is scored | conditions, one of which a pull request must hold for to be scored ([only some pull requests](#only-some-pull-requests))                                                                                             | adds conditions that must hold as well     |
| `confidence.risk`         | `low`                              | the highest [risk](#risk) a change may be rated and still be approved                                                                                                                                                | may only lower it; a higher one is dropped |
| `confidence.instructions` |                                    | the admin's guidance to the scorer on [rating risk](#risk) in your code                                                                                                                                              | the admin's alone                          |
| `confidence.floors`       | none                               | globs by risk level: a change touching a matching path is rated at least that level, whatever the scorer says ([risk](#risk))                                                                                        | the admin's alone                          |
