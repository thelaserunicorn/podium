package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/podium/podium/internal/application"
	"github.com/podium/podium/internal/deployment"
	"github.com/podium/podium/internal/storage"
)

// ImageLoader is the small surface area needed from the docker.Builder
// after a successful build: push the freshly-built image into the
// kind cluster. Declared here (rather than reusing docker.Builder
// directly) so the kubernetes package does not import the docker
// package — the wiring in cmd/podium/main.go passes the builder.
//
// `clusterName` is the kind cluster to target; the applier passes
// `a.Client.ClusterName` through. Empty string means "let kind pick"
// (single-cluster setups).
type ImageLoader interface {
	LoadIntoKind(ctx context.Context, tag, clusterName string) error
}

// Applier implements deployment.K8sApplier. It drives a single
// deployment through DEPLOYING → STARTING → RUNNING against a real
// kind cluster:
//
//  1. ensure the namespace exists
//  2. apply the Deployment + Service
//  3. push the image into the cluster (kind load)
//  4. poll until readyReplicas == desiredReplicas (or timeout)
//
// On success the deployment is set to RUNNING and nil is returned.
// On any failure a wrapped sentinel is returned; classifyReason in
// the orchestrator translates that into the short reason string
// persisted on the row.
type Applier struct {
	Client *Client
	Store  *storage.Queries
	App    AppReader
	// Loader pushes the built image into the kind cluster. Optional —
	// when nil, the load step is skipped (useful for tests).
	Loader ImageLoader
	// PollEvery is the readiness-poll interval. Default 2s.
	PollEvery time.Duration
	// ReadyTimeout caps the readiness poll. Default 5m.
	ReadyTimeout time.Duration
}

// AppReader is the subset of application.Service that the Applier
// needs. It is a real interface so tests can pass a fake without
// depending on the application package's database plumbing.
//
// The applier runs in the orchestrator's background goroutine, which
// has no userID (the deployment row carries the applicationID but not
// the owner). Ownership has already been authorized by the API handler
// that created the deployment row, so we use the ownership-free read
// here — calling Get with userID=0 would always return ErrNotFound.
type AppReader interface {
	GetByID(ctx context.Context, id int64) (application.Application, error)
}

func (a *Applier) defaults() {
	if a.PollEvery <= 0 {
		a.PollEvery = 2 * time.Second
	}
	if a.ReadyTimeout <= 0 {
		a.ReadyTimeout = 5 * time.Minute
	}
}

// Apply is the deployment.K8sApplier entry point.
//
// The supplied `ctx` is used for the apply steps (EnsureNamespace,
// ApplyDeployment, ApplyService, LoadIntoKind). The readiness poll
// uses a fresh `context.Background()` derived timeout so the orchestrator's
// Deploy-timeout cap does not bleed into the wait phase — those are
// independently tunable (Timeouts.Readiness).
func (a *Applier) Apply(ctx context.Context, deploymentID int64) error {
	a.defaults()
	d, err := a.Store.GetDeployment(ctx, deploymentID)
	if err != nil {
		return fmt.Errorf("kubernetes: load deployment %d: %w", deploymentID, err)
	}
	env, err := a.Store.GetEnvironment(ctx, d.EnvironmentID)
	if err != nil {
		return fmt.Errorf("%w: lookup environment: %v", deployment.ErrDeployFailed, err)
	}
	app, err := a.App.GetByID(ctx, d.ApplicationID)
	if err != nil {
		return fmt.Errorf("kubernetes: load application %d: %w", d.ApplicationID, err)
	}

	if err := a.Client.EnsureNamespace(ctx, env.Namespace); err != nil {
		return fmt.Errorf("%w: %v", deployment.ErrDeployFailed, err)
	}

	depName, err := a.Client.ApplyDeployment(ctx, &app, env.Namespace, d.Image, d.Replicas)
	if err != nil {
		return fmt.Errorf("%w: %v", deployment.ErrDeployFailed, err)
	}
	if err := a.Client.ApplyService(ctx, &app, env.Namespace); err != nil {
		return fmt.Errorf("%w: %v", deployment.ErrDeployFailed, err)
	}
	if a.Loader != nil {
		if err := a.Loader.LoadIntoKind(ctx, d.Image, a.Client.ClusterName); err != nil {
			return fmt.Errorf("%w: kind load: %v", deployment.ErrDeployFailed, err)
		}
	}

	if err := a.Store.SetDeploymentStatus(ctx, deploymentID, storage.StatusStarting, ""); err != nil {
		return fmt.Errorf("kubernetes: set STARTING: %w", err)
	}

	// Readiness wait uses a fresh poll context — see the doc above.
	// We pass the original ctx so writeCtx can inherit its deadline
	// for the SQLite status flips.
	return a.waitReady(ctx, deploymentID, env.Namespace, depName, d.Replicas)
}

