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
	return &Client{CS: cs, Source: path}, nil
}
