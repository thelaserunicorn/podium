# M7 — Remaining work for a future agent

This file is the continuation handoff for the two M7 slices that were
deliberately deferred when PR #5 was opened:

- **Slice 5** — extend `scripts/smoke.sh` with M4 / M5 / M6 coverage
- **Slice 6** — `scripts/smoke-acceptance.sh`, the §48 walkthrough as
  one script

Both are tracked as pending tasks (#91, #92). They were deferred because
the user said "I'll do these later" — there's no design work left, just
scripting work that depends on whichever environment is at hand when
the next agent picks them up.

## What already exists

The M7 plan and current state of the relevant scripts:

- **`scripts/smoke.sh`** — covers M1 + M2 + M3 + M4. M4 is partial
  (Scale + Restart + EnvVars happy path; Rollback is covered by the
  `internal/api` tests, not smoke.sh). M5 (logs, events) and M6
  (promote) are NOT in smoke.sh.
- **`scripts/podium-smoke-env.sh`** — env-var harness used by smoke.sh
  to boot the binary with the right test fixtures.
- **`scripts/` directory layout** — small. Just those two files plus
  whatever the new script adds.

There is no `scripts/smoke-acceptance.sh` yet. PR #5's DoD calls for it
but it was deferred.

## Slice 5 — extend `scripts/smoke.sh` for M4 / M5 / M6

Goal: every milestone in spec.md §47 has at least one acceptance line in
`scripts/smoke.sh` so a single `bash scripts/smoke.sh` proves the whole
stack works end-to-end against a running binary.

### M4 coverage (currently partial)

`scripts/smoke.sh` already covers:

- `POST /api/applications/{id}/scale` happy path
- `POST /api/applications/{id}/restart` happy path
- `GET /api/applications/{id}/env` + `POST` + `DELETE` happy path

What's missing for full M4 coverage:

- **`POST /api/deployments/{id}/rollback`** — pick a previously-running
  deployment, rollback, verify a new deployment row exists pointing at
  the previous image, no rebuild. Reuse `LatestSuccessfulDeployment`
  from `backend/internal/storage/deployments.go` — that's exactly the
  query the orchestrator runs.
- **Negative tests for env vars**:
  - `is_secret=true` value is base64 in the Secret on the cluster but
    **never returned** by `GET /api/applications/{id}/env`. The
    internal test covers this; smoke should too.
  - Invalid env-var keys (e.g. contains `.`, starts with digit) → 400.
- **Rollback with no prior successful deployment** → 4xx (the API
  returns 404 / 409 depending on whether a deployment row exists at all
  — read `internal/api/deployment.go:Rollback` to confirm which).

### M5 coverage (currently missing entirely)

- **`GET /api/applications/{id}/logs?pod=<podName>`** — pick a pod from
  the deployment, fetch logs, assert non-empty. The demo
  `podium-hello` pod logs from nginx are short and stable; use those.
- **`GET /api/deployments/{id}/events`** — assert at least one event
  returned. After a successful deploy there will be `Scheduled`,
  `Pulled`, `Created`, `Started`. Use a JSON grep on
  `reason=Scheduled` as the smoke check.
- **Build log polling** — `GET /api/deployments/{id}/logs?since=<ts>`
  should return lines with `ts > since`. The orchestrator writes lines
  as the build progresses; poll twice (early + late) and assert the
  second response has at least as many lines as the first.

### M6 coverage (currently missing entirely)

- **`POST /api/applications/{id}/promote`** — deploy to `podium-dev`,
  wait for `RUNNING`, promote to `podium-staging`, wait for
  `RUNNING` in staging, assert:
  - Both deployments share the same `image` string (DECISIONS.md C
    says promotion reuses the image bytes; verify).
  - Per-namespace state is isolated: rolling back `podium-prod` to v41
    leaves `podium-staging` at v42 (use `kubectl get deploy -A` after
    the rollback to assert).
- **YAML viewer** — `GET /api/applications/{id}/yaml?kind=Deployment`
  returns a non-empty YAML string matching the managed resource's
  `metadata.name` and `spec.replicas`. Don't try to parse the YAML in
  shell — just `grep` for the app name in the response.
- **Custom namespace** — `POST /api/applications/{id}/deploy` with a
  namespace string that doesn't pre-exist (e.g. `podium-test-<random>`)
  should succeed and create the namespace via `EnsureEnvironment`.
  After success, `kubectl get ns <name>` should show it.

### Where to add the new sections

Read `scripts/smoke.sh` first to see the existing pattern (each section
is a `_check_*` function with a printf prefix and `pass/fail` exit
code). Append new functions in milestone order:

```
# M4 (extend)
_check_rollback_happy
_check_rollback_no_target
_check_env_var_secret_redacted
_check_env_var_invalid_key

# M5 (new)
_check_pod_logs
_check_deployment_events
_check_build_log_polling

# M6 (new)
_check_promote_image_reuse
_check_namespace_isolation
_check_yaml_deployment
_check_custom_namespace
```

Add a short header comment above each new section explaining what it
covers (1-2 lines, no essay). Match the style of the existing M1/M2/M3
sections.

### Smoke-environment considerations

`scripts/podium-smoke-env.sh` already brings up the Go binary with a
test SQLite file. For M5/M6 you'll need a real kind cluster reachable
— either because `KUBECONFIG` points at a kind cluster or because the
script skips with a `[skip kind not reachable]` message. Read the
existing skip-when-no-kind pattern (search for `kubectl` or `kind`) and
match it — the smoke script must remain runnable on a laptop with no
kind.

## Slice 6 — `scripts/smoke-acceptance.sh`

Goal: the full §48 acceptance walkthrough from spec.md §48 as ONE script
that a reviewer can run after `docker compose up --build` + `kind create
cluster --name podium`. The script walks through all 15 spec.md §48
steps and prints `all 15 steps green` on success.

This is **separate from `scripts/smoke.sh`**, which tests per-milestone
API surfaces. `smoke-acceptance.sh` tests the user-visible end-to-end
flow: signup → admin approve → login → create app → deploy → wait
RUNNING → view build + pod logs → scale 1→3 → restart → rollback →
promote to staging.

### What it should do

1. Start a clean Podium stack (`docker compose up -d` against a fresh
   kind cluster, OR check that one is already running and skip).
2. Curl the §48 walkthrough as 15 sequential steps.
3. Use `jq` for JSON parsing (already a transitive dep of most dev
   machines; if missing, exit with a clear "install jq" message).
4. Use `kubectl --context kind-podium` for cluster assertions.
5. Print `STEP N/15: <description> ... PASS` or `FAIL` for each.
6. Exit 0 only if all 15 pass.

### The 15 steps from spec.md §48

1. `GET /healthz` → 200
2. `POST /api/auth/signup` (admin already seeded by `auth.SeedAdmin`)
3. New user is PENDING → `POST /api/auth/login` returns 403
4. Admin approves → `POST /api/admin/users/{id}/approve`
5. Approved user logs in successfully
6. Approved user creates an app pointing at
   `https://github.com/thelaserunicorn/podium-hello` with
   `container_port=80`
7. `POST /api/applications/{id}/deploy` → wait RUNNING (poll every 5s,
   timeout 300s)
8. `GET /api/applications/{id}` shows the app in `RUNNING` state
9. `GET /api/applications/{id}/logs?pod=<pod>` returns non-empty
10. `POST /api/applications/{id}/scale` to 3 replicas, wait until
    `kubectl get deploy -n podium-dev` shows 3/3
11. `POST /api/applications/{id}/restart` → `GET .../deployments` shows
    a new row
12. Deploy a broken app (use a known-bad image like
    `thelaserunicorn/podium-broken:v0`), assert `FAILED` state, assert
    build logs + events + pod logs are all retrievable
13. Deploy a fixed version (the same `podium-hello:v1`) → assert
    `RUNNING`
14. Promote the running image from `podium-dev` to `podium-staging` →
    assert RUNNING in staging with the same image string
15. Roll back to a previous deployment → assert a new row points at
    the prior image, no rebuild (compare `started_at` timestamps)

### Demo repo: `thelaserunicorn/podium-broken`

Step 12 needs a "known broken" demo. As of writing this, that repo
doesn't exist. Either:

- Create `thelaserunicorn/podium-broken` (mirror of `podium-hello`
  with a Dockerfile that does `RUN exit 1` mid-build), OR
- Use a known-broken image from Docker Hub (e.g. `alpine:this-tag-does-not-exist`).

The first option is more reproducible but adds a second repo to
maintain. Recommend the second option (`alpine:does-not-exist`) for
the smoke script — it's pure read access to Docker Hub's error
response and doesn't add a maintenance burden.

### Where to put the file

`scripts/smoke-acceptance.sh`, executable bit set
(`chmod +x scripts/smoke-acceptance.sh`). Shebang `#!/usr/bin/env bash`.

Match the style of `scripts/smoke.sh` for consistency (set -euo pipefail,
prefix output with `STEP` or `smoke-acceptance`, clear failure messages
with the curl command that failed).

## Reference

- `PLAN.md` §M7 — original slice breakdown
- `spec.md` §48 — the 15-step acceptance walkthrough
- `docs/popos-setup.md` §10 — the manual §48 walkthrough that this
  script automates
- `scripts/smoke.sh` — existing per-milestone smoke tests
- `scripts/podium-smoke-env.sh` — env harness for smoke.sh

## Status

- [x] Slice 1 — Dockerfile + .dockerignore (`a696740`)
- [x] Slice 2 — docker-compose.yml (`6cc5f97`)
- [x] Slice 3 — `thelaserunicorn/podium-hello` demo repo
- [x] Slice 4 — README.md + `docs/popos-setup.md` (`5c2e165`)
- [x] Slice 4b — frontend container + nginx proxy fix (`6b294b7`,
      `fb044f1`)
- [x] Slice 4c — agent-facing restart-from-cold checklist (`abee7f8`)
- [x] Bug fix — app-delete silent orphan warning (`237958e`)
- [ ] **Slice 5** — extend smoke.sh for M4/M5/M6 (#92)
- [ ] **Slice 6** — `scripts/smoke-acceptance.sh` (#91)
