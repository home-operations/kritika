# Dashboard

kritika serves a dashboard: sign in with a local admin password,
GitHub or an OIDC provider, and see the accounts you can read, the
GitHub App serving each and its repositories, live review and conversation state as it
runs, and, for an admin, the running configuration and the audit log. An
admin can also queue a re-run of a specific pull request, cancel a review
in progress, reindex a repository's embeddings, turn a repository on or
off, resync the repositories from GitHub, or uninstall an App from an
account it does not serve. Everything else is set in the
[configuration file](configuration.md), which the dashboard shows but does
not change.

The top bar switches between two scopes, each with its own tabs: the
instance, and one of the accounts you can read. No tab chooses an account
for you: you enter one from the switcher or a link that names it. The
switcher keeps the page: from one account's queue it opens another's, or
the instance's.

The instance's tabs are about every account at once:

- **Overview:** totals across the accounts you can read, and each
  account's own with its health: the open pull requests that want a
  look, a cap that is close, and whether its App's webhooks arrive or
  kritika only polls it. Each links to where the account shows it.
- **Queue:** with more than one account, the review, follow-up and
  index jobs of all of them, and how many of each account's model slots
  (`limits.concurrency`) running reviews hold, which is why a review of
  it waits.
- **Configuration:** for an admin, the
  [Configuration page](#configuration-page).

An account's tabs are its sections:

- **Analytics:** the account's reviews over the last 7, 30 or 90 days
  against the same span before: pull requests reviewed, reviews,
  findings, the share addressed, the median review time, the median time
  from opening to merging, the 👍 and 👎 on kritika's inline comments and
  spend, by day or week, and the most reviewed repositories. The poller
  reads reactions, for a week after a pull request's latest review, so
  they need polling on. Its Findings list has
  each finding once per pull request however many reviews repeated it,
  addressed once a later review of the pull request, at a newer head, no
  longer reports it, or dismissed, with the reason, once a maintainer
  replied `@<bot> dismiss <reason>` in its thread. A finding kritika posted inline links to its thread
  on GitHub, here and on its review, and one that enforces a written rule
  names it. Its search narrows the list with `repo:`, `severity:`,
  `category:`, `status:` (open, addressed or dismissed) and `rule:<id>`,
  the findings that cite a rule; the tab also counts by category. Spend has the month so far against the account's
  caps, and usage by day, model, repository or role.
- **Pull requests:** its pull requests and their reviews, the run queue
  and the follow-up questions. The search box takes text, or narrows the
  list with `repo:owner/name`, `author:login`, `status:` a last review
  status and `is:paused` or `is:blocking`, and suggests each as you type. An admin can pick pull requests,
  by checkbox or with Space on the keyboard's row, and re-run them
  together. Each row counts the reviews that completed and what every
  review of it cost. A pull request's page says when its review is queued or
  waiting to run again after a failed attempt, with the attempt's error,
  and the queue names the cause when GitHub did not answer.
- **Rules:** what its reviews check: rules written in the configuration, as text or a file, and context files
  that explain the code, each with where it is set (a layer
  of the configuration, a repository's entry, or a repository's
  `.kritika.yaml` as its last review read it), the paths it applies to,
  and the repositories that read it. A written rule also has the findings
  that cite it, counted as the Findings list counts them, and how many of
  those were addressed, so a noisy rule shows as many findings and few
  addressed. The page only lists them.
- **Settings:** its repositories, and for an admin its audit log.

Times are written in your browser's time zone, on the 12 or 24 hour
clock its locale keeps. **Settings**, in the user menu, choose
another zone or clock, and a light or dark theme; they are kept with your
user, so they hold in any browser you sign in from. A theme left to the
browser follows each browser's own system. Figures by day count each day in UTC whatever
the zone.

A dot in the top bar shows whether live updates are connected. Once they
have been down for two seconds it reads "Reconnecting…", and the page may
be out of date until they are back.

It is served at `KRITIKA_WEB_URL`, the chart's `config.webUrl`, which the webhook
listener shares under `/hooks`. A review's summary comment carries a re-run
badge that opens the pull request's page here, where an admin can queue a
fresh review. People sign in as
[`auth`](configuration.md#auth) configures, with the role it maps them to.

## First run

The dashboard does not configure kritika: the configuration file does. Until an instance can
review, with a GitHub App and a default review model, a banner
tells an admin so and leads to the Configuration page, whose Setup
checklist names each step still missing and what to set for it:

1. **A GitHub App is connected:** declared under `apps`.
2. **The App reaches a repository:** installed on an account its entry
   under `apps` lists.
3. **A review model is set:** `review.model`.
4. **An embedder is set:** `embedding`, which is optional.

## Configuration page

An admin's Configuration page, a tab of the instance, shows what the instance
runs: the Setup checklist, the accounts the GitHub Apps serve, each
instance setting with its source, the Apps with the accounts each is
installed on, and the admin audit log. Its one change is uninstalling an
App from an account its entry does not list. The command palette, `Ctrl`/`⌘` `K`,
finds each of those sections, and the navigation beside the page lists
them while it is open. The palette also opens any page of the instance or
of an account, and the recently updated pull requests of every account
you can read. A repository's page filters its effective
settings.

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

Re-run, cancel, reindex, turning a repository on or off, resyncing and
uninstalling an App are the only changes the dashboard makes. Re-run,
cancel and reindex respond `202 Accepted`, with a job ID for re-run and
reindex, and queue the work rather than running it inline. Re-running a
pull request with no known head or one already queued or running,
reindexing a repository already queued, or cancelling a review that is
not running, is a `409 Conflict`.

## Operational notes

- The configuration is read at startup. A file that does not load fails
  startup, so a rollout that brings one leaves the old pods serving;
  one the leader cannot apply to the store raises the
  `kritika_config_error` gauge, keeps the last applied configuration, and
  is not tried again before a restart.
- A secret is read from its variable at startup too: restart the pods
  after rotating one.
- A role mapping is only as trustworthy as what it reads. Map on groups
  or roles the IdP controls, not on an email or name a user can set on
  their own profile.
