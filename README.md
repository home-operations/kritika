<div align="center">

# kritika

**Repository-aware AI pull request review for GitHub, Forgejo, GitLab and Gitea.**

[![CI](https://img.shields.io/github/actions/workflow/status/home-operations/kritika/ci.yaml?branch=main&label=ci)](https://github.com/home-operations/kritika/actions/workflows/ci.yaml)
[![Release](https://img.shields.io/github/actions/workflow/status/home-operations/kritika/release.yaml?branch=main&label=release)](https://github.com/home-operations/kritika/actions/workflows/release.yaml)
[![License](https://img.shields.io/github/license/home-operations/kritika)](https://github.com/home-operations/kritika/blob/main/LICENSE)

</div>

kritika reviews pull requests on GitHub, Forgejo, GitLab and Gitea using the code around the change. It posts
an updated summary, inline findings and suggested fixes. Mention the bot to ask
a follow-up question or request another review.

**[Get started](docs/setup.md)** · [Documentation](https://kritika.home-operations.com/)

> [!WARNING]
> kritika is under active development. Check the [changelog](CHANGELOG.md)
> before upgrading; configuration and APIs can change between releases.

## What it does

- Reviews new pull requests and updates its review after a push.
- Reads repository context and your review rules.
- Answers questions in pull request comments.
- Offers optional confidence scores and automatic approvals.
- Shows reviews, usage and costs in a web dashboard.

## What you need

- Kubernetes and Helm.
- Postgres with VectorChord and three database roles.
- A GitHub App, a Forgejo or Gitea bot account or a GitLab service account, and a model provider credential.
- An HTTPS address for the dashboard and the forge's webhooks.

The [setup guide](docs/setup.md) walks through installation and the first review.
No repository configuration file is required.

## Learn more

| I want to…               | Read                                             |
| ------------------------ | ------------------------------------------------ |
| Use the bot              | [Reviews and commands](docs/reviews.md)          |
| Add review rules         | [Repository settings](docs/repository-config.md) |
| Manage reviews and costs | [Dashboard](docs/dashboard.md)                   |
| Configure the service    | [Configuration reference](docs/configuration.md) |
| Secure a deployment      | [Security](docs/security.md)                     |
| Work on kritika          | [Development](docs/development.md)               |

## License

[AGPL-3.0](LICENSE)
