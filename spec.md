# Podium — Project Specification

## 1. Overview

Podium is a lightweight Internal Developer Platform (IDP) / mini Platform-as-a-Service (PaaS).

It provides a web dashboard through which approved users can create, build, deploy, inspect, scale, restart, and roll back containerized applications running on Kubernetes.

For example, a user provides a GitHub repository URL; Podium fetches the application's source, reads the Dockerfile from that repository, builds the image, and deploys it to Kubernetes.

Podium intentionally abstracts common Docker and Kubernetes operations behind a simple web interface.

The project is for a final project and designed to be implementable in approximately one week. It is **not** intended to be production-grade but rather self hostable.

### Core workflow

```text
User
  ↓
React Dashboard
  ↓
Go API / Control Plane
  ├── SQLite
  ├── Docker
  └── Kubernetes
```

## 2. Goals

### Primary goals

- Build the backend in Go.
- Build a modern frontend using React, TypeScript, Vite, TailwindCSS, and shadcn/ui.
- Provide basic authentication.
- a default admin account
- Require admin approval before newly registered users can log in.
- Allow users to create and manage applications.
- Build Docker images from application source repositories.
- Deploy applications to Kubernetes.
- Support Development, Staging, and Production environments using Kubernetes namespaces.
- Display deployment progress and status.
- Display Docker build logs.
- Display Kubernetes events for failed deployments.
- Display application/pod logs through a dedicated Logs tab.
- Support scaling, restarting, and rollback.
- Persist Podium control-plane metadata in SQLite.
- Keep the implementation simple enough for a one-week project.

### Secondary goals

- Demonstrate practical Go backend development.
- Demonstrate Docker containerization.
- Demonstrate Kubernetes orchestration.
- Demonstrate namespaces and environment separation.
- Demonstrate deployment lifecycle management.
- Provide a polished final-project presentation.

## 3. Non-Goals

The following are explicitly out of scope for the MVP:

- OAuth
- GitHub/GitLab authentication
- Multi-factor authentication
- Multi-tenancy
- Billing / payments
- Complex RBAC
- Advanced secrets management
- Production-grade high availability
- Distributed control plane
- Multi-cluster management
- Cloud-provider provisioning
- Full CI/CD pipelines
- Git webhooks
- Custom Kubernetes operators
- Service mesh
- Prometheus / Grafana
- Advanced autoscaling
- Automatic DNS provisioning
- Automatic TLS provisioning
- Complex networking
- AI log summarization (may be added later as a stretch feature)

## 4. Technology Stack

### Frontend

- React
- TypeScript
- Vite
- TailwindCSS
- shadcn/ui

Do **not** use Next.js.

The frontend communicates with the Go backend through HTTP/JSON APIs.

### Backend

- Go
- `net/http`
- Go standard library where practical
- SQLite
- Kubernetes Go client
- Docker client/API

Do **not** introduce a large backend framework. Do not use Gin, Echo, Fiber, or Chi — use Go's standard HTTP server and routing.

### Infrastructure

- Docker
- Docker Compose
- Kubernetes

Local Kubernetes may use kind, minikube, or Docker Desktop Kubernetes. The application should avoid depending on a specific local Kubernetes distribution where practical.

## 5. High-Level Architecture

```text
                         ┌──────────────────┐
                         │     Browser      │
                         └────────┬─────────┘
                                  │
                                  ▼
                    ┌──────────────────────────┐
                    │ React + Vite + TypeScript │
                    │ TailwindCSS + shadcn/ui   │
                    └────────────┬─────────────┘
                                 │ HTTP/JSON
                                 ▼
                    ┌──────────────────────────┐
                    │       Podium API         │
                    │            Go            │
                    │       Control Plane      │
                    └─────┬────────┬───────────┘
                          │        │
                    ┌─────┘        └────────────┐
                    ▼                            ▼
              ┌────────────┐             ┌───────────────┐
              │   SQLite   │             │   Kubernetes   │
              │            │             │                │
              │ Users      │             │ Namespaces     │
              │ Apps       │             │ Deployments    │
              │ Deployments│             │ Services       │
              │ Environments│            │ Pods           │
              │ Config     │             │ Events / Logs  │
              └────────────┘             └───────┬────────┘
                                                  ▲
                                                  │
                                           ┌──────┴──────┐
                                           │   Docker    │
                                           │    Engine   │
                                           └─────────────┘
```

