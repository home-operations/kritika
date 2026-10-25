---
title: Dashboard
---

Open the address set in `config.webUrl` and sign in. The dashboard shows review
progress, findings and costs for the accounts you can access.

Use the forge to discuss findings. Use the dashboard to inspect reviews and manage
the service. Application settings live in the [configuration file](/configuration/).

## First run

Sign in as an admin and open **All accounts → Accounts**. The setup checklist
shows what is missing:

1. A GitHub App declared under `apps`, a Forgejo bot under `forgejo`, a GitLab service account under `gitlab`, or a Gitea bot under `gitea`.
2. An installed App, or a bot with access to the account, and at least one enabled repository.
3. A default model in `review.model`.
4. An optional embedder in `embedding` for finding similar code.

**Newly discovered repositories start off.** Open the account's
**Repositories** page and enable the ones you want reviewed.

## Navigation

The account switcher selects one account or **All accounts**. Press
`Ctrl+K` or `⌘K` to find a page or a recently updated pull request.

| Page          | Use it to…                                                          |
| ------------- | ------------------------------------------------------------------- |
| Overview      | See running work and problems that need attention.                  |
| Pull requests | Read reviews, inspect findings and open their threads on the forge. |
| Queue         | Find waiting, running, retrying or failed jobs.                     |
| Repositories  | Enable repositories and inspect their settings and code index.      |
| Analytics     | Compare review activity, findings and spending over time.           |
| Usage & costs | Inspect costs by account, repository, model or review.              |
| Follow-ups    | Read questions and the bot's replies.                               |
| Findings      | Search findings across reviews.                                     |
| Rules         | See which review rules apply and where they come from.              |
| Audit log     | Inspect management actions, as an admin.                            |

Some pages are available only within an account. **All accounts** combines
activity and usage across accounts you can read.

### Pull requests

