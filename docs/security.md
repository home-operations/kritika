# Security

Reviews process untrusted code and text. Each review runs in a separate pod
with limited credentials. Use the deployment controls below to restrict what
that pod can reach.

## Hardening an install

| Setting or action | Why it matters |
| --- | --- |
| Use a dedicated namespace. | kritika can create Jobs and Secrets in its namespace. |
| Keep `runner.admissionPolicy.enabled` on. | Restricts the Jobs and Secrets the service can create to the expected runner forms. |
| Enable `networkPolicy.enabled`. | Restricts runners to DNS, Postgres and the gateway. Configure [database](database.md#secrets-and-connection-uris) and [DNS](#name-lookups) destinations for your cluster. |
| Set `runner.podTemplate.spec.runtimeClassName`. | Uses an installed sandboxed runtime, such as gVisor, to isolate untrusted work. |
| Set `runner.podTemplate.spec.resources`. | Limits the CPU and memory a runner can consume. |
| Map sign-in roles from trusted groups or roles. | Avoids granting access from profile fields users can change themselves. See [role mappings](configuration.md#role-mappings). |

Without the admission policy, the service's Role can create Jobs that mount
other Secrets or use other ServiceAccounts. Without enforced Pod Security,
those Jobs may also reach the node. A dedicated namespace alone does not
replace these controls.

The rest of this page explains credentials, network access and runner tools.
For runner resource and runtime examples, go to [Runner sandbox](#runner-sandbox).

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

### Name lookups

Name lookups do not pass the gateway. With `networkPolicy.enabled`, the
pods send DNS only to the cluster's DNS: the pods
`networkPolicy.dns.namespace` and `podSelector` select, `k8s-app:
kube-dns` in `kube-system` unless set, and the addresses
`networkPolicy.dns.nodeLocalAddresses` lists, such as NodeLocal
DNSCache's `169.254.20.10` on each node's own network. The `cilium` flavor
allows DNS to the node itself for those, since Cilium gives the node's
addresses the host's identity, which no CIDR selects.

The cluster's DNS forwards the lookup of a name outside the cluster to
that name's servers, so a lookup alone tells them the name, and `curl`
looks up an `https://` URL's host even when it fetches the URL through the
gateway. With the `cilium` flavor, a runner pod may look up only the names
of the cluster's Services, `<service>.<namespace>.svc.cluster.local`,
which the cluster's DNS answers itself, the gateway's and an in-cluster
database's among them, and those `networkPolicy.dns.runnerNames` lists.
The lookup of an `ExternalName` Service's name goes on to the name the
Service points at, which its author set, not the runner. Add the host of
the runner role's database URI to `runnerNames` when the database is
outside the cluster. Cilium refuses any other name. The `default` and
`calico` flavors cannot filter names.

Runner pods resolve with `ndots: 4`, so the gateway's name, such as
`kritika-gateway.kritika.svc.cluster.local`, is looked up as written
rather than first through the search list, whose expansions of it Cilium
would refuse: musl's resolver, which mise uses, gives up at a refusal.

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
versions `build/tools/mise.toml` pins. Every other tool is the
repository's: its own
[`.kritika/mise.toml`](repository-config.md#repository-tools) layers
tools over the image's, as mise merges configs: per tool, the
repository's entry replaces the image's, options included, and the
image's other tools stay. So a repository adds a tool, `crane` for a
container image's tags, digest, manifest and labels, say, or `flate` for
a chart's rendered output, without an image build, and pins its download
in `.kritika/mise.lock`:

```toml
[tools]
crane = "0.22.1"
"github:home-operations/flate" = "0.6.5"
```

Both files are read from the merge base, so a pull request cannot change
the tools or download pins used to review itself. A tool at the image's
version runs from its system install; any other downloads on the first
run that uses it, into the run's own scratch space, at the URL and
checksum the lock pins. The run tool offers every command the merged
config's tools provide, `mise` itself excepted. An image set as the runner
container's in `runner.podTemplate` without mise offers the six default
commands its builder
put on its `PATH`, and reads no repository config, which the review
summary says. A follow-up posts no summary, so its reply carries such
notes, and a command's setup failure, instead.

A tool every repository should have belongs in the runner image: an
image built on the `-tools` image, with the tool added to its
`/etc/mise/config.toml` and to `/etc/mise/mise.lock`, which the runner's
user must be able to read (`mise lock` writes it mode 0600), and
installed with `mise install --system --locked` as the release's
Dockerfile installs the six, set as the runner container's image in
`runner.podTemplate`.

The runner keeps of a repository's `[tools]` entry its version and the
options that select, verify and extract its download. The options that
run a command or set its environment (`postinstall`, `install_env`,
`depends`), that would send mise's token to another host (`api_url`,
`headers`) and any option holding a template are dropped, since mise
renders templates, `exec()` among them, when it reads the config and
runs install hooks with the run's GitHub token in their environment.
Repository `[env]` permits only literal strings under non-reserved names;
templates and `_.` directives are dropped. `[tasks]`, `[settings]` and
hooks are dropped too, with summary notes. Egress permissions remain
the admin's, and so do the variables the runner container's `env` in
`runner.podTemplate` sets, which the commands get over the repository's
`[env]`, each from a literal value or a Secret's key:

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

A value from a Secret is masked out of what the commands print and of
the review, as the run's tokens are; a literal value is not. The Secret
must be in the release namespace, and the runner admission policies let
the runner pod read it. kritika sets the variables on the runner
container as `KRITIKA_COMMAND_ENV_<name>` and hands them to the commands
under their own names, so they configure the commands and never the
runner itself: a `GODEBUG` or `TMPDIR` meant for a tool does not change
how the runner fetches, logs or connects. The filtered config and its lock live
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
  own `HOME`, `XDG_CACHE_HOME` and the gateway, the mise config's `[env]`,
  the admin's variables, and for `gh` the run's token as `GH_TOKEN`. The pod's `NO_PROXY` is not
  passed on, so `curl` reaches even `localhost` and the gateway's own host
  through the gateway, and its `--connect-to` or an `--alt-svc` cache only
  changes what it asks the gateway for. The runner makes itself
  unreadable to the command first, so it cannot read the runner's
  credentials from `/proc`, and runs mise with the same bare environment,
  so neither a tool's install hooks nor a config's templates see them.
  `HOME` is apart from the checkout, so a pull request cannot plant a
  `.curlrc`; the runner writes one that turns off `curl`'s progress meter,
  which would otherwise land in the result, follows the redirects the
  gateway hands back, and fails on an HTTP error with its body kept, so a
  host the gateway refuses is a failed command naming the reason rather
  than a success whose output is the refusal. It also limits `curl` to
  `http` and `https`, the schemes the proxy variables cover, so a URL of
  any other, given or redirected to, is refused rather than reached
  without the gateway. Neither a config's `[env]` nor the admin's
  variables may set what the runner sets or what would point a tool past
  the argument checks:
  `HOME`, `PATH`, the proxy variables, `GH_*`, `GIT_*`, `MISE_*`, `LD_*`,
  `GITHUB_TOKEN`, `CURL_HOME`, `XDG_CONFIG_HOME`, `XDG_CACHE_HOME`,
  `RIPGREP_CONFIG_PATH`, `DOCKER_CONFIG`, `PAGER`, `BROWSER`, `EDITOR` and
  `VISUAL` are dropped from a config, and the review's summary says so,
  and kritika serve refuses to start with them among the admin's.
- **Few writable places:** `HOME`, with the binaries and config mise keeps
  there, is read-only once the commands are offered, and so is the scratch
  directory holding it. A command writes to the checkout, to the
  `../upstream/` directory beside it, and to `XDG_CACHE_HOME`, a directory
  of the same scratch space where a tool that keeps a cache, such as
  flate, puts it. mise's own cache stays under `HOME`.
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
  print a credential, run a program outside the list or reach the
  network around the gateway: `gh auth`, `alias`, `config` and
  `extension`; `fd -x` and `-X`; `rg --pre` and `--hostname-bin`;
  `curl --trace`, `--trace-ascii`, `--trace-config` and `--config`/`-K`,
  which would print the proxy credential in pieces the mask cannot
  match; `curl`'s proxy options (`-x`/`--proxy`, `--noproxy`,
  `--preproxy`, `--proxy1.0`, `--socks4`, `--socks4a`, `--socks5` and
  `--socks5-hostname`), `--resolve`, `--unix-socket` and
  `--abstract-unix-socket`, which would connect around the gateway; and
  `--proto`, `--next`/`-:` and `-q`/`--disable`, which would
  lift the `.curlrc`'s limit to `http` and `https`. A `curl` option is
  refused in its `--expand-` form too. It masks the token in a command's
  output and in the submitted review.
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

Declare a tool in `.kritika/mise.toml` by its registry short name where
it has one (`helm`, `crane`), which names its commands before it installs,
else as `aqua:owner/repo`, whose recipe names the release asset and what
mise verifies, and as `github:owner/repo` as a last resort, with
`filter_bins` naming its commands unless the repository's name is the
command. The binaries must be static or link only
against glibc, libgcc and libstdc++, which is all the image carries, so a
backend that builds or needs a runtime (`npm:`, `pipx:`, `cargo:`, `go:`)
does not work there.

### The gateway's download cache

A run installs the tools its repository declares, so each run downloads
them again. The gateway can keep those downloads
instead: with `gatewayCacheDir` set, a private emptyDir under Helm, each replica
keeps what a repository's locked installs download from the hosts its
`.kritika/mise.lock` pins, and serves it to the next run that asks for
the same URL. `egress.cache` names more hosts whose downloads it keeps
for every install, such as those of the tools the admin's mise config
declares, which no lock pins:

```yaml
gatewayCache:
  enabled: true
config:
  egress:
    allow: [get.helm.sh, "*.githubusercontent.com"]
    # Optional: what a repository's lock pins is kept without it.
    cache: [get.helm.sh]
```

- **What is kept.** A repository's locked install reaches each host its
  lock pins a download on over plain HTTP, `https://github.com/`
  rewritten to `http://github.com/`, with the proxy URL's username
  `locked`; every install does the same for
  a host `egress.cache` names. The gateway fetches such a request over
  HTTPS, as it does any plain HTTP request, following redirects itself,
  each through the egress rules: a GitHub release download that
  redirects to `release-assets.githubusercontent.com` needs that host
  allowed, as it does without the cache, and a host `egress.allow` and
  `egress.deny` refuse is refused with the cache too. The gateway keeps
  only what any run could fetch for itself: a GET or HEAD with no query
  and no credential, answered without a cookie or a `Cache-Control` of
  `private` or `no-store`, through no redirect to a URL with credentials.
  A host with an `egress.credentials` entry is never cached, nor
  `api.github.com`, which mise sends the run's token; a lock's downloads
  from it, or from `raw.githubusercontent.com`, which mise sends the
  token too, stay on HTTPS. A cached host's URLs must not carry a
  credential in their path.
  The fetch is the gateway's own, with its own User-Agent, and runs that
  ask the same replica for the same URL at once share it. A command's plain HTTP request
  to a cached host, such as `curl http://get.helm.sh/...`, is answered
  and kept the same way.
- **Integrity.** mise checks every download against the checksum the
  repository's lock pins, so a stale or damaged copy fails the install
  rather than changing a tool, and the runner then installs the tool
  once more, asking the gateway, by the proxy URL's username `uncached`,
  not to answer from its cache; a locked install's retry also leaves
  its lock's hosts on HTTPS, as it would go without the cache. The
  gateway asks the origin about a kept download after
  `gatewayCacheRefreshAfter`, a week unless set, with the ETag and
  Last-Modified it kept: an unchanged download is kept for another
  period at the cost of that one request, a changed one is fetched
  again, and an asset replaced upstream costs retries for at most that
  long, provided its origin's validators change with it; the validators
  are offered only to the resource the body came from, a redirect's
  signature parameters aside, not to wherever a changed redirect now
  points. No run can write the cache, only have
  the gateway fetch a
  URL a cached host serves or, by naming the proxy's user `locked`, a URL
  any other allowed host serves; what the gateway keeps from a host
  `egress.cache` does not name is served only to a request named
  `locked` too, which a locked install checks against its lock.
- **Filesystem size.** `gatewayCacheMaxSize`, 2Gi unless set, bounds what each
  replica keeps, the least recently used going first, and apart from
  that what the fetches under way, at most eight at once, may write: a
  fetch past either bound is forwarded, not kept, so the directory holds
  at most twice the bound, apart from evicted files still being served.
  Those files retain disk space until their readers finish, for at most
  two minutes per response; their total size is not bounded by
  `gatewayCacheMaxSize`. Allow ephemeral storage for twice the bound
  plus those files and the pod's other storage use. Twice the bound is
  not a hard limit on disk usage. Each replica keeps its own cache,
  which lasts as long as its pod.

#### Shared S3 storage

Use an existing S3-compatible bucket to reuse completed downloads across replicas
and rollouts. Mise still downloads through the gateway and verifies locked tools;
only serve receives bucket credentials. The initial targets are MinIO, Ceph RGW,
Garage and VersityGW. Use the same configuration for each, with its endpoint and
signing region:

```yaml
gatewayCache:
  enabled: true
  refreshAfter: 168h
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

The existing Secret has `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` keys, the
names a Rook `ObjectBucketClaim` and the AWS tooling write, so a claim's generated
Secret can be named directly. Without it, the
SDK uses IAM credentials, including web identity where configured. Credentials
are never passed to runner Jobs or stored in application configuration. The
chart permits the endpoint's TCP port in serve's network policy; runner egress
continues to go through the gateway.

Entries are complete objects under `<prefix>/v1/<hash-of-original-HTTPS-URL>`.
The object's metadata records the fetch time, body size and origin response
headers. Its key identifies a URL, not a content checksum. The gateway retains
its checks for credentials, privacy and redirect destinations. Bucket writers
are trusted: protect the bucket from runner pods and unrelated workloads,
particularly because instance tools can install without a repository lock.

Each replica first checks its bounded local cache, then the bucket. A cold fill
is staged locally and queued for a background single-request upload;
`maxEntrySize` cannot exceed 5Gi. No conditional writes, shared locks, bucket versioning, tagging or
multipart operations are required. Concurrent misses on different replicas can
fetch and upload the same URL twice. Completed objects are reused thereafter;
there is no promise of exactly one upstream download.

The local emptyDir remains private to each pod. In S3 mode its retained-entry
budget and, separately, its aggregate fill budget are each eight times
`maxEntrySize`. With 256Mi entries, allow 4Gi plus evicted files still being
served or uploaded and the pod's other storage use. Two active uploads can hold
up to another 512Mi of evicted files open; queued uploads retain only keys.
`storage.maxSize` applies only to the filesystem backend. Hits read from S3 are
streamed, not added to local storage.
Uploads use two workers and a queue of at most 16 keys per replica. A full queue,
failed upload or entry evicted before upload leaves the download usable locally
when still present. The requesting install never waits for publication. Each
upload has a deadline of 30 seconds plus one second per MiB of the entry, so a
slow link fails only below about 1 MiB/s; shutdown cancels active uploads and
drops queued keys. Failed or skipped uploads are not retried in the background.

Metadata lookups have a two-second total deadline. Credential discovery has its
own two-second budget, including SDK refreshes detached from the request.
S3 connection, TLS handshake and response-header waits are also capped at two
seconds; a body read with no
progress for ten seconds fails. The existing overall download deadline remains.
Read and write circuit breakers are separate: three consecutive failures pause
that direction for 30 seconds, then one eligible request probes recovery while
others skip the bucket. A successful read probe, including a normal missing
object, or upload reopens access; a failed probe starts another cooldown. Client
cancellation does not count as a storage failure. Local hits remain available
through an S3 outage, and failed uploads do not suppress healthy S3 reads.

A failed or circuit-open lookup is a miss: the replica fills its own directory
and serves that copy to later requests, as the filesystem backend does, so an
outage costs each replica one origin fetch per URL rather than one per run. The
fill's upload is skipped while the write circuit is open. A failure reading an
opened S3 object can cost the runner its existing uncached retry. Startup does
not contact S3 and never creates the bucket. Missing buckets, unavailable hosts
and denied credentials recover through subsequent requests once the storage
problem is corrected.

`refreshAfter` is checked by kritika, independently of physical object deletion.
An entry past it is revalidated with the origin: on 304 the replica copies the
body it has, or the bucket's, under a new fetch time and uploads that, so the
object is rewritten rather than downloaded from origin again. It is
not a bucket size or retention limit. Provision retention outside the chart,
using lifecycle expiration where supported or an external cleanup job. Include
noncurrent versions if bucket versioning is enabled. Without cleanup, unused
entries remain in the bucket. Use a dedicated prefix per instance, shared only
by its replicas.

The bucket may also live outside the cluster, at a provider such as Cloudflare
R2, Backblaze B2 or Wasabi. Nothing in the gateway assumes it is nearby: the same
bounded lookups, circuits and local fallback apply, and an unreachable bucket
leaves each replica caching in its own directory. The deadlines are chosen for
a bucket in the cluster, though, and an external one meets them with less
margin. A metadata lookup has two seconds in total, including a TLS handshake
on a cold connection; a link that regularly needs longer opens the read circuit
and shows up on the `KritikaCacheStorageErrors` alert as `s3_lookup` errors
while downloads keep working from local fills. A cold replica streams a hit
over one connection, so a large entry may arrive more slowly than from its
origin. Uploads are off the request path and their deadline grows with size.
Run `mise run test-s3` against the provider before relying on it; the
[compatibility suite](development.md#s3-cache-compatibility) was only run
against the servers it names.

Costs follow the request pattern: a HEAD per eligible request, a GET per cold
replica hit, a PUT per fill, plus egress for the GETs. Providers with a minimum
storage duration bill an object for that period however soon lifecycle
expiration or a `refreshAfter` overwrite removes it, so set the lifecycle
expiry at or above the minimum rather than close to `refreshAfter`. Give an
`https://` endpoint, so credentials and bodies are encrypted in transit; scope
the key to this bucket alone and keep the bucket private, since
a key that works from anywhere widens what a leak exposes, and the gateway
serves an unlocked instance tool from the bucket without a checksum. Use the
provider's signing region, `auto` for R2; the chart's network policy already
allows serve's egress on 443.

For the filesystem backend, the Helm capacity setting is now
`gatewayCache.storage.maxSize`; migrate the former `gatewayCache.maxSize` value.
`storage.type` defaults to `filesystem`, retaining the per-replica emptyDir.
`maxEntrySize` applies to either backend; larger downloads are forwarded.

### Migrating from image-mounted tools

`runner.tools` and `KRITIKA_RUNNER_TOOLS` are removed. Delete them from
Helm values, the application configuration and the environment. Declare
each tool in the `.kritika/mise.toml` of the repositories whose reviews
use it instead, with its lock; for example, a flate image mount becomes:

```toml
[tools]
"github:home-operations/flate" = "0.6.5"
```

Tools no longer use Kubernetes image volumes, so adding one does not
require Kubernetes 1.33. A custom runner image can still provide named
commands on its own `PATH`.

### Migrating from the instance mise config

`runner.mise.config`, `runner.miseConfig` and `KRITIKA_RUNNER_MISE_CONFIG`
are removed. The instance's config had no lock, so its tools downloaded
with only the verification their backend gives them. Delete it from Helm
values, the application configuration and the environment, and move what
it held:

- **`[tools]`:** into the `.kritika/mise.toml` of each repository whose
  reviews use the tool, locked with
  `mise -C .kritika lock --platform linux-x64,linux-arm64`; a tool every
  repository needs goes into a runner image built on the `-tools` image
  (see [runner tools](#runner-tools)).
- **`[env]`:** a plain value into each repository's `[env]`, as a
  literal string, or into the runner container's `env` in
  `runner.podTemplate` when the repository must not hold it (see
  [runner tools](#runner-tools)). A value marked `redact = true` is a
  secret and does not belong in a repository: a token a tool sends to a
  host goes in [`egress.credentials`](#the-egress-gateway), which the
  gateway adds to the tool's plain `http://` requests, so the runner
  never holds it, or else in a Secret the runner container's `env` names.
- **`[settings]`:** no longer apply.

The image's `/etc/mise/config.toml` declares `curl`, `fd`, `gh`, `jq`,
`rg` and `yq`, and every review offers those and the repository's tools.
`crane` is not a default: declare it when needed. If building on top of
the `-tools` image, its preinstalled tools live in mise's system install
directory, not `/usr/local/bin`.

## Runner sandbox

A runner parses untrusted content and runs what the model asks. Set
`runner.podTemplate.spec.runtimeClassName` to a sandboxed runtime the
cluster offers
(`gvisor` with runsc, or a Kata class), so a kernel vulnerability
reachable from the pod is contained by the sandbox rather than the node.
It is advised, not required: without it the pod's other bounds still hold,
but the container runtime alone separates it from the node.

Set `runner.podTemplate.spec.resources` too, which bound the whole pod:
the runner and every command its run tool starts. Without them runner
pods are BestEffort, with no memory limit, so a runaway run presses on
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
the runner container, which sets no resources of its own: its default
limit bounds the runner within the pod's, and the API refuses a runner pod
whose requests are below its default requests, so every run fails to start.

`runner.podTemplate` keeps runner pods, which run untrusted content, on
nodes of their own with `nodeSelector` and `tolerations`. Where the nodes'
kernel and container runtime support user namespaces, `hostUsers: false`
maps the runner's users to unprivileged IDs on the node, a further layer
where there is no sandbox RuntimeClass. An API server with user
namespaces turned off drops the field, and the pod runs in the node's
user namespace as before.

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
