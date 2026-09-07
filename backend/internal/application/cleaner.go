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