// Scale is the deployment.K8sApplier entry point for changing the
// replica count of an existing Deployment (M4). The API handler has
// already authorized ownership and validated the namespace + replica
// range, so we only need to translate the k8s client error into the
// deploy-failed sentinel so classifyReason maps it to the right
// reason string.
func (a *Applier) Scale(ctx context.Context, app *application.Application, namespace string, replicas int) error {
	depName := DeploymentName(app.Name, app.ID)
	if err := a.Client.ScaleDeployment(ctx, namespace, depName, replicas); err != nil {
		return fmt.Errorf("%w: scale: %v", deployment.ErrDeployFailed, err)
	}
	return nil
}

// Restart is the deployment.K8sApplier entry point for restarting a
// running app. We delete every pod that matches the app's label
// selector; the Deployment controller recreates them. Empty result
// (no pods to delete) is success — restart against a fresh namespace
// is a no-op.
func (a *Applier) Restart(ctx context.Context, app *application.Application, namespace string) error {
	selector := AppLabelSelector(app.Name)
	if _, err := a.Client.DeletePodsBySelector(ctx, namespace, selector); err != nil {
		return fmt.Errorf("%w: restart: %v", deployment.ErrDeployFailed, err)
	}
	return nil
}

func (a *Applier) waitReady(origCtx context.Context, deploymentID int64, namespace, depName string, replicas int) error {
	pollCtx, cancel := context.WithTimeout(context.Background(), a.ReadyTimeout)
	defer cancel()
	// writeCtx is the context used for the SQLite status writes; it
	// must not be cancelled when pollCtx fires. We derive from
	// context.Background so the status flip survives a readiness
	// timeout.
	writeCtx := context.Background()
	if deadline, ok := origCtx.Deadline(); ok {
		var writeCancel context.CancelFunc
		writeCtx, writeCancel = context.WithDeadline(context.Background(), deadline)
		defer writeCancel()
	}

	ticker := time.NewTicker(a.PollEvery)
	defer ticker.Stop()

	// Scale-to-zero flips to RUNNING as soon as the Deployment object
	// exists (DECISIONS.md B). Anything else polls.
	if replicas == 0 {
		return a.Store.SetDeploymentStatus(writeCtx, deploymentID, storage.StatusRunning, "")
	}

	for {
		cur, des, err := a.Client.CurrentReplicas(pollCtx, namespace, depName)
		if err != nil {
			// Distinguish a poll-deadline-expired read (which means
			// "readyReplicas never reached desired" — the readiness
			// poll timed out) from a transient k8s read failure (a
			// genuine deploy failure). The pollCtx is the only one
			// that fires context.DeadlineExceeded here because every
			// other call site uses writeCtx / origCtx.
			if errors.Is(err, context.DeadlineExceeded) {
				return fmt.Errorf("%w: %v", deployment.ErrReadinessTimeout, err)
			}
			return fmt.Errorf("%w: %v", deployment.ErrDeployFailed, err)
		}
		if cur >= des {
			return a.Store.SetDeploymentStatus(writeCtx, deploymentID, storage.StatusRunning, "")
		}
		select {
		case <-pollCtx.Done():
			_ = a.Store.SetDeploymentStatus(writeCtx, deploymentID, storage.StatusFailed, "readiness_timeout")
			return fmt.Errorf("%w: %v", deployment.ErrReadinessTimeout, pollCtx.Err())
		case <-ticker.C:
		}
	}
}
