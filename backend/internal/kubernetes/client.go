// Package kubernetes is the Podium control plane's interface to a real
// Kubernetes cluster (kind in development; spec.md §15). Everything in
// this package talks to the cluster through client-go; nothing here
// shells out to `kubectl`.
//
// The package is intentionally thin. Each file owns one concern
// (namespace ensure, Deployment apply, Service apply, Pod listing),
// and the Applier wires them together with the orchestrator's
// K8sApplier interface (deployment/applier.go in M3+).
//
// The Client holds a kubernetes.Interface rather than a concrete
// *kubernetes.Clientset so tests can swap in
// k8s.io/client-go/kubernetes/fake.NewSimpleClientset without
// touching the network.
package kubernetes

import (
	"fmt"
	"os"
	"strings"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// Client is the thin wrapper every other type in this package embeds.
// Construct it via New(); tests construct it directly with a fake
// clientset.
type Client struct {
	// CS is the typed Kubernetes client. Held as the interface so tests
	// can pass a fake.
	CS kubernetes.Interface
	// Source is the kubeconfig path that produced CS, or "fake" when CS
	// was injected. Useful for log lines.
	Source string
	// ClusterName is the kind cluster name this Client talks to,
	// derived from the kubeconfig current-context (kind-<name>
	// convention). Used as `--name` to `kind load docker-image` so the
	// image lands in the right cluster when the host has more than
	// one. Empty string means "could not derive / unknown"; callers
	// pass it through and `kind` will default to its own selection
	// (single-cluster setups only).
	ClusterName string
}

// New builds a Client from the local kubeconfig. If $KUBECONFIG is set
// it wins; otherwise $HOME/.kube/config is used. Returns a non-nil
// error if the config cannot be loaded or the clientset cannot be
// built; the caller decides whether that error is fatal or simply
// disables the k8s apply step (cmd/podium/main.go treats it as the
// latter — same shape as the docker fallback in M2).
func New() (*Client, error) {
	path := os.Getenv("KUBECONFIG")
	if path == "" {
		path = clientcmd.RecommendedHomeFile
	}
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil {
		return nil, fmt.Errorf("kubernetes: load kubeconfig: %w", err)
	}
	if cfg == nil {
		return nil, fmt.Errorf("kubernetes: empty kubeconfig at %s", path)
	}
	restCfg, err := clientcmd.NewDefaultClientConfig(*cfg, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("kubernetes: build rest config: %w", err)
	}
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("kubernetes: build clientset: %w", err)
	}
	return &Client{CS: cs, Source: path, ClusterName: deriveKindClusterName(cfg.CurrentContext)}, nil
}

// deriveKindClusterName decodes a kubeconfig current-context into the
// matching kind cluster name. `kind create cluster --name <name>` and
// the kubelogin convention produce a context called `kind-<name>`; we
// strip the prefix to recover the kind cluster name. Contexts that
// don't follow the convention (e.g. manually-authored contexts named
// after the cluster directly) pass through unchanged — `kind load`
// will then either succeed (if the name matches a real kind cluster)
// or fail loudly with "cluster not found" (which is the correct UX
// versus silently loading into the wrong cluster).
func deriveKindClusterName(currentContext string) string {
	return strings.TrimPrefix(currentContext, "kind-")
}
