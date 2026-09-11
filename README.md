# Podium

A lightweight Kubernetes-based Internal Developer Platform (mini-PaaS). A
Go control plane + React dashboard that builds Docker images from GitHub
repos and deploys them to a local kind cluster.

Podium is a final-year project, not a production system. The goal is to
demonstrate a coherent mini-PaaS end-to-end: Go API + SQLite + Docker
builds + Kubernetes orchestration + per-app localhost ingress, behind a
modern React UI.

## What you can do with it

- Sign up, wait for admin approval, log in.
- Point at a public GitHub repo with a `Dockerfile`, click **Deploy**,
  watch Podium `git clone` the source, build a Docker image, load it
  into the kind cluster, and start the pods.
- See live build logs, pod status, replica counts, Kubernetes events.
- Scale 1–5 replicas, restart, roll back, promote to another namespace.
- Open the deployed app on a per-app localhost port (e.g.
  `http://127.0.0.1:40000`) — a kubectl port-forward straight into the
  pod, no port-forwarding gymnastics required.

See [spec.md §48](./spec.md) for the full §48 acceptance flow.

## Architecture

```
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

The frontend never talks to Docker or Kubernetes directly — all
infrastructure operations go through the Go backend. See
[spec.md §5](./spec.md) for the full diagram and rationale.

## Quick start

```bash
# 1. Get the source
git clone https://github.com/thelaserunicorn/podium.git
cd podium

# 2. Install Docker + kind on a fresh laptop
#    (full steps for Ubuntu / Pop!_OS in docs/popos-setup.md,
#     for macOS in docs/macos-setup.md)
sudo apt update && sudo apt install -y docker.io  # or use the apt repo
sudo usermod -aG docker $USER    # log out and back in
kind create cluster --name podium

# 3. Build and run
docker compose up --build
# Visit http://localhost:5173
# Sign in as admin / change-me-now (set PODIUM_ADMIN_PASSWORD in .env first)
```

That's the whole demo: two containers (Go API + nginx serving the React
bundle), one `kind` cluster, three mount points. SQLite persists in the
`podium-data` named volume.

For step-by-step instructions tailored to your OS:

- **Pop!_OS / Ubuntu** — [docs/popos-setup.md](./docs/popos-setup.md)
- **macOS** — [docs/macos-setup.md](./docs/macos-setup.md) (TODO)
- **Generic Linux** — [docs/ubuntu-setup.md](./docs/ubuntu-setup.md)
- **Restart-from-cold checklist** — [docs/restart-checklist.md](./docs/restart-checklist.md) (agent-facing runbook for "it's been a while, just get it running")
- **M7 next-steps handoff** — [docs/m7-next-steps.md](./docs/m7-next-steps.md) (the deferred smoke-script work, ready for a future agent)

## Demo script

The full §48 walkthrough takes ~5 minutes:

1. Open <http://localhost:5173/signup>, create user `alice`.
2. In a private window, log in as `admin` → Admin dashboard → Approve `alice`.
3. Log in as `alice` → Applications → New application:
   - name: `hello`
   - repository_url: `https://github.com/thelaserunicorn/podium-hello`
   - container_port: `80`
4. Click **Deploy**, pick a namespace (e.g. `podium-dev`), confirm.
5. Watch the deployment row transition
   `QUEUED → BUILDING → BUILT → DEPLOYING → STARTING → RUNNING`.
6. On the Overview tab, click **Open** on the App URL card. A new tab
   opens at `http://127.0.0.1:40000` served by a `kubectl port-forward`
   straight into the running Pod. Refresh a few times after scaling to
   >1 replica to see the pod hostname rotate.

The `podium-hello` demo app is intentionally tiny — static HTML/CSS/JS
on `nginx:alpine` with a `/hostname` endpoint that prints the pod
hostname. Source: <https://github.com/thelaserunicorn/podium-hello>.

## Environment variables

