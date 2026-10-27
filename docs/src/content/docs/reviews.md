---
title: Reviews
---

kritika reviews enabled repositories when pull requests open or change. It
updates one summary comment and adds findings beside the relevant lines.

## Commands

Mention the bot by its login: your GitHub App's slug, or the username of
the bot or service account on another forge. You need write access to the
repository, and webhooks must be working.

| Comment                   | What it does                                                                           |
| ------------------------- | -------------------------------------------------------------------------------------- |
| `@<bot> <question>`       | Reads the code and answers in the thread.                                              |
| `@<bot> review`           | Requests a full review of the current commit.                                          |
| `@<bot> review <name>`    | Runs the [agent](/repository-config/#agents) of that name alone on the current commit. |
| `@<bot> pause`            | Stops automatic reviews for this pull request.                                         |
| `@<bot> resume`           | Resumes automatic reviews.                                                             |
| `@<bot> dismiss <reason>` | Dismisses a finding when posted in its thread.                                         |

`@<bot> review <name>` can name a specialist, the default pass
(`default`), or the default pass on the model of an agent that replaces
it. If the word names no agent that is on, every agent runs. If the
comment also asks for a plain review, it gets one. While a review of the
commit by other agents is queued or running, kritika says so and runs
nothing; ask again once it has finished.

A manual review also works on paused, filtered, merged or closed pull requests.
It does not start a duplicate review if the current commit is already queued
or running. Reviews of merged or closed pull requests post comments but do not
set a commit status or approve the pull request.

kritika handles at most five mentions per pull request per hour, including commands.

## What it posts

| Item          | What to expect                                                          |
| ------------- | ----------------------------------------------------------------------- |
| Summary       | One comment updated after each review.                                  |
| Findings      | Inline comments with an explanation and a suggested fix when available. |
| Commit status | `Kritika / Review` on the reviewed commit.                              |
| Reactions     | 👀 while working, 👍 when posted, 😕 on failure.                        |
| Approval      | Only when [automatic approvals](/confidence/#approvals) are enabled.    |

Reactions and automatic thread resolution need the relevant
[GitHub App permissions](/setup/#register-the-github-app).

### Findings

| Severity | Meaning                       |
| -------- | ----------------------------- |
| `p0`     | Must be fixed before merging. |
| `p1`     | Should be fixed.              |
| `p2`     | Optional polish.              |

Findings also have a category: correctness, security, performance, reliability,
maintainability or tests. With [agents](/repository-config/#agents) beside
the default pass, a finding that a specialist raised also names the agents
that raised it. A finding that the default pass alone raised names none. The
summary includes findings outside the visible diff, which cannot get an
inline comment.

A later review can mark a finding fixed. To dismiss one you disagree with,
reply `@<bot> dismiss <reason>` in its thread. Later reviews of that pull
request will not raise it again.

Resolving the thread on GitHub also dismisses it, unless GitHub marks the
thread outdated because its lines changed. In that case, resolution counts
as a fix; use the dismiss command if you want a lasting dismissal.
Unresolving a thread takes back its dismissal. Without Contents write
permission, kritika records dismissals but leaves thread resolution to you.
On Forgejo, kritika neither reads nor resolves threads, so the dismiss
command is the way, as [Git forges](/forges/#what-works-where) lists. On GitLab
and Gitea, kritika resolves a fixed finding's thread, but resolving one
yourself sends no event, so it does not dismiss the finding.

### Replies in finding threads

With [`review.replies`](/configuration/#what-only-the-admin-sets) on,
kritika also reads a reply in one of its finding threads that does not
mention it. The reply must come from someone with write access and be
made while the thread is unresolved. kritika answers when the reply needs
the reviewer: a question about the finding, a disagreement with it, or a
claim about the code worth checking against the head. It posts nothing for
an acknowledgement, a message meant for someone else, or a point the thread
already settled. The dashboard records why. When the thread convinces
kritika that the finding was wrong, its reply says so. The finding is then
dismissed as `@<bot> dismiss` would dismiss it, and the thread is resolved.
This works on GitHub alone, for the reason
[Git forges](/forges/#what-works-where) gives.

Such replies count against the five follow-ups per pull request per hour,
whether or not a reply is posted. Once kritika has replied three times in
a thread, it leaves the thread to a mention. A reply that mentions the bot
is a mention wherever it is made.

### Dependency bumps

A change that moves a version, whoever wrote it, gets a procedure of its
own. kritika lists each version the diff moves: each line rewritten with
only a version, tag or digest changed, with lockfiles left out. Then it
tells the review to:

- classify the step as major, minor, patch or a digest alone. A move under
  1.0 counts as a minor step. So does a new digest under a tag that names
  no version, unless the image's labels show otherwise;
- find the upstream from the manifest rather than the description, and
  read there what changed between the two versions. That can be release
  notes by tag with `gh`, a chart's annotations, an image's source label
  with `fetch_oci`, or the diff between the versions with `fetch_repo`.
  The description's notes are a start, not the source: GitHub cuts long
  descriptions, and Renovate and Dependabot embed notes only when they
  found them;
- search this repository for every symbol, flag, configuration key or
  default that the notes say changed, and report what the change does to
  each usage. Report it on the usage's line when the diff shows the usage,
  and otherwise on the line that moves the version, naming the usage. A
  usage that no longer builds or no longer does what the code intends is
  `p0`, even where a build would catch it. A usage that the new version
  serves only by a path it deprecates and announces it will remove is
  `p1`. Any other new deprecation on a used symbol is `p2`. A change that
  no usage meets is no finding, and goes under what the review checked;
- read major and minor steps first when a grouped update has many. In the
  [summary](#the-summary-comment), name the notes it could not read, or
  left unread, as a check to make, never as a finding and never as safe;
- never propose adopting what the new version adds, except where this
  repository works around its absence: a comment citing the dependency's
  issue or pull request, such as `google/go-github#1234`, that the notes
  close, or a usage the notes say the new version makes unnecessary. That
  is a `p2` finding on the usage's line, naming the note. A feature that no
  such code meets is no finding.

A version number alone never sets a severity. The
[confidence scorer](/confidence/#risk) rates the step's risk separately.

### The summary comment

With [confidence scoring](/confidence/) on, the comment's heading is the
score, and the change's [risk](/confidence/#risk) leads the scorer's reason
below it. The approval decision, when enabled, comes next. Then come the
findings, whose suggested fixes are in their inline comments, and then the
summary of the change. The comment also includes:

- earlier findings that were fixed, dismissed or not rechecked;
- findings an incremental re-review held back, folded away;
- what the change does well, left out when a bot opened the pull request;
- expandable review context, including the agents that ran, sources, skills
  and commands used;
- a footer with the number of reviews, the last reviewed commit, the model
  and its effort, the agent that picked the model when one replaced the
  default pass, and the reviews' cost when `review.cost` is on;
- View and Re-run badges linking to the dashboard, when it has a public URL
  and `comments.dashboard` is on.

The summary says what the change does and how. Where the evidence the
review read shows it, the summary also says what merging sets off: what
takes effect and when, what restarts, what a revert cannot undo, or a
check to make afterwards. It does not repeat the findings. A suggested check
is not evidence that it passed. Optional
[flow diagrams](/repository-config/#flow-diagrams) and
[comment templates](/repository-config/#comment-templates) change
presentation.

### The commit status

**Findings alone do not fail the check.** To block merges based on a score,
enable [confidence gating](/confidence/#the-commit-status) and make the check
required on the forge.

| State   | Meaning                                                                             |
| ------- | ----------------------------------------------------------------------------------- |
| Pending | Review running.                                                                     |
| Success | Review posted and passed any configured score gate.                                 |
| Failure | Confidence score below the configured threshold.                                    |
| Error   | Review failed, was incomplete, was canceled, or a gated review could not be scored. |

Skipped reviews normally pass, as do reviews an account's limits capped. If a
skipped review carries an earlier score, that score still determines whether
it passes the gate.

## When a review runs

| Event                      | Behavior                                                                                |
| -------------------------- | --------------------------------------------------------------------------------------- |
| Open, reopen or mark ready | Reviews the current commit unless already reviewed.                                     |
| Push                       | Reviews changes since the previous review, or the full pull request for larger updates. |
| Add or remove a label      | Can trigger a first review or retry a failed one.                                       |
| Poll                       | Catches updates missed by webhooks.                                                     |
| Manual review              | Reviews the whole pull request again.                                                   |

Drafts, forks and bot pull requests are included unless
[trigger rules](/configuration/#which-pull-requests-are-reviewed) exclude them.
An unchanged bot rebase is usually skipped; it is reviewed again if scoring
is enabled but the previous review had no score.

Automatic reviews stop when the pull request closes, is paused, or reaches
`trigger.limit`. See [incremental reviews](/configuration/#incremental-reviews)
for how later reviews handle earlier findings.

### Overlapping reviews

When a newer review starts, it replaces an older running review of the same
pull request. Queued work for an outdated commit is skipped. The dashboard
links canceled reviews to their replacements; money already spent still counts.

If posting a completed review fails, kritika retries publication from the
saved result without calling the model again. Infrastructure interruptions
can restart a runner and repeat unfinished model work. A runner that fails
before its first model call, on its fetch for instance, is retried with
backoff and repeats none. Split reviews save each completed part and reuse
it on retries of the same job when the review inputs, model policy and
prompts still match. A manual re-review starts fresh. The dashboard marks
reused parts; their usage remains charged to the original run. Check the
dashboard's [Queue](/dashboard/#queue) for retries and errors.
