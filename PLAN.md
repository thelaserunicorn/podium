# Podium — Build Plan

## Context

This is a final-year project: a lightweight mini-PaaS. A Go control plane (`net/http` + SQLite + Kubernetes + Docker) plus a React dashboard that lets approved users build Docker images from GitHub repos and deploy them to a local **kind** cluster.

The repository is essentially empty: `backend/go.mod`, a stub `cmd/podium/main.go`, frontend config files (`package.json`, `tsconfig.json`, `.prettierrc.json`), `.puku/settings.json`, and the four top-level markdowns (`spec.md`, `AGENTS.md`, `DECISIONS.md`, `PUKU.md`). **No source code, no schema, no tests, no Dockerfile, no Vite config, no migrations.** The plan below fills that gap end-to-end, in the exact checkpoint order from `spec.md §47` and respecting all 8 sections of `DECISIONS.md`.

**Goal:** When the plan is fully executed, the §48 acceptance flow works against `docker compose up` + `kind create cluster`, with green tests and a clean README.

**Precedence:** `DECISIONS.md` > `AGENTS.md` > `spec.md`. When any rule below disagrees with another doc, the most specific source wins.

## Cross-cutting foundation — before / alongside M1

These run once at the start, then everything builds on them.

### Backend dependencies (`backend/go.mod`)

- `modernc.org/sqlite` — pure Go SQLite driver (no CGo)
- `github.com/docker/docker/client`
- `k8s.io/client-go`
- `golang.org/x/crypto/bcrypt`
- `github.com/google/uuid` — session token helper

`net/http` + `http.ServeMux` only. No Gin/Echo/Fiber/Chi (AGENTS.md §3).

### Storage (`backend/internal/storage/`, `backend/migrations/`)

- `storage.go`: opens SQLite at `PODIUM_DB_PATH` (default `./data/podium.db`), WAL mode, returns `*sql.DB`.
- Migration runner: `PRAGMA user_version` driven, idempotent, safe to re-run (AGENTS.md §7).
- `migrations/0001_init.sql` creates 6 tables per `spec.md §29` and `DECISIONS.md F`:

| Table | Notes |
|---|---|
| `users` | `username` UNIQUE, `email` UNIQUE, `password_hash` (bcrypt), `role CHECK IN ('ADMIN','USER')`, `status CHECK IN ('PENDING','APPROVED','REJECTED','DISABLED')` |
| `applications` | FK `user_id`, `name`, `repository_url`, `container_port`, `UNIQUE(user_id, name)` |
| `environments` | seeded with `podium-dev`, `podium-staging`, `podium-prod` (DECISIONS.md C); code treats these as Kubernetes namespaces, not a separate promotion ladder |
| `deployments` | FK `application_id`, `environment_id` (the namespace id), `version` starts at 1 per app (DECISIONS.md D), `status` covers the 7 states in `spec.md §11` |
| `environment_variables` | `application_id`, `environment_id` (namespace id), `key`, `value`, `is_secret` (DECISIONS.md E) |
| `sessions` | `id TEXT PRIMARY KEY` (opaque random token), `user_id`, `expires_at` |

Plus `deploy_log_lines(deployment_id, ts, line)` for build logs (DECISIONS.md B — build logs in SQLite, pod logs **NOT** in SQLite).

### Default admin seeding

