# syntax=docker/dockerfile:1

# Images that live on Docker Hub are pulled through mirror.gcr.io, Google's
# cache of it, so a build never meets Docker Hub's pull limits. Renovate
# still resolves them on Docker Hub: the shared preset aliases the mirror.

# ARGs used in a FROM must live in the global scope (before the first FROM).
# GO_VERSION and NODE_VERSION are supplied by the release workflow from mise,
# the single source of truth for the toolchain (see .mise/config.toml).
ARG GO_VERSION
ARG NODE_VERSION

# ---- UI build ---------------------------------------------------------------
# The built UI is the same bytes on every platform, so it is built once, on
# the build host, rather than per target under emulation.
FROM --platform=$BUILDPLATFORM mirror.gcr.io/library/node:${NODE_VERSION}-trixie-slim AS ui
WORKDIR /ui
COPY internal/web/package.json internal/web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci
COPY internal/web/ ./
RUN npm run build

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

# ---- Runner tools -----------------------------------------------------------
# The release builds of the tools the -tools image puts on PATH, installed by
# mise from build/tools/mise.toml at the URLs and checksums its lockfile pins
# for this platform, as mise system installs: the runner's own mise uses a
# system install for any config that resolves the same version, so a default
# tool is never downloaded at run time. The same file becomes the image's
# system config, which declares those versions. A glibc stage, so mise
# records the same linux-x64 and linux-arm64 platforms as the repository's
# own lockfile. mise's image has no shell to copy the binaries out with, so
# its static binary and the CA roots it downloads with are taken from it
# instead. The binary lives outside PATH: the runner runs it by path, and a
# command the agent runs must not find it.
FROM mirror.gcr.io/jdxcode/mise:2026.10.9 AS mise
FROM mirror.gcr.io/library/debian:trixie-slim AS runner-tools
COPY --from=mise /usr/local/bin/mise /opt/mise/bin/mise
COPY --from=mise /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
WORKDIR /tools
COPY build/tools/mise.toml build/tools/mise.lock ./
# The manifest keeps each tool's bin path down to the executables it is
# there for, so the bin paths are copied whole.
RUN /opt/mise/bin/mise trust && /opt/mise/bin/mise install --system --locked \
    && mkdir -p /etc/mise && cp mise.toml /etc/mise/config.toml && cp mise.lock /etc/mise/mise.lock \
    && mkdir /out && /opt/mise/bin/mise bin-paths | xargs -I{} cp -rL {}/. /out/

# ---- Runtime with runner tools ----------------------------------------------
# kritika with the agent's commands on PATH for its run tool, the chart's
# runner image for runner Jobs. Built with --target tools; published as the
# -tools tag of each release. Distroless with glibc, libgcc and libstdc++ for
# the tools' release builds, and no shell or package manager: the commands
# on PATH, mise outside it, and what the tools' archives carry beside their
# exposed binaries are the only other programs in the image.
FROM gcr.io/distroless/cc:nonroot AS tools
COPY --from=runner-tools /opt/mise/bin/mise /opt/mise/bin/mise
COPY --from=runner-tools /etc/mise/ /etc/mise/
COPY --from=runner-tools /usr/local/share/mise/ /usr/local/share/mise/
COPY --from=runner-tools /out/ /usr/local/bin/
COPY --from=builder /workspace/kritika /kritika
ENTRYPOINT ["/kritika"]

# ---- Runtime --------------------------------------------------------------
# The default target, last so a build without --target produces it.
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/kritika /kritika
ENTRYPOINT ["/kritika"]
