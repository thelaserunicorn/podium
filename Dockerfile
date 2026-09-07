# syntax=docker/dockerfile:1.7
#
# Podium control plane — multi-stage build.
#
# Stage 1 (build): golang:1.26-bookworm compiles the Go binary with
#                  CGO_ENABLED=0 so we can statically link against
#                  modernc.org/sqlite (pure-Go SQLite, no CGo). The
#                  version tracks the `go` directive in backend/go.mod.
#
# Stage 2 (runtime): distroless/static:nonroot — ~15MB image, runs as
#                    UID 65532. The /data volume is where Podium keeps
#                    its SQLite database and cloned source trees; mount
#                    a host directory or named volume there (see
#                    docker-compose.yml).
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

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app

COPY --from=build /out/podium /app/podium

# Defaults; docker-compose.yml / docs/ubuntu-setup.md override where needed.
ENV PODIUM_ADDR=:8080 \
    PODIUM_DB_PATH=/data/podium.db \
    PODIUM_SOURCE_ROOT=/data/sources

EXPOSE 8080
VOLUME ["/data"]

# distroless/static has no shell, so the user is already nonroot. ENTRYPOINT
# must be exec-form (the JSON array below) because there is no shell to parse
# a string command.
USER nonroot:nonroot
ENTRYPOINT ["/app/podium"]