The frontend must never communicate directly with Docker or Kubernetes. The Go backend is the only component that controls Docker and Kubernetes.

## 6. Authentication

Podium has two user roles: **ADMIN** and **USER**.

### Default administrator

On first application startup, Podium must ensure that a default administrator exists. Credentials must be configurable through environment variables:

```text
PODIUM_ADMIN_USERNAME=admin
PODIUM_ADMIN_PASSWORD=<configured-password>
```

The password must be stored as a secure password hash, preferably bcrypt. Do not store plaintext passwords.

### Registration

A new user can register with: username, email, password.

A newly registered account starts with status **PENDING**. A pending user cannot log in.

### Admin approval

Administrators can view pending accounts and: Approve, Reject, Disable. Only an approved user may authenticate successfully.

### User status

Supported statuses: `PENDING`, `APPROVED`, `REJECTED`, `DISABLED`.

### Sessions

Use simple server-side sessions with a secure HTTP-only cookie. JWT is not required.

## 7. Authentication Flow

```text
Signup → User created → PENDING → Admin Dashboard → Approve
   → APPROVED → User can Login → Session Cookie → Dashboard
```

Login must check:

1. User exists.
2. Password is correct.
3. User status is APPROVED.

PENDING, REJECTED, and DISABLED users must be denied access.

## 8. User Management

Admin dashboard must provide a basic user management page.

| Username | Email | Status |
|---|---|---|
| john | john@example.com | ● Pending |
| sarah | sarah@example.com | ● Approved |
| bob | bob@example.com | ● Disabled |

Actions: `[ Approve ]` `[ Reject ]` `[ Disable ]`

No complex permissions system is required.

## 9. Applications

An application represents a deployable project.

Minimum fields: `id`, `name`, `repository_url`, `container_port`, `created_at`, `updated_at`.

Users should be able to create, view, edit, delete, and list applications. Applications should be associated with the user who created them.

## 10. Environments

Podium supports: Development, Staging, Production.

Kubernetes namespaces: `podium-dev`, `podium-staging`, `podium-prod`. Podium should ensure these namespaces exist. Each deployment must specify an environment.

Users should also be able to create additional Kubernetes namespaces beyond the default Podium environments when needed for custom workflows or experimentation.

## 11. Deployments

A deployment represents one deployment attempt for an application/environment.

Minimum fields: `id`, `application_id`, `environment_id`, `version`, `image`, `replicas`, `status`, `created_at`, `started_at`, `finished_at`.

Deployment statuses: `QUEUED`, `BUILDING`, `BUILT`, `DEPLOYING`, `STARTING`, `RUNNING`, `FAILED`.

## 12. Deployment Workflow

```text
User clicks Deploy → Create deployment record → QUEUED
  → Build Docker image → BUILDING → Image created → BUILT
  → Create/update Kubernetes Deployment → DEPLOYING
  → Wait for application Pods → STARTING
  → Pods become Ready → RUNNING
```

Failure at any relevant stage should result in `FAILED`. The deployment detail page must expose enough information to understand where the failure occurred.

## 13. Docker

For the MVP, applications are expected to contain a valid Dockerfile. Podium should:

1. Obtain the application source.
2. Build a Docker image.
3. Tag the image (e.g. `my-api:v42`).
4. Make the image available to Kubernetes.
5. Deploy the image.

The exact source acquisition mechanism may be simplified for the MVP. The implementation should prioritize a working local demonstration over supporting every Git provider.

## 14. Docker Build Logs

Podium must capture Docker build output, e.g.:

