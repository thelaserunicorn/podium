# Podium — Agent Instructions

This file summarizes the Podium specification for coding agents.

## 1. Read the spec first

Before implementing or modifying Podium, read spec.md.

Spec defines what the project should do. This file defines how the coding agent should work.

Do not add major functionality that is not described in spec.md unless explicitly requested.

## 2. Project priorities

Prioritize, in order:

1. Correctness
2. Simplicity
3. Maintainability
4. Clear architecture
5. Good developer experience
6. Visual polish

This is a final-year project. Do not over-engineer it.

## 3. Technology rules

### Backend

Use:

- Go
- net/http
- SQLite
- Kubernetes Go client
- Docker client/API

Do not introduce:

- Gin
- Echo
- Fiber
- Chi
- Large backend frameworks

Prefer the Go standard library where practical.

### Frontend

Use:

- React
- TypeScript
- Vite
- TailwindCSS
- shadcn/ui

Do not replace React with Next.js.

## 4. Architecture rules

Keep the architecture simple:

React → Go API → SQLite / Docker / Kubernetes

The frontend must never communicate directly with Docker or Kubernetes. All infrastructure operations go through the Go backend.

## 5. Kubernetes rules

Kubernetes is the source of truth for runtime state.

SQLite stores Podium metadata such as users, applications, deployment history, environment configuration, and Docker image/version information.

Do not duplicate complete Kubernetes runtime state into SQLite. Do not permanently store full Pod logs in SQLite.

Use Kubernetes APIs for pods, deployments, services, events, logs, and runtime status.

Default namespaces:

- podium-dev
- podium-staging
- podium-prod

Users may also create additional Kubernetes namespaces beyond the defaults.

## 6. Authentication rules

Requirements:

- Passwords must be hashed, preferably bcrypt
- Never store plaintext passwords
- Use HTTP-only session cookies
- Check user status during login
- PENDING users cannot log in
- REJECTED users cannot log in
- DISABLED users cannot log in
- Admin endpoints require the ADMIN role
- Users can only access their own applications

Default admin credentials must come from environment variables:

- PODIUM_ADMIN_USERNAME
- PODIUM_ADMIN_PASSWORD

Never commit real credentials.

## 7. Database rules

Use SQLite. Keep the schema simple.

Core entities:

- users
- applications
- environments
- deployments
- environment_variables

Use migrations or a deterministic initialization process. Database initialization should be safe to run more than once.

Do not introduce PostgreSQL, Redis, or another database unless explicitly requested.

## 8. Docker rules

Applications are expected to provide a Dockerfile for the MVP.

The deployment flow should be:

Application → GitHub source fetch → Docker build → Image → Kubernetes deployment

Capture Docker build output so the UI can display it. Avoid arbitrary shell execution when a Docker API/client can be used.

## 9. Deployment rules

Use a simple deployment state machine:

QUEUED → BUILDING → BUILT → DEPLOYING → STARTING → RUNNING
                                         ↘ FAILED

Update the deployment record as the workflow progresses.

When a deployment fails, preserve enough metadata to allow the user to inspect build logs, Kubernetes events, and Pod logs.

Do not build an AI diagnosis system into the initial implementation.

## 10. Logs and diagnostics

Logs are a first-class feature.

The application detail page should include:

- Overview
- Deployments
- Logs
- Events
- Settings

Application logs should come from Kubernetes.
Build logs should come from the Docker build process.
Kubernetes events should come from the Kubernetes API.

Keep these sources conceptually separate.

Future AI summarization may consume these logs, but AI is not part of the MVP. Keep the log service modular enough that an AI summarizer could be added later.

## 11. API design

Use simple REST-style JSON APIs. Keep handlers thin.

Prefer:

HTTP Handler → Service → Repository / Infrastructure Client

Do not put large blocks of Kubernetes or Docker logic directly inside HTTP handlers. Validate input at the API boundary. Return appropriate HTTP status codes.

## 12. Go style

Write idiomatic Go.

Prefer:

- Small functions
- Explicit error handling
- Clear names
- Small interfaces
- Dependency injection where useful
- Standard library functionality

Avoid:

- Clever abstractions
- Excessive interfaces
- Deep package hierarchies
- Global mutable state
- Huge service files
- Premature optimization

## 13. Frontend style

Use TypeScript. Prefer reusable components. Use shadcn/ui components where appropriate.

The UI should feel like a modern developer platform:

- Clean
- Minimal
- Neutral
- Spacious
- Responsive
- Accessible

Use clear status indicators.

Provide loading, error, and empty states. Do not add excessive animations.

### Frontend implementation decisions

- Use a left sidebar for primary navigation and a top header for page context and user actions
- Use status badges and compact tables for applications, deployments, pods, and events
- Use tabs on detail pages to keep overview, logs, events, and settings in one place
- Prefer reusable shadcn/ui components for forms, dialogs, drawers, and empty states
- Keep destructive actions clearly separated and require confirmation where appropriate
- Show loading skeletons and inline operation states for builds, deploys, scaling, and restarts

## 14. Ownership and authorization

Every application belongs to a user.

Backend authorization must verify ownership before returning or mutating application resources. Never trust a frontend-provided application ID.

Normal users must not access another user's applications.

