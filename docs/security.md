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
| Set `runner.runtimeClass` to a sandboxed runtime | a kernel vulnerability reachable from the pod is contained by the sandbox rather than the node ([runner sandbox](#runner-sandbox))                                                            |
| Set `runner.resources`                                 | a runaway run is killed in its own cgroup rather than pressing on its node                                                                                                                    |
| Map roles on what the IdP controls                     | a role mapping is only as trustworthy as what it reads: map on groups or roles, not on an email or name a user can set on their own profile ([role mappings](configuration.md#role-mappings)) |

## What a runner holds

kritika serve creates a runner Job for each review, follow-up and index
run, and hands it a Secret of its own:

| Credential                        | What it can do                                                                                                               |
| --------------------------------- | ---------------------------------------------------------------------------------------------------------------------------- |
| a GitHub token minted for the run | read the repository under review and public repositories; the git fetch and `gh` use it, `fetch_repo` does not               |
| a gateway token                   | reach the egress proxy and, for a review or follow-up, the run's model through the gateway, until `config.gatewayTokenTtl` past the Job's deadline; revoked when the run ends |
| the runner role's database URI    | connect as the restricted runner role                                                                                          |

Each run also receives a separate random database token. Row-level security
requires that token as well as the run ID for reads and writes. Only its
SHA-256 hash is stored; IDs published in database notifications grant no
access. The runner checks its configured database role and run credential
before fetching repository content.

It never holds a provider key, the App's private key, or a credential
`egress.credentials` lets the gateway add. The pod runs with a read-only
root and no capabilities, as a service account with no permissions.

## What a pull request cannot change

- A repository's [`.kritika.yaml`](repository-config.md), its rule files
  and its skills are read from the merge base, never the pull request's
  own tree, so a pull request cannot rewrite them to steer its own review.
- `.kritika.yaml` can narrow the admin's settings and add rules, but
  [some keys](configuration.md#what-only-the-admin-sets) are the admin's
  alone: the agent's bounds and commands, `confidence.instructions`, so a
  pull request cannot talk its own risk down, and `review.cost`.
- A trigger condition in `.kritika.yaml` under a name the admin's lists
  use, or a rule under an admin's rule id, is dropped.
- A skill grants no tool or command, whatever its frontmatter says.

Context files, including explicitly listed `AGENTS.md` or `CLAUDE.md`,
are read from the pull request's head. A pull request can change or delete
them and thereby change the background material its reviewer reads. They
are optional references, not trusted review instructions. Put requirements
that a pull request must not rewrite in merge-base [rules](repository-config.md#rules).

## The egress gateway

The kritika serve pods serve a forward proxy on `service.gatewayPort`, and
runner Jobs are handed it as `HTTPS_PROXY` and `HTTP_PROXY`, with the run's
gateway token as the proxy URL's password. The gateway admits a proxy
request by that token alone, so nothing else that reaches the port can
relay through it or use a credential it adds. With
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
config:
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

The refusal a runner's command sees says why, and the gateway records it
against the run, as the host the agent asked for and the reason, never an
address the name resolved to, so a private address stays out of the
dashboard. The **Refused hosts** panel of the
[analytics](dashboard.md#analytics) lists them, so what to add to `allow`
comes from real refusals. A run has at most 100 such rows, one per host
and reason, so a looping agent cannot fill the table, and the leader
deletes them with the run's transcripts.

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

The model runs allowlisted programs over a checkout of the head commit:
to read a dependency bump's release notes and upstream pull requests with
`gh`, fetch anything else with `curl`, search with `rg` and `fd`, or read
JSON and YAML with `jq` and `yq`. Runner Jobs run on the release's `-tools`
image, pinned by digest like the chart's image: a distroless image that
carries [mise](https://mise.jdx.dev/) and those six tools as mise system
installs, declared by its system config, `/etc/mise/config.toml`, at the
versions `build/tools/mise.toml` pins. The chart's `runner.mise.config`
layers the instance's config over it, as mise merges configs: per tool,
the instance's entry replaces the image's, options included, and the
image's other tools stay. So an admin adds a tool, `crane` for a
container image's tags, digest, manifest and labels, say, or `flate` for
a chart's rendered output, without an image build or a list of the six:

```yaml
runner:
  mise:
    config: |
      [tools]
      crane = "0.22.1"
      "github:home-operations/flate" = "0.6.5"
```

A tool at the image's version runs from its system install; any other
downloads on the first run that uses it, into the run's own scratch space,
at the version the config pins and with the verification its backend
gives it. The run tool offers every command the merged config's tools
provide, `mise` itself excepted. An image set in `runner.image` without
mise offers the six default commands its builder put on its `PATH`, and
reads no repository config, which the review summary says. A follow-up
posts no summary, so its reply carries such notes, and a command's setup
failure, instead.

A repository's own [`.kritika/mise.toml`](repository-config.md#repository-tools)
layers tools over the image and instance configs. Both this file and
`.kritika/mise.lock` are read from the merge base, so a pull request
cannot change the tools or download pins used to review itself.

The runner keeps of a repository's `[tools]` entry its version and the
options that select, verify and extract its download. The options that
run a command or set its environment (`postinstall`, `install_env`,
`depends`), that would send mise's token to another host (`api_url`,
`headers`) and any option holding a template are dropped, since mise
renders templates, `exec()` among them, when it reads the config and
runs install hooks with the run's GitHub token in their environment.
Repository `[env]` permits only literal strings under non-reserved names;
templates and `_.` directives are dropped. `[tasks]`, `[settings]` and
hooks are dropped too, with summary notes. Mise settings and egress
permissions remain the admin's. The filtered config and its lock live
outside the checkout in the run's read-only `HOME`, trusted only for the
runner's own mise calls. A tool without a locked version, URL and
checksum for the runner's platform is not offered, unless the image has
that version installed already and nothing is downloaded. Downloads use
`mise install --locked` and stay within the existing install timeout and
gateway restrictions.

Enabling repository tools trusts the repository's merged tool definitions.
kritika does not vet arbitrary binaries, install hooks or `install_env`.
A tool with an interpreter, plugin loader or subprocess feature can run
code from the pull request despite the checkout's missing execute bits.
Such a tool can also invoke mise by its absolute path; keeping mise out of
the offered commands does not prevent subprocess execution.

The clean environment hides the runner's database and model credentials,
but commands still receive the gateway proxy credential, and the tool
offered as `gh` receives the run's read-only GitHub token, so a repository
tool is not offered as `gh` unless the image has it installed at the
repository's version; a summary note says so. Egress remains bounded by
the gateway and network policy. Replacing a default tool also
keeps the argument restrictions written for the shipped version; another
version may interpret those flags differently. An instance that reviews
repositories whose tool choices it does not trust should run them on a
runner image without mise, which reads no repository config.

`gh` signs in with the run's own GitHub token, which reads the repository
under review and public repositories and no other private one; the run
tool's description says so, so the agent does not try another way in. An
app already allows `github.com` and `api.github.com` through the gateway.
Every other host `curl` or a declared tool reaches must pass the gateway
too, so add the release hosts and registries to `egress.allow`; the run
tool's and `fetch_repo`'s descriptions name the allowed hosts to the
agent, so it does not spend calls on the rest:

```yaml
egress:
  allow: ["*.githubusercontent.com"]
```

What bounds a command:

- **No shell:** a command runs without one, and the `-tools` image has
  none either.
- **A bare environment:** `PATH`, with the tools' directories first, its
  own `HOME` and the gateway, the mise config's `[env]`, and for `gh` the
  run's token as `GH_TOKEN`. The runner makes itself unreadable to the
  command first, so it cannot read the runner's credentials from `/proc`,
  and runs mise with the same bare environment, so neither a tool's
  install hooks nor a config's templates see them. `HOME` is apart from
  the checkout, so a pull request cannot plant a `.curlrc`; the runner
  writes one that turns off `curl`'s progress meter, which would otherwise
  land in the result, follows the redirects the gateway hands back, and
  fails on an HTTP error with its body kept, so a host the gateway
  refuses is a failed command naming the reason rather than a success
  whose output is the refusal. A config's `[env]` may not set what the
  runner sets or what would point a tool past the argument checks:
  `HOME`, `PATH`, the proxy variables, `GH_*`, `GIT_*`, `MISE_*`, `LD_*`,
  `GITHUB_TOKEN`, `CURL_HOME`, `XDG_CONFIG_HOME`, `RIPGREP_CONFIG_PATH`,
  `DOCKER_CONFIG`, `PAGER`, `BROWSER`, `EDITOR` and `VISUAL` are dropped,
  and the review's summary says so.
- **mise is never a command:** the runner runs it by its path outside
  `PATH`, a tool that would provide a `mise` command is not offered, and
  every install goes through the runner, with a two-minute timeout. A
  tool that cannot be installed, or does not provide the command it was
  offered as, fails that call with the reason. The summary records these
  failures even if the model omits them, as well as tools that could not
  be offered. Installs reach the tool's release host
  through the gateway, so `egress.allow` must name it: for a GitHub
  release `release-assets.githubusercontent.com`, which the download is
  redirected to from `github.com`, which an app already allows, plus
  `tuf-repo-cdn.sigstore.dev` for a tool whose aqua recipe verifies an
  attestation; mise also asks `mise-versions.jdx.dev` for a tool's
  versions, and falls back to the GitHub API when the gateway refuses it.
- **Refused arguments:** the run tool refuses the arguments that would
  print a credential or run a program outside the list: `gh auth`,
  `alias`, `config` and `extension`, `fd -x` and `-X`,
  `rg --pre` and `--hostname-bin`, and `curl --trace`, `--trace-ascii`,
  `--trace-config` and `--config`/`-K`, which would print the proxy
  credential in pieces the mask cannot match. It masks the token in a
  command's output and in the submitted review.
- **A plain checkout:** the files are written without execute bits,
  preventing direct execution. An interpreter or plugin loader can still
  read and run them. An empty `.git` whose
  `origin` is the repository's clone URL, with no history, lets a tool
  that locates a repository by its working tree, such as flate, find this
  one. Ignored paths, lockfiles and vendored trees among them, are left
  out of it; the agent is told `read_file` and `read_diff` still read
  them, so it need not reach for the forge's API for a lockfile.
- **Bounded output:** a command's output is cut to the agent's `output`
  (32 KiB by default), keeping the end of its stderr past the cut. A cut
  output is kept whole, with the token masked, in a file beside the
  checkout under `../upstream/`, which the note after the cut names, so
  the agent searches a long render or page with `rg` or reads it with
  `read_file` in line ranges rather than running the command again with
  narrower arguments. One file holds at most 4 MiB, and the files count
  against the 256 MiB `fetch_repo` may write there; past either, the note
  says the output was not kept.

### Other repositories

When the run tool offers `gh` or `curl`, which already reach the network,
the agent also gets `fetch_repo`. It fetches another repository at a tag,
branch or commit, such as the upstream of a dependency on any host, and
writes its files beside the checkout for `read_file` to read and `rg` and
`fd` to search, with the diff from an earlier ref when asked, or lists the
repository's tags, so the agent can find a version's tag rather than guess
its name. kritika makes the fetch itself, with go-git: the model names a
repository and a ref, never git arguments.

- **Anonymous HTTPS:** an `https://` clone URL, fetched without a
  credential through the gateway, so a private repository, the one under
  review included, is not fetched; the tool's description says so.
  `github.com` is already allowed; any other host must be in
  `egress.allow`, as for `curl`.
- **Only what is asked for:** depth one, without tags, and with paths only
  the files under them, from a server that can filter; one that cannot
  sends the whole tree, within the limit below.
- **Tags by prefix:** a listing asks the server for `refs/tags/` only,
  through the same gateway and limits as a fetch, and writes nothing. A
  fetch that names a tag the repository lacks lists the tags as well,
  counted among the listings: when the repository tags the version named
  under one other name, `v1.2.3` or `chart-1.2.3` for `1.2.3`, that tag is
  fetched in its place and the answer says so; otherwise the error names
  the closest.
- **Bounded:** [`agent.fetches`](configuration.md#the-agent) fetches, 8
  unless set, and 8 tag listings a review, each taking at most 128 MiB
  from the server and two minutes, and 256 MiB of files written across
  the fetches and the run tool's kept outputs, beside the checkout.
  Symlinks, submodules and files over 1 MiB are left out, and the files
  are written without execute bits.
- **Read in place:** `read_file` reads a fetched file, or a kept output,
  at the path the fetch or the run tool names, under `../upstream/`,
  opening it through a root confined to that directory, so neither a path
  nor a link reaches past it. Every other path it reads from the head
  commit's git objects.

### More tools

Declare a tool in `runner.mise.config` by its registry short name where
it has one (`helm`, `crane`), which names its commands before it installs,
else as `aqua:owner/repo`, whose recipe names the release asset and what
mise verifies, and as `github:owner/repo` as a last resort, with
`filter_bins` naming its commands unless the repository's name is the
command. The binaries must be static or link only
against glibc, libgcc and libstdc++, which is all the image carries, so a
backend that builds or needs a runtime (`npm:`, `pipx:`, `cargo:`, `go:`)
does not work there. The config's `[settings]` apply to every runner: a
`url_replacements` rule sends downloads to a mirror, whose host must pass
the gateway like any other, and `[env]` reaches every command.

### Migrating from image-mounted tools

`runner.tools` and `KRITIKA_RUNNER_TOOLS` are removed. Delete them from
Helm values, the application configuration and the environment. Declare
each tool in the instance's mise config instead; for example, a flate
image mount becomes:

```yaml
runner:
  mise:
    config: |
      [tools]
      "github:home-operations/flate" = "0.6.5"
```

Outside Helm, the application key is `runner.miseConfig`, or use
`KRITIKA_RUNNER_MISE_CONFIG`. Tools no longer use Kubernetes image volumes,
so adding one does not require Kubernetes 1.33. A custom runner image can
still provide named commands on its own `PATH`.

The chart's `runner.mise.config` is empty by default: the image's
`/etc/mise/config.toml` declares `curl`, `fd`, `gh`, `jq`, `rg` and `yq`,
and every review offers those, the instance's tools and the repository's.
`crane` is not a default: declare it when needed. If building on top of the
`-tools` image, its preinstalled tools live in mise's system install
directory, not `/usr/local/bin`.

## Runner sandbox

A runner parses untrusted content and runs what the model asks. Set
`runner.runtimeClass` to a sandboxed runtime the cluster offers
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
