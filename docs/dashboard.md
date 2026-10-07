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

The top bar switches between two scopes, each with its own tabs: the
instance, and one of the accounts you can read. No tab chooses an account
for you: you enter one from the switcher or a link that names it. The
switcher keeps the page: from one account's queue it opens another's, or
the instance's.

The instance's tabs are about every account at once:

| Tab           | What it shows                                                                                                                                                                                                                                           |
| ------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Overview      | totals across the accounts you can read, and each account's own with its health: the open pull requests that want a look, a cap that is close, and whether its App's webhooks arrive or kritika only polls it; each links to where the account shows it |
| Queue         | with more than one account, the review, follow-up and index jobs of all of them, and how many of each account's model slots (`limits.concurrency`) running reviews hold, which is why a review of it waits                                              |
| Configuration | for an admin, the [Configuration page](#configuration-page)                                                                                                                                                                                             |

An account's tabs are its sections:

| Tab           | What it shows                                                                                                    |
| ------------- | ---------------------------------------------------------------------------------------------------------------- |
| Analytics     | the account's reviews over time, its findings and its spend ([analytics](#analytics))                            |
| Pull requests | its pull requests and their reviews, the run queue and the follow-up questions ([pull requests](#pull-requests)) |
| Rules         | what its reviews check ([rules](#rules))                                                                         |
| Settings      | its [repositories](#repositories), and for an admin its audit log                                                |

The command palette, `Ctrl`/`⌘` `K`, opens any page of the instance or of
an account, the recently updated pull requests of every account you can
read, and each section of the Configuration page.

### Analytics

The account's reviews over the last 7, 30 or 90 days, against the same
span before, by day or week:

- pull requests reviewed, reviews, findings and the share addressed;
- the median review time, and the median time from opening to merging;
- the 👍 and 👎 on kritika's inline comments, which the poller reads for a
  week after a pull request's latest review, so they need polling on;
- spend, and the most reviewed repositories.

Its **Findings** list has each finding once per pull request, however many
reviews repeated it: addressed once a later review of the pull request, at
a newer head, no longer reports it, or dismissed, with the reason, once a
maintainer dismissed it ([commands](reviews.md#commands)). A finding
kritika posted inline links to its thread on GitHub, here and on its
review, and one that enforces a written rule names it. The tab also counts
findings by category.

Its **Spend** has the month so far against the account's caps, and usage
by day, model, repository or role.

### Pull requests

Each row counts the reviews that completed and what every review of it
cost. An admin can pick pull requests, by checkbox or with Space on the
keyboard's row, and re-run them together. A pull request's page says when
its review is queued or waiting to run again after a failed attempt, with
the attempt's error, and the queue names the cause when GitHub did not
answer.

The search boxes take text, or narrow their list with tokens they suggest
as you type:

| List          | Tokens                                                                                                                    |
| ------------- | ------------------------------------------------------------------------------------------------------------------------- |
| Pull requests | `repo:owner/name`, `author:login`, `status:` the last review's status, `is:paused` or `is:blocking`                       |
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
be out of date until they are back.

## First run

The dashboard does not configure kritika: the configuration file does.
Until an instance can review, with a GitHub App and a default review
model, a banner tells an admin so and leads to the Configuration page,
whose Setup checklist names each step still missing and what to set for
it:

1. **A GitHub App is connected:** declared under `apps`.
2. **The App reaches a repository:** installed on an account its entry
   under `apps` lists.
3. **A review model is set:** `review.model`.
4. **An embedder is set:** `embedding`, which is optional.

## Configuration page

An admin's Configuration page, a tab of the instance, shows what the
instance runs:

- the Setup checklist;
- the accounts the GitHub Apps serve;
- each instance setting with its source;
- the Apps, with the accounts each is installed on;
- the admin audit log.

Its one change is uninstalling an App from an account its entry does not
list. The navigation beside the page lists its sections while it is open.
A repository's page filters its effective settings.

## Repositories

An admin switches repositories on and off on an account's Repositories
page, one at a time or a selection together, and reindexes a selection
from there too. A switch is the dashboard's own choice, kept per
repository and audited: the configuration only says where a repository
starts ([which repositories run](configuration.md#which-repositories-run)).

The page lists the repositories that can run, with any fork turned on; its
Type filter lists the forks, or the archived repositories, instead.
"Resync from GitHub" lists the repositories the App reaches again, such as
right after unarchiving one. An account's repository count is of the ones
that run.

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
