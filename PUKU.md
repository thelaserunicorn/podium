# PUKU.md

This file provides guidance to puku-cli when working with code in this repository.

## Read first

- **Product spec**: @spec.md — source of truth for *what* Podium should do.
- **Agent rules**: @AGENTS.md — technology choices, architecture rules, Go/TS style, file organization, Definition of Done, and Git workflow.
- **Locked decisions**: @DECISIONS.md — resolves ambiguities in spec.md and AGENTS.md (namespaces vs. environments, image versioning, sessions, etc.). These are the most recent and binding.
- **Build plan**: @PLAN.md — ordered milestones M1–M7 with package-by-package scope, endpoints, tests, and DoD per slice.

**Precedence when they disagree**: `DECISIONS.md` > `AGENTS.md` > `spec.md`.

## Project context

Podium is a final-year project: a lightweight Internal Developer Platform (mini-PaaS). A Go control plane + React dashboard that builds Docker images from GitHub repos and deploys them to local Kubernetes (kind). See spec.md §1–§5.

**Current state**: spec, agents instructions, and locked decisions are written; source code is being implemented incrementally per the checkpoints in spec.md §47.

## Source layout (per spec.md §22)

```
Podium/
├── backend/
│   ├── cmd/podium/main.go
│   ├── internal/{auth,api,application,deployment,docker,kubernetes,logs,storage}/
│   ├── migrations/
│   ├── go.mod
│   └── go.sum
└── frontend/
    ├── src/
    ├── package.json
    └── vite.config.ts
```

## Rules Puku CLI must not violate

- Read spec.md + AGENTS.md + DECISIONS.md before any non-trivial implementation.
- One concern per commit; use `type(scope): summary` format (see AGENTS.md "Git workflow tips").
- Priorities, in order: correctness, simplicity, maintainability, clear architecture, developer experience, visual polish.
- **Do NOT introduce**: Gin, Echo, Fiber, Chi, Next.js, PostgreSQL, Redis, Kafka, microservices, Kubernetes operators, AI agents, or message queues (see AGENTS.md §18).
- Kubernetes is the source of truth for runtime state. SQLite stores only Podium metadata (users, applications, deployment history, env config, image tags).
- Frontend NEVER talks to Docker or Kubernetes directly — always through the Go API.
- Backend authorization is mandatory. Frontend hiding alone is insufficient. Users can only access their own applications.
- Passwords hashed with bcrypt; HTTP-only session cookies; `PODIUM_ADMIN_USERNAME` / `PODIUM_ADMIN_PASSWORD` env vars for the default admin.
- Do not expose stack traces, password hashes, or secrets via the API.

## Common commands (wire these up as code lands)

- **Backend**: `go test ./...`, `go vet ./...`, `gofmt -l .`, `go build ./cmd/podium`
- **Frontend**: `npm run dev`, `npm run build`, `npm run typecheck`, `npx prettier --check .`
- **Full verify**: see `.puku/skills/podium-verify/SKILL.md`
- **Scaffold a new package**: see `.puku/skills/podium-scaffold/SKILL.md`

## Skills installed in this repo

38 workflow skills under `.puku/skills/` (from `mattpocock/skills`). Highlights:

- `/implement-spec` — turn a spec section into a working slice (good fit for spec.md checkpoints).
- `/tdd` — test-driven workflow (matches AGENTS.md §20 testing priorities).
- `/code-review`, `/diagnosing-bugs`, `/resolving-merge-conflicts` — engineering hygiene.
- `/grill-me`, `/grill-with-docs` — stress-test design decisions.
- `/to-spec`, `/to-tickets`, `/triage` — planning.
- `/podium-verify`, `/podium-scaffold` — project-specific helpers added by `/init`.

## Notes

- This is a one-week, final-year project. Do not over-engineer. Keep packages small; do not create abstractions for their own sake.
- Build features in spec.md §47 checkpoint order: Foundation → Docker → Kubernetes → Deployment Management → Diagnostics → Environments → Finalization.
