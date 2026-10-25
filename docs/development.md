# Development

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

`mise watch` runs under watchexec, which `mise install` does not install;
`mise use -g watchexec` does.

`mise run smoke` is what CI's Serve Smoke job runs: it builds the image,
creates a kind cluster, installs CloudNativePG and a one-instance Postgres
with VectorChord as an image volume extension and the three roles, installs
the chart with two replicas and waits for both to be ready, runs `helm
test`, and checks that the replicas settle on one leader with the
configuration applied. `KRITIKA_SMOKE_KEEP=1` leaves the cluster running
for a look afterwards, and `KRITIKA_SMOKE_IMAGE` names an image already
built, to skip the build.

kritika serve's image is `gcr.io/distroless/static:nonroot`. The runner's
`-tools` image is `FROM scratch` on a root of [Red Hat Hardened
Images](https://www.redhat.com/en/products/hardened-images): the
Dockerfile's `tools-root` stage, the builder variant of `core-runtime`,
installs `curl`, `find`, `grep`, `jq`, libstdc++ and the timezone database
with dnf into a copy of `core-runtime`, so the image has its bash and
coreutils but no package manager, and fetches `gh` from its release,
checked against the release's checksums. Grype reads the root's rpm
database; Trivy does not recognize Hardened Images yet. Renovate bumps the
two images' digests and `gh`, through the annotation above its version. A repository's `.kritika.yaml` `tools` add to the runner
image's commands. `mise run
generate` rebuilds the chart's README and schema from `values.yaml`; the
Renovate regeneration workflow commits both.

`mise run ui-lint` lints the dashboard with oxlint, and `mise run
ui-typecheck` type-checks it with svelte-check on TypeScript 7.

The dashboard installs its dependencies with pnpm, which mise pins. Its
install, locally, in CI and in the image build, fails on a dependency with
a build script the `allowBuilds` setting of
`internal/web/pnpm-workspace.yaml` does not name, and on a locked version
published less than a day ago (pnpm's default `minimumReleaseAge`) that its
`minimumReleaseAgeExclude` does not name. Neither applies today, so the
file does not exist. In `internal/web`, `pnpm approve-builds` records a
build decision there, and pnpm adds an exclusion there itself when an
install has to take a version that new; commit the file with the lockfile.
Renovate holds npm updates for three days, so its version bumps pass; its
security fixes skip that hold, so one published under a day ago fails
until it is a day old or an exclusion for it is committed.

## S3 cache compatibility

`mise run test-s3` runs the gateway cache against a real S3 endpoint. It checks
sharing across replicas with empty local directories, origin response headers,
HEAD and range requests, concurrent fills, and reads during object replacement.
Use a disposable, unversioned bucket and credentials with bucket creation,
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
it. A pre-provisioned bucket works with credentials scoped to that bucket;
Garage's region must match its `s3_region`. Run the same suite against MinIO,
Ceph RGW, Garage and VersityGW before changing the SDK or storage protocol.
Production serve only needs object read/write access, not the test suite's
bucket management permissions.

The initial compatibility run passed against MinIO
`RELEASE.2025-10-15T17-29-55Z` built from source, Ceph RGW 19.2.0,
Garage 2.4.1 and VersityGW 1.9.0 with its POSIX backend.

## Docs

This site is MkDocs Material, declared in `pyproject.toml`, pinned in
`uv.lock` and run through uv, which mise installs. Pages live in `docs/`, and their order in
`mkdocs.yml`'s `nav`.

```sh
mise run docs-serve   # live preview at http://127.0.0.1:8000/
mise run docs         # build into site/ with --strict, as CI does
```

`--strict` fails the build on a broken link or a page missing from `nav`.
Pull requests into `main` build the site; a push to `main` publishes it to
GitHub Pages.

## Evaluation

Review quality is measured offline: `mise run bench-mine` builds a corpus,
from sibling checkouts of flate and konflate, of pull requests whose lines a
later fix commit changed, and `mise run
bench` (with `OPENROUTER_API_KEY`) runs them through the service's own
fetch, context and prompt code. By default these are one-step agentic reviews, what
`agent.steps: 1` sends: the agentic system prompt, the read-only tools
over the head commit and a `submit_review` the one step is told to make. Stage 4, the
similar-code index, needs a database and is left out. Each case runs with
the diff alone and with the context stages, reporting recall on the
expected findings, cost and latency per ablation.

To compare model effort and output limits, repeat the same selected cases and
modes with these environment variables. Every report records the settings,
per-case step/tool counts and input/output tokens, alongside quality and latency.

| Variable | Default | Purpose |
| --- | --- | --- |
| `KRITIKA_BENCH_EFFORT` | Provider default | Reasoning effort supported by the chosen model |
| `KRITIKA_BENCH_STEPS` | `1` | Step the agent is told to submit on, with up to two more steps for a missing submission; use multiple steps to evaluate tool use |
| `KRITIKA_BENCH_OUTPUT_TOKENS` | Agent default | Output limit per step, including reasoning where the provider counts it |
| `KRITIKA_BENCH_TOOL_BYTES` | Agent default | Maximum bytes returned by each tool |

For example, run the same small corpus selection at two efforts:

```sh
env KRITIKA_BENCH_LIMIT=3 KRITIKA_BENCH_STEPS=10 KRITIKA_BENCH_EFFORT=high mise run bench
env KRITIKA_BENCH_LIMIT=3 KRITIKA_BENCH_STEPS=10 KRITIKA_BENCH_EFFORT=medium mise run bench
```

Live runs spend model tokens. Repeat comparisons to distinguish variance from
an improvement, inspect errors and unmatched findings, and review the actual
findings before changing production defaults. Expected line matches are a
recall proxy, not a complete quality judgment. Dry runs validate input
construction but cannot establish quality or model latency. This harness uses
repository read tools; it does not reproduce the production command environment.

The repository `grep` tool accepts `context_lines` (default 0, capped at 10)
to return nearby source lines with each match. Overlapping windows appear once,
`max_results` still limits matching lines, and the tool byte limit still applies:
context that would push the output over it is dropped, with a note, before any
match is.
Use this with a narrow `path_glob` when a small excerpt answers the question;
`read_file` can retrieve larger ranges when needed. Compare tool calls, tokens,
and finding quality with multi-step evaluations before claiming a saving.

### Split-summary comparisons

`TestMergeBench` exercises the production merge prompt, schema and parser on
synthetic split-review fixtures. Each report retains the complete request and
answer, token usage, latency, transport errors and evidence-check failures.
Compare the same cases at different `KRITIKA_BENCH_EFFORT` and
`KRITIKA_BENCH_OUTPUT_TOKENS` values; the output default is 16384, as in the worker.

```sh
env KRITIKA_BENCH_EFFORT=high mise run test -- -tags bench ./bench/ -run '^TestMergeBench$' -v -count=1 -timeout 30m
```

`-count=1` keeps Go from replaying a cached run instead of calling the model
again. Set `KRITIKA_BENCH_DRY=1` to construct prompts without a model call, or
`KRITIKA_BENCH_MERGE_CASES` to a JSON array shaped like `bench/cases/merge.json`
for additional, sanitized cases. Reports go to `KRITIKA_BENCH_OUT` or
`bench/results`. The fixtures cover migration conditions and a cross-part
flow; add representative production cases before choosing defaults. Evidence
markers are lexical regression checks: a paraphrase can fail and an incorrect
claim can pass. Review each answer for missing conditions, invented claims,
summary usefulness and diagram accuracy. A completed harness run does not mean
all model responses passed; inspect `error` and `check_error` in the report.

### Confidence comparisons

`TestConfidenceBench` uses the production confidence prompt, schema, parser and
finding ceilings, with a separate fixture corpus. It checks score ranges and
risk ratings for a documentation-only change, production data loss and an
administrative grant. The output-token default is 4096, as in the worker.

```sh
env KRITIKA_BENCH_EFFORT=high mise run test -- -tags bench ./bench/ -run '^TestConfidenceBench$' -v -count=1 -timeout 30m
```

Use `KRITIKA_BENCH_CONFIDENCE_CASES` to supply additional cases shaped like
`bench/cases/confidence.json`; custom risk instructions and an earlier rating
are supported. The same dry-run, output directory and effort controls apply.
Score checks hold the model's own score to the expected range as well as the
ceiling the findings enforce, so a scorer that overrates a change fails even
where production would clamp it; inspect risk ratings and reasons too.
These small synthetic cases detect regressions; they do not establish accuracy
on production reviews. Model errors and failed checks stay in the report and
must count against a candidate. Neither stage harness reproduces production
slot waits, gateway accounting or fallback routing, so its latency measures
model calls rather than the entire publication stage.

## Cluster development loop

Everything that touches a private cluster lives in the gitignored
`.private/` folder, so account names, secret references and cluster
pointers never enter the repository:

- `.private/deploy`: a kustomization for what the chart does not create
  (namespace, a CloudNativePG cluster with the `vector` extension via a
  CNPG `Database` resource and External Secrets `Password` generators for
  its roles, the bot and provider credentials as ExternalSecrets). It
  expects the CloudNativePG, External Secrets and Prometheus operators.
- `.private/values.yaml`: the chart values for that cluster; `mise run
deploy` installs `charts/kritika` with them and the freshly pushed image,
  so every dev loop exercises the chart.
- `.private/mise.local.toml`: `[env] KUBECONFIG = "..."`, symlinked from
  the repo root as `.mise.local.toml` so mise picks it up.
- `.private/image` and `.private/runner-image`: the last image references
  pushed.

```sh
mise run deploy     # build, push, apply, wait for rollout
mise run logs       # follow one of the deployment's pods
mise run undeploy   # delete everything, database included
```

Images go to the registry `KRITIKA_DEV_REGISTRY` names, or to `ttl.sh` (24h
TTL) when it is unset, under a fresh name on every deploy, so a redeploy
always pulls new code.
The Helm chart is the supported way to run kritika; `.private/deploy` is a
development harness, not an example to copy.
