---
title: Security
---

Reviews process untrusted code and text. Each review runs in a separate pod
with limited credentials. Use the deployment controls below to restrict what
that pod can reach.

## Hardening an install

| Setting or action                               | Why it matters                                                                                                              |
| ----------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------- |
| Use a dedicated namespace.                      | kritika can create Jobs and Secrets in its namespace.                                                                       |
| Keep `runner.admissionPolicy.enabled` on.       | Restricts the Jobs and Secrets the service can create to the expected runner forms.                                         |
| Keep `networkPolicy.enabled` on, the default.   | Restricts runners to DNS, Postgres and the gateway, the only bound on where the commands a review runs can connect.         |
| Set `runner.podTemplate.spec.runtimeClassName`. | Uses an installed sandboxed runtime, such as gVisor, to isolate untrusted work, including the commands a review runs.       |
| Set `runner.podTemplate.spec.resources`.        | Limits the CPU and memory a runner can consume.                                                                             |
| Map sign-in roles from trusted groups or roles. | Avoids granting access from profile fields users can change themselves. See [role mappings](/configuration/#role-mappings). |

`networkPolicy.type: auto`, the default, renders Cilium's or Calico's own
policies where the cluster serves their API. Configure
[database](/database/#secrets-and-connection-uris) and [DNS](#name-lookups)
destinations for your cluster.

Without the admission policy, the service's Role can create Jobs that mount
other Secrets or use other ServiceAccounts. Without enforced Pod Security,
those Jobs may also reach the node. A dedicated namespace alone does not
replace these controls.

The rest of this page explains credentials, network access and runner tools.
For runner resource and runtime examples, go to [Runner sandbox](#runner-sandbox).

## What a runner holds

kritika serve creates a runner Job for each review, follow-up and index
run, and hands it a Secret of its own:

| Credential                     | What it can do                                                                                                                                                                                        |
| ------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| a forge token for the run      | fetch the repository under review; which token it is and what else it can do depend on the forge (see below)                                                                                          |
| a gateway token                | reach the egress proxy and, for a review or follow-up, the run's model through the gateway; revoked when the run ends, and valid until `config.gatewayTokenTtl` past the Job's deadline at the latest |
| the runner role's database URI | connect as the restricted runner role                                                                                                                                                                 |

The forge token for the run depends on the forge:

- On GitHub, it is a token minted to read the repository under review and
  public repositories. The git fetch and `gh` use it.
- On Forgejo, it is a token signed for the run as the `runnerAudience`
  integration. Without a `runnerAudience` integration, it is signed as the
  `audience` integration, which can write.
- On GitLab and Gitea, it is the connection's `runnerToken`. Without one,
  it is the connection's `token`, which can write.

Off GitHub, only the git fetch uses it. `fetch_repo` uses none of these
tokens.

Each run also receives a separate random database token. Row-level security
requires that token as well as the run ID for reads and writes. Only its
SHA-256 hash is stored; IDs published in database notifications grant no
access. The runner checks its configured database role and run credential
before fetching repository content.

The runner never holds a provider key, the App's private key, or a
credential that `egress.credentials` lets the gateway add. The pod runs
with a read-only root and no capabilities, as a service account with no
permissions.

## What a pull request cannot change

- A repository's [`.kritika.yaml`](/repository-config/), its rule files
  and its skills are read from the merge base, never from the pull
  request's own tree. A pull request therefore cannot rewrite them to
  steer its own review.
- `.kritika.yaml` can narrow the admin's settings and add rules, but
  [some keys](/configuration/#what-only-the-admin-sets) are the admin's
  alone: the agent's bounds, `confidence.instructions`,
  `confidence.floors`, `review.cost` and `review.replies`. Because the
  two `confidence` keys are the admin's, a pull request cannot talk its
  own risk down. A repository adds commands only through its
  [`tools`](/repository-config/#repository-tools), which are read from
  the merge base.
- A trigger condition in `.kritika.yaml` is dropped when one of the
  admin's trigger conditions has the same name. So is a rule with an
  admin's rule id.
- A skill grants no tool or command, whatever its frontmatter says.

Context files, including explicitly listed `AGENTS.md` or `CLAUDE.md`,
are read from the pull request's head. A pull request can change or delete
them and thereby change the background material its reviewer reads. They
are optional references, not trusted review instructions. Put requirements
that a pull request must not rewrite in merge-base [rules](/repository-config/#rules).

## The egress gateway

The kritika serve pods serve a forward proxy on `service.gatewayPort`.
Runner Jobs are handed it as `HTTPS_PROXY` and `HTTP_PROXY`, with the
run's gateway token as the password in the proxy URL. The gateway admits a
proxy request by that token alone, so nothing else that reaches the port
can relay through it or use a credential it adds.

With `networkPolicy.enabled`, the default, a runner's git fetch and every
command it runs go through the gateway to reach anything except the
cluster's DNS and the database. The gateway allows a destination by name
and by the address it resolves to. Each connection's forge is allowed once
it is declared: `github.com` and `api.github.com` for a GitHub App, and
the instance's host for a Forgejo, GitLab or Gitea connection. The
configuration file's `egress` sets the rest:

```yaml
egress:
  allow: ["*", 10.10.0.5]
  deny: ["*.pastebin.com", 203.0.113.0/24]
  credentials:
    api.github.com: { env: GITHUB_TOKEN }
```

| Key           | What                                                                                                                                              |
| ------------- | ------------------------------------------------------------------------------------------------------------------------------------------------- |
| `allow`       | the destinations a runner may reach: a host, exact or `*.`-prefixed for every host under a domain, `"*"` for every host, or an IP address or CIDR |
| `deny`        | destinations refused however `allow` matches them, in the same forms                                                                              |
| `credentials` | a token the gateway adds to a plain `http://` request to that host, so the runner never holds it; the host must be allowed                        |

`deny` host patterns are checked at startup: one that cuts off a host a
connection's forge needs is rejected. A `deny` address or CIDR is instead
checked on each request against what a host resolves to, so one can still
cut a forge off.

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

A host is checked by name, then resolved once. The gateway connects to an
address it checked rather than resolving the name again.

An address that is not on the public internet is refused unless an
`allow` address covers it. Such addresses include RFC 1918 and other
private ranges, loopback, and link-local, where cloud metadata endpoints
live. As a result, `"*"` opens the internet and not the cluster or the
LAN behind the gateway, and a split-horizon name is reached only at its
public address. A destination given as an address, such as
`https://10.10.0.5/`, is reached only when an `allow` address covers it,
whether it is public or not.

The refusal a runner's command sees says why. The gateway also records it
against the run, as the host the agent asked for and the reason. It never
records an address the name resolved to, so a private address stays out
of the dashboard. The **Refused hosts** panel of the
[analytics](/dashboard/#analytics) lists these refusals, so what you add
to `allow` comes from real refusals. A run has at most 100 such rows, one
per host and reason, so a looping agent cannot fill the table. The leader
deletes them with the run's transcripts.

| Refusal                                               | Why                                                         |
| ----------------------------------------------------- | ----------------------------------------------------------- |
| `host not allowed`                                    | no `allow` entry matches the name                           |
| `host denied`                                         | a `deny` entry matches the name                             |
| `address not allowed`                                 | no `allow` address covers the address given                 |
| `address denied`                                      | a `deny` address covers it                                  |
| `resolves to a private address no allow entry covers` | the name resolves only to addresses off the public internet |
| `resolves to a denied address`                        | every address the name resolves to is denied                |

### Name lookups

Name lookups do not pass the gateway. With `networkPolicy.enabled`, the
pods send DNS only to the cluster's DNS:

- the pods that `networkPolicy.dns.namespace` and `podSelector` select,
  which are `k8s-app: kube-dns` in `kube-system` unless set
- the addresses `networkPolicy.dns.nodeLocalAddresses` lists, such as
  NodeLocal DNSCache's `169.254.20.10` on each node's own network

For those addresses, the `cilium` flavor allows DNS to the node itself.
Cilium gives the node's addresses the host's identity, which no CIDR
selects.

The cluster's DNS forwards the lookup of a name outside the cluster to
that name's servers, so a lookup alone tells those servers the name.
`curl` looks up the host of an `https://` URL even when it fetches the URL
through the gateway.

With the `cilium` flavor, a runner pod may look up only the names of the
cluster's Services, `<service>.<namespace>.svc.cluster.local`, including
the gateway's and the database's. On a cluster with another DNS domain,
set the chart's `clusterDomain` to it: the gateway's address runner Jobs
are handed and this rule both end in it. The cluster's DNS answers those itself.
The lookup of an `ExternalName` Service's name goes on to the name the
Service points at, which the Service's author set, not the runner. Cilium
refuses any other name. The `default` and `calico` flavors cannot filter
names.

These rules need Cilium's L7 proxy, which is on unless turned off. Where
it is off, Cilium refuses the runner's CiliumNetworkPolicy for them. The
`cilium` flavor also renders a plain NetworkPolicy for runner pods that
allows the database and the gateway but no DNS. So a refused policy
leaves runner pods unable to look up any name. Every run fails,
rather than running with no policy, until `networkPolicy.type` is
`default`.

Runner pods resolve with `ndots: 3`, so the gateway's name, such as
`kritika-gateway.kritika.svc.cluster.local` or
`kritika-gateway.kritika.svc.local` with a single-label `clusterDomain`,
is looked up as written rather than first through the search list.
Cilium would refuse the search list's expansions of that name, and musl's
resolver, which statically linked tools carry, gives up at a refusal.

### The model endpoint

The same port is also a runner's model endpoint. Every review runs through
it, so it has no switch:

- kritika serve mints a token for each run and hands it to the pod in
  place of a provider key.
- The gateway answers each step through the account's provider, with the
  key that only kritika serve holds. It refuses a step once the run's
  token budget or the account's `tokensPerMonth` is spent, and it records
  the step's usage. Provider endpoints are therefore not in a runner's
  allowlist.
- The runner also asks it for the review's similar code, once for each
  part of a split review. The agent's `search_code` asks it at most 10
  more times. The gateway embeds the diff or the query with the instance's
  embedder, answers from the repository's index, and charges the embedding
  to the run.

## Runner tools

The model runs allowlisted programs over a checkout of the head commit. It
can:

- read a dependency bump's release notes and upstream pull requests with
  `gh`, which is offered on GitHub alone, as
  [Git forges](/forges/#what-works-where) lists
- fetch anything else with `curl`
- search with `grep` and `find`
- read JSON with `jq`

It reads a container image with [`fetch_oci`](#images-and-artifacts),
which needs no tool.

Runner Jobs run on the release's `-tools` image, which is pinned by digest
like the chart's image. Its root, from [Red Hat Hardened
Images](https://www.redhat.com/en/products/hardened-images), holds bash
and coreutils but no package manager. It also holds:

- `curl`, `find`, `grep` and `jq`, as its packages build them, with glibc,
  libstdc++ and the timezone database
- `gh`, as its release builds it, checked against the release's checksums

Every other tool is the repository's: the
[`tools`](/repository-config/#repository-tools) of its `.kritika.yaml`,
packages of the [aqua registry](https://github.com/aquaproj/aqua-registry).
A tool from it replaces an image command of the same name, except `gh`.
That lets a repository add a tool without an image build, such as `flate`
for a chart's rendered output, or `rg` where it prefers it to `grep`:

```yaml
tools:
  aqua:home-operations/flate: v0.6.5
```

The file is read from the merge base, so a pull request cannot change
the tools used to review itself. A tool downloads on the first run that
uses it, at the declared version, into the run's own scratch space.

The runner reads the registry at the release this kritika pins. It
fetches that release's `registry.yaml` and checks it against the SHA-256
recorded with the release, then resolves each tool's download, its
packing and its commands as aqua does for the runner's platform. Nothing
of aqua's or the tool's runs to install it.

Before it downloads a tool, the runner fetches what the download must
match, around the gateway's cache: the digest GitHub keeps of a release's
asset, which also confirms the version is one of the release's tags, else
the checksum file the registry names for the package. A tool with neither
is not installed. The download is checked before anything of it is
unpacked, unpacked beside the tool's directory and moved into it whole.
Unpacking never writes through a link or out of that directory, and is
bounded in what it decompresses. No committed lock pins a download across
runs, so a release asset replaced upstream under the same tag, its digest
or checksum file with it, installs.

Where the registry declares a signature of the download or of its checksum
file, the runner checks it before unpacking, and an install whose signature
does not verify fails:

- a cosign bundle, against the certificate identity and issuer the
  registry's options name, and its GitHub workflow repository and ref; an
  option the runner does not read fails the install rather than loosen the
  identity
- a GitHub artifact attestation, from the GitHub API, held to what
  `gh attestation verify` holds it to: the signer workflow or repository
  the registry names, else any workflow of the release's repository,
  GitHub Actions' issuer, the source repository and the predicate type.
  The runner reads the bundle the API returns inline, else the one at
  the attestation's `bundle_url`, on a host `egress.allow` must name,
  `tmaproduction.blob.core.windows.net` today; an attestation with
  neither fails
- SLSA provenance in a Sigstore bundle, held to slsa-github-generator's
  generic, Go or container-based builder at a release's tag, the
  builders slsa-verifier trusts by default, and to the source repository
  and the release's tag
- a minisign signature, against the public key the registry pins

A Sigstore bundle is checked offline, against Sigstore's public-good trusted
root, which kritika carries as Sigstore's TUF repository publishes it, so a
runner needs no Sigstore host. A file with a Sigstore bundle's media type
that does not parse as one fails the install, as does a cosign bundle that
is not a Sigstore bundle. A signed checksum file is read in place of
GitHub's digest. A cosign signature and certificate apart from a bundle, a
key-signed one, provenance outside a Sigstore bundle and an attestation by
GitHub's own Sigstore instance are not checked; the runner's log names
each, and the tool installs on its checksum alone.

The run tool offers the image's commands that it finds on `PATH`, and
the commands the registry names for the repository's tools. An image set
as the runner container's image in `runner.podTemplate` offers whichever
of `curl`, `find`, `gh`, `grep` and `jq` its builder put on its `PATH`,
and the repository's tools as any image does. A tool that is not offered,
and why, goes in the review's summary. A follow-up posts no summary, so
its reply carries such notes instead, along with a command's setup
failure.

From an image's `PATH`, the run tool offers only the five commands above,
so a tool every repository should have is declared in each repository's
`tools`.

A `tools` entry is a package's name and a release's tag, and nothing
else. A package that builds from source, or takes a repository's files
rather than a release's, is not offered: an install only downloads and
unpacks.

Egress permissions remain the admin's, and so do the variables that the
runner container's `env` in `runner.podTemplate` sets. The commands get
these variables, each from a literal value or a Secret's key:

```yaml
runner:
  podTemplate:
    spec:
      containers:
        - name: runner
          env:
            - name: KONFLATE_URL
              value: https://konflate.example.internal
            - name: KONFLATE_TOKEN
              valueFrom:
                secretKeyRef: { name: konflate, key: token }
```

A value from a Secret is masked out of what the commands print and out
of the review, as the run's tokens are. A literal value is not. The
Secret must be in the release namespace, and the runner admission
policies let the runner pod read it.

kritika sets the variables on the runner container as
`KRITIKA_COMMAND_ENV_<name>` and hands them to the commands under their
own names. They configure the commands and never the runner itself: a
`GODEBUG` or `TMPDIR` meant for a tool does not change how the runner
fetches, logs or connects.

Installs stay within the install timeout and the gateway's restrictions.
Reading the registry reaches `raw.githubusercontent.com`, and looking a
release's digest up reaches the GitHub API, with the run's token on
GitHub and without one on another forge. Those hosts need `egress.allow`
too.

Enabling repository tools trusts the repository's tool choices. kritika
does not vet arbitrary binaries. A tool with an interpreter, plugin
loader or subprocess feature can run code from the pull request despite
the checkout's missing execute bits.

The run tool chooses which programs run, never how. It passes the model's
arguments as given, and those follow what the model reads, including the
pull request under review. A command, whether the image's or a
repository's, can therefore do whatever its program can. It can run
another program in the image, read what the runner's user can read in the
pod, print what its own environment holds, and send it wherever the
network lets it.

The runner pod is the boundary. Its NetworkPolicy, on by default, lets
runner pods reach three places:

- the cluster's DNS
- the Postgres port where `networkPolicy.postgres` says the database is
- the gateway

`egress.allow` and `egress.deny` therefore hold for everything else a
command connects to. Without the policy, a command can connect around the
gateway, so turn the policy off only where something else confines the
egress of runner pods.

A command reaches the first two directly as well. The cluster's DNS
forwards the lookup of an outside name, which only the `cilium` flavor
filters (see [Name lookups](#name-lookups)). The database admits only the
runner role's credentials, which a command never sees. A sandboxed
runtime keeps a kernel exploit from reaching the node (see
[Runner sandbox](#runner-sandbox)).

The clean environment hides the runner's database and model credentials.
Commands still receive the gateway proxy credential, though, and the tool
offered as `gh` receives the run's read-only GitHub token. For that
reason, a repository tool is never offered as `gh`; the image's is, and a
summary note says so.

Both credentials are the run's own. The gateway token stops working when
the run ends, or `config.gatewayTokenTtl` past the Job's deadline at the latest.
The GitHub token reads what the run may read and nothing more. It expires
an hour after it is minted, whether the run has ended or not.

`gh` signs in with the run's own GitHub token. That token reads the
repository under review and public repositories, and no other private
one. The run tool's description says so, so the agent does not try
another way in. An app already allows `github.com` and `api.github.com`
through the gateway. Every other host that `curl` or a declared tool
reaches must pass the gateway too, so add the release hosts and
registries to `egress.allow`. The descriptions of the run tool and
`fetch_repo` name the allowed hosts to the agent, so it does not spend
calls on the rest:

```yaml
egress:
  allow: ["*.githubusercontent.com"]
```

What bounds a command:

- **No shell of its own:** the run tool starts the program itself, not
  through a shell. The `-tools` image does carry bash, which a command
  such as `find -exec` can start, inside the same bounds.
- **Its own lifetime:** a command and the processes it starts form a
  process group of their own. The group is stopped as a whole when the
  command times out and once it exits, so nothing the command started
  outlives the call. A program that leaves the group on purpose, with
  `setsid`, is still stopped when the run's pod ends. Where the runtime
  refuses the wait this relies on, the run's log says so. A timeout then
  stops the command alone, since its group's id may no longer be its own.
- **A bare environment:** a command gets `PATH`, with the repository's
  tools first, its own `HOME`, `XDG_CACHE_HOME`, the gateway and the
  admin's variables. `gh` also gets the
  run's token as `GH_TOKEN`. The pod's `NO_PROXY` is not passed on, so
  `curl` reaches even `localhost` and the gateway's own host through the
  gateway. Its `--connect-to` or an `--alt-svc` cache only changes what it
  asks the gateway for.
  The runner first makes itself unreadable to the command, so the command
  cannot read the runner's credentials from `/proc`.
  `HOME` is apart from the checkout, so a pull request cannot plant a
  `.curlrc`. The runner writes one that turns off `curl`'s progress meter,
  which would otherwise land in the result. It also makes `curl` follow
  the redirects the gateway hands back, and fail on an HTTP error with the
  body kept. A host the gateway refuses is then a failed command that
  names the reason, rather than a success whose output is the refusal.
  The `.curlrc` also limits `curl` to `http` and `https`, the schemes the
  proxy variables cover. A URL with any other scheme, given or redirected
  to, fails rather than trying a connection without the gateway. An
  argument can lift that limit; the NetworkPolicy then refuses such a
  connection anywhere but the cluster's DNS and the database's port.
  The admin's variables may not set what the runner sets, or what would
  change every call of a tool rather than the one the model makes. These
  are `HOME`, `PATH`, the proxy variables, `GH_*`, `GIT_*`, `LD_*`,
  `GITHUB_TOKEN`, `CURL_HOME`, `XDG_CONFIG_HOME`, `XDG_CACHE_HOME`,
  `RIPGREP_CONFIG_PATH`, `DOCKER_CONFIG`, `PAGER`, `BROWSER`, `EDITOR` and
  `VISUAL`. kritika serve refuses to start with any of them among the
  admin's variables.
- **Few writable places:** `HOME`, with the repository's tools installed
  there, is read-only once the commands are offered. So is the scratch
  directory that holds it. A command writes to the checkout, to the
  `../upstream/` directory beside it, and to `XDG_CACHE_HOME`.
  `XDG_CACHE_HOME` is a directory in the same scratch space, where a tool
  that keeps a cache, such as flate, puts it. These are permissions, not a
  mount. The commands run as the
  runner's user, so a program that changes permissions, such as an
  interpreter a repository adds, can make `HOME` writable again. It can
  then change what the run's later commands find there: the `.curlrc` or
  a tool installed there. The image's own tools, `gh` among them, are in
  its read-only root.
- **Installs:** every install goes through the runner, with a two-minute
  timeout. A tool that cannot be installed fails that call with the
  reason. The summary records these failures even if the model omits
  them, as well as tools that could not be offered.
  Installs reach the tool's release host through the gateway, so
  `egress.allow` must name it. For a GitHub release, that host is
  `release-assets.githubusercontent.com`, which the download is
  redirected to from `github.com`; an app already allows `github.com`.
- **Masked output:** the run's tokens, the proxy credential as `curl -v`
  prints it, and the values of the admin's variables from a Secret are
  all masked. They are masked out of a command's output, the files a cut
  output is kept in, and the submitted review. A program can still print
  one in a form the mask does not match, as `curl --trace` does in its
  hex dump of the headers. The gateway token stops working with the run,
  and the GitHub token within the hour, but an admin's variable never
  does. Scope its token to what the commands need, or have the gateway
  add it through [`egress.credentials`](#the-egress-gateway) so the runner
  never holds it.
- **A plain checkout:** the files are written when the first command
  runs, without execute bits, which prevents direct execution. A review
  that reads the head only through its own tools writes no files. An
  interpreter or plugin loader can still read and run them. The checkout
  has an empty `.git`, with no history, whose `origin` is the repository's
  clone URL. It lets a tool that locates a repository by its working
  tree, such as flate, find this one. Ignored paths, including lockfiles
  and vendored trees, are left out of the checkout. The agent is told
  that `read_file` and `read_diff` still read them, so it need not reach
  for the forge's API for a lockfile.
- **Bounded output:** a command's output is cut to the agent's `output`
  (32 KiB by default), keeping the end of its stderr past the cut. A cut
  output is kept whole, masked like the output, in a file beside the
  checkout under `../upstream/`. The note after the cut names that file.
  The agent can then search a long render or page with `grep`, or read it
  with `read_file` in line ranges, rather than run the command again with
  narrower arguments. One file holds at most 4 MiB, and the files count
  against the 256 MiB that `fetch_repo` may write there. Past either
  limit, the note says the output was not kept.

### Other repositories

When the run tool offers `gh` or `curl`, which already reach the network,
the agent also gets `fetch_repo`. It fetches another repository, such as
the upstream of a dependency on any host, at a tag, branch or commit. It
writes the files beside the checkout, for `read_file` to read and for
`grep` and `find` to search. When asked, it also gives the diff from an
earlier ref. It can instead list the repository's tags, so the agent can
find a version's tag rather than guess its name. kritika makes the fetch
itself, with go-git: the model names a repository and a ref, never git
arguments.

- **Anonymous HTTPS:** an `https://` clone URL, fetched through the
  gateway without a credential. A private repository, including the one
  under review, is therefore not fetched, and the tool's description says
  so. A connection's forge, such as `github.com` for a GitHub App, is
  already allowed. Any other host must be in `egress.allow`, as for
  `curl`.
- **Only what is asked for:** the fetch is depth one and without tags.
  With paths, a server that can filter sends only the files under them. A
  server that cannot filter sends the whole tree, within the limit below.
- **Tags by prefix:** a listing asks the server for `refs/tags/` only,
  through the same gateway and limits as a fetch, and writes nothing. A
  fetch that names a tag the repository lacks lists the tags as well, and
  that listing counts among the listings. When the repository tags the
  named version under one other name, such as `v1.2.3` or `chart-1.2.3`
  for `1.2.3`, that tag is fetched in its place and the answer says so.
  Otherwise the error names the closest tag.
- **Bounded:** [`agent.fetches`](/configuration/#the-agent) fetches, 8
  unless set, and 8 tag listings a review. Each takes at most 128 MiB
  from the server and two minutes. The fetches and the run tool's kept
  outputs share 256 MiB of files written beside the checkout. Symlinks,
  submodules and files over 1 MiB are left out, and the files are written
  without execute bits.
- **Read in place:** `read_file` reads a fetched file, or a kept output,
  at the path under `../upstream/` that the fetch or the run tool names.
  It opens the file through a root confined to that directory, so neither
  a path nor a link reaches past it. Every other path it reads from the
  head commit's git objects.

### Images and artifacts

Beside `fetch_repo`, under the same condition, the agent gets `fetch_oci`.
It inspects a container image or an OCI artifact in a registry without
pulling it:

- what a tag resolves to
- the platforms of an index
- the config of its first image: the user, entrypoint, command, ports,
  environment, created time, layer count and size
- that image's labels and annotations, which name the repository and
  revision it was built from and the version it carries
- for a Helm chart or another artifact, its config document, cut to 8 KiB

With `tags`, it lists a repository's tags. kritika makes the reads
itself, with [oras-go](https://oras.land): the model names an image,
never a registry request.

- **Anonymous:** the registry's challenge is answered with the token it
  grants anyone, so a private image is not read, and the tool's
  description says so. A bare Docker Hub name, such as `postgres:17`, is
  read as `docker.io/library/postgres:17`.
- **Through the gateway:** the registry, its token endpoint and the
  storage its blobs redirect to must all be in `egress.allow`. For Docker
  Hub, these are `registry-1.docker.io`, `auth.docker.io` and its storage
  hosts, which are `production.cloudflare.docker.com` and
  `production.cloudfront.docker.com` as of this writing. For GitHub's
  registry, they are `ghcr.io` and `pkg-containers.githubusercontent.com`.
  A refused host fails the read, and the error names it.
- **Bounded:** as many inspections as
  [`agent.fetches`](/configuration/#the-agent) allows fetches, and 8 tag
  listings. Each takes at most 16 MiB from the registry and a minute, and
  reads a manifest or config of at most 4 MiB. Nothing is written: no
  layer is ever pulled.

### More tools

Declare a tool under `tools` in `.kritika.yaml` by its aqua registry
package, `aqua:owner/repo`. A tool the registry does not have yet can be
added to it upstream ([adding a package](https://github.com/aquaproj/aqua-registry/blob/main/docs/add_package.md)),
and is offered once a kritika release reads a registry release that has
it.

The binaries must be static or link only against glibc, libgcc and
libstdc++, which is all the image carries. A package that builds from
source or needs a runtime is refused.

### The gateway's download cache

A run installs the tools its repository declares, so each run downloads
them again. The gateway can keep those downloads instead. With
`gatewayCacheDir` set (under Helm, a private emptyDir), each replica keeps
what a repository's tools download, by the digest the run checks each
download against, and serves it to the next request that names that
digest. The runner reads the aqua registry through it the same way.

Fetching that checksum, from the GitHub API or a checksum file, goes
around the cache, so the checksum an install is checked against is never
one the cache chose. Only a request that names the digest its body must
have is kept, and only a body that has it. What any other request fetches
through the gateway is verified against nothing, so it is not shared
between runs.

```yaml
gatewayCache:
  enabled: true
config:
  egress:
    allow: [get.helm.sh, "*.githubusercontent.com"]
```

- **What is kept.** A repository's tool reaches its download's host over
  plain HTTP, with `https://github.com/` rewritten to `http://github.com/`,
  and names the download's digest in the `Kritika-Digest` header, which
  goes no further than the gateway. The gateway fetches such a request
  over HTTPS, as it does any plain HTTP request. It follows redirects
  itself, each through the egress rules. A GitHub release download that
  redirects to `release-assets.githubusercontent.com` needs that host
  allowed, as it does without the cache. A host that `egress.allow` and
  `egress.deny` refuse is refused with the cache too.
  The gateway keeps only what any run could fetch for itself. That is a
  GET or HEAD with no query and no credential, answered without a cookie
  or a `Cache-Control` of `private` or `no-store`, and through no redirect
  to a URL with credentials. A host with an `egress.credentials` entry is
  never cached. Neither is `api.github.com`, which the runner sends the
  run's token; a download from it stays on HTTPS. A download's URL must not
  carry a credential in its path. The fetch is the gateway's own, with its
  own User-Agent. Requests that ask the same replica for the same digest at
  once share it, whichever URL each names.
- **Integrity.** The gateway hashes what it fetches and keeps it only if it
  has the digest asked for, so an entry is the very body the runner checks
  for, and never goes stale. A body that does not have the digest, such as
  a release asset replaced upstream, is not kept, and the request is
  forwarded as it would be without the cache. The runner still checks
  every download before unpacking it; should one fail through the cache,
  it installs the tool once more over HTTPS, around the cache.
  No run can write the cache. A run can only have the gateway fetch a URL
  for a digest it names, and what the gateway keeps under a digest is
  served only to a request that names it.
- **Filesystem size.** `gatewayCache.storage.maxSize`, which the chart
  renders as the configuration's `gatewayCacheMaxSize`, bounds what each
  replica keeps. It is 2Gi unless set, and the least recently used entries
  go first. Separately, it bounds what the fetches under way may write,
  with at most eight fetches at once. A fetch past either bound is
  forwarded, not kept. So the directory holds at most twice the bound,
  apart from evicted files still being served. Those files retain disk
  space until their readers finish, for at most two minutes per response.
  The bound does not cover them. Allow ephemeral storage for twice the
  bound plus those files and the pod's other storage use. Twice the bound
  is not a hard limit on disk usage. Each replica keeps its own cache,
  which lasts as long as its pod.

#### Shared S3 storage

Use an existing S3-compatible bucket to reuse completed downloads across
replicas and rollouts. The runner still downloads through the gateway and
checks each download against its checksum; only kritika serve receives bucket
credentials. The initial
targets are MinIO, Ceph RGW, Garage and VersityGW. Use the same configuration
for each, with its endpoint and signing region:

```yaml
gatewayCache:
  enabled: true
  maxEntrySize: 256Mi
  storage:
    type: s3
    endpoint: https://s3.example.internal
    region: us-east-1
    bucket: kritika-cache
    prefix: gateway/
    forcePathStyle: true
    existingSecret: kritika-cache-s3
```

The existing Secret has `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY`
keys. Those are the names a Rook `ObjectBucketClaim` and the AWS tooling
write, so a claim's generated Secret can be named directly. Without it,
the SDK uses IAM credentials, including web identity where configured.
Credentials are never passed to runner Jobs or stored in application
configuration. Runner egress continues to go through the gateway.

With the chart's NetworkPolicies, kritika serve reaches a bucket outside the
cluster on `networkPolicy.egressPorts`, 443 unless set. For a bucket in the
cluster, allow its pods in `networkPolicy.extraEgress`, on the port the pods
listen on. A NetworkPolicy matches the connection after its Service has
sent it to a pod, so a Service's own port never matches when it differs.
Under Rook, Ceph RGW's Service sends its `gateway.port`, such as 80, to
port 8080 on the pods:

```yaml
networkPolicy:
  extraEgress:
    - to:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: rook-ceph
          podSelector:
            matchLabels:
              rook_object_store: ceph-objectstore
      ports:
        - port: 8080
          protocol: TCP
```

Entries are complete objects under `<prefix>/v1/<algorithm>-<hex>`, the
digest of the body, such as `sha256-<hex>`. The object's metadata records the
URL the body was fetched from, its digest, its size and its content type. The
gateway retains its checks for credentials, privacy and redirect destinations. Bucket writers
are trusted: protect the bucket from runner pods and unrelated workloads.

Each replica first checks its bounded local cache, then the bucket. A cold fill
is staged locally and queued for a background single-request upload.
`maxEntrySize` cannot exceed 5Gi. No conditional writes, shared locks,
bucket versioning, tagging or multipart operations are required.
Concurrent misses on different replicas can fetch the same digest twice.
Completed objects are reused thereafter; there is no promise of exactly one
upstream download.

The local emptyDir remains private to each pod. In S3 mode its retained-entry
budget and, separately, its aggregate fill budget are each eight times
`maxEntrySize`. With 256Mi entries, allow 4Gi plus evicted files still being
served or uploaded and the pod's other storage use. Two active uploads can hold
up to another 512Mi of evicted files open; queued uploads retain only keys.
`storage.maxSize` applies only to the filesystem backend. Hits read from S3 are
streamed, not added to local storage.

Uploads use two workers and a queue of at most 16 keys per replica. An
upload that finds the digest's entry in the bucket already, published by
another replica, is skipped rather than made: it holds the same body. A
full queue, a failed upload or an entry evicted before upload leaves the
download usable locally when it is still present. The requesting install
never waits for publication. Each upload has a deadline of 30 seconds
plus one second per MiB of the entry, so a slow link fails only below
about 1 MiB/s. Shutdown cancels active uploads and drops queued keys.
Failed or skipped uploads are not retried in the background.

Metadata lookups have a two-second total deadline. Credential discovery has its
own two-second budget, including SDK refreshes detached from the request.
S3 connection, TLS handshake and response-header waits are also capped at
two seconds, and a body read with no progress for ten seconds fails. The
existing overall download deadline remains.

Read and write circuit breakers are separate. Three consecutive failures
pause that direction for 30 seconds. Then one eligible request probes
recovery while others skip the bucket. A successful read probe reopens
access, and a normal missing object counts as a successful read. A
successful upload also reopens access. A failed probe starts another
cooldown. Client cancellation does not count as a storage failure. Local
hits remain available through an S3 outage, and failed uploads do not
suppress healthy S3 reads.

A failed or circuit-open lookup is a miss. The replica fills its own
directory and serves that copy to later requests, as the filesystem
backend does. An outage therefore costs each replica one origin fetch per
digest rather than one per run. The fill's upload is skipped while the write
circuit is open. A failure reading an opened S3 object costs the runner its
retry around the cache. Startup does
not contact S3 and never creates the bucket. Missing buckets, unavailable hosts
and denied credentials recover through subsequent requests once the storage
problem is corrected.

An object is kept by the digest of its body, so it never goes stale, and
kritika never rewrites one. Nothing bounds the bucket's size or retention.
Provision retention outside the chart,
using lifecycle expiration where supported or an external cleanup job. Include
noncurrent versions if bucket versioning is enabled. Without cleanup, unused
entries remain in the bucket. Use a dedicated prefix per instance, shared only
by its replicas.

The bucket may also live outside the cluster, at a provider such as Cloudflare
R2, Backblaze B2 or Wasabi. Nothing in the gateway assumes it is nearby: the same
bounded lookups, circuits and local fallback apply, and an unreachable bucket
leaves each replica caching in its own directory. The deadlines are chosen for
a bucket in the cluster, though, and an external one meets them with less
margin. A metadata lookup has two seconds in total, including a TLS
handshake on a cold connection. A link that regularly needs longer opens
the read circuit. It shows up on the `KritikaCacheStorageErrors` alert as
`s3_lookup` errors, while downloads keep working from local fills. A cold
replica streams a hit over one connection, so a large entry may arrive
more slowly than from its origin. Uploads are off the request path and
their deadline grows with size.
Run `mise run test-s3` against the provider before relying on it; the
[compatibility suite](/development/#s3-cache-compatibility) was only run
against the servers it names.

Costs follow the request pattern: a HEAD per eligible request, a GET per cold
replica hit, a PUT per fill, plus egress for the GETs. Providers with a minimum
storage duration bill an object for that period however soon lifecycle
expiration removes it. Set the lifecycle expiry at or above the minimum.

Give an `https://` endpoint, so credentials and bodies are encrypted in
transit. Scope the key to this bucket alone and keep the bucket private.
A key that works from anywhere widens what a leak exposes, and whoever
can write to the bucket chooses what a request for a digest is offered,
which the runner's check of that digest then has to refuse. Use the provider's signing region, `auto`
for R2; the chart's network policy already allows serve's egress on 443.

`storage.type` is `filesystem`, the per-replica emptyDir, unless set.
`maxEntrySize` applies to either backend; larger downloads are forwarded.

## Runner sandbox

A runner parses untrusted content and runs what the model asks. Set
`runner.podTemplate.spec.runtimeClassName` to a sandboxed runtime the
cluster offers (`gvisor` with runsc, or a Kata class). A kernel
vulnerability reachable from the pod is then contained by the sandbox
rather than the node. A command may run any program in the image with
any arguments, so set it wherever the cluster offers one. Without it, the
pod's other bounds still hold, but the container runtime alone separates
the pod from the node.

Set `runner.podTemplate.spec.resources` too. They bound the whole pod:
the runner and every command its run tool starts. Without them, runner
pods are BestEffort, with no memory limit. A runaway run then presses on
its whole node rather than being killed in its own cgroup. Index and
review runs have stayed under 100 MiB of working set so far, so a
`128Mi` request with a `1Gi` limit leaves ample room:

```yaml
runner:
  podTemplate:
    spec:
      resources:
        requests: { cpu: 10m, memory: 128Mi }
        limits: { memory: 1Gi }
```

A LimitRange's container defaults in the release namespace still apply to
the runner container, which sets no resources of its own. The
LimitRange's default limit bounds the runner within the pod's limit. The
API also refuses a runner pod whose requests are below the LimitRange's
default requests, so every run fails to start.

`runner.podTemplate` keeps runner pods, which run untrusted content, on
nodes of their own with `nodeSelector` and `tolerations`. Where the nodes'
kernel and container runtime support user namespaces, `hostUsers: false`
maps the runner's users to unprivileged IDs on the node. That is a
further layer where there is no sandbox RuntimeClass. An API server with
user namespaces turned off drops the field, and the pod runs in the
node's user namespace.

## What people can read

- A transcript may contain repository content the agent read, and every
  member of its account can read it
  ([retention](/configuration/#how-kritika-runs)).
- A review agent's conversation is also kept, for the pull request's next
  re-review. It is kept unmasked, since a provider's prompt cache matches
  only the exact text. No dashboard view shows it, a conversation that
  holds the run's own tokens is not kept, and the leader deletes it after
  two hours.
- Every write by an admin is audit-logged in the same transaction as the
  change it makes. The Settings page shows a secret only as set or not,
  with a URL's credentials hidden.
- The summary comment is public, on a fork's pull request too. That is
  why the cost of a pull request's reviews shows only when the admin turns
  `review.cost` on.

### Personal access tokens

A user creates API tokens in their own settings, for agents that read
through the [MCP endpoint](/mcp/). No admin, instance or configuration-file
tokens exist.

- Only the MCP endpoint takes a token, and it takes nothing else. The
  dashboard's API refuses a token, and the endpoint ignores a session
  cookie, so a page open in a signed-in browser cannot call it.
- A token reads what its creator's sign-in let them read when they created
  it, or the accounts they chose among those, and never more. It never
  carries an admin's rights, and nothing a token reaches changes anything.
- It expires when its creator chose, or never. It also stops reading, like
  a session, when the sign-in it came from is removed or its role mappings
  change. Unlike a session, it cannot re-check membership, so someone who
  leaves keeps a token until it expires or is revoked. Choose an expiry,
  and revoke a token on the admin's Users page when someone leaves.
- kritika keeps only a token's SHA-256. Its value, prefixed `kritika_` so a
  leaked one is easy to recognise, is shown once.
- Creating and revoking a token are audit-logged, without its value.
