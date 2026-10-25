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
[Your settings](dashboard.md#your-settings) describes, and give it to the
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

| Tool | What it reads |
| --- | --- |
| `get_pull` | A pull request by `repository` and `number`: its head commit, its reviews newest first with the commit each one reviewed and why it was skipped or failed, its review job that has not finished, and the bot's follow-ups. |
| `get_review` | A review by `repository` and `reviewId`: its summary and findings, each with its explanation, suggested fix, prompt for a coding agent, rules, and whether it is open, addressed or dismissed, and by what. |
| `list_findings` | The findings of a repository or account, newest first, filtered as the dashboard's Findings page filters them: `severity`, `category`, `status`, `dismissed`, `rule`, `agent`, `path`, `downvoted` and a text `query`. |
| `common_findings` | The same filters' findings counted, with their most common categories, rules, agents and directories, and how many of each were addressed, dismissed or drew a 👎. |
| `list_rules` | The written rules and context files a repository or account is reviewed with, and the findings that cite each written rule. |
| `list_followups` | The bot's follow-ups to @-mentions in a repository or account, newest first. |

`list_findings` and `list_followups` return a page at a time: `limit` takes
up to 200, 50 unless given, and a page's `nextCursor`, passed back as
`cursor`, reads the next.

## What it leaves out

The endpoint changes nothing: asking for a review, a question or a dismissal
stays on the forge. It also reports no runner runs, model usage or cost,
transcripts, raw output, diffs, or anything only an admin sees. See
[personal access tokens](security.md#personal-access-tokens) for what a
token can read and when it stops.
