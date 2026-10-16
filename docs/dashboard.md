# Dashboard

kritika serves a dashboard at `KRITIKA_WEB_URL`, the chart's
`config.webUrl`, which the webhook listener shares under `/hooks`. Sign in
with a local admin password, GitHub or an OIDC provider, as
[`auth`](configuration.md#auth) configures, with the role it maps you to.
You see the accounts you can read, the GitHub App serving each and its
repositories, live review and conversation state as it runs, and, as an
admin, the running configuration and the audit log.

An admin can also queue a re-run of a pull request, cancel a review in
progress, reindex a repository's embeddings, turn a repository on or off,
resync the repositories from GitHub, or uninstall an App from an account
it does not serve ([actions](#actions)). Everything else is set in the
[configuration file](configuration.md), which the dashboard shows but does
not change. A review's summary comment carries a re-run badge that opens
the pull request's page here.

## Navigation

The scope switcher chooses **All accounts** or one account you can read.
Pull requests and Queue keep their portable filters when you change scope.
Switching directly between accounts clears a pull request's repository filter;
narrowing a global repository search to an account retains it. From a
record, switching accounts opens the corresponding list. The account name
stays visible in the switcher and page breadcrumbs.

All accounts has these destinations:

| Tab | What it shows |
| --- | --- |
| Overview | A compact summary of running work and usage, one list of review and queue alerts, then a comparison of accounts. Alerts link to the relevant filtered list. |
| Pull requests | A paginated, searchable list across readable accounts, with an Account column and the same filters as an account's list. |
| Queue | Jobs across readable accounts and the model slots they hold. |
| Analytics | The [analytics](#analytics) of every readable account added up: no medians, since each account's is its own, an Account column on the repositories, and the refused hosts of every account together. |
| Usage & costs | The spend of every readable account added up, with a By account grouping whose rows open each account's own page; the month tiles carry no caps. |
| Settings | For an admin, the [configuration page](#configuration-page). |

Choosing an account opens its operational overview. Its navigation stays
within that account; use the scope switcher to return to All accounts.

| Tab | What it shows |
| --- | --- |
| Overview | A compact summary of running work, daily reviews and usage, one attention list, and repositories. Connection details open on demand; connection problems remain in the attention list. |
| Pull requests | The account's [pull requests](#pull-requests) and reviews. |
| Queue | The account's running, waiting and finished jobs. |
| Repositories | Connected [repositories](#repositories) and their status. |
| Analytics | Review trends, findings and spend over time ([analytics](#analytics)). |
| Usage & costs | Usage, costs and limits for the account. |
| Follow-ups | Follow-up questions and replies. |
| Findings | Findings across the account's reviews. |
| Rules | What its reviews check ([rules](#rules)). |
| Audit log | For an admin, the account's recorded management actions. |

Opening a pull request or review from a list keeps a **Back to** link to
that list, including its filters, loaded rows and scroll position during
navigation. Analytics lives at `#/a/<forge>/<account>/analytics`; dated
bookmarks on the account's root still open Analytics.

On reload, a neutral loading screen waits for the session check before
showing login or the workspace. Initial data loads reserve content space;
background refreshes keep the current content visible. A failed session
check offers Retry and preserves the requested page.

The command palette, `Ctrl`/`⌘` `K`, opens any page of the instance or of
an account, the recently updated pull requests of every account you can
read, and each section of the Configuration page.

### Analytics

The account's reviews over the last 24 hours, 7, 30 or 90 days, against the same
span before, by day or week:

- pull requests reviewed, reviews, findings and the share addressed;
- the median review time, and the median time from opening to merging;
- the 👍 and 👎 on kritika's inline comments, which the poller reads for a
  week after a pull request's latest review, so they need polling on;
- tokens, API spend, and the most reviewed repositories;
- the hosts the egress gateway refused the account's runs;
- failure rate, calculated as failed / (completed + failed), excluding
  canceled, capped, skipped and in-progress reviews.

Six tiles lead the page: reviews, median review, time to merge, addressed,
reactions and API spend, each with its related counts beneath. Choose a
metric in **Review trends** to see reviews, findings by severity, failure
rate, duration, tokens or API spend as a chart or table. Buckets without completed reviews have no median;
buckets without completed or failed reviews have no failure rate. Both are
omitted from their respective charts.

**Refused hosts** lists the hosts the gateway refused a run's commands and
fetches in the period, the most refused first, each with how many runs
asked for it and the reasons it was refused
([security](security.md#the-egress-gateway)); a Repository filter narrows
it to one repository's runs. A host is shown as the agent asked for it,
as text, never as a link, since the content under review steers what an
agent asks for. The list is what to add to the configuration's
`egress.allow`, and a host that should stay refused is one to leave
there. Refusals are kept as long as transcripts
([`transcriptRetention`](configuration.md#how-kritika-runs)).

Its **Findings** list has each finding once per pull request, however many
reviews repeated it: addressed once a later review of the pull request, at
a newer head, no longer reports it, or dismissed, with the reason, once a
maintainer dismissed it ([commands](reviews.md#commands)). A finding
kritika posted inline links to its thread on GitHub, here and on its
review, and one that enforces a written rule names it. The tab also counts
findings by category.

Its **Spend** has the month so far against the account's caps, and usage
by hour, day, week, model, repository, role or review. Select a repository
row to see its models, then a model to see its reviews and open the usage
of an individual review. Selection totals compare with the preceding
period of equal length; named groups also show each row's spend change,
including groups with no usage in the current period. Comparisons are
withheld when either period contains unpriced calls. Filters stay in the
URL for reloads, sharing and browser Back. API spend excludes subscription
fees. **Show cache details** reveals the cache columns; cache detail is
limited by transcript retention.

### Queue

Filter loaded jobs by running, waiting, finished or needs attention, and
search by repository, account or job number. Needs attention includes
retryable and discarded jobs and running jobs with no worker heartbeat
for over three minutes. An overdue heartbeat is a warning to investigate;
the worker may recover or the leader may rescue it. The view shows the
last heartbeat, attempt or completion time and refreshes every 15 seconds.
Each account supplies up to 500 active and 50 finished jobs. A row's
chevron expands its attempts, timing and full error beneath the row.

Admins can cancel an active review, re-run a discarded or canceled review
at the pull request's current head, or reindex after a failed index job.
These use the same confirmed, audited actions as the record pages.
Retryable jobs resume automatically; follow-up and thread jobs remain inspectable
without a manual replay control.

### Pull requests

GitHub remains the place to discuss findings and approve pull requests.
Pull and review pages have prominent GitHub links, and posted findings
link directly to their threads. The internal code view and run details
remain available for investigation. **Overview**, **Findings & code** and
**Activity** are the primary sections. The **Run details** selector opens
the timeline, conversation, usage or raw data. **Pull request details**
shows branches, commit IDs and labels.

The review overview combines the summary and merge impact, followed by compact
finding rows. Expand a row for the explanation and suggested fix, or select its
title to inspect the code. Confidence explanations, positive notes and checked
rules open on demand. Incomplete reviews and pending retries remain visible.

Use a row's chevron, or click anywhere on the row that is not a link, to
preview its latest review and up to three findings without leaving the
list. The title opens the pull request. Previews load when opened and
follow live review updates.

Each row counts the reviews that completed and what every review of it
cost. An admin can pick open pull requests, by checkbox or with Space on
the keyboard's row, and re-run them together; a merged or closed one is
re-run from its own page. A pull request's page says when
its review is queued or waiting to run again after a failed attempt, with
the attempt's error, and the queue names the cause when GitHub did not
answer.

The search boxes take text, or narrow their list with tokens they suggest
as you type:

| List          | Tokens                                                                                                                    |
| ------------- | ------------------------------------------------------------------------------------------------------------------------- |
| Pull requests | `repo:owner/name`, `author:login`, `status:` the last review's status, `is:paused` or `is:p0`                       |
| Findings      | `repo:`, `severity:`, `category:`, `status:` open, addressed or dismissed, and `rule:<id>`, the findings that cite a rule |

### Rules

The Rules tab lists what the account's reviews check: rules written in the
configuration, as text or a file, and context files that explain the
code. Each shows where it is set (a layer of the configuration, a
repository's entry, or a repository's `.kritika.yaml` as its last review
read it), the paths it applies to, and the repositories that read it. A
written rule also has the findings that cite it, counted as the Findings
list counts them, and how many of those were addressed, so a noisy rule
shows as many findings and few addressed. The page only lists them.

### Your settings

Times are written in your browser's time zone, on the 12 or 24 hour clock
its locale keeps. **Settings**, in the user menu, chooses another zone or
clock, and a light or dark theme; they are kept with your user, so they
hold in any browser you sign in from. A theme left to the browser follows
each browser's own system. Figures by day count each day in UTC whatever
the zone.

A dot in the top bar shows whether live updates are connected. Once they
have been down for two seconds it reads "Reconnecting…", and the page may
be out of date until they are back. If they come back from a kritika that
serves another build of the dashboard, as after an upgrade, the page
reloads itself rather than show the new data with its old code.

## First run

The dashboard does not configure kritika: the configuration file does.
Until an instance can review, with a GitHub App and a default review
model, a banner tells an admin so and leads to the Configuration page,
whose Setup checklist names each step still missing and what to set for
it:

1. **A GitHub App is connected:** declared under `apps`.
2. **A repository is enabled:** install the App on an account its entry
   under `apps` lists, then turn on a repository on that account's
   Repositories page. Newly discovered repositories start off.
3. **A review model is set:** `review.model`.
4. **An embedder is set:** `embedding`, which is optional.

## Configuration page

An admin's Configuration page, a tab of the instance, shows what the
instance runs:

- the Setup checklist;
- the accounts the GitHub Apps serve;
- the Apps, with the accounts each is installed on;
- searchable instance settings with their sources and a section filter;
- the admin audit log.

Its one change is uninstalling an App from an account its entry does not
list. Use the section filter to narrow the settings list.
A repository row's chevron previews its effective review settings and code
index. The repository page leads with pull requests. Expand **Repository settings**
to search its effective settings, or **Index runs** to inspect indexing
history. A failed latest index run opens its details automatically.

## Repositories

An admin switches repositories on and off on an account's Repositories
page, one at a time or a selection together, and reindexes a selection
from there too. A switch is the dashboard's own choice, kept per
repository and audited. Newly discovered repositories wait for enablement;
the configuration's defaults apply to existing or explicitly listed
repositories without a dashboard choice
([which repositories run](configuration.md#which-repositories-run)).

The page lists the repositories that can run, with any fork turned on; its
Type filter lists the forks, the public repositories, or the archived
repositories, instead. Each repository is badged as GitHub reports it: a
fork, archived, public or internal; a private one carries no badge.
"Resync from GitHub" lists the repositories the App reaches again, such as
right after unarchiving one, and disables the ones it no longer reaches.
After unarchiving and resyncing, turn the repository on if it is still off.
Enabling a repository lets the next poll pick up its existing open pull
requests, including ones opened while it was off.
An account's repository count is of the ones that run.

## Actions

These are the only changes the dashboard makes:

| Action                      | Response                      | `409 Conflict` when                                                  |
| --------------------------- | ----------------------------- | -------------------------------------------------------------------- |
| Re-run a pull request       | `202 Accepted`, with a job ID | it has no known head, or a review of it is already queued or running |
| Cancel a review             | `202 Accepted`                | the review is not running                                            |
| Reindex a repository        | `202 Accepted`, with a job ID | a reindex of it is already queued                                    |
| Turn a repository on or off |                               |                                                                      |
| Resync from GitHub          |                               |                                                                      |
| Uninstall an App            |                               |                                                                      |

Re-run, cancel and reindex queue the work rather than running it inline.

## Operational notes

- The configuration is read at startup. A file that does not load fails
  startup, so a rollout that brings one leaves the old pods serving; one
  the leader cannot apply to the store raises the `kritika_config_error`
  gauge, keeps the last applied configuration, and is not tried again
  before a restart.
- A secret is read from its variable at startup too: restart the pods
  after rotating one.
- A role mapping is only as trustworthy as what it reads
  ([security](security.md#hardening-an-install)).
