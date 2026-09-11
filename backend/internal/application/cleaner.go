package application

import "context"

// AppResourceCleaner is the subset of the Kubernetes client that the
// application service needs when deleting an app, or a single
// deployment attempt. The real *kubernetes.Client satisfies this;
// tests supply a fake.
//
// The contract: DeleteAppResources must be idempotent (NotFound =
// success) so the API layer can call it without checking whether the
// app was ever deployed.
type AppResourceCleaner interface {
	DeleteAppResources(ctx context.Context, app *Application, namespace string) error
}

// IngressEvicter drops cached ingress state for an (app, namespace)
// pair. The application service calls this after tearing down k8s
// resources so the next ingress lookup starts a fresh port-forward
// against whatever now exists in the cluster, instead of handing the
// user a stale URL pointing at a dead kubectl subprocess.
//
// Why a separate interface from AppResourceCleaner: the application
// package must not import the ingress package (that would be a
// circular dependency — ingress already imports application for the
// AppSource / Application types). The wiring happens in main.go.
//
// The real *ingress.Router satisfies this; nil is a valid value
// meaning "no router wired" (Podium booted without a k8s cluster), in
// which case the service silently skips eviction.
type IngressEvicter interface {
	Evict(appID int64, namespace string)
	EvictApp(appID int64)
}