On boot: `auth.SeedAdmin(db, os.Getenv("PODIUM_ADMIN_USERNAME"), os.Getenv("PODIUM_ADMIN_PASSWORD"))` upserts an `APPROVED` admin. bcrypt cost **10** (Risk #4 — keep signup under 100ms).

### Session manager (`backend/internal/auth/session.go`)

32-byte `crypto/rand` token, base64url; cookie name `podium_session`, `HttpOnly`, `SameSite=Lax`, 7-day TTL; `Create/Load/Delete`. Survives Podium restart (DECISIONS.md F).

### Middleware (`backend/internal/api/middleware.go`)

Composable net/http wrappers, in order: `Recover` → `RequestLog` (slog) → `WithSession` → `RequireAuth` → `RequireAdmin` / `RequireOwner`. `RequireOwner` loads the app, returns 404 (not 403) when not owned to avoid leaking existence (AGENTS.md §19, §41).

### Frontend scaffold (`frontend/src/`)

- `index.html`, `vite.config.ts`, `tailwind.config.ts`, `postcss.config.js`, `src/main.tsx`, `src/App.tsx`.
- `react-router-dom` for routes; sidebar + top-header shell (spec.md §13 frontend decisions).
- `src/lib/api.ts`: single fetch client with `credentials: "include"`.
- `src/lib/auth.tsx`: `AuthProvider` hits `GET /api/auth/me` on boot.
- shadcn/ui components: `Button`, `Input`, `Card`, `Table`, `Badge`, `Dialog`, `Drawer`, `Tabs`, `Select`.

## Milestone 1 — Checkpoint 1: Foundation

**Backend packages:** `internal/storage`, `internal/auth`, `internal/application`, `internal/api`.

**Endpoints:**

- `POST /api/auth/signup` → creates PENDING user (spec.md §6)
- `POST /api/auth/login` → bcrypt verify + status === APPROVED check (spec.md §7)
- `POST /api/auth/logout`
- `GET  /api/auth/me`
- `GET|POST /api/applications`, `GET|PUT|DELETE /api/applications/{id}`
- `GET  /api/admin/users`
- `POST /api/admin/users/{id}/{approve|reject|disable}`
- `EnsureDefaultNamespaces(ctx)` called on boot; stub returns nil if kubeconfig missing (real impl lands in M3).

**Frontend:** `/login`, `/signup`, `/dashboard` (count cards + empty state per spec.md §37), `/apps`, `/apps/new`, `/admin/users` (gated on `user.role === 'ADMIN'`).

**Tests (AGENTS.md §20):**

- bcrypt hash/verify roundtrip
- PENDING user cannot log in
- APPROVED user can log in
- admin middleware returns 403 to non-admin
- user A cannot read user B's application (returns 404)
- application CRUD roundtrips through `:memory:` SQLite

**DoD (AGENTS.md §24):** backend works, API correct, frontend exposes it, loading/error/empty states present, authz enforced, data persisted, `gofmt -l .` and `go vet ./...` and `go test ./...` green.

**Depends on:** foundation only.

## Milestone 2 — Checkpoint 2: Docker

**Backend packages:** `internal/docker/`, `internal/deployment/` (state machine only — Orchestrator wires to k8s in M3).

- `docker.Client` wrapping the Docker Engine API (`client.NewClientWithOpts(client.FromEnv)`).
- `Build(ctx, dir, tag, logSink)` via `ImageBuild` + `stdcopy.StdCopy` to demux build output.
- `LoadIntoKind(ctx, tag)` shelling out to `kind load docker-image <tag>` (DECISIONS.md A — Podium is on host, has direct access to kind + docker).
- `Clone(ctx, url, dir)` shelling `git clone --depth=1 <url> <dir>` (DECISIONS.md A — public GitHub repos only, no token).
- `internal/deployment/orchestrator.go`: one goroutine per deployment driving the state machine in `spec.md §12`.
- `StatusStore` writes transitions to `deployments` and appends to `deploy_log_lines`.

**Frontend:** `/apps/:id` with tabs (Overview/Deployments/Logs/Events/Settings); only Overview + Deployments tab live for this checkpoint. Deploy button + modal. Build-log viewer polls `GET /api/deployments/{id}/logs?since=<ts>` every ~2s (DECISIONS.md B).

**Endpoints:** `POST /api/applications/{id}/deploy`, `GET /api/deployments/{id}`, `GET /api/deployments/{id}/logs?since=`.

**Tests:** state transitions with a fake docker client; second concurrent deploy for same app returns 409; build log persisted; `kind load` failure → deployment FAILED with reason string (`build_timeout`, `build_failed`).

**DoD:** backend builds images; frontend shows live build logs; authz enforced on deploy; FAILED visible.

**Depends on:** M1.

## Milestone 3 — Checkpoint 3: Kubernetes

**Backend package:** `internal/kubernetes/`.

- `Client` via `clientcmd.BuildConfigFromFlags("", os.Getenv("KUBECONFIG"))` (default `~/.kube/config`).
- `EnsureNamespace(name)` idempotent, validates DNS-1123 (Risk #5).
- `ApplyDeployment(app, ns, image, replicas)` — name `<app>-<6char short id>` (DECISIONS.md E).
- `ApplyService(app, ns, port)` — internal ClusterIP only (spec.md §17).
- `ListPods(ns, labelSelector)`, `CurrentReplicas(ns, name)`.

Per DECISIONS.md C: namespaces replace environments in code; a user may create additional namespaces via the deploy form.

**Frontend:** Overview tab shows pod table + status badges + current/desired replicas; refresh polls `GET /api/applications/{id}`.

**Endpoint:** `GET /api/applications/{id}` enriched with live k8s state; `GET /api/namespaces` (user-visible).

**Tests:** namespace create idempotent against fake clientset (`k8s.io/client-go/kubernetes/fake`); service port matches `container_port`; ownership check happens server-side.

**DoD:** real `Deployment` + `Service` exist in kind cluster; pods reach Running.

**Depends on:** M2 (needs images to deploy).

## Milestone 4 — Checkpoint 4: Deployment Management

In `internal/deployment/`:

- `Orchestrator.WaitForReady` polls until `readyReplicas == desiredReplicas` (or immediately if scale-to-zero per DECISIONS.md B); any `ImagePullBackOff`/`CrashLoopBackOff`/`ErrImagePull`/timeout → FAILED with reason.
- `Scale(ctx, app, ns, n)` patches Deployment.
- `Restart(ctx, app, ns)` deletes pods, lets the Deployment recreate them.
- `Rollback(ctx, app, ns, targetVersion)` looks up prior image from the deployments table, applies without rebuild (DECISIONS.md D — per-namespace).

In `internal/application/env.go`:

- `SetEnv/GetEnv/DeleteEnv`. `is_secret=true` → `kubernetes.io/basic-auth`-style Secret with value base64-encoded **inside the Secret object only** (DECISIONS.md E). Values for secret env vars MUST NOT be returned by any GET endpoint.

**Frontend:** Scale slider 1–5 (default 3), Restart with confirm dialog, env-vars editor with secret toggle (masked value), rollback dropdown on the Deployments tab.

**Endpoints:** `POST /api/applications/{id}/scale`, `POST /api/applications/{id}/restart`, `POST /api/deployments/{id}/rollback`, `GET|POST /api/applications/{id}/env`, `DELETE /api/applications/{id}/env/{key}`.

**Tests:** Scale updates replicas; Rollback reuses prior image, version counter monotonic; `version` test for concurrent inserts; secret value never returned in JSON (AGENTS.md §19, §41).

**DoD:** Scale/restart/rollback work end-to-end; env vars land as ConfigMap / Secret; secret values never leak.

**Depends on:** M3.

## Milestone 5 — Checkpoint 5: Diagnostics

**Backend package:** `internal/logs/`.

- `PodLogs(ctx, ns, pod)` via `RESTClient().Pods(ns).GetLogs(pod, &corev1.PodLogOptions{Follow: false}).DoRaw()`. **No `--previous`** (DECISIONS.md B). No SQLite persistence.
- `BuildLogs(ctx, deploymentID, since)` reads from `deploy_log_lines` (DECISIONS.md B).
- `Events(ctx, ns, uid)` via `EventsV1().Events(ns).List`.

**Frontend:** Logs tab (pod selector + refresh), Events tab (time/type/reason table), Deployment detail page aggregating status + build logs + events + pod logs.

**Endpoints:** `GET /api/applications/{id}/logs?pod=`, `GET /api/deployments/{id}/events`.

**Tests:** Pod-logs service against fake clientset; readiness timeout → FAILED with `readiness_timeout`; pod logs never written to SQLite.

**DoD:** Failed deploys show actionable diagnostics; no stack traces / hashes / secrets exposed.

**Depends on:** M4.

## Milestone 6 — Checkpoint 6: Environments, Promotion, YAML, Admin UI polish

**Backend:**

- Per-namespace state in SQLite (each `deployments` row already carries `environment_id`/namespace); namespace switcher persists `(application, current_namespace)` view-state.
- `Promote(ctx, app, targetNs)` picks the latest RUNNING deployment's image, applies to target namespace without rebuild (DECISIONS.md C: any ns → any ns, no Dev→Staging→Prod ordering).
- `internal/api/yaml.go` — read-only YAML serializer for managed resources (DECISIONS.md G). The single mutating affordance **inside** the YAML view is "edit replicas", which calls the same `Scale` action.

**Frontend:** Namespace switcher on the app detail page scoping all tabs; Promote button; YAML viewer drawer; empty states everywhere (AGENTS.md §44, DECISIONS.md G). UI copy uses "namespace switcher", not "environment switcher" (DECISIONS.md C).

**Endpoint:** `POST /api/applications/{id}/promote`.

**Tests:** Promote reuses image and bumps version per target ns; YAML view returns identical manifest; custom namespaces validated DNS-1123 + unique per user.

**DoD:** Cross-namespace promotion + rollback work; admin UI polished; YAML view read-only.

**Depends on:** M5.

## Milestone 7 — Checkpoint 7: Finalization

**Backend / infra:**

- `Dockerfile` (multi-stage: `golang:1.22` builder → `gcr.io/distroless/static-debian12` runtime).
- `docker-compose.yml` with persistent volume `podium-data:/data` (spec.md §39). Doc this as the demo path requiring host-docker access per DECISIONS.md A.
- `k8s/namespace.yaml`, `k8s/deployment.yaml`, `k8s/service.yaml` — secondary, for users who want to self-host Podium in their cluster.
- Structured `slog` JSON logs; `GET /healthz`.
- Green: `gofmt -l .`, `go vet ./...`, `go test ./...`, `go build ./cmd/podium`.

**Frontend:** green `npm run typecheck`, `npx prettier --check .`, `npm run build`.

**Demo app:** tiny Go `net/http` "hello" repo with Dockerfile for the §48 acceptance walkthrough (DECISIONS.md H — ship with Podium so review is self-contained).

**Docs:** `README.md` (spec.md §21 + AGENTS.md §21): what/why/prereqs/local-setup/demo-script — **no duplication of spec.md**. Architecture diagram (cites spec.md §5).

**Smoke test:** `scripts/smoke.sh` running the §48 flow against `docker compose up` + `kind create cluster`.

**DoD:** Acceptance flow reproducible step-by-step from a fresh `git clone`.

**Depends on:** M6.

## Top 5 Execution Risks (from DECISIONS.md)

1. **kind cluster + `kind load`** (DECISIONS.md A): `kind load docker-image` needs the `kind` binary on PATH and the cluster's container network reachable from the host. Detect early in M2; fail fast with a clear error.
2. **Docker daemon access from Podium** (DECISIONS.md A): in `docker compose` mode the daemon socket isn't mounted, breaking M2/M3. Doc up front: "for the demo, run Podium as a host binary or with the docker socket mounted".
3. **bcrypt cost vs. signup latency**: lock cost to **10** in foundation; document.
4. **Per-namespace version counter** (DECISIONS.md D): counter is per-application but rollback/promotion operate per-namespace. Unit-test `nextVersion` with concurrent inserts in M4.
5. **Custom namespace DNS-1123 + uniqueness per user** (DECISIONS.md C): add validator + tests in M1 alongside `internal/application`. Do not defer.

## Critical files to be created (cumulative)

- `backend/go.mod` (expand existing)
- `backend/cmd/podium/main.go` (replace stub)
- `backend/internal/storage/storage.go`, `backend/migrations/0001_init.sql`
- `backend/internal/auth/{password.go,session.go,service.go,handler.go}`, `_test.go`
- `backend/internal/application/{repo.go,service.go,handler.go,env.go}`, `_test.go`
- `backend/internal/docker/{client.go}`, `_test.go` (M2)
- `backend/internal/deployment/{orchestrator.go,status_store.go}`, `_test.go`
- `backend/internal/kubernetes/{client.go,namespace.go,deployment.go,service.go}`, `_test.go` (M3)
- `backend/internal/logs/{build_logs.go,pod_logs.go,events.go}`, `_test.go` (M5)
- `backend/internal/api/{middleware.go,router.go,yaml.go}`
- `frontend/` foundation: `index.html`, `vite.config.ts`, `tailwind.config.ts`, `postcss.config.js`, `src/main.tsx`, `src/App.tsx`, `src/lib/{api.ts,auth.tsx}`, `src/components/ui/*` (shadcn)
- `frontend/src/routes/{LoginPage,SignupPage,DashboardPage,AppListPage,NewAppPage,AppDetailPage,DeploymentDetailPage,AdminUsersPage}.tsx`
- `Dockerfile`, `docker-compose.yml`, `k8s/{namespace,deployment,service}.yaml`, `scripts/smoke.sh`, `README.md`

## How to verify end-to-end

1. **Format / lint / typecheck** — `podium-verify` skill runs:
   - `cd backend && gofmt -l . && go vet ./... && go test ./... && go build ./cmd/podium`
   - `cd frontend && npx prettier --check . && npx tsc --noEmit`
2. **Boot** — `cd backend && PODIUM_ADMIN_USERNAME=admin PODIUM_ADMIN_PASSWORD=secret go run ./cmd/podium`. Confirm SQLite file written, `curl :8080/healthz` returns 200.
3. **Acceptance script** — `bash scripts/smoke.sh` (M7) walks the full §48 flow:
   - signup → admin approval → login → create app (public GitHub repo with Dockerfile) → deploy → wait RUNNING → view build + pod logs → scale 1→3 → restart → rollback → promote to staging.
4. **Negative tests** — `go test ./...` covers PENDING login block, cross-user app access denial, secret env var never serialized, readiness timeout → FAILED.
5. **YAML viewer** — open an app detail page, click YAML, confirm only Deployment / Service / ConfigMap / Secret shown; replica field editable; rest read-only.

## Workflow rules to enforce throughout

- One concern per commit; `type(scope): summary` (AGENTS.md Git workflow tips).
- Read `spec.md` + `AGENTS.md` + `DECISIONS.md` before any non-trivial slice.
- Run `gofmt` + `prettier` (auto-formatting hooks are wired in `.puku/settings.json` — activate via `/hooks`).
- For each slice: write the test **first** (use the bundled `/tdd` skill) per AGENTS.md §20.
- After each slice, run `/podium-verify` before committing.
- Do not introduce Gin/Echo/Fiber/Chi, Next.js, PostgreSQL, Redis, microservices, AI agents (AGENTS.md §18).
