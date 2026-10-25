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

# ---- Runner tools root ------------------------------------------------------
# The runner's root: Red Hat Hardened Images' core runtime, its bash,
# coreutils, glibc and CA certificates, with curl, find, grep and jq,
# libstdc++ for a repository's tools and the timezone database, legacy names
# included, for a TZ the admin sets. The builder variant's dnf installs them
# into a copy of it, so the image carries no package manager. Renovate bumps
# both images' digests.
FROM registry.access.redhat.com/hi/core-runtime:latest@sha256:329ff8f46fe69f036b96c3463272aa778a6df6a4f7570f080fd248a414652b6e AS core-runtime
FROM registry.access.redhat.com/hi/core-runtime:latest-builder@sha256:057ba34d73d5308cd0eeeb2637ec9880c55fe4075ce3321edd9e7ee12c363754 AS tools-root
USER 0
COPY --from=core-runtime / /rootfs
RUN dnf install -y --installroot /rootfs --use-host-config --setopt=install_weak_deps=False --nodocs \
      curl findutils grep jq libstdc++ tzdata \
 && dnf --installroot /rootfs --use-host-config clean all
# gh as its release builds it, a static binary, checked against its
# release's checksums.
ARG TARGETARCH
# renovate: datasource=github-releases depName=cli/cli
ARG GH_VERSION=v2.103.0
RUN dnf install -y --setopt=install_weak_deps=False gawk gzip tar \
 && version="${GH_VERSION#v}" && asset="gh_${version}_linux_${TARGETARCH}.tar.gz" \
 && release="https://github.com/cli/cli/releases/download/${GH_VERSION}" \
 && curl -fsSLO "${release}/${asset}" \
 && curl -fsSL "${release}/gh_${version}_checksums.txt" | awk -v a="${asset}" '$2 == a' | sha256sum -c - \
 && mkdir -p /rootfs/usr/local/bin \
 && tar -xzf "${asset}" -C /rootfs/usr/local/bin --strip-components=2 "gh_${version}_linux_${TARGETARCH}/bin/gh"

# ---- Runtime with runner tools ----------------------------------------------
# kritika with the commands the agent's run tool offers: the chart's runner
# image. Built with --target tools, published as the -tools tag.
FROM mirror.gcr.io/jdxcode/mise:${MISE_VERSION} AS mise
FROM scratch AS tools
COPY --from=tools-root /rootfs /
ENV PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
# mise installs a repository's tools. It lives outside PATH: the runner
# runs it by path, and a command the agent runs must not find it.
COPY --from=mise /usr/local/bin/mise /opt/mise/bin/mise
USER 65532:65532
COPY --from=builder /workspace/kritika /kritika
ENTRYPOINT ["/kritika"]

# ---- Runtime --------------------------------------------------------------
# The default target, last so a build without --target produces it.
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/kritika /kritika
ENTRYPOINT ["/kritika"]
