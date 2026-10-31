---
title: Reviews
---

kritika reviews enabled repositories when pull requests open or change. It
updates one summary comment and adds findings beside the relevant lines.

## Commands

Mention the bot by its login: your GitHub App's slug, or the username of
the bot or service account on another forge, such as Forgejo. You need write
access to the repository, and webhooks must be working.

| Comment                   | What it does                                                                           |
| ------------------------- | -------------------------------------------------------------------------------------- |
| `@<bot> <question>`       | Reads the code and answers in the thread.                                              |
| `@<bot> review`           | Requests a full review of the current commit.                                          |
| `@<bot> review <name>`    | Runs the [agent](/repository-config/#agents) of that name alone on the current commit. |
| `@<bot> review <focus>`   | Requests a review that looks hardest at what the rest of the line names.               |
| `@<bot> pause`            | Stops automatic reviews for this pull request.                                         |
| `@<bot> resume`           | Resumes automatic reviews.                                                             |
| `@<bot> dismiss <reason>` | Dismisses a finding when posted in its thread.                                         |

`@<bot> review <name>` can name a specialist, the default pass
(`default`), or the default pass on the model of an agent that replaces
it. A specialist is an agent limited to some categories. The default pass
runs kritika's own instructions.

If the word names no agent that is on, every agent runs. If the comment
also asks for a plain review, it gets one. While a review of the commit by
other agents is queued or running, kritika says so and runs nothing. Ask
again after that review finishes.

Anything else on the line after `review`, or after the agent's name, is
a focus: `@<bot> review the retry loop in the webhook parser` or
`@<bot> review security the token refresh`. kritika tells the review to
look hardest at what the focus names, in the requester's own words. The
summary's review context repeats the focus. A focus is a priority, not a
filter. The review still reports a concrete problem elsewhere in the diff,
and it takes nothing outside the focus as checked.

The focus ends at the end of the line or at the next mention. kritika cuts
the focus to 500 characters. A single word that names no agent is not a
focus. It asks for every agent, with a note. A plain `@<bot> review`
anywhere in the comment asks for every agent with no focus, whatever
another line of the comment names. While a review of the commit with
another focus, or another agent, is queued or running, kritika says so and
runs nothing.

A manual review also works on paused, filtered, merged or closed pull requests.
It does not start a duplicate review if the current commit is already queued
or running. Reviews of merged or closed pull requests post comments but do not
set a commit status or approve the pull request.

Before a review starts, kritika asks the forge whether the pull request is
still open. If the pull request merged or closed since the event that queued
an automatic review, kritika skips that review. An earlier attempt of the
same job can leave a pending status on the commit. While that status is still
the job's to set, the status then says that kritika skipped the review. A
manual request still runs.

kritika handles at most five mentions per pull request per hour. Commands
count toward this limit.

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
| `p1`     | We recommend that you fix it. |
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

Resolving the thread on GitHub also dismisses the finding, unless GitHub
marks the thread outdated because its lines changed. In that case, resolution
counts as a fix. If you want a lasting dismissal, use the dismiss command.
Unresolving a thread takes back its dismissal. Without Contents write
permission, kritika records dismissals but leaves thread resolution to you.

On Forgejo, kritika neither reads nor resolves threads, so the dismiss
command is the way, as [Git forges](/forges/#what-works-where) lists. On GitLab
and Gitea, kritika resolves a fixed finding's thread. Resolving one yourself
sends no event, so it does not dismiss the finding.

### Replies in finding threads

With [`review.replies`](/configuration/#what-only-the-admin-sets) on,
kritika also reads a reply in one of its finding threads that does not
mention it. The reply must come from someone with write access, and they
must make it while the thread is unresolved. kritika answers when the reply
needs the reviewer. That is a question about the finding, a disagreement with
it, or a claim about the code that is worth comparing with the head. The head
is the pull request's newest commit. kritika posts nothing for an
acknowledgement, a message meant for someone else, or a point that the thread
already settled. The dashboard records why.

When the thread convinces kritika that the finding was wrong, its reply says
so. kritika then dismisses the finding as `@<bot> dismiss` does, and resolves
the thread. This works on GitHub alone, for the reason that
[Git forges](/forges/#what-works-where) gives.

Such replies count against the five follow-ups per pull request per hour,
whether or not kritika posts a reply. After kritika replies three times in
a thread, it answers in that thread only when a reply mentions the bot. A
reply that mentions the bot is a mention wherever someone posts it.

### Dependency bumps

A change that moves a version, whoever wrote it, gets a procedure of its
own. kritika lists each version the diff moves: each line rewritten with
only a version, tag or digest changed, with lockfiles left out. Then it
tells the review to:

- Classify the step as major, minor, patch or a digest alone. A move under
  1.0 counts as a minor step. So does a new digest under a tag that names
  no version, unless the image's labels show otherwise.
- Find the upstream, the dependency's own repository, from the manifest
  rather than the description. Read there what changed between the two
  versions. That can be release notes by tag with `gh`, or a chart's
  annotations. It can also be an image's source label with `fetch_oci`, or
  the diff between the versions with `fetch_repo`. The description's notes
  are a start, not the source: GitHub cuts long descriptions, and Renovate
  and Dependabot embed notes only when they found them.
- Search this repository for every symbol, flag, configuration key or
  default that the notes say changed. Report what the change does to each
  usage. When the diff shows the usage, report it on the usage's line.
  Otherwise, report it on the line that moves the version, and name the
  usage. A usage that no longer builds or no longer does what the code
  intends is `p0`, even where a build catches it. A usage that the new
  version serves only by a path that it deprecates and announces it will
  remove is `p1`. Any other new deprecation on a used symbol is `p2`. A
  change that no usage meets is no finding, and goes under what the review
  checked.
- When a grouped update has many steps, read major and minor steps first.
  In the [summary](#the-summary-comment), name the notes that the review
  failed to read, or left unread, as a check to make. Never name them as a
  finding and never as safe.
- Never propose adopting what the new version adds, except where this
  repository works around its absence. This takes one of two forms. One
  form is a comment that cites the dependency's issue or pull request, such
  as `google/go-github#1234`, that the notes close. The other form is a
  usage that the notes say the new version makes unnecessary. Such a
  workaround is a `p2` finding, even though the code stays correct and
  unchanged. Anchor it as the step above anchors a usage finding. Name the
  note and the removal or replacement that the code can take. A feature
  that no such code meets is no finding.

A version number alone never sets a severity. The
[confidence scorer](/confidence/#risk) rates the step's risk separately.

### The summary comment

With [confidence scoring](/confidence/) on, the comment's heading is the
score, and the change's [risk](/confidence/#risk) leads the scorer's reason
below it. The approval decision, when enabled, comes next. Then come the
findings, whose suggested fixes are in their inline comments, and then the
summary of the change. The comment also includes:

- Earlier findings that were fixed, dismissed or not rechecked.
- Findings that an incremental re-review held back, folded away.
- What the change does well, left out when a bot opened the pull request.
- Expandable review context. It includes the agents that ran and the
  sources, skills and commands used. It also includes the repository's other
  open pull requests that change a file this one changes.
- A footer with the number of reviews, the last reviewed commit, and the
  model and its effort. When an agent replaced the default pass, the footer
  names the agent that picked the model. When `review.cost` is on, the footer
  has the reviews' cost.
- View and Re-run badges linking to the dashboard, when it has a public URL
  and `comments.dashboard` is on.

The summary says what the change does and how. Where the evidence the
review read shows it, the summary also says what merging sets off: what
takes effect and when, what restarts, what a revert cannot undo, or a
check to make afterwards. It does not repeat the findings. A suggested check
is not evidence that it passed.

The review context lists some other open pull requests. They are the ones
whose last review changed a file that this change changes, with the most
shared files first. They warn of merge conflicts and of work done twice.
kritika reads them from its own record of those reviews. So a pull request
that kritika did not review is not listed, and one whose head moved since
its last review is marked so.

kritika leaves out pull requests that a bot opened, because a bot rebases
its own. It also leaves out pull requests stacked with this one, that is, a
branch built on the other's, because they share files by design. kritika
tells the review itself nothing of the listed pull requests.

Optional [flow diagrams](/repository-config/#flow-diagrams) and
[comment templates](/repository-config/#comment-templates) change
presentation.

### The commit status

Findings alone do not fail the check. To block merges based on a score,
enable [confidence gating](/confidence/#the-commit-status). Then make the
check required on the forge.

| State   | Meaning                                                                                 |
| ------- | --------------------------------------------------------------------------------------- |
| Pending | Review running.                                                                         |
| Success | Review posted and passed any configured score gate.                                     |
| Failure | Confidence score below the configured threshold.                                        |
| Error   | Review failed, was incomplete, was canceled, or kritika failed to score a gated review. |

Skipped reviews normally pass, as do reviews an account's limits capped. If a
skipped review carries an earlier score, that score still determines whether
it passes the gate.

## When a review runs

| Event                      | Behavior                                                                                                                 |
| -------------------------- | ------------------------------------------------------------------------------------------------------------------------ |
| Open, reopen or mark ready | Reviews the current commit unless already reviewed.                                                                      |
| Push                       | Reviews changes since the previous review, or the full pull request for larger updates, after `trigger.settle` when set. |
| Add or remove a label      | Can trigger a first review or retry a failed one.                                                                        |
| Poll                       | Catches updates missed by webhooks.                                                                                      |
| Manual review              | Reviews the whole pull request again.                                                                                    |

Drafts, forks and bot pull requests are included unless
[trigger rules](/configuration/#which-pull-requests-are-reviewed) exclude them.
kritika usually skips an unchanged bot rebase. kritika reviews it again if
scoring is enabled but the previous review had no score.

Automatic reviews stop when the pull request closes, is paused, or reaches
`trigger.limit`. See [incremental reviews](/configuration/#incremental-reviews)
for how later reviews handle earlier findings.

### Overlapping reviews

When a newer review starts, it replaces an older running review of the same
pull request. Queued work for an outdated commit is skipped. The dashboard
links canceled reviews to their replacements. Money already spent still counts.

If posting a completed review fails, kritika retries publication from the
saved result without calling the model again. Infrastructure interruptions
can restart a runner, the Kubernetes Job that runs a review, and repeat
unfinished model work. A runner that fails before its first model call, for
example on its fetch, is retried with backoff and repeats no model work.

Split reviews save each completed part. They reuse the part on retries of
the same job when the review inputs, model policy and prompts still match.
A manual re-review starts fresh. The dashboard marks reused parts. Their
usage remains charged to the original run. Look in the dashboard's
[Queue](/dashboard/#queue) for retries and errors.