```text
[10:42:01] Building image...
[10:42:02] FROM node:22
[10:42:04] Installing dependencies...
[10:42:15] Copying application...
[10:42:17] Building application...
[10:42:21] Successfully built image
```

On failure:

```text
[10:42:01] Building image...
[10:42:03] Installing dependencies...
[10:42:10] ERROR: build failed
```

The user must be able to inspect these logs from the deployment detail page.

## 15. Kubernetes

Podium should use the Kubernetes API through the Go Kubernetes client and should manage: Namespaces, Deployments, Services, Pods, ConfigMaps, and Secrets where required. The Kubernetes cluster is the source of truth for runtime state.

## 16. Kubernetes Deployment

For each application/environment, Podium creates or updates a Kubernetes Deployment, configuring: application image, container port, replica count, environment variables.

Example: namespace `podium-dev`, deployment `my-api`, replicas `2`, image `my-api:v42`.

## 17. Kubernetes Service

Each deployed application should have a Kubernetes Service exposing the configured application port internally. External ingress/DNS is optional and out of scope for the MVP.

## 18. Pods

The application detail page must display Pods belonging to the application, showing: pod name, status, restart count, created time.

```text
Pods
my-api-7f8d9c-x1    Running    Restarts: 0
my-api-7f8d9c-x2    Running    Restarts: 0
```

Users should be able to view logs for a selected Pod. Deleting/restarting Pods may be implemented by deleting the Pod and allowing the Deployment to recreate it.

## 19. Scaling

Users can change the replica count via a slider or input (e.g. 1–5, defaulting to 3), then `[ Scale ]`. The Go backend updates the Kubernetes Deployment. The frontend should show current and desired replicas, e.g. `3 / 3 replicas`.

## 20. Restart

Provide a Restart action for an application. A simple implementation may delete the application's Pods and allow Kubernetes to recreate them. The UI should show `Restarting...` followed by `Running` / `3 / 3 replicas`.

## 21. Deployment History

Every deployment attempt must be persisted in SQLite.

```text
Deployment History
#42   v42   Production    ● Running
#41   v41   Production    ✓ Successful
#40   v40   Production    ✕ Failed
```

Each deployment should have a detail page.

## 22. Rollback

Users should be able to roll back to a previous successful deployment. Rollback should reuse the previous Docker image rather than rebuilding it.

```text
Current:  my-api:v42
Previous: my-api:v41
[ Roll Back to v41 ]
```

## 23. Environment Promotion

Podium should support promotion: Development → Staging → Production. Promotion should reuse the same Docker image (e.g. `my-api:v42` deployed to `podium-dev`, `podium-staging`, `podium-prod`) without rebuilding. This demonstrates immutable deployment concepts.

## 24. Environment Variables

Applications may define environment variables per environment, e.g. (Development):

```text
NODE_ENV=development
PORT=3000
DATABASE_URL=...
```

Environment variables must be associated with an Application and an Environment. Normal configuration may use Kubernetes ConfigMaps; sensitive values may use Kubernetes Secrets. Advanced secrets management is out of scope.

## 25. Logs

Every application/resource must have a dedicated Logs tab.

Navigation: `Overview | Deployments | Logs | Events | Settings`

The Logs tab should allow the user to select a Pod, view its logs, refresh logs, and see recent log output. Logs should be retrieved from Kubernetes. Do not permanently store full application logs in SQLite.

## 26. Deployment Diagnostics

When a deployment fails, Podium should expose: deployment status, Docker build logs, Kubernetes events, and Pod logs where available. Podium should **not** attempt sophisticated automatic diagnosis — it should surface the information Kubernetes and Docker already provide (build result, deployment creation, pod status, and relevant events such as a readiness probe failure).

## 27. Kubernetes Events

The deployment detail page must display relevant Kubernetes events, e.g.:

```text
10:42:01  Normal   Scheduled
10:42:02  Normal   Pulled
10:42:03  Normal   Created
10:42:04  Normal   Started
10:42:15  Warning  Unhealthy
```

These events are intended to help users understand deployment failures.

