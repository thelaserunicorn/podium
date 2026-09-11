# Podium on Pop!_OS with kind

Step-by-step runbook for running Podium end-to-end on a fresh Pop!_OS
(System76) or Ubuntu laptop. Tested against Pop!_OS 22.04 LTS on x86_64.
Works on any Debian-derived distro with the same `apt` commands.

Total time: **~30 minutes** on a fresh machine, mostly waiting for Docker
images to download.

## What you'll have at the end

- A `kind` cluster named `podium` running locally
- Podium control plane + dashboard reachable on <http://localhost:5173>
- An admin account (`admin` / whatever you set in `.env`)
- The full §48 walkthrough — signup → approve → deploy → open app URL —
  working against a real public GitHub repo

If anything in this doc goes wrong, jump to [Common gotchas](#common-gotchas)
at the bottom. Most issues are docker-socket permissions or kind
containerd mismatches.

## 1. Open a terminal and update the system

```bash
sudo apt update
sudo apt upgrade -y
```

## 2. Install Docker Engine (apt repo, not the snap)

The Pop!_OS snap package of Docker is missing kernel features kind
needs (notably `mount`). Use the apt repo.

```bash
sudo apt install -y ca-certificates curl gnupg
sudo install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg | \
  sudo gpg --dearmor -o /etc/apt/keyrings/docker.gpg
sudo chmod a+r /etc/apt/keyrings/docker.gpg

# Pop!_OS reports VERSION_CODENAME as "jammy" (22.04) — same as Ubuntu.
echo \
  "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] \
  https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "$VERSION_CODENAME") stable" | \
  sudo tee /etc/apt/sources.list.d/docker.list > /dev/null

sudo apt update
sudo apt install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
```

## 3. Add yourself to the docker group

Without this, every `docker` command needs `sudo`, and Podium's
container can't talk to the host daemon either.

```bash
sudo usermod -aG docker $USER
```

**Log out and back in** (or `newgrp docker` in your current shell).
Verify it worked:

```bash
docker version
# The "Server" section should show a version, not "permission denied".
```

## 4. Install kubectl and kind

```bash
# kubectl
curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl"
sudo install -o root -g root -m 0755 kubectl /usr/local/bin/kubectl
kubectl version --client

# kind (v0.33.0 — matches the version pinned in Dockerfile)
curl -Lo ./kind https://kind.sigs.k8s.io/dl/v0.33.0/kind-linux-amd64
chmod +x ./kind
sudo mv ./kind /usr/local/bin/kind
kind version
```

Git is almost certainly already installed on Pop!_OS. Skip otherwise.

## 5. Create the kind cluster

```bash
kind create cluster --name podium
kubectl cluster-info
```

The default cluster config is fine. The control-plane URL should print
on success.

## 6. Clone Podium

```bash
cd ~
git clone https://github.com/thelaserunicorn/podium.git
cd podium
```

## 7. Set your admin password (security hygiene)

```bash
cp .env.example .env
nano .env        # change PODIUM_ADMIN_PASSWORD to something real
```

```env
PODIUM_ADMIN_USERNAME=admin
PODIUM_ADMIN_PASSWORD=replace-with-a-real-password
```

`.env` is gitignored — your password never leaves your laptop.

## 8. Build and start the Podium stack

```bash
docker compose up --build
```

First build takes ~3 minutes (downloads `golang:1.26-bookworm`,
`nginx:1.27-alpine`, `debian:bookworm-slim`). Subsequent builds reuse
the cache and finish in seconds.

When you see:

```
podium-frontend  | ... Configuration complete; ready for start up
podium           | ...
```

…both containers are up.

## 9. Open the dashboard

Visit <http://localhost:5173>.

Sign in with `admin` and the password from your `.env`.

## 10. Run the §48 walkthrough

This is the proof the whole stack works.

1. **Sign up.** Open <http://localhost:5173/signup> in one window.
   Create user `alice` with any email/password.

2. **Approve.** Open <http://localhost:5173/login> in a private window
   and sign in as `admin`. Go to the Admin dashboard and click
   **Approve** on `alice`.

3. **Create an app.** Back in `alice`'s window → Applications → **New
   application**:
   - name: `hello`
   - repository_url: `https://github.com/thelaserunicorn/podium-hello`
   - container_port: `80`

4. **Deploy.** Click **Deploy**, pick `podium-dev` as the namespace,
   confirm.

5. **Watch the state machine.** The deployment row will transition
   through `QUEUED → BUILDING → BUILT → DEPLOYING → STARTING → RUNNING`.
   This takes 1–3 minutes for a cold-cache first build.

6. **Open the app.** On the Overview tab, the **App URL** card shows
   a link like `http://127.0.0.1:40000`. Click it. A new tab opens
   showing a static HTML page with the **pod hostname** and a counter
   that increments on each reload.

7. **Scale and watch the hostname rotate.** On the Settings tab, slide
   replicas to 3 and click **Scale**. After ~10 seconds you'll see 3/3
   in the header. Reload the app URL several times — the hostname will
   rotate across the three pods (round-robin via the Service).

8. **Roll back.** On the Deployments tab, click **Roll back** on the
   previous successful deployment. A new row appears with the previous
   image, no rebuild.

9. **Promote.** Click **Promote to Staging** → pick `podium-staging`.
   The same image is deployed to a second namespace without rebuild.

10. **Clean up.** Delete the app from the Settings tab. Watch the
    Deployment / Service vanish from the cluster (we hardened this
    recently; you'll see a `clean k8s resources after app delete` log
    line on success).

## 11. Reset everything (when you want a fresh start)

```bash
# Stop the Podium stack
docker compose down

# Tear down the kind cluster
kind delete cluster --name podium

# Wipe the SQLite volume + cloned sources
docker volume rm podium_podium-data
rm -rf ~/.podium

# Optional: blow away Docker's image cache for Podium
docker image prune -a --filter "label=stage=podium"   # if you tagged them
```

Then re-run from step 8.

## Common gotchas

### `docker.sock` permission denied

```
permission denied while trying to connect to the Docker daemon socket
```

You skipped step 3 (`usermod -aG docker $USER`) or forgot to log out
and back in. `groups $USER` should list `docker`. Fix: re-run the
`usermod` line, then either log out/back in or run `newgrp docker` in
every shell.

### `kind create cluster` fails with containerd v4 error

```
unknown containerd config version: 4 (supported versions: 2 and 3)
```

You're on kind v0.27.0 or earlier. Upgrade to v0.33.0 (step 4).

### App URL card shows "couldn't reach the app"

The Service has no endpoints. Usually one of:

- The deployment row isn't `RUNNING` yet — wait, watch the state column.
- `container_port` doesn't match what the image actually listens on.
  The `podium-hello` demo listens on **80**.
- The image failed to start — check the Events tab for
  `ImagePullBackOff`, `CrashLoopBackOff`, or `ErrImagePull`.

### Port 40000–40099 collision

Each `(app, namespace)` pair gets a port in this fixed range. If two
apps land on the same port, the second one wins. To free a port:
delete the conflicting app or pick a different `container_port` (so a
different `DeploymentName` gets used).

### `Browser: localhost:5173 connection refused`

The frontend container isn't up. Check:

```bash
docker compose ps
docker compose logs frontend --tail 20
docker compose logs podium --tail 20
```

The single most common cause is the `podium` container crashlooping —
look for "WARN kubernetes client unavailable" at startup, which means
the kind cluster wasn't reachable when Podium booted. Fix: `docker
compose restart podium` after the kind cluster is fully up.

### Firewall

If you're hitting Podium from a different machine on the LAN:

```bash
sudo ufw allow 5173/tcp
sudo ufw allow 8080/tcp
```

For a single-laptop demo, you don't need either port open externally.

## Where things live on disk

| Path                                               | Owner  | Notes                                                                          |
| -------------------------------------------------- | ------ | ------------------------------------------------------------------------------ |
| `~/.kube/config`                                   | kind   | Cluster credentials (bind-mounted into the `podium` container)                 |
| `podium_podium-data`                               | Podium | Docker named volume, holds SQLite + cloned sources                             |
| `/var/run/docker.sock`                             | Docker | Host daemon socket (bind-mounted into `podium` for image builds + `kind load`) |
| Docker images `podium:dev` + `podium-frontend:dev` | Docker | Built locally from the `Dockerfile` and `frontend/Dockerfile`                  |
| `kind` Docker network                              | kind   | Cluster nodes                                                                  |

## Reference

- [README.md](../README.md) — high-level what/why
- [docs/ubuntu-setup.md](./ubuntu-setup.md) — generic Ubuntu runbook
  (this doc is the Pop!_OS-specific version, with one extra note about
  `VERSION_CODENAME=jammy`)
- [spec.md §48](../spec.md) — the full §48 acceptance walkthrough the
  runbook reproduces
