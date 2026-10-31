---
title: Development
---

Tool versions and tasks live in [`.mise/config.toml`](https://github.com/home-operations/kritika/blob/main/.mise/config.toml):

```sh
mise install
mise run build
mise run test               # unit tests; mise run test -- ./internal/worker/ runs one package's
mise run test-integration   # Postgres suites against a throwaway VectorChord container
mise run lint
mise run smoke              # kritika serve in a kind cluster with CloudNativePG (needs docker, kind, kubectl, helm, openssl)
mise run ui-mock            # the dashboard at http://localhost:5544/ on mocked data; mise watch ui-mock -r rebuilds on change
```

`mise watch` runs under watchexec, which `mise install` does not install.
`mise use -g watchexec` installs it.

`mise run smoke` is what CI's Serve Smoke job runs. It:

1. Builds the image.
2. Creates a kind cluster.
3. Installs CloudNativePG and a one-instance Postgres with VectorChord as
   an image volume extension and the three roles.
4. Installs the chart with two replicas and waits for both to be ready.
5. Runs `helm test`.
6. Makes sure that the replicas settle on one leader with the configuration
   applied.

`KRITIKA_SMOKE_KEEP=1` leaves the cluster running for a look afterwards.
`KRITIKA_SMOKE_IMAGE` names an image that is already built, to skip the
build.

kritika serve's image is `gcr.io/distroless/static:nonroot`. The runner's
`-tools` image is `FROM scratch` on a root of [Red Hat Hardened
Images](https://www.redhat.com/en/products/hardened-images). The
Dockerfile's `tools-root` stage is the builder variant of `core-runtime`.
It installs `curl`, `find`, `grep`, `jq`, libstdc++ and the timezone
database with dnf into a copy of `core-runtime`. So the image has its bash
and coreutils but no package manager. The stage also fetches `gh` from its
release, and makes sure that it matches the release's checksums.

The image's `kritika-runner` is built from `cmd/kritika-runner`, which
runs `kritika-runner run` alone. Its test fails when the binary links
one of serve's packages. Grype reads the root's rpm database. Trivy does
not recognize Hardened Images yet. Renovate bumps the two images' digests,
and it bumps `gh` through the annotation above its version. The `tools` in
a repository's `.kritika.yaml` add to the runner image's commands.

`mise run generate` rebuilds the chart's README and schema from
`values.yaml`, and fetches Sigstore's trusted root into
`internal/aqua/trusted_root.json`. The Renovate regeneration workflow
commits all three.

`mise run ui-lint` lints the dashboard with oxlint, and `mise run
ui-typecheck` type-checks it with svelte-check on TypeScript 7.

The dashboard installs its dependencies with pnpm, which mise pins. The
install fails, locally, in CI and in the image build, in two cases. One case
is a dependency with a build script that the `allowBuilds` setting of
`internal/web/pnpm-workspace.yaml` does not name. The other case is a locked
version published less than a day ago (pnpm's default `minimumReleaseAge`)
that the file's `minimumReleaseAgeExclude` does not name.

Neither applies today, so the file does not exist. In `internal/web`,
`pnpm approve-builds` records a build decision there. pnpm adds an
exclusion there itself when an install has to take a version that new.
Commit the file with the lockfile.

Renovate holds npm updates for three days, so its version bumps pass. Its
security fixes skip that hold. So a fix published under a day ago fails
until it is a day old, or until someone commits an exclusion for it.

## S3 cache compatibility

`mise run test-s3` runs the gateway cache against a real S3 endpoint. It
makes sure that these things work:

- Sharing across replicas with empty local directories.
- Origin response headers.
- HEAD and range requests.
- Concurrent fills.
- Reads during object replacement.

Use a disposable, unversioned bucket. The credentials need bucket creation,
listing and deletion permissions as well as object read/write permissions:

```sh
env KRITIKA_TEST_S3_ENDPOINT=http://127.0.0.1:9000 \
  KRITIKA_TEST_S3_REGION=us-east-1 \
  KRITIKA_TEST_S3_BUCKET=kritika-test \
  KRITIKA_TEST_S3_ACCESS_KEY=test-access \
  KRITIKA_TEST_S3_SECRET_KEY=test-secret \
  mise run test-s3
```

The suite creates the bucket if missing, writes under a unique test prefix,
and removes that prefix on completion. It removes the bucket only if it created
it. A pre-provisioned bucket works with credentials scoped to that bucket.
Garage's region must match its `s3_region`. Before you change the SDK or
storage protocol, run the same suite against MinIO, Ceph RGW, Garage and
VersityGW. Production serve only needs object read/write access, not the test
suite's bucket management permissions.

The initial compatibility run passed against MinIO
`RELEASE.2025-10-15T17-29-55Z` built from source, Ceph RGW 19.2.0,
Garage 2.4.1 and VersityGW 1.9.0 with its POSIX backend.

## Forge repository listing

`mise run test-forges` starts throwaway Gitea, Forgejo and page-capped
Gitea containers, and builds accounts on each. It makes sure that the client
lists every repository that the bot can reach, and only those:

- An organization larger than one page.
- Private and limited organizations reached through teams.
- An organization where most pages hold nothing the bot sees.
- Another user's repositories, among them a fork and a private
  collaboration.
- An archived repository.

The bot calls with an access token that has the scopes the
[Gitea setup](/forges/#gitea) gives it. On Forgejo the same token stands in
for the authorized integration, since Forgejo fetches an integration's
issuer over HTTPS. The suite needs docker. To run it against instances of
your own, set `KRITIKA_TEST_FORGES` to `name=URL` pairs separated by commas.
Also set `KRITIKA_TEST_FORGE_ADMIN` to a site admin's `user:password`. Each
run creates its accounts under a fresh suffix and deletes nothing.

## Docs

This site is Astro Starlight, a Node project in `docs/` pinned in
`docs/pnpm-lock.yaml`. mise installs Node and pnpm. Pages live in
`docs/src/content/docs/`, and each is titled in its front matter. Their
order in the sidebar is set in `docs/astro.config.mjs`, so a new page needs
an entry there. Pages link to each other by their site paths, such as
`/database/#secrets-and-connection-uris`.

```sh
mise run docs-serve   # live preview at http://localhost:4321/
mise run docs         # build into site/, as CI does
```

The build fails on a broken internal link or anchor. Pull requests into
`main` build the site. A push to `main` publishes it to GitHub Pages.

## Evaluation

Review quality is measured offline. `mise run bench-mine` builds a corpus
of pull requests whose lines a later fix commit changed, from sibling
checkouts of flate and konflate. `mise run bench` (with
`OPENROUTER_API_KEY`) runs them through the service's own fetch, context
and prompt code. By default these are one-step agentic reviews, which send
what `agent.steps: 1` sends:

- The agentic system prompt.
- The read-only tools over the head commit.
- A `submit_review` that the one step is told to make.

Stage 4, the similar-code index, needs a database and is left out. Each
case runs with the diff alone and with the context stages. The run reports
recall on the expected findings, cost and latency per ablation. Recall is the
share of expected findings that the review found. An ablation is what the
prompt carries beyond the diff.

To compare model effort and output limits, repeat the same selected cases and
modes with these environment variables. Every report records the configuration,
per-case step/tool counts and input/output tokens, alongside quality and latency.

| Variable                      | Default             | Purpose                                                                                                                          |
| ----------------------------- | ------------------- | -------------------------------------------------------------------------------------------------------------------------------- |
| `KRITIKA_BENCH_EFFORT`        | Provider default    | Reasoning effort supported by the chosen model                                                                                   |
| `KRITIKA_BENCH_STEPS`         | `1`                 | Step the agent is told to submit on, with up to two more steps for a missing submission. Use multiple steps to evaluate tool use |
| `KRITIKA_BENCH_OUTPUT_TOKENS` | Agent default       | Output limit per step. It includes reasoning where the provider counts it                                                        |
| `KRITIKA_BENCH_TOOL_BYTES`    | Agent default       | Maximum bytes returned by each tool                                                                                              |
| `KRITIKA_BENCH_JUDGE`         | None                | Model id on OpenRouter that judges a finding off an expected range ([the judge](#the-judge))                                     |
| `KRITIKA_BENCH_JUDGE_EFFORT`  | Provider default    | The judge's reasoning effort                                                                                                     |
| `KRITIKA_BENCH_FLOORS`        | `bench/floors.json` | The floors a run is held to ([floors](#floors)). None when the file is absent                                                    |
| `KRITIKA_BENCH_SCRIPT`        | Off                 | `1` answers every model call from the corpus itself ([the wiring smoke](#the-wiring-smoke))                                      |

For example, run the same small corpus selection at two efforts:

```sh
env KRITIKA_BENCH_LIMIT=3 KRITIKA_BENCH_STEPS=10 KRITIKA_BENCH_EFFORT=high mise run bench
env KRITIKA_BENCH_LIMIT=3 KRITIKA_BENCH_STEPS=10 KRITIKA_BENCH_EFFORT=medium mise run bench
```

Live runs spend model tokens. Before you change production defaults, repeat
comparisons to distinguish variance from an improvement. Also inspect errors
and unmatched findings, and review the actual findings.

Expected line matches are a recall proxy, not a complete quality judgment.
Dry runs make sure that the input construction works, but they cannot establish
quality or model latency. This harness uses repository read tools. It does not
reproduce the production command environment.

### The judge

A finding that identifies an expected defect but anchors it a few lines
away, or on its cause, misses the 3-line match. With `KRITIKA_BENCH_JUDGE`
set to a model id on OpenRouter, a second model acts as a judge. It takes
each expected range that no finding landed on. It judges that range against
each finding in the same file that landed on no range. It judges from the
defect's description and the finding's title, explanation and fix. A match at
confidence 0.5 or more counts as a hit.

The report records every verdict, its reasoning and its cost. It also records
how many hits the judge alone gave, so a judged run reads apart from a run
matched on lines. `KRITIKA_BENCH_JUDGE_EFFORT` sets how hard the judge reasons.
If you change the judge, or its effort, the numbers are not comparable. The
report names the judge, and the floors are pinned to it.

### Floors

`bench/floors.json`, when present, holds the floors: the least that a run can
measure before it fails. Each mode has a floor for the must-find recall. Each
mode also has two collapse floors, for the mean findings and the mean tool
calls per case. The collapse floors trip when the engine breaks rather than on
noise. A run under a floor fails the test and says which floor.

The floors name the judge that scored the runs that they were set from. A run
scored otherwise is not held to them, with a note. Set them from two or more
runs on the same commit:

```sh
go run -tags bench ./bench/cmd/floors -out bench/floors.json bench/results/<a>.json bench/results/<b>.json
```

The recall floor is the mean over the runs less 2.2 standard deviations.
Each collapse floor is half the measured mean. Kodus sets its own floors the
same way. Each floor is rounded down to the thousandth. If more than 5% of a
mode's cases did not run, the mode fails too. Its measures are of too few cases
to judge. `KRITIKA_BENCH_FLOORS` names another file.

### The wiring smoke

`mise run bench-smoke` runs the bench's own tests, then one corpus case. In
that case, the corpus's own script answers for the model. The script submits a
finding on every expected range and matches every judged pair. The run needs no
key and has no cost. It exercises every other line of the harness and of the
review code that the harness calls, from fetch to report.

The run fails when a case errors or the script's findings miss a range, because
that means that the harness is broken. The run is not held to the floors, which
are for a live model. The split-summary and confidence comparisons are also for
a live model, and the run skips them. CI runs it on every pull request, so a
change that breaks the harness fails there rather than at the next live run.
`KRITIKA_BENCH_SCRIPT=1` runs the whole corpus that way.

The repository `grep` tool accepts `context_lines` (default 0, capped at 10)
to return nearby source lines with each match. Overlapping windows appear once,
and `max_results` still limits matching lines. The tool byte limit still
applies. The tool drops context that pushes the output over it, with a note,
before it drops any match.

When a small excerpt answers the question, use this with a narrow `path_glob`.
`read_file` can retrieve larger ranges when needed. Before you claim a saving,
compare tool calls, tokens, and finding quality with multi-step evaluations.

### Split-summary comparisons

`TestMergeBench` exercises the production merge prompt, schema and parser on
synthetic split-review fixtures. Each report retains the complete request and
answer, token usage, latency, transport errors and evidence-check failures.
Compare the same cases at different `KRITIKA_BENCH_EFFORT` and
`KRITIKA_BENCH_OUTPUT_TOKENS` values. The output default is 16384, as in the worker.

```sh
env KRITIKA_BENCH_EFFORT=high mise run test -- -tags bench ./bench/ -run '^TestMergeBench$' -v -count=1 -timeout 30m
```

`-count=1` keeps Go from replaying a cached run instead of calling the model
again. To construct prompts without a model call, set `KRITIKA_BENCH_DRY=1`.
For additional, sanitized cases, set `KRITIKA_BENCH_MERGE_CASES` to a JSON
array shaped like `bench/cases/merge.json`. Reports go to
`KRITIKA_BENCH_OUT` or `bench/results`.

The fixtures cover migration conditions and a cross-part flow. Before you
choose defaults, add representative production cases. Evidence markers are
lexical regression checks: a paraphrase can fail and an incorrect claim can
pass. Review each answer for missing conditions, invented claims, summary
usefulness and diagram accuracy. A completed harness run does not mean that
all model responses passed. Inspect `error` and `check_error` in the report.

### Confidence comparisons

`TestConfidenceBench` uses the production confidence prompt, schema, parser and
finding ceilings, with a separate fixture corpus. It makes sure that score
ranges and risk ratings are as expected for a documentation-only change,
production data loss and an administrative grant. The output-token default is
4096, as in the worker.

```sh
env KRITIKA_BENCH_EFFORT=high mise run test -- -tags bench ./bench/ -run '^TestConfidenceBench$' -v -count=1 -timeout 30m
```

Use `KRITIKA_BENCH_CONFIDENCE_CASES` to supply additional cases shaped like
`bench/cases/confidence.json`. The harness supports custom risk instructions
and an earlier rating. The same dry-run, output directory and effort controls
apply. The check derives the score from the findings and the answer's concern,
and holds the risk to the steps of the versions the diff moves, as production
does. So the check judges what production would publish. Inspect risk ratings
and reasons too.

These small synthetic cases detect regressions. They do not establish accuracy
on production reviews. Model errors and failed checks stay in the report and
must count against a candidate. Neither stage harness reproduces production
slot waits, gateway accounting or fallback routing, so its latency measures
model calls rather than the entire publication stage.

## Cluster development loop

Everything that touches a private cluster lives in the gitignored
`.private/` folder, so account names, secret references and cluster
pointers never enter the repository:

- `.private/deploy`: a kustomization for what the chart does not create.
  It expects the CloudNativePG, External Secrets and Prometheus operators,
  and holds:
  - The namespace.
  - A CloudNativePG cluster with the `vector` extension via a CNPG
    `Database` resource, and External Secrets `Password` generators for its
    roles.
  - The bot and provider credentials as ExternalSecrets.
- `.private/values.yaml`: the chart values for that cluster.
  `mise run deploy` installs `charts/kritika` with them and the freshly
  pushed image, so every dev loop exercises the chart.
- `.private/mise.local.toml`: `[env] KUBECONFIG = "..."`, symlinked from
  the repo root as `.mise.local.toml` so mise picks it up.
- `.private/image` and `.private/runner-image`: the last image references
  pushed.

```sh
mise run deploy     # build, push, apply, wait for rollout
mise run logs       # follow one of the deployment's pods
mise run undeploy   # delete everything, database included
```

Images go to the registry that `KRITIKA_DEV_REGISTRY` names, or to `ttl.sh`
(24h TTL) when it is unset. They are pushed under a fresh name on every
deploy, so a redeploy always pulls new code. The Helm chart is the
supported way to run kritika. `.private/deploy` is a development harness,
not an example to copy.
