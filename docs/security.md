# Security

A review feeds untrusted content, a pull request's code and description
and whatever its commands fetch, to a model that can run tools. kritika
does that in a runner pod that holds little and reaches little, and keeps
what a pull request could use to weaken its own review out of its reach.

## Hardening an install

| Do                                                     | Why                                                                                                                                                                                           |
| ------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Install kritika into a namespace of its own            | runner Jobs run in the release namespace, and kritika's Role can create, patch and delete every Secret there, though it can never get or list one                                             |
| Turn on the chart's `networkPolicy.enabled`            | a runner pod can then reach nothing but DNS, Postgres and the [egress gateway](#the-egress-gateway)                                                                                           |
| Set `config.runnerRuntimeClass` to a sandboxed runtime | a kernel vulnerability reachable from the pod is contained by the sandbox rather than the node ([runner sandbox](#runner-sandbox))                                                            |
| Set `runner.resources`                                 | a runaway run is killed in its own cgroup rather than pressing on its node                                                                                                                    |
| Map roles on what the IdP controls                     | a role mapping is only as trustworthy as what it reads: map on groups or roles, not on an email or name a user can set on their own profile ([role mappings](configuration.md#role-mappings)) |

## What a runner holds

kritika serve creates a runner Job for each review, follow-up and index
run, and hands it a Secret of its own:

| Credential                        | What it can do                                                                                                               |
| --------------------------------- | ---------------------------------------------------------------------------------------------------------------------------- |
| a GitHub token minted for the run | read the repository under review and public repositories; the git fetch and `gh` use it                                      |
| a gateway token                   | reach the run's model through the gateway, until `config.gatewayTokenTtl` past the Job's deadline; revoked when the run ends |
| the runner role's database URI    | write its own run, and nothing else                                                                                          |

It never holds a provider key, the App's private key, or a credential
`egress.credentials` lets the gateway add. The pod runs with a read-only
root and no capabilities, as a service account with no permissions.

## What a pull request cannot change

- A repository's [`.kritika.yaml`](repository-config.md), its rule files,
  its skills and its agent files are read from the merge base, never the
  pull request's own tree, so a pull request cannot rewrite them to steer
  its own review.
- `.kritika.yaml` can narrow the admin's settings and add rules, but
  [some keys](configuration.md#what-only-the-admin-sets) are the admin's
  alone: the agent's bounds and commands, `confidence.instructions`, so a
  pull request cannot talk its own risk down, and `review.cost`.
- A trigger condition in `.kritika.yaml` under a name the admin's lists
  use, or a rule under an admin's rule id, is dropped.
- A skill grants no tool or command, whatever its frontmatter says.

## The egress gateway

The kritika serve pods serve a forward proxy on `service.gatewayPort`, and
runner Jobs are handed it as `HTTPS_PROXY` and `HTTP_PROXY`. With
`networkPolicy.enabled`, a runner's git fetch and every command it runs go
through the gateway, which allows a destination by name and by the address
it resolves to. `github.com` and `api.github.com` are allowed once an app
is declared; the configuration file's `egress` sets the rest:

```yaml
egress:
  allow: ["*", 10.10.0.5]
  deny: ["*.pastebin.com", 203.0.113.0/24]
  credentials:
    api.github.com: { env: GITHUB_TOKEN }
```

| Key           | What                                                                                                                                                            |
| ------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `allow`       | the destinations a runner may reach: a host, exact or `*.`-prefixed for every host under a domain, `"*"` for every host, or an IP address or CIDR               |
| `deny`        | destinations refused however `allow` matches them, in the same forms; one that cuts off `github.com` or `api.github.com` is rejected while an app is configured |
| `credentials` | a token the gateway adds to a plain `http://` request to that host, so the runner never holds it; the host must be allowed                                      |

A credential's token is a secret reference like any other in the file,
set from a Secret:

```yaml
configFile:
  egress:
    allow: [api.github.com, "*.githubusercontent.com", ghcr.io]
    credentials:
      api.github.com: { env: GITHUB_TOKEN }
env:
  GITHUB_TOKEN:
    valueFrom:
      secretKeyRef: { name: kritika-github-token, key: token }
```

A host is checked by name, then resolved once, and the gateway connects to
an address it checked rather than resolving the name again. An address
that is not on the public internet (RFC 1918 and other private ranges,
loopback, link-local, where cloud metadata endpoints live, and so on) is
refused unless an `allow` address covers it, so `"*"` opens the internet
and not the cluster or the LAN behind the gateway; a split-horizon name is
reached at its public address alone. A destination given as an address,
`https://10.10.0.5/`, is reached only when an `allow` address covers it,
public or not.

The refusal a runner's command sees says why:

| Refusal                                               | Why                                                         |
| ----------------------------------------------------- | ----------------------------------------------------------- |
| `host not allowed`                                    | no `allow` entry matches the name                           |
| `host denied`                                         | a `deny` entry matches the name                             |
| `address not allowed`                                 | no `allow` address covers the address given                 |
| `address denied`                                      | a `deny` address covers it                                  |
| `resolves to a private address no allow entry covers` | the name resolves only to addresses off the public internet |
| `resolves to a denied address`                        | every address the name resolves to is denied                |

### The model endpoint

The same port is a runner's model endpoint, and every review runs through
it, so it has no switch:

- kritika serve mints a token for each run and hands it to the pod in
  place of a provider key.
- The gateway answers each step through the account's provider with the
  key only kritika serve holds, refuses a step once the run's token budget
  or the account's `tokensPerMonth` is spent, and records the step's
  usage. Provider endpoints are therefore not in a runner's allowlist.
- The runner also asks it once for the review's similar code: the gateway
  embeds the diff with the instance's embedder and answers from the
  repository's index, charging the embedding to the run.

## Runner tools

A repository's `agent.commands` lets the model run allowlisted programs
over a checkout of the head commit: to read a dependency bump's release
notes and upstream pull requests with `gh`, fetch anything else with
`curl`, search with `rg` and `fd`, or read JSON and YAML with `jq` and
`yq`. Runner Jobs run on the release's `-tools` image, a distroless image
with all six, pinned by digest like the chart's image. A command is
offered only when the runner image has it on its `PATH`, so an image set
in `runner.image` without them offers none.

`gh` signs in with the run's own GitHub token, and an app already allows
`github.com` and `api.github.com` through the gateway. Every other host
`curl` reaches must pass the gateway too, so add the release hosts and
registries to `egress.allow`:

```yaml
egress:
  allow: ["*.githubusercontent.com"]
repositories:
  org-1/repo-1:
    agent: { commands: [gh, curl, fd, rg] }
```

What bounds a command:

- **No shell:** a command runs without one, and the `-tools` image has
  none either.
- **A bare environment:** `PATH`, its own `HOME` and the gateway, and for
  `gh` the run's token as `GH_TOKEN`. The runner makes itself unreadable
  to the command first, so it cannot read the runner's credentials from
  `/proc`.
- **Refused arguments:** the run tool refuses the arguments that would
  print the token or run a program outside the list: `gh auth`, `alias`,
  `config` and `extension`, `fd -x` and `-X`, `rg --pre` and
  `--hostname-bin`. It masks the token in a command's output and in the
  submitted review.
- **A plain checkout:** the files are written without execute bits, so
  nothing in the pull request can be started, and an empty `.git` whose
  `origin` is the repository's clone URL, with no history, lets a tool
  that locates a repository by its working tree, such as flate, find this
  one.

### Other repositories

When the run tool offers `gh` or `curl`, which already reach the network,
the agent also gets `fetch_repo`. It fetches another repository at a tag,
branch or commit, such as the upstream of a dependency on any host, and
writes its files beside the checkout for `rg` and `fd` to search, with the
diff from an earlier ref when asked. kritika makes the fetch itself, with
go-git: the model names a repository and a ref, never git arguments.

- **Anonymous HTTPS:** an `https://` clone URL, fetched without a
  credential through the gateway. `github.com` is already allowed; any
  other host must be in `egress.allow`, as for `curl`.
- **Only what is asked for:** depth one, without tags, and with paths only
  the files under them, from a server that can filter; one that cannot
  sends the whole tree, within the limit below.
- **Bounded:** 8 fetches a review, each taking at most 128 MiB from the
  server and two minutes, and 256 MiB of files written across them,
  beside the checkout. Symlinks, submodules and files over 1 MiB are left
  out, and the files are written without execute bits.

### More tools

The chart's `runner.tools` mounts more programs from their own images,
each as a read-only image volume put first on the runner's `PATH`, which
`agent.commands` may then allow. It needs Kubernetes 1.33 or newer.

| Key        | What                                                           |
| ---------- | -------------------------------------------------------------- |
| `name`     | the tool's name                                                |
| `image`    | its image, pinned by digest                                    |
| `path`     | the directory of its binaries inside the image; `/` unless set |
| `commands` | the commands it provides; its name unless set                  |

The binaries must be static or link only against glibc, libgcc and
libstdc++.

```yaml
runner:
  tools:
    - name: flate
      image: ghcr.io/home-operations/flate:0.6.5@sha256:e0e2d2de97539b3141977e8bb3bbb50ea5c5afe89477950e5fa972c9862478da
configFile:
  agent: { commands: [gh, flate] }
```

## Runner sandbox

A runner parses untrusted content and runs what the model asks. Set
`config.runnerRuntimeClass` to a sandboxed runtime the cluster offers
(`gvisor` with runsc, or a Kata class), so a kernel vulnerability
reachable from the pod is contained by the sandbox rather than the node.
It is advised, not required: without it the pod's other bounds still hold,
but the container runtime alone separates it from the node.

Set `runner.resources` too. Without them runner pods are BestEffort, with
no memory limit, so a runaway run presses on its whole node rather than
being killed in its own cgroup. Index and review runs have stayed under
100 MiB of working set so far, so a `128Mi` request with a `1Gi` limit
leaves ample room:

```yaml
runner:
  resources:
    requests: { cpu: 10m, memory: 128Mi }
    limits: { memory: 1Gi }
```

## What people can read

- A transcript may contain repository content the agent read, and every
  member of its account can read it
  ([retention](configuration.md#how-kritika-runs)).
- A review agent's conversation is also kept, unmasked since a provider's
  prompt cache matches only the exact text, for the pull request's next
  re-review: no dashboard view shows it, one that holds the run's own
  tokens is not kept, and the leader deletes it after two hours.
- An admin's every write is audit-logged in the same transaction as the
  change it makes, and the Configuration page shows a secret only as set
  or not, with a URL's credentials hidden.
- The summary comment is public, on a fork's pull request too, which is
  why the cost of a pull request's reviews shows only when the admin turns
  `review.cost` on.