## 28. Future AI Log Summarization

AI log summarization is explicitly a future feature. Do not implement it in the MVP. The backend should keep log retrieval sufficiently modular so that an AI summarizer can be added later:

```text
Kubernetes → Log Service ──→ Logs UI
                          └──→ Future AI Summarizer
```

The MVP only needs the log retrieval portion.

## 29. SQLite Data Model

**users**: id, username, email, password_hash, role, status, created_at, updated_at

**applications**: id, user_id, name, repository_url, container_port, created_at, updated_at

**environments**: id, name, namespace, created_at

**deployments**: id, application_id, environment_id, version, image, replicas, status, created_at, started_at, finished_at

**environment_variables**: id, application_id, environment_id, key, value, is_secret

Optional metadata tables may be added if required by the implementation.

## 30. SQLite Responsibilities

SQLite is responsible for: users, authentication metadata, applications, application ownership, environments, deployment history, Docker image/version metadata, environment configuration.

SQLite is **not** the source of truth for: Pod state, deployment runtime state, Services, Kubernetes events, application logs, CPU/memory metrics. Kubernetes remains the source of truth for runtime state.

## 31. Source of Truth

```text
SQLite     → What Podium remembers
Kubernetes → What is actually running
```

Example: SQLite says "Deployment #42, Image: my-api:v42"; Kubernetes says "Deployment my-api, Pods: 2/2 Running." The Go backend should query Kubernetes when displaying current runtime information.

## 32. Backend Structure

```text
backend/
├── cmd/
│   └── podium/
│       └── main.go
├── internal/
│   ├── auth/
│   ├── api/
│   ├── application/
│   ├── deployment/
│   ├── docker/
│   ├── kubernetes/
│   ├── logs/
│   └── storage/
├── migrations/
├── go.mod
└── go.sum
```

Keep packages small. Do not create packages solely for abstraction's sake. Business logic should not be embedded directly in HTTP handlers.

## 33. HTTP API

**Authentication**
```text
POST /api/auth/signup
POST /api/auth/login
POST /api/auth/logout
GET  /api/auth/me
```

**Admin**
```text
GET  /api/admin/users
POST /api/admin/users/{id}/approve
POST /api/admin/users/{id}/reject
POST /api/admin/users/{id}/disable
```

**Applications**
```text
GET    /api/applications
POST   /api/applications
GET    /api/applications/{id}
PUT    /api/applications/{id}
DELETE /api/applications/{id}
```

**Deployments**
```text
GET  /api/applications/{id}/deployments
POST /api/applications/{id}/deploy
GET  /api/deployments/{id}
POST /api/deployments/{id}/rollback
```

**Operations**
```text
POST /api/applications/{id}/scale
POST /api/applications/{id}/restart
```

**Logs/events**
```text
GET /api/applications/{id}/logs
GET /api/deployments/{id}/logs
GET /api/deployments/{id}/events
```

**Environment variables**
```text
GET    /api/applications/{id}/env
POST   /api/applications/{id}/env
DELETE /api/applications/{id}/env/{key}
```

Exact endpoint naming may be adjusted during implementation if the responsibilities remain equivalent.

## 34. Authorization

Basic authorization is required.

**Admin-only:** manage users, approve/reject/disable users, view/manage platform-wide resources where appropriate.

**Normal users:** manage their own applications, deploy their own applications, view their own logs, scale/restart their own applications, manage their own environment configuration.

Users must not be able to access another user's applications by changing an ID in the URL.

## 35. Frontend Pages

```text
/login
/signup
/
/dashboard
/apps
/apps/new
/apps/:id
/apps/:id/deployments
/apps/:id/deployments/:deploymentId
/apps/:id/logs
/apps/:id/settings
/admin/users
```

The UI should hide admin navigation from normal users. Backend authorization must still enforce the permission; frontend hiding alone is not sufficient.

## 36. Frontend Design

Use React, TypeScript, TailwindCSS, shadcn/ui.

