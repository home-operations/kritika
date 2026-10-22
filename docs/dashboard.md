# Dashboard

Open the address set in `config.webUrl` and sign in. The dashboard shows review
progress, findings and costs for the accounts you can access.

Use GitHub to discuss findings. Use the dashboard to inspect reviews and manage
the service. Application settings live in the [configuration file](configuration.md).

## First run

Sign in as an admin and open **All accounts → Settings**. The setup checklist
shows what is missing:

1. A GitHub App declared under `apps`, a Forgejo bot under `forgejo`, a GitLab service account under `gitlab`, or a Gitea bot under `gitea`.
2. An installed App and at least one enabled repository.
3. A default model in `review.model`.
4. An optional embedder in `embedding` for finding similar code.

**Newly discovered repositories start off.** Open the account's
**Repositories** page and enable the ones you want reviewed.

## Navigation

The account switcher selects one account or **All accounts**. Press
`Ctrl+K` or `⌘K` to find a page or a recently updated pull request.

| Page | Use it to… |
| --- | --- |
| Overview | See running work and problems that need attention. |
| Pull requests | Read reviews, inspect findings and open GitHub threads. |
| Queue | Find waiting, running, retrying or failed jobs. |
| Repositories | Enable repositories and inspect their settings and code index. |
| Analytics | Compare review activity, findings and spending over time. |
| Usage & costs | Inspect costs by account, repository, model or review. |
| Follow-ups | Read questions and the bot's replies. |
| Findings | Search findings across reviews. |
| Rules | See which review rules apply and where they come from. |
| Audit log | Inspect management actions, as an admin. |

Some pages are available only within an account. **All accounts** combines
activity and usage across accounts you can read.

### Pull requests

Open a pull request for its summary, findings and activity. Expand a finding
for its explanation and suggested fix. **Run details** contains the timeline,
model conversation, usage and raw data for troubleshooting.

Search accepts plain text and filters:

| List | Example filters |
| --- | --- |
| Pull requests | `repo:owner/name`, `author:login`, `status:failed`, `is:paused`, `is:p0` |
| Findings | `repo:owner/name`, `severity:p1`, `category:security`, `status:open`, `rule:wrap-errors` |

Filters are suggested as you type. Expand a list row to preview its latest
review without leaving the list.

### Queue

Start with **Needs attention** to find retrying or discarded jobs and workers
whose heartbeat is overdue. Expand a row to see attempts, timing and the full
error. An overdue heartbeat can recover; it does not always mean a failed job.

Retryable jobs resume automatically. Admins can cancel active reviews, re-run
canceled or discarded reviews, and reindex after a failed index job.

### Analytics

Choose a period to compare reviews, findings, duration, tokens and API spending.
The page also shows addressed findings and reactions to inline comments.
Reactions need polling enabled.

**Refused hosts** shows destinations blocked by the egress gateway. Review why
a host was blocked before adding it to [`egress.allow`](security.md#the-egress-gateway).

In **Usage & costs**, select a repository, model or review to inspect spending.
**Unpriced** means a call has no known price and totals are incomplete. API
spending excludes subscription fees. Cache details depend on transcript retention.

### Rules

The Rules page lists configured rules and context files, their source and
scope, and findings that cite each rule. Edit rules in the
[configuration file](configuration.md) or [repository settings](repository-config.md#rules).

### Your settings

Use **Settings** in the user menu to change theme, time zone and clock format.
Daily charts use UTC. If the top bar says **Reconnecting…**, live data may be
out of date until the connection returns.

## Repositories

Admins can enable or disable repositories individually or in bulk, and request
reindexing. Enablement changes are saved and audited.

Use **Resync from GitHub** after changing App access or unarchiving a repository.
Then enable the repository if it is still off. The next poll can pick up open
pull requests created while it was disabled.

Expand **Repository settings** to inspect effective settings, or **Index runs**
to investigate indexing failures. See [which repositories run](configuration.md#which-repositories-run)
for how dashboard choices and configured defaults interact.

## Configuration page

Admins can inspect the setup checklist, connections, served accounts, effective
settings and audit log. Search or filter settings to find their source.

This page does not edit the configuration file. Change the file and roll out
the deployment. External ConfigMap changes and secret rotations require a
restart or a controller such as Reloader.

## Actions

Admins can:

- re-run a pull request, or select several open pull requests to re-run;
- cancel a review;
- reindex a repository;
- enable or disable repositories;
- resync repository access from GitHub;
- uninstall an App from an account the configuration does not serve.

Re-run, cancel and reindex requests queue work. A duplicate review or index
request can be refused while one is already queued or running.

## Operational notes

A configuration that fails validation prevents startup. If the leader cannot
apply it to the database, kritika keeps the last applied configuration and
sets `kritika_config_error`. Fix the problem and restart to try again.

See [monitoring](metrics.md) for alerts and [security](security.md) for access controls.
