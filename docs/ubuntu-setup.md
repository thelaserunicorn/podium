# Podium on Ubuntu with kind

End-to-end runbook for running Podium on a fresh Ubuntu laptop with a local
kind cluster. The same steps work on macOS or any Linux with the same
toolchain — the Ubuntu-specific bits are the `apt` lines and the
docker-socket group step.

## Prerequisites

Install these once before touching Podium:

```bash
# 1. Docker Engine (NOT the snap — the snap is missing kernel features kind needs).
#    https://docs.docker.com/engine/install/ubuntu/
sudo apt update
sudo apt install -y ca-certificates curl gnupg
sudo install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg | \
  sudo gpg --dearmor -o /etc/apt/keyrings/docker.gpg
sudo chmod a+r /etc/apt/keyrings/docker.gpg
echo \
  "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] \
  https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "$VERSION_CODENAME") stable" | \
  sudo tee /etc/apt/sources.list.d/docker.list > /dev/null
sudo apt update
sudo apt install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin

# Add yourself to the docker group so docker.sock is writable without sudo.
sudo usermod -aG docker $USER
# IMPORTANT: log out and back in (or `newgrp docker`) before continuing.
docker version        # Server section must show a version

# 2. kubectl
curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl"
sudo install -o root -g root -m 0755 kubectl /usr/local/bin/kubectl
kubectl version --client

# 3. kind
curl -Lo ./kind https://kind.sigs.k8s.io/dl/v0.27.0/kind-linux-amd64
chmod +x ./kind && sudo mv ./kind /usr/local/bin/kind
kind version

# 4. Git (almost always present; included for completeness)
sudo apt install -y git
```

You only need Go installed if you intend to build Podium from source.
The Docker image bundles a compiled binary, so for a pure demo you can
skip Go entirely.

## Create the kind cluster

```bash
kind create cluster --name podium
kubectl cluster-info   # control-plane URL prints — confirms reachability
```

The default cluster is fine. Podium's `kubectl port-forward` ingress
doesn't require any `extraPortMappings` — kubectl already bridges the
kind node network to the host loopback.

## Get Podium

```bash
git clone https://github.com/thelaserunicorn/podium.git
cd podium
```

Pick **one** of the three run options below.

## Option A — Run from source (recommended while developing)

```bash
cd backend
go build -o ../bin/podium ./cmd/podium
cd ..

export PODIUM_ADMIN_USERNAME=admin
export PODIUM_ADMIN_PASSWORD='change-me-now'
export KUBECONFIG=$HOME/.kube/config
export PODIUM_DB_PATH=$HOME/.podium/podium.db
mkdir -p "$HOME/.podium"

./bin/podium    # listens on :8080
```

Open <http://localhost:8080> and sign in as `admin` / the password you set.

## Option B — Docker image

```bash
docker build -t podium:dev .
docker run --rm -p 8080:8080 \
  -e PODIUM_ADMIN_USERNAME=admin \
  -e PODIUM_ADMIN_PASSWORD='change-me-now' \
  -v "$HOME/.podium:/data" \
  -v "$HOME/.kube/config:/root/.kube/config:ro" \
  -v /var/run/docker.sock:/var/run/docker.sock \
  podium:dev
```

The three `-v` mounts are required:

- `/data` — SQLite database (persistent across restarts)
- `/root/.kube/config:ro` — kind's kubeconfig (read-only)
- `/var/run/docker.sock` — host Docker daemon; Podium builds images here
  and then runs `kind load docker-image` to push them into the kind
  cluster. Without this mount Podium can't build or deploy anything.

## Option C — docker compose

```bash
docker compose up --build
```

The shipped `docker-compose.yml` wires the same three mounts for you.
SQLite lives on the `podium-data` named volume at `/data/podium.db`.

## End-to-end smoke (spec.md §48)

1. Open <http://localhost:8080/signup> and create user `alice`.
2. In a private window, log in as `admin` → Admin dashboard → Approve `alice`.
3. Log in as `alice` → Applications → New application:
   - name: `hello`
   - repository_url: any public GitHub repo with a Dockerfile
   - container_port: the port the app listens on inside the container (commonly `8080`)
4. Click **Deploy**, pick a namespace (e.g. `podium-dev`), confirm.
5. Watch the deployment row transition `QUEUED → BUILDING → BUILT → DEPLOYING → STARTING → RUNNING`.
6. On the Overview tab, click **Open** on the App URL card. A new tab opens
   at `http://127.0.0.1:40000` (or whichever 40000–40099 port was assigned)
   served by a `kubectl port-forward` straight into the running Pod.

If the URL shows "Could not reach the app", the deployment row probably
isn't RUNNING yet — wait for it. The card polls on mount and only fetches
the URL once the namespace has at least one deployment.

## Common gotchas

1. **`docker.sock` permission denied.**
   Without `usermod -aG docker` and a re-login, every docker operation
   fails. `docker ps` should work for your user before you start Podium.

2. **Snap Docker.**
   `snap install docker` doesn't grant all kernel capabilities kind needs
   (notably `mount`). Use the apt repo above.

3. **`kind load docker-image` fails.**
   Usually means Podium's container can't reach the kind cluster's
   control-plane container. With Option B/C, the docker socket mount is
   what makes this work — confirm it's present:
   `docker exec -it $(docker ps -qf label=io.x-k8s.kind.role=control-plane) ls /`.

4. **App URL card returns "couldn't reach the app".**
   The Service exists but has no endpoints — usually the deployment row
   isn't RUNNING yet, or the image binds a different port than
   `container_port`. Check the Events tab; look for `ImagePullBackOff`,
   `CrashLoopBackOff`, or `ErrImagePull`.

5. **Port 40000–40099 collision.**
   The ingress router assigns one port per `(app, namespace)` from this
   fixed range. With Option A you can change the base in
   `backend/internal/ingress/router.go`. With the Docker image, stop the
   process listening on the conflicting port or change `container_port`
   so fewer apps claim the same slot.

6. **Firewall blocks :8080 from a browser on another machine.**
   `sudo ufw allow 8080/tcp`. For a single-laptop demo you don't need this.

7. **Reset everything.**
   ```bash
   kind delete cluster --name podium
   rm -rf "$HOME/.podium" "$HOME/.kube"
   # then re-run from "Create the kind cluster"
   ```

## Where things live on disk

| Path                                  | Owner | Notes                              |
|---------------------------------------|-------|------------------------------------|
| `$PODIUM_DB_PATH`                     | Podium| SQLite database (users, apps, etc.)|
| `$KUBECONFIG` (`~/.kube/config`)      | kind  | Cluster credentials                |
| `backend/bin/podium`                  | you   | Compiled binary (Option A only)    |
| `/data/podium.db`                     | Podium| SQLite inside the container        |
| `/var/run/docker.sock`                | Docker| Daemon Podium builds images into   |
| `kind` Docker network                 | kind  | Cluster nodes                      |
