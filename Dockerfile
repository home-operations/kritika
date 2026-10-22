# syntax=docker/dockerfile:1

# Images that live on Docker Hub are pulled through mirror.gcr.io, Google's
# cache of it, so a build never meets Docker Hub's pull limits. Renovate
# still resolves them on Docker Hub: the shared preset aliases the mirror.

# ARGs used in a FROM must live in the global scope (before the first FROM).
# GO_VERSION, NODE_VERSION and MISE_VERSION are supplied by the release
# workflow from mise, the single source of truth for the toolchain (see
# .mise/config.toml). Every build needs all three: a FROM that resolves to
# no image fails the build even in a stage its target skips.
ARG GO_VERSION
ARG NODE_VERSION
ARG MISE_VERSION

# ---- UI build ---------------------------------------------------------------
# The built UI is the same bytes on every platform, so it is built once, on
# the build host, rather than per target under emulation.
FROM --platform=$BUILDPLATFORM mirror.gcr.io/library/node:${NODE_VERSION}-trixie-slim AS ui
# PNPM_VERSION is supplied as the others are. The Node image ships no pnpm,
# and its npm package's install script only swaps the wrappers that run it
# through Node for its native binary, so it is installed without scripts.
ARG PNPM_VERSION
RUN npm install --global --ignore-scripts "pnpm@${PNPM_VERSION:?PNPM_VERSION is required}"
WORKDIR /ui
# The wildcard takes pnpm-workspace.yaml too once it exists: its allowBuilds
# and minimumReleaseAgeExclude decide whether the install passes.
COPY internal/web/package.json internal/web/pnpm-*.yaml ./
RUN --mount=type=cache,target=/root/.local/share/pnpm/store pnpm install --frozen-lockfile
COPY internal/web/ ./
RUN pnpm run build

# ---- Go build -------------------------------------------------------------
FROM mirror.gcr.io/library/golang:${GO_VERSION}-trixie AS builder
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG REVISION=dev

WORKDIR /workspace
# Cache module downloads before copying source.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY cmd/ cmd/
COPY internal/ internal/
COPY --from=ui /ui/dist/ internal/web/dist/

# Static, stripped, reproducible binary. GOARCH is left to the platform.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${REVISION}" \
    -o kritika ./cmd/kritika

# ---- Runtime with runner tools ----------------------------------------------
# kritika with mise and the tools the agent's run tool offers: the chart's
# runner image. Built with --target tools, published as the -tools tag.
# Distroless with glibc for the tools' release builds, and no shell.
FROM mirror.gcr.io/jdxcode/mise:${MISE_VERSION} AS mise
FROM gcr.io/distroless/cc:nonroot AS tools
# mise lives outside PATH. The runner runs it by path; a command the agent
# runs must not find it.
COPY --from=mise /usr/local/bin/mise /opt/mise/bin/mise
# The tools are system installs from the image's system config, at what
# its lock pins. A run's mise uses them at those versions instead of
# downloading. Installing from the system config also makes the links
# mise keeps beside each install, which a run could not add later. Root's
# home holds mise's state and cache, on a tmpfs, so only the installs
# reach the image.
COPY build/tools/mise.toml /etc/mise/config.toml
COPY build/tools/mise.lock /etc/mise/mise.lock
USER root
RUN --mount=type=tmpfs,target=/root ["/opt/mise/bin/mise", "install", "--system", "--locked"]
USER 65532
COPY --from=builder /workspace/kritika /kritika
ENTRYPOINT ["/kritika"]

# ---- Runtime --------------------------------------------------------------
# The default target, last so a build without --target produces it.
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/kritika /kritika
ENTRYPOINT ["/kritika"]
