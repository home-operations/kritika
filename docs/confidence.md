# Confidence and approvals

Two opt-in features judge a reviewed pull request beyond its findings: a
second model's confidence score, which can gate the commit status, and
approvals, which kritika posts when the review allows it.

```yaml
review:
  approve: true
confidence:
  model: openrouter/other-vendor/model
  threshold: 4
  gate: true
  risk: medium
  instructions: anything under db/migrations is critical
```

## The score

With a `confidence.model`, a second model scores every reviewed pull
request from 0 to 5: how ready it is to merge, from the pull request's
description and diff, the findings the review reported, and the review's
account of itself: its summary, what it checked beyond the diff and the
sources it read. A concern of the scorer's own names what that account
leaves out, such as a release note no check covers, rather than guessing
at what the review read. A different vendor's model than the review's
makes it a second opinion.

| Key                       | Default                 | What                                                                                       | From a `.kritika.yaml`                     |
| ------------------------- | ----------------------- | ------------------------------------------------------------------------------------------ | ------------------------------------------ |
| `confidence.model`        | none: nothing is scored | the model that scores, a `<provider>/<model>` held to the same providers as `review.model` | replaces                                   |
| `confidence.fallback`     | none                    | the model that takes the scorer's call when the confidence model fails ([fallback](models.md#fallback)) | replaces                                   |
| `confidence.effort`       | the provider's          | how hard the scorer, and the fallback that takes its call, reasons ([effort](models.md#effort)) | replaces                                   |
| `confidence.threshold`    | 5                       | the score approvals need, and a gated commit status                                        | replaces, either way                       |
| `confidence.gate`         | off                     | `true` fails the commit status under the threshold, so it can be a required check          | replaces, either way                       |
| `confidence.risk`         | `low`                   | the highest [risk](#risk) a change may be rated and still be approved                      | may only lower it; a higher one is dropped |
| `confidence.instructions` |                         | the admin's guidance to the scorer on [rating risk](#risk) in your code                    | the admin's alone                          |

How the score is reached and used:

- **Findings cap it:** the findings set the most a pull request can score,
  however the scorer reads them: 2 with a blocking finding, 3 with an
  important one; nits take nothing off.
- **Dismissals:** a dismissed finding stops counting at the next review,
  which a push or `@<app slug> review` starts.
- **Unchanged rebases:** a bot's rebase that leaves its patch unchanged is
  skipped when its last review was scored, and keeps that score; one whose
  last review has no score is reviewed again.
- **Retries and fallback:** the scorer's call is tried again as a review's
  step is, with its provider's [`retries`](models.md#retries), and once
  those are spent `confidence.fallback` takes it, as `review.fallback`
  takes a review's step ([fallback](models.md#fallback)), all within the
  two minutes a score gets, the wait for a slot on the model included.
  With a fallback on another provider, the confidence model's attempts
  get at most half of what is left, so the fallback always gets a turn. A
  call that still fails leaves the review unscored. The scorer's fallback
  is its own, not `review.fallback`, which is often the review model
  through another provider: the second opinion stays one.
- **Cost:** the scorer's call counts towards the account's
  `tokensPerMonth`, and shows in the review's transcript. It asks for no
  prompt caching: no later call reads it back, and writing a prompt to a
  provider's cache can cost more than sending it uncached.
- **Prompt size:** the scorer is sent a prompt of
  [`agent.prompt`](configuration.md#the-agent) tokens, without tools, so a
  file left out of the prompt is one it never sees. A `confidence.model`
  with a smaller context window than the review's refuses a prompt it
  cannot take, and the review then has no score.

## The commit status

| Setting                          | The commit status                                                                         |
| -------------------------------- | ----------------------------------------------------------------------------------------- |
| no `confidence.model`            | reports success whatever the review found                                                 |
| a model, `confidence.gate` off   | reports the score and passes whatever it is                                               |
| a model, `confidence.gate: true` | fails under the threshold; a review the scorer did not answer for is an error, not a pass |

A score blocks a merge only where someone asked it to. The Reviews page
lists the [statuses' descriptions](reviews.md#the-commit-status).

## Risk

The same call rates the change's risk, how much damage it could do if the
review missed something. Authentication, authorization, secrets, billing,
data and its migrations, infrastructure, CI, public interfaces, and build
or runtime configuration are where damage tends to come from, but a change
there is rated by what it can do there, not by the area it touches:

| Risk       | What the change can do                                                                                                                          |
| ---------- | ----------------------------------------------------------------------------------------------------------------------------------------------- |
| `low`      | nothing that matters: documentation, tests, formatting, comments, and small changes that leave behavior alone                                   |
| `medium`   | a routine change that a revert undoes, such as ordinary application logic or a single setting                                                   |
| `high`     | a breaking or major change to something much else depends on                                                                                    |
| `critical` | lose or corrupt data, delete managed resources or secrets, widen access, expose a secret, or cause an outage that a revert does not quickly fix |

A dependency update is rated by what the dependency does and how far its
version moves, not as a class of its own:

| Update                                     | Risk                                                                                                                                                                                               |
| ------------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| major                                      | `medium` for an application nothing else depends on, `high` otherwise; `critical` when it can do what the critical row names, such as migrating stored data to a form an older version cannot read |
| minor                                      | `low` for an application nothing else depends on, `medium` for one that other things do, `high` for one much else depends on                                                                       |
| patch, or a new digest of the same version | `low`; `medium` for one much else depends on                                                                                                                                                       |

A version under 1.0 promises nothing from one release to the next, but
many projects, Helm charts among them, move its first non-zero component
for routine releases, so 0.1 to 0.2 and 0.0.1 to 0.0.2 rate as minor
updates, and at least `medium`. A new digest under a tag that names no
version, such as `latest`, rates as a minor update unless the description
shows the step. Release notes that show an update does more than its step
says, such as a breaking change in a minor release, rate it as the bigger
step; a digest pinned beside its tag, a bot author or a description that
calls the update safe never rates it below its step.

What the scorer cannot verify, such as an upstream change whose release
notes the description does not carry, may take the score down as a
concern of its own, but never raises the risk: risk is what the change can
do, however much of it could be checked.

A review of a new head is shown the risk the last review was rated, with
its reason, and keeps that rating unless the change now does something
the reason does not account for, so a second reading of the same change
does not move it. A re-run of the same head rates afresh, as does one
someone asked for with `@<app slug> review` or from the dashboard, and the
first review after kritika's scoring instructions or
`confidence.instructions` change.

`confidence.instructions` is plain guidance to the scorer on rating risk
in your code, such as "the media apps under `kubernetes/apps/default` are
low whatever moves" or "anything under `db/migrations` is critical"; it
refines the tables above and changes nothing else about the score. It is
the admin's alone, so a pull request cannot talk its own risk down. The
[`kritika_confidence_scores_total`](metrics.md) series counts the scored
reviews by score and risk, so what a change to the instructions does
shows over time.

Risk bears on approvals alone, never on the commit status: a risky change
that scores well passes its check and waits for a person.

## Approvals

With `review.approve: true`, kritika approves a pull request its review
allows, as a review pinned to the head it saw. Off unless set; a
repository's `.kritika.yaml` replaces the admin's in either direction, so
a repository can turn it on where the instance leaves it off.

| Setting               | A pull request is approved when                                                                                            |
| --------------------- | -------------------------------------------------------------------------------------------------------------------------- |
| no `confidence.model` | its review finds nothing blocking or important; nits alone do not withhold it                                              |
| a `confidence.model`  | its score reaches `confidence.threshold` and its risk is within `confidence.risk`; a review left unscored approves nothing |

So one threshold decides the approval and, where gated, the check: a
repository can have kritika approve what scores well without ever failing
a check, or fail the check and leave approving to people.

- **Withdrawn:** a later review of the same pull request whose verdict no
  longer allows it dismisses kritika's approval, as does a reviewer who
  stands as requesting changes. A head that moved while it was reviewed is
  left to its own review.
- **Merged or closed:** a review someone asks for of a merged or closed
  pull request neither approves it nor withdraws an approval.
- **Unchanged rebases:** a bot's unchanged rebase, skipped with the score
  it carries, has that score decide its approval the same way.
- **Reported:** the summary says which way it went: `Approved`, or
  `Not approved` with the reason.