| Variable | Default | Used by |
|---|---|---|
| `PODIUM_ADMIN_USERNAME` | `admin` | seeded admin username on first boot |
| `PODIUM_ADMIN_PASSWORD` | `change-me-now` | seeded admin password on first boot |
| `PODIUM_ADDR` | `:8080` | Go API listen address (host networking) |
| `PODIUM_DB_PATH` | `/data/podium.db` | SQLite file inside the container |
| `PODIUM_SOURCE_ROOT` | `/data/sources` | where `git clone` writes cloned repos |
| `KUBECONFIG` | `/home/nonroot/.kube/config` | bind-mounted from `~/.kube/config` |

For local development outside Docker, see
[docs/ubuntu-setup.md §Option A](./docs/ubuntu-setup.md#option-a--run-from-source-recommended-while-developing).

## Project layout

```
podium/
├── backend/
│   ├── cmd/podium/main.go              # binary entry point
│   ├── internal/
│   │   ├── auth/                       # signup, login, sessions, admin actions
│   │   ├── api/                        # HTTP handlers, middleware, routing
│   │   ├── application/                # app CRUD + namespace-aware k8s cleanup
│   │   ├── deployment/                 # orchestrator + state machine
│   │   ├── docker/                     # docker build, git clone, kind load
│   │   ├── kubernetes/                 # client, deployments, services, envvars
│   │   ├── logs/                       # pod logs, k8s events, build logs
│   │   ├── storage/                    # SQLite + queries
│   │   └── ingress/                    # kubectl port-forward router
│   ├── migrations/                     # SQL migrations (embedded)
│   └── go.mod
├── frontend/
│   ├── src/                            # React + Vite
│   ├── Dockerfile                      # multi-stage: node:20 → nginx:alpine
│   └── nginx.conf                      # SPA fallback + /api proxy
├── docs/
│   ├── ubuntu-setup.md                 # generic Ubuntu runbook
│   ├── popos-setup.md                  # Pop!_OS step-by-step
│   └── macos-setup.md                  # TODO
├── scripts/
│   └── smoke.sh                        # per-milestone API surface tests
├── Dockerfile                          # multi-stage: golang:1.26 → debian-slim
├── docker-compose.yml                  # podium + frontend services
├── .env.example                        # template for admin credentials
├── AGENTS.md                           # how to work on this codebase
├── DECISIONS.md                        # locked decisions overriding spec/AGENTS
├── PLAN.md                             # milestone-by-milestone build plan
├── PUKU.md                             # AI-assistant guidance
└── spec.md                             # product spec (source of truth)
```

## Development workflow

```bash
# Backend — from backend/
gofmt -l .                    # formatting check (no output = clean)
go vet ./...                  # static analysis
go test ./...                 # all tests
go build ./cmd/podium         # build the binary

# Frontend — from frontend/
npm install                   # first time only
npm run dev                   # Vite dev server on :5173, proxies /api to :8080
npm run build                 # tsc + Vite production build → dist/
npm run typecheck             # tsc --noEmit
npx prettier --check .        # formatting check
```

See [AGENTS.md §23](./AGENTS.md) for the slice-by-slice workflow.

## Documentation map

- [spec.md](./spec.md) — what Podium should do (source of truth)
- [AGENTS.md](./AGENTS.md) — how the codebase is organized, style, Git
  workflow
- [DECISIONS.md](./DECISIONS.md) — locked decisions that override spec
  and AGENTS when they disagree
- [PLAN.md](./PLAN.md) — milestone-by-milestone build plan
- [docs/ubuntu-setup.md](./docs/ubuntu-setup.md) — generic Ubuntu
  runbook
- [docs/popos-setup.md](./docs/popos-setup.md) — Pop!_OS step-by-step
- [docs/restart-checklist.md](./docs/restart-checklist.md) — agent-facing
  restart-from-cold runbook
- [PUKU.md](./PUKU.md) — AI-assistant guidance

## Out of scope (deliberately)

- OAuth / GitHub / GitLab authentication
- Multi-tenancy, billing, complex RBAC
- Production-grade high availability
- Multi-cluster management
- Cloud-provider provisioning
- Full CI/CD pipelines, Git webhooks
- Custom Kubernetes operators
- Service mesh, Prometheus / Grafana
- AI log summarization (stretch feature)

See [spec.md §3](./spec.md) for the full non-goals list.

## License

Final-year academic project. No license file — treat as
all-rights-reserved unless explicitly granted otherwise.