Design direction: modern, minimal, developer-focused, clean typography, generous whitespace, subtle borders, neutral surfaces, clear status indicators, minimal animations, responsive layout.

The visual language can be inspired by modern developer tools and Anthropic-style interfaces without copying proprietary UI.

### Frontend implementation decisions

- Use a left sidebar for primary navigation and a top header for page context and user actions.
- Use status badges and compact tables for applications, deployments, pods, and events.
- Use tabs on detail pages to keep overview, logs, events, and settings in one place.
- Prefer reusable shadcn/ui components for forms, dialogs, drawers, and empty states.
- Keep destructive actions clearly separated and require confirmation where appropriate.
- Show loading skeletons and inline operation states for builds, deploys, scaling, and restarts.

## 37. Dashboard

Summary counts (e.g. Applications: 4, Running: 3, Deploying: 1, Failed: 0) and a list of applications with name, status, environment, and replica count, e.g. `my-api ● Running Production 3/3 replicas`.

## 38. Application Detail Page

Shows app name and status, an environment switcher (Development | Staging | Production), and tabs: Overview, Deployments, Logs, Events, Settings. The page should expose operational information without requiring the user to use `kubectl`.

The UI should also allow users to view the YAML for managed Kubernetes resources and edit supported resource manifests when appropriate, so they can inspect and adjust the underlying configuration without leaving Podium.

## 39. Dockerization

Podium itself must be Dockerized. Provide a `Dockerfile` and `docker-compose.yml`. The application should be startable with `docker compose up`. SQLite should be stored on a persistent volume `podium-data:/data`, database path `/data/podium.db`.

## 40. Kubernetes Manifests

Provide basic manifests for running Podium itself if desired:

```text
k8s/
├── namespace.yaml
├── deployment.yaml
├── service.yaml
└── configmap.yaml
```

The primary local demonstration may run Podium using Docker Compose while Podium manages applications in a local Kubernetes cluster.

## 41. Security Requirements

Even though Podium is not production-grade, implement basic security correctly:

- Passwords must be hashed with bcrypt.
- Sessions must use HTTP-only cookies.
- Authentication endpoints must validate input.
- Application ownership must be enforced.
- Admin endpoints must require admin role.
- User status must be checked during login.
- Do not expose password hashes through APIs.
- Do not expose secret environment variable values unnecessarily.
- Avoid arbitrary shell execution.
- Validate application names, ports, replica counts, and URLs.
- Do not allow the frontend to directly control Docker/Kubernetes.

Production-grade security hardening is out of scope.

## 42. Error Handling

Return appropriate HTTP status codes and JSON error responses, e.g. `{ "error": "deployment_failed" }`. The frontend should show useful human-readable messages. Do not expose Go stack traces to users; technical debugging information may be logged by the backend.

## 43. Loading and Async States

The frontend must clearly represent long-running operations: Building..., Deploying..., Waiting for Pods..., Scaling..., Restarting... Disable duplicate actions while an operation is in progress where appropriate.

## 44. Empty States

Example: "No applications yet. Create your first application and deploy it to your Kubernetes cluster. `[ Create Application ]`"

The same principle should apply to: no deployments, no logs, no pending users, no Pods.

## 45. MVP Feature Checklist

**Authentication**
- [ ] Signup
- [ ] Login
- [ ] Logout
- [ ] Session management
- [ ] Password hashing
- [ ] Default admin
- [ ] Admin approval
- [ ] Admin rejection
- [ ] User disabling
- [ ] Admin/user roles

**Applications**
- [ ] Create
- [ ] Read/list
- [ ] Update
- [ ] Delete
- [ ] Application ownership

**Docker**
- [ ] Docker image build
- [ ] Image tagging
- [ ] Build status
- [ ] Build logs
- [ ] Build failure handling

**Kubernetes**
- [ ] Namespace management
- [ ] Development environment
- [ ] Staging environment
- [ ] Production environment
- [ ] Deployment creation
- [ ] Service creation
- [ ] Pod listing
- [ ] Pod status
- [ ] Pod restart
- [ ] Replica scaling
- [ ] Kubernetes events

