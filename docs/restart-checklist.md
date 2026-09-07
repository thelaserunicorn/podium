# Podium — restart-from-cold checklist

A copy-paste runbook for bringing Podium back up after a Mac restart,
or any time the cluster / containers / kind state has gone away. Optimised
for "it's been two weeks since I last ran this and I forgot everything".

This is an **agent-facing** document: the assumption is the reader is
an AI coding assistant or a developer who just wants the commands in
order, with no narrative.

**Time to a running demo: ~5 minutes** (most of it waiting for Docker
to start and `docker compose up` to spin the containers).

## 0. Quick health check — do I even need to restart anything?

Before doing anything destructive, check what's already running:

```bash
docker ps --format 'table {{.Names}}\t{{.Status}}\t{{.Ports}}'
kubectl --context kind-podium get nodes 2>&1 | head -5
```

If you see `podium`, `podium-frontend`, and "kind-podium control-plane
Ready", **skip to step 5** — everything is already up.

## 1. Is Docker running?

Docker Desktop doesn't auto-start on macOS after a reboot unless you
opted in to "Start Docker Desktop when you sign in". Open Docker
Desktop and wait for the whale icon to settle. Or:

```bash
# OrbStack (preferred on Apple Silicon)
orbctl start 2>&1 || open -a OrbStack
docker info >/dev/null 2>&1 && echo "Docker OK" || echo "Docker NOT running"
```

## 2. Is the kind cluster still around?

```bash
kind get clusters
```

If the output is empty (or doesn't list `podium`), recreate it:

```bash
kind create cluster --name podium
kubectl cluster-info --context kind-podium
```

If the cluster exists, just make sure it's reachable:

```bash
kubectl get nodes --context kind-podium
```

`control-plane   Ready` is what you want.

## 3. Is the Podium stack running?

```bash
docker ps --filter name=podium --format 'table {{.Names}}\t{{.Status}}'
```

If `podium` and `podium-frontend` are present and `Up`, skip to step 5.

If absent, `cd` to the Podium checkout and bring it up:

```bash
cd ~/code/podium    # or wherever you cloned it
docker compose up -d
docker compose ps
```

Both services should report `running` (or `Up`). Wait for the health
check:

```bash
sleep 3
curl -s -o /dev/null -w "Backend :8080 healthz : %{http_code}\n" http://localhost:8080/healthz
curl -s -o /dev/null -w "Frontend :5173 /       : %{http_code}\n" http://localhost:5173/
curl -s -o /dev/null -w "Frontend :5173 /api/*  : %{http_code}\n" http://localhost:5173/api/auth/me
```

Expected: `200`, `200`, `401`. The `401` proves nginx is proxying to
the Go backend through `host.docker.internal`.

## 4. Common "something is wrong" cases

### Frontend container keeps restarting

```bash
docker compose logs frontend --tail 20
```

If the log shows `host not found in upstream "podium"`, the nginx config
predates the `host.docker.internal` fix. Pull the latest and rebuild:

```bash
git pull
docker compose build frontend
docker compose up -d frontend
```

### Backend container keeps restarting

```bash
docker compose logs podium --tail 30
```

If the log shows `WARN kubernetes client unavailable; ...` at startup,
Podium booted before the kind cluster was reachable. Restart it:

```bash
docker compose restart podium
```

### Podium can't build images / `kind load` fails

```bash
docker exec podium ls -la /var/run/docker.sock
```

If you get "No such file or directory" the socket bind-mount is gone
(Mac users sometimes see this after Docker Desktop updates). Bring the
stack down and up:

```bash
docker compose down
docker compose up -d
```

### SQLite is locked

```bash
docker exec podium sh -c 'ls -la /data/'
```

If you see `podium.db-journal` or `podium.db-wal` and the container
is crashlooping, you likely have two Podium processes pointing at the
same volume (e.g. an old `docker run` from before you switched to
compose). Find and kill them:

```bash
docker ps -a --filter ancestor=podium:dev
docker stop <container-id>
```

### Frontend returns "couldn't reach the app"

This is a deployment issue, not an infra issue. See
[docs/popos-setup.md §10](./popos-setup.md) for the full §48 walkthrough
and the "App URL card" debugging checklist.

## 5. Open the dashboard

```bash
open http://localhost:5173
```

Sign in with `admin` and the password from your `.env`. If you don't
have one set, copy the example and edit it:

```bash
[ -f .env ] || cp .env.example .env
$EDITOR .env        # set PODIUM_ADMIN_PASSWORD to something real
docker compose restart podium
```

## 6. Where state lives

| Where                                       | What                      | Lost on reboot?                            |
| ------------------------------------------- | ------------------------- | ------------------------------------------ |
| `~/.kube/config`                            | kind cluster credentials  | No (file on disk)                          |
| `podium_podium-data` Docker volume          | SQLite + cloned sources   | No (named volume persists across restarts) |
| `/var/run/docker.sock`                      | host Docker daemon socket | No (Docker-managed)                        |
| `kind` Docker network                       | cluster node containers   | No (managed by Docker)                     |
| `podium:dev` + `podium-frontend:dev` images | built Podium images       | No (in Docker's image cache)               |

Everything persists across reboots. The only thing that doesn't persist
is the running containers themselves — `docker compose up -d` after a
restart is enough; you don't need to rebuild.

## 7. Tear it all down (when you're done for the day)

```bash
docker compose down          # stop containers, keep volume + images
kind delete cluster --name podium   # only if you also want to nuke the cluster
```

To wipe everything (cluster + Podium state + images):

```bash
kind delete cluster --name podium
docker compose down --volumes
docker image rm podium:dev podium-frontend:dev
```

## 8. If you need to rebuild from source

Only needed if you changed code. The Dockerfile handles the rebuild:

```bash
cd ~/code/podium
git pull
docker compose build
docker compose up -d
```

Source-only edits don't need a `git pull`; the bind mount is into the
container, not the source.

## Reference

- [README.md](../README.md) — project overview
- [docs/popos-setup.md](./popos-setup.md) — first-time setup walkthrough
- [docs/ubuntu-setup.md](./ubuntu-setup.md) — generic Linux reference
- [DECISIONS.md](../DECISIONS.md) — why the architecture is the way it is
