export PODIUM_ADMIN_USERNAME=admin
export PODIUM_ADMIN_PASSWORD=admin-secret
export PODIUM_DB_PATH=/tmp/podium-smoke.db
export PODIUM_ADDR=:18080
# Pick up a kind cluster kubeconfig if one exists. Smoke scripts gracefully
# SKIP the live M3 checks when no cluster is reachable; this just makes the
# happy path automatic for `kind create cluster --name podium` demos.
export KUBECONFIG=${KUBECONFIG:-$(kind get kubeconfig --name podium 2>/dev/null || true)}