**Deployments**
- [ ] Deployment lifecycle
- [ ] Deployment history
- [ ] Deployment details
- [ ] Failure state
- [ ] Rollback
- [ ] Environment promotion

**Logs**
- [ ] Resource Logs tab
- [ ] Pod selection
- [ ] Pod logs
- [ ] Build logs
- [ ] Deployment events
- [ ] Refresh logs

**Configuration**
- [ ] Environment variables
- [ ] Per-environment configuration
- [ ] ConfigMap support
- [ ] Basic Secret support

**Frontend**
- [ ] Login
- [ ] Signup
- [ ] Dashboard
- [ ] Application list
- [ ] Application creation
- [ ] Application details
- [ ] Deployment history
- [ ] Deployment details
- [ ] Logs
- [ ] Settings
- [ ] Admin user management

## 46. Stretch Features

Only implement these after the MVP is complete:

- Live deployment log streaming (Server-Sent Events / WebSockets)
- CPU/memory usage
- Health checks (readiness/liveness probes)
- Ingress / application URLs
- Deployment diff / YAML viewer
- Search/filtering
- Dark mode
- AI log summarization
- AI deployment failure explanation

AI should consume logs/events retrieved by Podium rather than directly accessing Kubernetes.

## 47. Implementation Plan

| Checkpoints | Focus |
|---|---|
| 1 — Foundation | Go server, React/Vite, Tailwind, shadcn/ui, SQLite, authentication, application CRUD, dashboard |
| 2 — Docker | Docker integration, image building, tagging, build status, build logs |
| 3 — Kubernetes | Kubernetes client, namespace management, deployment creation, service creation, pod listing, runtime status |
| 4 — Deployment Management | Deployment lifecycle, deployment history, scaling, restart, environment variables |
| 5 — Diagnostics | Pod logs, deployment logs, Kubernetes events, failure states, deployment details |
| 6 — Environments | Development/Staging/Production, promotion, rollback, admin user management, UI polish |
| 7 — Finalization | Docker Compose, Kubernetes manifests, testing, demo application, error handling, README, architecture diagram, presentation prep |

## 48. Acceptance Criteria

The following workflow must work:

1. Start Podium and open the web dashboard.
2. Default admin account exists.
3. New user can sign up and is marked PENDING.
4. Pending user cannot log in.
5. Admin can approve the user; approved user can log in.
6. User can create and configure an application, selecting Development.
7. User can deploy it: Podium builds a Docker image (build logs visible), creates the Kubernetes Deployment and Service, and Kubernetes starts the Pod.
8. Dashboard shows the application as Running.
9. User can view Pods and application logs.
10. User can scale from 1 to 3 replicas; Kubernetes creates additional Pods.
11. User can restart the application and view deployment history.
12. User can intentionally deploy a broken application; Podium reports deployment failure and the user can inspect build logs, Kubernetes events, and Pod logs.
13. User can deploy a fixed version.
14. User can promote an image from Development to Staging, then to Production.
15. User can roll back to a previous deployment.

If this workflow works reliably, the MVP is considered complete.

## 49. Final Project Positioning

Podium should be presented as:

> A lightweight Kubernetes-based Internal Developer Platform that provides approved developers with a web interface for container building, application deployment, environment management, scaling, rollback, and operational log inspection.

The project demonstrates: Go backend development, REST APIs, authentication and authorization, Docker containerization and image lifecycle, Kubernetes orchestration (namespaces, deployments, services, pods), configuration management, environment promotion, rollbacks, operational logs, Kubernetes events, and React frontend development.

## 50. Guiding Principle

Keep the implementation simple underneath while making the developer experience polished on top. Do not introduce complexity merely to make the project appear sophisticated.

```text
Simple React frontend
        ↓
Simple Go control plane
        ↓
SQLite + Docker + Kubernetes
```

The main engineering challenge should be integrating these technologies cleanly, not building unnecessary infrastructure.