## 15. Admin features

Admin users can:

- View users
- Approve users
- Reject users
- Disable users

Normal users cannot access admin endpoints. The frontend may hide admin UI from normal users, but backend authorization is mandatory.

## 16. Environments

Use exactly these MVP environments:

- Development
- Staging
- Production

Namespaces are:

- podium-dev
- podium-staging
- podium-prod

Promotion should reuse the same Docker image. Do not rebuild an image when promoting an existing deployment.

## 17. Scope control

This is a project.

If a requested feature significantly expands scope, first determine whether it is:

1. Required MVP functionality
2. A small improvement
3. A stretch feature

Do not let stretch features delay the core deployment workflow.

The core workflow must remain functional:

Signup → Admin Approval → Login → Create Application → Deploy → Docker Build → Kubernetes Deployment → Running → Logs → Scale / Restart → Promotion → Rollback

## 18. Do not over-engineer

Avoid adding, unless explicitly requested later:

- Microservices
- Message queues
- PostgreSQL
- Redis
- Kafka
- Prometheus / Grafana
- Service mesh
- Kubernetes operators
- Complex event buses
- Complex workflow engines
- Custom schedulers
- AI agents

Podium should remain a small monolithic Go control plane.

## 19. Error handling

Errors should be useful to the developer.

Do not expose stack traces, password hashes, internal secrets, or sensitive infrastructure credentials.

Backend logs can contain technical debugging information.

## 20. Testing

Prioritize tests for important business behavior:

- Password hashing and authentication
- Pending user cannot log in
- Approved user can log in
- Admin authorization
- Application ownership
- Deployment state transitions
- Basic API handlers
- Database operations

Infrastructure integration tests can be limited because the project is intended for local Kubernetes.

## 21. Documentation

Keep the README focused on:

- What Podium is
- Architecture
- Prerequisites
- Local setup
- Starting Podium
- Starting Kubernetes
- Default admin setup
- Creating an application
- Deploying an application
- Development workflow

Do not duplicate the entire spec into the README.

## 22. File organization

Prefer a modular, industry-standard Go structure with clear separation of concerns, for example:

Podium/
├── agents.md
├── spec.md
├── README.md
├── backend/
│   ├── cmd/
│   │   └── podium/
│   │       └── main.go
│   ├── internal/
│   │   ├── auth/
│   │   ├── api/
│   │   ├── application/
│   │   ├── deployment/
│   │   ├── docker/
│   │   ├── kubernetes/
│   │   ├── logs/
│   │   └── storage/
│   ├── migrations/
│   ├── go.mod
│   └── go.sum
├── frontend/
│   ├── src/
│   ├── package.json
│   └── vite.config.ts
├── k8s/
├── Dockerfile
└── docker-compose.yml

Adjust the structure only when there is a clear reason.

## 23. Implementation workflow

When implementing a feature:

1. Read the relevant section of spec.md
2. Inspect the existing implementation
3. Make the smallest coherent change
4. Keep backend and frontend responsibilities separated
5. Run relevant tests
6. Run formatting or linting where configured
7. Verify the application still starts
8. Avoid unrelated refactors

Do not rewrite large parts of the application without a clear reason.

## 24. Definition of done

A feature is not complete merely because the code compiles.

For each feature:

- Backend behavior works
- API behavior is correct
- Frontend exposes the feature where appropriate
- Loading, error, and empty states are handled
- Authorization is enforced where required
- Data is persisted when required
- Docker and Kubernetes integration works where applicable
- Existing functionality still works

## 25. Final principle

Build Podium as a simple, coherent codebase with basic separation of concerns rather than a big monolithic implementation. The project should demonstrate that a developer can build a useful platform by combining:

Go + React + SQLite + Docker + Kubernetes

The goal is not to recreate a commercial PaaS. The goal is to create a polished, understandable miniature PaaS that can be confidently demonstrated and explained during a final-year project defense.

## Git workflow tips

Use a professional Git workflow to keep the project easy to review and maintain:

- Create short-lived feature branches such as `feature/auth-login` or `fix/k8s-deploy-status`.
- Keep commits focused on one concern at a time; avoid mixing backend, frontend, and config changes in the same commit.
- Write clear commit messages in the format: `type(scope): summary` (for example, `feat(auth): add login session validation`).
- Check `git status` and `git diff --stat` before committing to confirm the change set is intentional.
- Prefer rebasing or fast-forward merges on feature branches when the branch is not shared, but use normal PR-style merges when collaborating with others.
- Never force-push to shared branches unless the team explicitly agrees to it.
- Keep the default branch clean; do not merge work-in-progress commits without review.
- Review diffs before pushing to catch accidental debug logs, secrets, or temporary files.
- Add `.gitignore` entries for local environment files, build artifacts, editor settings, and generated data.
- If a change is risky or large, split it into smaller logical PRs instead of one large commit.
- Always pull the latest base branch before opening a PR or merging to minimize conflicts.

A good workflow for this project is:

1. Start from the latest main branch.
2. Create a feature branch.
3. Implement one clearly scoped change.
4. Run targeted validation.
5. Commit with a precise message.
6. Open a PR or share the branch for review.
7. Merge only after the diff is reviewed and the change is verified.