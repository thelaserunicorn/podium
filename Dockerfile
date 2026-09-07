# syntax=docker/dockerfile:1.7
#
# Podium control plane — multi-stage build.
#
# Stage 1 (build): golang:1.26-bookworm compiles the Go binary with
#                  CGO_ENABLED=0 so we can statically link against
#                  modernc.org/sqlite (pure-Go SQLite, no CGo). The
#                  version tracks the `go` directive in backend/go.mod.
#
# Stage 2 (runtime): distroless/static-debian12 (no shell, no busybox).
#                    We pre-create /data by COPYing a placeholder file
#                    (see below). The /data path may also be a bind-
#                    mounted volume; in that case the bind mount
#                    shadows this directory and the placeholder is
#                    hidden.
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

FROM gcr.io/distroless/static-debian12
WORKDIR /app

COPY --from=build /out/podium /app/podium

# Pre-create the data directory by COPYing a placeholder file. `COPY`
# with a source directory creates the destination if it doesn't exist
# (Dockerfile semantics); the placeholder file is harmless because
# distroless has no shell to trip on it. The /data path may also be a
# bind-mounted volume; in that case the bind mount shadows this
# directory and the placeholder is hidden.
COPY docker-data-placeholder /data/.placeholder

# Defaults; docker-compose.yml / docs/ubuntu-setup.md override where needed.
ENV PODIUM_ADDR=:8080 \
    PODIUM_DB_PATH=/data/podium.db \
    PODIUM_SOURCE_ROOT=/data/sources

EXPOSE 8080
VOLUME ["/data"]

ENTRYPOINT ["/app/podium"]
