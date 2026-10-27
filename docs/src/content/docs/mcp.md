---
title: MCP endpoint
---

kritika serves a read-only [Model Context Protocol](https://modelcontextprotocol.io)
endpoint, so a coding agent can read its reviews, findings, rules and
follow-ups instead of scraping forge comments. An agent can check that the
newest review covered its latest push, read each finding's prompt for a
coding agent, or see which findings and rules keep coming up.

The endpoint is the dashboard's URL followed by `/mcp`, such as
`https://kritika.example.com/mcp`, or `https://example.com/kritika/mcp` when
the dashboard is served under a path. It speaks Streamable HTTP and keeps no
session, so any replica answers any request.

## Connect an agent

Create a token under **Settings → API tokens** in the dashboard, as
[Your settings](/dashboard/#your-settings) describes, and give it to the
agent as a bearer token. The endpoint takes nothing else: a dashboard
session does not sign it in.

Claude Code:

```sh
claude mcp add --transport http kritika https://kritika.example.com/mcp \
  --header "Authorization: Bearer kritika_..."
```

Codex, which reads the token from an environment variable you set:

```sh
codex mcp add kritika --url https://kritika.example.com/mcp --bearer-token-env-var KRITIKA_TOKEN
```

## Tools

Every tool reads one account the token can read. Name a repository as
`owner/name`: its owner is its account. The list tools also take a whole
`account` instead. Pass `forge` (`github`, `forgejo`, `gitlab` or `gitea`)
only when your token reads accounts of that name on more than one forge.

| Tool              | What it reads                                                                                                                                                                                                              |
| ----------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `get_pull`        | A pull request by `repository` and `number`: its head commit, its reviews newest first with the commit each one reviewed and why it was skipped or failed, its review job that has not finished, and the bot's follow-ups. |
| `get_review`      | A review by `repository` and `reviewId`: its summary and findings, each with its explanation, suggested fix, prompt for a coding agent, rules, and whether it is open, addressed or dismissed, and by what.                |
| `list_findings`   | The findings of a repository or account, newest first, filtered as the dashboard's Findings page filters them: `severity`, `category`, `status`, `dismissed`, `rule`, `agent`, `path`, `downvoted` and a text `query`.     |
| `common_findings` | The same filters' findings counted, with their most common categories, rules, agents and directories, and how many of each were addressed, dismissed or drew a 👎.                                                         |
| `list_rules`      | The written rules and context files a repository or account is reviewed with, and the findings that cite each written rule.                                                                                                |
| `list_followups`  | The bot's follow-ups to @-mentions in a repository or account, newest first.                                                                                                                                               |

`list_findings` and `list_followups` return a page at a time. `limit` takes
up to 200 and is 50 unless given. Pass a page's `nextCursor` back as
`cursor` to read the next page.

## What to ask

Once the agent is connected, ask it in plain words; it picks the tools.
Some requests the endpoint answers well, with the tools behind them:

- **"Did kritika review my last push to owner/repo#123?"** `get_pull`
  reports the pull request's `headSha` and each review's. When the newest
  review's matches and its `status` is `completed`, the push was reviewed.
  Otherwise `job`, the pull request's newest unfinished review job, says
  whether a review is waiting, running or retrying, and after what error;
  its own `headSha` says which commit it is for. A review's `status`,
  `skipReason` and `error` say why one was skipped or stopped short.
- **"Fix what it found."** `get_review` with the newest review's id lists
  its findings. Each one whose `status` is `open` carries its `path` and
  `line` and its `explanation`, and, when the review gave them, a
  `suggestedFix` and an `agentPrompt` written for a coding agent to apply.
  The agent applies the prompts, pushes, and asks the first question again.
- **"Which of these were already handled?"** A finding's `status` is
  `addressed` once a later completed review of a newer commit no longer
  reported it, or `dismissed`,
  with `dismissSource` naming what dismissed it, a `dismiss` command, a
  resolved thread or a conceded reply, and `dismissReason` the reason
  given.
- **"What does kritika keep flagging here?"** `common_findings` for the
  repository, or with `path` set to the directory about to change, counts
  its findings by category, rule, agent and directory, and `list_findings`
  with the same filters lists them. Reading them before a change avoids
  the findings the next review would raise.
- **"What rules is this repository reviewed with?"** `list_rules` returns
  each written rule's text or file path, where it applies, and how its
  findings fared,
  so an agent can check its change against the rules before it opens the
  pull request.
- **"Why did the bot not answer my mention?"** `list_followups` reports
  each follow-up's `status`, one of `answered`, `limited`, `ignored` or
  `failed`, and its `reason`.

## Compared with reading the comments

An agent with the forge's CLI, such as `gh`, can read the same reviews as
comments. The endpoint is the better source for them:

- **Fields, not markdown.** The summary comment mixes open findings,
  earlier ones that were fixed, dismissed or not rechecked, held-back ones
  folded away, review context and a footer, and is rewritten after each
  review. Each inline comment holds one finding's explanation, with its
  fix and prompt when it has them, the prompt in a collapsed block. An
  agent reading them has to
  tell kritika's comments from everyone else's and parse that layout. The
  tools return each finding once, with its path, line, severity, category,
  rules, prompt and status as fields.
- **Status in one field.** Whether a finding is open, addressed or
  dismissed, and what dismissed it, is kritika's record. On the forge it is
  spread over the summary's list of earlier findings and the state of each
  thread, and on Forgejo, whose API cannot resolve a thread, the summary
  alone.
- **Findings with no comment.** A finding outside the visible diff gets no
  inline comment, so the summary gives its location, title and
  explanation, but no fix or prompt. `get_review` reports it like any
  other.
- **Whether the latest push was reviewed.** The commit status is pending
  while a review runs and says nothing about one queued behind others or
  retrying after an error. `get_pull` reports both, each with the commit
  it is for.
- **Across pull requests.** What keeps coming up, how often each rule is
  cited and how often its findings are dismissed would take a search over
  every pull request's comments. `common_findings`, `list_findings` and
  `list_rules` answer in one call.
- **Every forge, one shape.** The tools are the same on GitHub, Forgejo,
  GitLab and Gitea, so the agent needs no forge CLI to read reviews, and
  the token it holds reads kritika alone and can change nothing.

The endpoint reads kritika's record of a review, not the pull request: the
diff, the code and the conversation stay on the forge.

## What it leaves out

The endpoint changes nothing: asking for a review, a question or a dismissal
stays on the forge. It also reports no runner runs, model usage or cost,
transcripts, raw output, diffs, or anything only an admin sees. See
[personal access tokens](/security/#personal-access-tokens) for what a
token can read and when it stops.
