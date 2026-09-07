# syntax=docker/dockerfile:1.7
#
# Podium control plane — multi-stage build.
#
# Stage 1 (build): golang:1.26-bookworm compiles the Go binary with
#                  CGO_ENABLED=0 so we can statically link against
#                  modernc.org/sqlite (pure-Go SQLite, no CGo). The
#                  version tracks the `go` directive in backend/go.mod.
#
# Stage 2 (runtime): debian:bookworm-slim. We need git at runtime
#                    (the orchestrator shells out to `git clone` for
#                    every deploy — see backend/internal/docker/source.go)
#                    so a distroless base doesn't work. bookworm-slim
#                    is ~80MB but keeps git available via apt and
#                    includes a shell for debugging.
#
# Build context must be the Podium repo root; only `backend/` is copied
# into the build stage. See .dockerignore for the full exclusion list.

FROM golang:1.26-bookworm AS build
WORKDIR /src

# Cache the module download layer separately so source edits don't bust
# the cache. go.mod / go.sum are the only files needed for go mod download.
COPY backend/go.mod backend/go.sum ./
RUN go mod download

COPY backend/ ./
RUN CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags="-s -w" \
        -o /out/podium \
        ./cmd/podium

FROM debian:bookworm-slim
WORKDIR /app

# git is required by the source fetcher (internal/docker/source.go).
# ca-certificates is required for git clone over HTTPS — without it
# every clone fails with "SSL certificate problem". curl is needed
# to download the kind + docker CLIs below. --no-install-recommends
# keeps the layer lean.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        git \
        ca-certificates \
        curl \
    && rm -rf /var/lib/apt/lists/*

# kind is the CLI Podium shells out to in order to push built images
# into the kind cluster's node Docker daemon (see
# internal/docker/docker.go:loadIntoKind). Pinned to v0.27.0 to match
# docs/ubuntu-setup.md.
#
# docker is needed too: `kind load docker-image` works by shelling out
# to the docker CLI to find which container is the cluster's node, then
# `docker exec` into that node. Without the CLI kind errors with
# `exec: "docker": executable file not found in $PATH`. We don't
# install dockerd — only the CLI talking to the host's socket mount.
ARG KIND_VERSION=0.33.0
RUN curl -fsSL "https://github.com/kubernetes-sigs/kind/releases/download/v${KIND_VERSION}/kind-linux-amd64" \
        -o /usr/local/bin/kind \
    && chmod +x /usr/local/bin/kind \
    && kind version

# Docker CLI only — no dockerd. Talks to the host's daemon via the
# /var/run/docker.sock bind mount. Pinned to match the host daemon
# runtime (recent stable).
ARG DOCKER_VERSION=27.3.1
RUN curl -fsSL "https://download.docker.com/linux/static/stable/x86_64/docker-${DOCKER_VERSION}.tgz" \
        -o /tmp/docker.tgz \
    && tar -xzf /tmp/docker.tgz -C /tmp \
    && install -m 0755 /tmp/docker/docker /usr/local/bin/docker \
    && rm -rf /tmp/docker /tmp/docker.tgz \
    && docker --version

COPY --from=build /out/podium /app/podium

# Pre-create /data by COPYing a placeholder file. The /data path may
# also be a bind-mounted volume; in that case the bind mount shadows
# this directory and the placeholder is hidden.
COPY docker-data-placeholder /data/.placeholder

# Defaults; docker-compose.yml / docs/ubuntu-setup.md override where needed.
ENV PODIUM_ADDR=:8080 \
    PODIUM_DB_PATH=/data/podium.db \
    PODIUM_SOURCE_ROOT=/data/sources

EXPOSE 8080
VOLUME ["/data"]

ENTRYPOINT ["/app/podium"]
