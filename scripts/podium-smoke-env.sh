export PODIUM_ADMIN_USERNAME=admin
export PODIUM_ADMIN_PASSWORD=admin-secret
export PODIUM_DB_PATH=/tmp/podium-smoke.db
export PODIUM_ADDR=:18080
# The Go client (internal/kubernetes/client.go) reads KUBECONFIG as a file
# path; we deliberately don't auto-derive one from `kind get kubeconfig`
# because newer kind prints inline YAML, which the loader cannot read.
# Demo authors should either point KUBECONFIG at a file or merge the kind
# config into $HOME/.kube/config. The smoke script gracefully SKIPs the
# live M3 checks when no cluster is reachable.
