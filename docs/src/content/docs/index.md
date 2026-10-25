---
title: kritika
---

kritika is an AI reviewer for pull requests on GitHub, Forgejo, GitLab and Gitea. It reads the change and
related code, posts findings with suggested fixes, and answers follow-up
questions in comments.

**New installation? Start with [Setup](/setup/).**

:::caution[Under active development]

Configuration and APIs can change between releases. Read the
[changelog](https://github.com/home-operations/kritika/blob/main/CHANGELOG.md)
before upgrading.

:::

## How it works

1. You open a pull request or push a change.
2. kritika reviews it using repository context and any review rules you set.
3. It posts a summary and inline findings on the pull request.
4. You fix the findings or mention the bot to discuss them.

Reviews run in separate Kubernetes pods. The dashboard shows their progress,
results and costs. Confidence scores and automatic approvals are optional.

## Find what you need

| I want to…                                   | Start here                                 |
| -------------------------------------------- | ------------------------------------------ |
| Install kritika and get a first review       | [Setup](/setup/)                           |
| Ask the bot a question or re-run a review    | [Reviews and commands](/reviews/#commands) |
| Add rules for my repository                  | [Repository settings](/repository-config/) |
| Find a failed review or check spending       | [Dashboard](/dashboard/)                   |
| Let a coding agent read reviews and findings | [MCP endpoint](/mcp/)                      |
| Use scores or automatic approvals            | [Confidence and approvals](/confidence/)   |
| Connect a Forgejo, GitLab or Gitea instance  | [Git forges](/forges/)                     |

The **Operations** pages cover the database, security and monitoring.
The **Reference** pages list configuration options, providers and Helm values.
