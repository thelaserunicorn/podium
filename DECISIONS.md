# Podium — Implementation Decisions

Locked decisions from the grill-me interview on 2026-09-06. These resolve
ambiguities in `spec.md` and `AGENTS.md` and are the source of truth for
implementation until/unless the spec is amended.

## A. Source acquisition & build path

- **Source**: `git clone https://github.com/<owner>/<repo>.git` over HTTPS.
  Public repos only. No token, no GitHub App. Private repos and non-GitHub
  providers are explicitly out of scope for the MVP.
- **Cluster**: kind only. Podium's local-Kubernetes target is a single
  kind cluster.
- **Image transport**: Podium builds into the host's local Docker daemon,
  then calls `kind load docker-image <image>:<tag>` to push the image into
  the kind cluster's nodes. No sidecar registry container required.
- **Podium runtime**: Podium runs **on the host** for the demo
  (`go run ./cmd/podium` or the compiled binary). `docker-compose.yml` is
  shipped as a secondary deployment option but is not the documented
  local-demo path, because Podium needs direct access to the host's Docker
  daemon and to the `kind` CLI.

## B. Deployment state machine

- **Driver**: one goroutine per deployment runs the full pipeline
  (`QUEUED → BUILDING → BUILT → DEPLOYING → STARTING → RUNNING`,
  any step → `FAILED`). No external queue, no Redis.
- **Concurrency**: two deploys for the same app fired within seconds —
  the second is rejected with `409 Conflict`. No queueing, no parallel
  builds per app.
- **Timeouts**: hard wall-clock timeout per stage. On timeout the
  deployment is marked `FAILED` with a short reason string
  (`build_timeout`, `deploy_timeout`, `readiness_timeout`).
- **RUNNING definition**: Podium polls Kubernetes until
  `readyReplicas == desiredReplicas`, then marks the deployment RUNNING.
  Scale-to-zero (`desiredReplicas == 0`) flips to RUNNING as soon as the
  Deployment object exists. Anything else
  (ImagePullBackOff, CrashLoopBackOff, ErrImagePull, timeout) → FAILED.
- **Build logs**: stored in SQLite. They are bounded per build and central
  to diagnostics. Pod logs are NOT stored in SQLite.
- **Build log UI transport**: client polls
  `GET /api/deployments/{id}/logs?since=<ts>` every ~2s. SSE is stretch.
- **Pod log retrieval**: only the current container's logs
  (`kubectl logs <pod>`, no `--previous`). Matches the spec's
  "recent log output" wording.

## C. Namespaces (formerly "Environments")

- **Environments are namespaces, full stop.** `podium-dev`, `podium-staging`,
  `podium-prod` are three namespaces Podium ensures exist. They are not a
  separate first-class entity with their own promotion ladder.
- **Custom namespaces**: a user types a namespace name into the deploy
  form; if it doesn't exist, Podium creates it. No namespace management UI.
  Namespace names must be DNS-1123 compatible and unique per user.
- **Per-namespace state**: each namespace has its own deployment history
  per app, its own pods, its own logs, its own rollback target.
- **App detail namespace switcher**: scopes the entire detail page
  (Overview / Deployments / Logs / Events / Settings) to the selected
  namespace. The spec's "environment switcher" wording is replaced by
  "namespace switcher" in implementation and UI copy.
- **Promotion**: any namespace can deploy to any other namespace. There is
  no Dev → Staging → Production ordering. A "Promote to <namespace>" button
  may exist as a convenience but it is not gated.

## D. Rollback & image versioning

- **Rollback scope**: per-namespace. Rolling back Production to v41 leaves
  Staging at v42.
- **`version` field**: monotonic counter per application starting at 1.
  Used as the image tag suffix (deployment #1 → image `my-api:v1`). Simple,
  human-readable, fits the spec's example in §16.

## E. Configuration & secrets

- **Env vars**: a single `is_secret` boolean per var. `is_secret=false`
  routes to a Kubernetes ConfigMap; `is_secret=true` routes to a Kubernetes
  Secret with the value base64-encoded inside the Secret object. No
  re-encryption at rest in SQLite beyond the K8s Secret boundary.
- **App name uniqueness**: unique per user. The Kubernetes Deployment name
  in a namespace becomes ` <app-name>-<short-id>` to dodge collisions
  across users and within a namespace.
- **Multiple apps, one repo**: each app is independent. Two apps pointing
  at the same repo get two builds, two image tags, two deployment histories.

## F. Auth, sessions, status

- **Sessions**: server-side sessions stored in a SQLite `sessions` table.
  Session ID is a random opaque token in an HttpOnly cookie. Survives
  Podium restart. No JWT.
- **User status semantics**:
  - `PENDING` — newly signed up, awaiting admin action.
  - `APPROVED` — can log in.
  - `REJECTED` — signup denied. Terminal. Cannot be flipped back to
    APPROVED.
  - `DISABLED` — was APPROVED, now blocked. Admin can flip back to
    APPROVED.
  - All four statuses except `APPROVED` block login.

## G. UI

- **Sidebar**: generic IDP pattern. Primary nav (Dashboard, Applications,
  etc.), admin items visible only when the current user is an admin,
  user menu top-right. No marketing pages, no docs link, no settings page
  beyond per-app settings.
- **YAML viewer**: read-only YAML view of every Kubernetes resource
  Podium manages (Deployment, Service, ConfigMap, Secret). The one
  mutating affordance inside the YAML view is **edit replicas** on the
  Deployment, which is just the existing scale action. No arbitrary
  manifest editing in the MVP.
- **Loading / async states**: explicit inline states for Building,
  Deploying, Waiting for Pods, Scaling, Restarting. Duplicate actions
  disabled while an operation is in progress.
- **Empty states**: every list page has one (no apps, no deployments,
  no logs, no pending users, no pods).

## H. Demo, docs, scope

- **No demo app shipped with Podium.** A tiny Go `net/http` "hello" repo
  with a Dockerfile can be added later if needed for the acceptance
  walkthrough.
- **README**: focused on what/why/prerequisites/local-setup/demo-script.
  Includes the architecture diagram. Does not duplicate `spec.md`.
- **Cut order if time runs out**: (1) YAML viewer, (2) replica edit on the
  Deployment view, (3) custom namespaces beyond the three defaults,
  (4) "Promote to <namespace>" convenience button, (5) environment
  variables. Core loop (signup → approve → login → create app → deploy
  → logs → scale → restart → rollback) is non-negotiable.