Open a pull request for its summary, findings and activity. Expand a finding
for its explanation and suggested fix. A finding an
[agent](/repository-config/#agents) raised carries the agent's name; one
without a name is the default pass's. **Run details** contains the timeline,
model conversation, usage and raw data for troubleshooting. When agents ran,
the timeline lists each agent's parts with their cost, and the cost of each
agent. The default pass is listed as `default`.

Search accepts plain text and filters:

| List          | Example filters                                                                                                                                                    |
| ------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Pull requests | `repo:owner/name`, `author:login`, `status:failed`, `is:paused`, `is:p0`                                                                                           |
| Findings      | `repo:owner/name`, `severity:p1`, `category:security`, `status:open`, `dismissed:conceded`, `rule:wrap-errors`, `agent:security`, `path:internal/`, `is:downvoted` |

Filters are suggested as you type. `agent:` is offered once a finding on the
page names an agent; `agent:default` finds the default pass's. Expand a list row to preview its latest
review without leaving the list.

### Findings

**Common findings** counts the findings the filters keep, and how many of
them a later review found addressed, were dismissed, or drew a 👎. Group
them by category, rule, agent or directory, and select a group to narrow
the list to it; a directory then shows the directories below it. A group that keeps
coming back is worth an instruction in `AGENTS.md` or a skill, so the
change's author gets it right the first time. A group that is often
dismissed or drew a 👎 is worth a [rule](/repository-config/#rules)
telling reviews what not to report. Like the analytics, 👎 counts come
from GitHub alone and need polling enabled.

`path:` keeps the findings under a path, and `is:downvoted` keeps those
that drew a 👎. `dismissed:` keeps those dismissed one way:

- `command`, with `@<bot> dismiss`;
- `resolved`, by resolving the thread;
- `conceded`, when kritika agreed in the thread that the finding was wrong.

Hover a dismissed finding's status to see which.

**Export Markdown** and **Export JSON** download the findings the filters
keep, newest first, up to 1,000. Give the Markdown to a coding agent and
ask it to propose those instructions and rules. Reviews read `AGENTS.md`
only when it is listed under [`context`](/repository-config/#context).

### Queue

Start with **Needs attention** to find retrying or discarded jobs and workers
whose heartbeat is overdue. Expand a row to see attempts, timing and the full
error. An overdue heartbeat can recover; it does not always mean a failed job.

Retryable jobs resume automatically. Admins can cancel active reviews, re-run
canceled or discarded reviews, and reindex after a failed index job.

### Analytics

Choose a period to compare reviews, findings, duration, tokens and API spending.
The page also shows addressed findings and reactions to inline comments.
Reactions are counted on GitHub alone and need polling enabled.

**Refused hosts** shows destinations blocked by the egress gateway. Review why
a host was blocked before adding it to [`egress.allow`](/security/#the-egress-gateway).

In **Usage & costs**, select a repository, model or review to inspect spending.
**Unpriced** means a call has no known price and totals are incomplete. API
spending excludes subscription fees. Cache details depend on transcript retention.

### Rules

The Rules page lists configured rules and context files, their source and
scope, and findings that cite each rule, with how many of those were
addressed, dismissed, or drew a 👎. A rule whose findings are often
dismissed may need rewording. Edit rules in the
[configuration file](/configuration/) or [repository settings](/repository-config/#rules).

### Your settings

Use **Settings** in the user menu to change theme, time zone and clock format.
Daily charts use UTC. If the top bar says **Reconnecting…**, live data may be
out of date until the connection returns.

Under **API tokens**, create a personal access token for an agent that reads
through kritika's [MCP endpoint](/mcp/). Name it, and choose whether it
reads everything you can read or only some of those accounts. Then choose
when it expires: in 7, 30 or 90 days, a year, or never. Its value is shown
once. A token never changes anything. It stops reading once it expires, is
revoked, or the sign-in it came from changes; the list then marks it
**expired** or **stale**. See
[personal access tokens](/security/#personal-access-tokens).

## Repositories

Admins can enable or disable repositories individually or in bulk, and request
reindexing. Enablement changes are saved and audited.

Use **Resync** after changing the App's or the bot's access, or after
unarchiving a repository. It names the account's forge, such as
**Resync from Forgejo**. Then enable the repository if it is still off. The
next poll can pick up open pull requests created while it was disabled.

Expand **Repository settings** to inspect effective settings, or **Index runs**
to investigate indexing failures. See [which repositories run](/configuration/#which-repositories-run)
for how dashboard choices and configured defaults interact.

## Administration

Admins run the instance from **All accounts**, under **Configuration**:

| Page        | Use it to…                                                                                                                                                                  |
| ----------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Accounts    | Follow the setup checklist while something is missing, and see the accounts the instance serves.                                                                            |
| Connections | Inspect GitHub Apps and forge bots and their installations, and uninstall an App from an account nobody serves.                                                             |
| Users       | See everyone who has signed in, how they sign in, what their newest session reads and when they last signed in; sign someone out everywhere, and revoke anyone's API token. |
| Settings    | Find each effective setting and where it is set, the egress gateway's **Download cache** settings and the **Most used tools** of recent runs.                               |
| Audit log   | Inspect management actions across accounts.                                                                                                                                 |

These pages do not edit the configuration file. Change the file and roll out
the deployment. External ConfigMap changes and secret rotations require a
restart or a controller such as Reloader.

## Actions

Admins can:

- re-run a pull request, or select several open pull requests to re-run;
- cancel a review;
- reindex a repository;
- enable or disable repositories;
- resync repository access from the account's forge;
- uninstall an App from an account the configuration does not serve;
- sign a user out everywhere, which ends every session of theirs, an
  admin's own included;
- revoke anyone's API token.

Re-run, cancel and reindex requests queue work. A duplicate review or index
request can be refused while one is already queued or running.

## Operational notes

A configuration that fails validation prevents startup. If the leader cannot
apply it to the database, kritika keeps the last applied configuration and
sets `kritika_config_error`. Fix the problem and restart to try again.

See [monitoring](/metrics/) for alerts and [security](/security/) for access controls.
