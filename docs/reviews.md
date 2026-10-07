# Reviews

What kritika does on a pull request: when it reviews one, what it posts,
and the commands that steer it.

## When a review runs

| What happens                                           | What kritika reviews                                                                                                                              |
| ------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------- |
| The pull request opens, reopens or is ready for review | its head, unless a review has already seen it                                                                                                     |
| A push                                                 | the new head, once `trigger.settle` has passed: only what changed since the last review, unless more files moved than `review.incremental` allows |
| A label is added or removed                            | the head, only if it has no review yet: the trigger lists kept it out, or its review failed                                                       |
| A poll finds a push the webhook missed                 | the new head                                                                                                                                      |
| `@<bot> review`, or a re-run from the dashboard        | the head, even one the admin's trigger lists keep out or that is paused                                                                           |

Drafts, forks and bots' pull requests are reviewed like any other unless a
`trigger.exclude` condition keeps them out
([which pull requests are reviewed](configuration.md#which-pull-requests-are-reviewed)).
A bot's rebase that leaves its patch unchanged is skipped, unless a
confidence score is asked for and its last review has none. A pull request
stops getting automatic reviews after `trigger.limit` of them, or once
someone comments `@<bot> pause`.

## What it posts

| What              | Where                                                                                                                                                                |
| ----------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| The summary       | one comment, which each review edits                                                                                                                                 |
| Findings          | inline comments on the lines they concern, each with its severity, category, explanation, a one-click suggestion where it has a fix, and a prompt for a coding agent |
| The commit status | `Kritika / Review`, on the head commit                                                                                                                               |
| Reactions         | 👀 on the pull request while a review runs, 👍 once one is posted; a mention kritika answers gets the same ([permissions](setup.md#register-the-github-app))         |
| An approval       | with `review.approve` on ([approvals](confidence.md#approvals))                                                                                                      |

### Findings

A review comments on every line a maintainer could act on: bugs and risks,
and also smaller improvements, missing tests and questions. Each finding
is posted inline wherever the diff shows its line, with a one-click
suggestion wherever the fix changes those lines or adds lines after them,
unless `comments.inline` is off; the summary lists every finding.

Every finding carries a category beside its severity, what kind of problem
it is: `correctness`, `security`, `performance`, `reliability`,
`maintainability` or `tests`. The comments show it, the dashboard filters
by it, and `kritika_findings_total` counts by it.

### The summary comment

From the top, each part shown only when it has something to say:

1. The headline and the count of findings by severity.
2. The confidence score and risk, with the scorer's reason, when the
   review was scored.
3. **Approved**, or **Not approved** with the reason, when
   `review.approve` is on.
4. **Findings**, each linking to its lines and its inline thread; **Outside
   the diff**, findings on lines the diff does not show; and **Earlier
   findings**, the last review's findings since resolved or dismissed.
5. **Summary**, the review's take, with the change's flow as a Mermaid
   diagram when `review.diagram` is on.
6. **What's good**, left out on a bot's pull request.
7. **Sources consulted**, links to what the review's commands fetched, and
   notes, such as files left out of the prompt or a `.kritika.yaml` value
   that was dropped.
8. The footer: `Reviews (3) · Last reviewed commit: "subject" · <model>`,
   ending with the pull request's cost when the admin turns `review.cost`
   on.

The re-run badge at the top opens the pull request's page on the
dashboard. A repository can replace the comment with its own template
([`comments.summary`](repository-config.md#comment-templates)).

### The commit status

| State   | Description                                                                                                       | When                                                                  |
| ------- | ----------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------- |
| pending | `kritika: review running`                                                                                         | a review is running                                                   |
| success | `kritika: 2 finding(s)`, or `kritika: confidence 4/5, 2 finding(s)` when scored                                   | the review posted, and its score passed or is not gated               |
| failure | `kritika: confidence 2/5, below 4, 3 finding(s)`                                                                  | `confidence.gate` is on and the score is under the threshold          |
| error   | `kritika: review failed`, `kritika: review incomplete (<reason>)`, `kritika: confidence not scored, 2 finding(s)` | the review failed, or a gated review went unscored                    |
| success | `kritika: skipped (<reason>)`, such as `skipped (filtered: skip-label)`                                           | the review was skipped; a score it carries still decides under a gate |

## Commands

Mention the bot by its App's slug in a comment on the pull request. Only
someone with write access to the repository gets an answer.

| Comment                   | What it does                                                                                                                                                                                            |
| ------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `@<bot> <question>`       | An agent with a review's tools and commands reads the code, looks things up, and answers in the thread.                                                                                                 |
| `@<bot> review`           | Queues a review of the head, even one the admin's trigger lists keep out, such as a fork's where forks are excluded, or a paused pull request's.                                                        |
| `@<bot> pause`            | Stops the pull request's automatic reviews.                                                                                                                                                             |
| `@<bot> resume`           | Starts them again.                                                                                                                                                                                      |
| `@<bot> dismiss <reason>` | As a reply in one of kritika's finding threads: resolves the thread, tells later reviews of the pull request not to raise the finding again, and lists it dismissed, with the reason, on the dashboard. |

Resolving one of kritika's finding threads on GitHub, as someone with
write access, dismisses its finding too, and unresolving it takes the
dismissal back. kritika resolves a
thread only when its App has write access to contents; otherwise it leaves
the thread open for a person to resolve.

kritika answers at most five mentions of a pull request an hour, commands
included, and says so once when it stops.
