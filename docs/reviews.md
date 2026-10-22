# Reviews

kritika reviews enabled repositories when pull requests open or change. It
updates one summary comment and adds findings beside the relevant lines.

## Commands

Mention the bot using your GitHub App's slug. You need write access to the
repository, and webhooks must be working.

| Comment | What it does |
| --- | --- |
| `@<bot> <question>` | Reads the code and answers in the thread. |
| `@<bot> review` | Requests a full review of the current commit. |
| `@<bot> pause` | Stops automatic reviews for this pull request. |
| `@<bot> resume` | Resumes automatic reviews. |
| `@<bot> dismiss <reason>` | Dismisses a finding when posted in its thread. |

A manual review also works on paused, filtered, merged or closed pull requests.
It does not start a duplicate review if the current commit is already queued
or running. Reviews of merged or closed pull requests post comments but do not
set a commit status or approve the pull request.

kritika handles at most five mentions per pull request per hour, including commands.

## What it posts

| Item | What to expect |
| --- | --- |
| Summary | One comment updated after each review. |
| Findings | Inline comments with an explanation and a suggested fix when available. |
| Commit status | `Kritika / Review` on the reviewed commit. |
| Reactions | 👀 while working, 👍 when posted, 😕 on failure. |
| Approval | Only when [automatic approvals](confidence.md#approvals) are enabled. |

Reactions and automatic thread resolution need the relevant
[GitHub App permissions](setup.md#register-the-github-app).

### Findings

| Severity | Meaning |
| --- | --- |
| `p0` | Must be fixed before merging. |
| `p1` | Should be fixed. |
| `p2` | Optional polish. |

Findings also have a category: correctness, security, performance, reliability,
maintainability or tests. The summary includes findings outside the visible
diff, which cannot get an inline comment.

A later review can mark a finding fixed. To dismiss one you disagree with,
reply `@<bot> dismiss <reason>` in its thread. Later reviews of that pull
request will not raise it again.

Resolving the thread on GitHub also dismisses it, unless GitHub marks the
thread outdated because its lines changed. In that case, resolution counts
as a fix; use the dismiss command if you want a lasting dismissal.
Unresolving a thread takes back its dismissal. Without Contents write
permission, kritika records dismissals but leaves thread resolution to you.
On Forgejo, threads are neither read nor resolved: the dismiss command is
the way, as [Git forges](forges.md#what-works-where) lists.

### Replies in finding threads

With [`review.replies`](configuration.md#what-only-the-admin-sets) on,
kritika also reads a reply made in one of its finding threads without
mentioning it, by someone with write access, while the thread is
unresolved. It answers when the reply needs the reviewer: a question about
the finding, a disagreement with it, or a claim about the code worth
checking against the head. It posts nothing for an acknowledgement, a
message meant for someone else, or a point the thread already settled; the
dashboard records why. When the thread convinces it the finding was wrong,
its reply says so, the finding is dismissed as `@<bot> dismiss` would, and
the thread is resolved.

Such replies count against the five follow-ups per pull request per hour,
whether or not one is posted, and once kritika has replied three times in
a thread it leaves the thread to a mention. A reply that mentions the bot
is a mention wherever it is made.

### Dependency bumps

A change that moves a version, whoever wrote it, gets a procedure of its
own. kritika lists each version the diff moves (a line rewritten with only
a version, tag or digest changed, lockfiles left out) and tells the review
to:

- classify the step: major, minor, patch or a digest alone, a move under
  1.0 counting as a minor step, and a new digest under a tag that names no
  version as a minor step unless the image's labels show otherwise;
- find the upstream from the manifest rather than the description, and
  read what changed between the two versions there: release notes by tag
  with `gh`, a chart's annotations, an image's source label with
  `fetch_oci`, or the diff between the versions with `fetch_repo`. The description's
  notes are a start, not the source: GitHub cuts long descriptions, and
  Renovate and Dependabot embed notes only when they found them;
- search this repository for every symbol, flag, configuration key or
  default the notes say changed, and report what it does to each usage, on
  the usage's line when the diff shows it and else on the line that moves
  the version, naming the usage. A usage that no longer builds or no
  longer does what the code intends is `p0`, even where a build would
  catch it; one the new version serves only by a path it deprecates and
  announces it will remove is `p1`; any other new deprecation on a used
  symbol is `p2`. A change no usage meets is no finding, and goes under
  what the review checked;
- read major and minor steps first when a grouped update has many, and
  put the notes it could not read, or left unread, under
  [merge impact](#the-summary-comment) as a check to make, never as a
  finding and never as safe;
- never propose adopting what the new version adds.

A version number alone never sets a severity. The
[confidence scorer](confidence.md#risk) rates the step's risk separately.

### The summary comment

Start with the findings and suggested fixes. The summary also includes:

- a confidence score and approval decision, when enabled;
- earlier findings that were fixed, dismissed or not rechecked;
- merge impact, when the review identifies a concrete operational concern;
- expandable review context, including sources, skills and commands used.

Merge impact notes explain consequences or checks to make. A suggested check
is not evidence that it passed. Optional [flow diagrams](repository-config.md#flow-diagrams)
and [comment templates](repository-config.md#comment-templates) change presentation.

### The commit status

**Findings alone do not fail the check.** To block merges based on a score,
enable [confidence gating](confidence.md#the-commit-status) and make the check
required in GitHub.

| State | Meaning |
| --- | --- |
| Pending | Review running. |
| Success | Review posted and passed any configured score gate. |
| Failure | Confidence score below the configured threshold. |
| Error | Review failed, was incomplete, or a gated review could not be scored. |

Skipped reviews normally pass. If a skipped review carries an earlier score,
that score still determines whether it passes the gate.

## When a review runs

| Event | Behavior |
| --- | --- |
| Open, reopen or mark ready | Reviews the current commit unless already reviewed. |
| Push | Reviews changes since the previous review, or the full pull request for larger updates. |
| Add or remove a label | Can trigger a first review or retry a failed one. |
| Poll | Catches updates missed by webhooks. |
| Manual review | Reviews the whole pull request again. |

Drafts, forks and bot pull requests are included unless
[trigger rules](configuration.md#which-pull-requests-are-reviewed) exclude them.
An unchanged bot rebase is usually skipped; it is reviewed again if scoring
is enabled but the previous review had no score.

Automatic reviews stop when the pull request closes, is paused, or reaches
`trigger.limit`. See [incremental reviews](configuration.md#incremental-reviews)
for how later reviews handle earlier findings.

### Overlapping reviews

When a newer review starts, it replaces an older running review of the same
pull request. Queued work for an outdated commit is skipped. The dashboard
links canceled reviews to their replacements; money already spent still counts.

If posting a completed review fails, kritika retries publication from the
saved result without calling the model again. Infrastructure interruptions
can restart a runner and repeat unfinished model work. Split reviews save each
completed part and reuse it on retries of the same job when the review inputs,
model policy and prompts still match. A manual re-review starts fresh. The
dashboard marks reused parts; their usage remains charged to the original run. Check the dashboard's
[Queue](dashboard.md#queue) for retries and errors.
