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
| `confidence.effort`       | the provider's          | how hard the scorer reasons ([effort](models.md#effort))                                   | replaces                                   |
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
review missed something, from what the change does and not from where its
files live:

| Risk       | What the change is                                                                                      |
| ---------- | ------------------------------------------------------------------------------------------------------- |
| `low`      | documentation, tests, formatting, comments, and small changes with no effect on behavior that matters   |
| `medium`   | ordinary application or business logic                                                                  |
| `high`     | build or runtime configuration, modules much else depends on                                            |
| `critical` | authentication, authorization, secrets, billing, data migrations, infrastructure, CI, public interfaces |

A dependency update is rated by what the dependency does and how far its
version moves, not as a class of its own. A move of the version's first
non-zero component is a major update: 1.x to 2.x, but also 0.1 to 0.2 and
0.0.1 to 0.0.2, since a version under 1.0 promises nothing from one such
step to the next.

| Update         | Risk                                                                                                                                                             |
| -------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| major          | at least `high`; `critical` when the dependency is one the critical row names                                                                                    |
| minor or patch | `low` for an application nothing else depends on, `medium` for one that other things do, `high` for a module much else depends on or that the critical row names |

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
