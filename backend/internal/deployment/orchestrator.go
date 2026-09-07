// Package deployment owns the deployment state machine and the
// orchestrator goroutine that drives a single deployment through its
// lifecycle:
//
//	QUEUED → BUILDING → BUILT → DEPLOYING → STARTING → RUNNING
//	                                      ↘ FAILED
//
// One goroutine per deployment (DECISIONS.md B). Concurrent deploys
// for the same app are rejected at the API layer with 409 Conflict;
// the orchestrator itself only runs one deployment at a time per
// instance.
package deployment

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/podium/podium/internal/application"
	"github.com/podium/podium/internal/docker"
	"github.com/podium/podium/internal/storage"
)

// Orchestrator wires the storage layer to the docker.Builder and
// drives a single deployment through its lifecycle.
//
// Only M2 responsibilities live here: clone → build → BUILT. The
// M3 work (kubernetes apply + readiness poll) plugs into the
// K8sApplier hook without changing the orchestrator's shape.
type Orchestrator struct {
	store    *storage.Queries
	builder  docker.Builder
	fetcher  docker.SourceFetcher
	workdir  string // root for cloned sources (e.g. /var/lib/podium/sources)
	timeouts Timeouts
	k8s      K8sApplier // nil in M2; orchestrator stops at BUILT

	mu     sync.Mutex
	active map[int64]context.CancelFunc // deploymentID → cancel
}

// Timeouts bounds the duration of each pipeline stage. On timeout the
// deployment is marked FAILED with a short reason string per
// DECISIONS.md B.
type Timeouts struct {
	Build     time.Duration // clone + docker build (M2)
	Deploy    time.Duration // EnsureNamespace + ApplyDeployment + ApplyService + kind-load (M3)
	Readiness time.Duration // readyReplicas poll loop (M3)
}

// K8sApplier is the M3 hook. When nil, the orchestrator stops at BUILT
// (used in M2 tests and dev runs without a kind cluster).
//
// M4 widens this interface to add Scale and Restart so the orchestrator
// can delegate operational actions to the same component that owns the
// k8s client. NopApplier satisfies the wider interface with no-op
// implementations so tests and dev boots without a kind cluster keep
// working unchanged.
type K8sApplier interface {
	Apply(ctx context.Context, deploymentID int64) error
	Scale(ctx context.Context, app *application.Application, namespace string, replicas int) error
	Restart(ctx context.Context, app *application.Application, namespace string) error
}

// NewOrchestrator wires dependencies. Caller owns the *Queries and
// must close them; the orchestrator does not take ownership.
func NewOrchestrator(store *storage.Queries, builder docker.Builder, fetcher docker.SourceFetcher, workdir string, timeouts Timeouts, k8s K8sApplier) *Orchestrator {
	if timeouts.Build == 0 {
		timeouts.Build = 15 * time.Minute
	}
	if timeouts.Deploy == 0 {
		timeouts.Deploy = 2 * time.Minute
	}
	if timeouts.Readiness == 0 {
		timeouts.Readiness = 5 * time.Minute
	}
	return &Orchestrator{
		store:    store,
		builder:  builder,
		fetcher:  fetcher,
		workdir:  workdir,
		timeouts: timeouts,
		k8s:      k8s,
		active:   map[int64]context.CancelFunc{},
	}
}

// ErrAlreadyRunning is returned by Run if the deployment is already
// being driven by another goroutine on this process. The API layer
// should reject with 409 in that case.
var ErrAlreadyRunning = errors.New("deployment already running")

// ErrBuildFailed is exposed for callers that want to inspect the
// underlying docker failure. Underlying error is the docker.ErrBuildFailed
// (or its wrapper from the SourceFetcher) and is safe to surface.
var ErrBuildFailed = errors.New("build failed")

// ErrDeployFailed wraps the kubernetes package's failure modes (ensure
// namespace, apply Deployment / Service, kind load). The kubernetes
// applier returns errors that errors.Is-match this sentinel; classifyReason
// translates it into the "deploy_failed" reason string persisted on the
// row.
var ErrDeployFailed = errors.New("deploy failed")

// ErrReadinessTimeout wraps the readiness poll deadline. The applier
// returns errors that errors.Is-match this sentinel when readyReplicas
// never reaches desiredReplicas within Timeouts.Readiness.
var ErrReadinessTimeout = errors.New("readiness timeout")

// SourceDir returns the path where this orchestrator would clone the
// given app's source. Exposed so tests can assert on layout.
func (o *Orchestrator) SourceDir(appID int64, name string) string {
	return fmt.Sprintf("%s/app-%d-%s", o.workdir, appID, name)
}

// IsActive reports whether a deployment is currently being driven on
// this process. Used by the API handler to reject concurrent deploys
// even before they hit the database.
func (o *Orchestrator) IsActive(deploymentID int64) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, ok := o.active[deploymentID]
	return ok
}

// Scale patches the running Deployment's replica count (M4). When no
// k8s applier is wired (M2 dev mode), the call is a no-op — there is
// no Deployment to scale. Errors are returned to the caller; the API
// handler turns them into a 502 with the underlying message.
func (o *Orchestrator) Scale(ctx context.Context, app *application.Application, namespace string, replicas int) error {
	if o.k8s == nil {
		return errors.New("kubernetes unavailable: no applier wired")
	}
	return o.k8s.Scale(ctx, app, namespace, replicas)
}

// Restart deletes every pod owned by the app in the namespace; the
// Deployment controller recreates them. Mirrors Scale's nil-applier
// behaviour.
func (o *Orchestrator) Restart(ctx context.Context, app *application.Application, namespace string) error {
	if o.k8s == nil {
		return errors.New("kubernetes unavailable: no applier wired")
	}
	return o.k8s.Restart(ctx, app, namespace)
}

// Run drives a single deployment through the pipeline. It blocks until
// the deployment reaches a terminal state (RUNNING or FAILED). Errors
// here are internal; the deployment row is the source of truth that
// the API handler reads back.
//
// `app` carries the repository_url, name, and id used to clone+tag.
// `envID` and `replicas` are passed in (envID comes from
// storage.EnvironmentIDByNamespace).
func (o *Orchestrator) Run(ctx context.Context, deploymentID, appID int64, appName, repoURL string, envID int64, replicas int) error {
	if o.IsActive(deploymentID) {
		return ErrAlreadyRunning
	}

	runCtx, cancel := context.WithCancel(ctx)
	o.mu.Lock()
	o.active[deploymentID] = cancel
	o.mu.Unlock()
	defer func() {
		cancel()
		o.mu.Lock()
		delete(o.active, deploymentID)
		o.mu.Unlock()
	}()

	return o.runLocked(runCtx, deploymentID, appID, appName, repoURL, envID, replicas)
}

func (o *Orchestrator) runLocked(ctx context.Context, deploymentID, appID int64, appName, repoURL string, envID int64, replicas int) error {
	// Tag is "<app-name>:v<version>" — image identity for the rest of
	// the lifecycle (DECISIONS.md D).
	d, err := o.store.GetDeployment(ctx, deploymentID)
	if err != nil {
		return fmt.Errorf("deployment %d not found: %w", deploymentID, err)
	}
	tag := d.Image

	// 1. Clone source.
	srcDir := o.SourceDir(appID, appName)
	if err := o.fetcher.Fetch(ctx, repoURL, srcDir); err != nil {
		o.fail(ctx, deploymentID, fmt.Errorf("%w: %v", ErrBuildFailed, err))
		return err
	}

	// 2. BUILDING → build docker image, demuxed logs into SQLite.
	buildCtx, cancel := context.WithTimeout(ctx, o.timeouts.Build)
	defer cancel()
	if err := o.store.SetDeploymentStatus(ctx, deploymentID, storage.StatusBuilding, ""); err != nil {
		return err
	}

	sink := &storageSink{store: o.store, deploymentID: deploymentID, ctx: ctx}
	if err := o.builder.Build(buildCtx, srcDir, tag, sink); err != nil {
		wrapped := fmt.Errorf("%w: %v", ErrBuildFailed, err)
		o.fail(ctx, deploymentID, wrapped)
		return wrapped
	}

	if err := o.store.SetDeploymentStatus(ctx, deploymentID, storage.StatusBuilt, ""); err != nil {
		return err
	}

	// 3. BUILT — M3 picks up here. For M2 we leave it at BUILT.
	if o.k8s == nil {
		return nil
	}

	// 4. DEPLOYING → STARTING → RUNNING via the M3 hook. The hook
	// owns the per-stage status writes (Deploying → Starting →
	// Running) and the readiness poll; the orchestrator only stamps
	// DEPLOYING before delegating and translates failures into
	// FAILED via fail().
	if err := o.store.SetDeploymentStatus(ctx, deploymentID, storage.StatusDeploying, ""); err != nil {
		return err
	}
	deployCtx, cancel := context.WithTimeout(ctx, o.timeouts.Deploy)
	defer cancel()
	if err := o.k8s.Apply(deployCtx, deploymentID); err != nil {
		o.fail(ctx, deploymentID, err)
		return err
	}
	// Applier flipped to RUNNING on success; nothing else to do.
	return nil
}

func (o *Orchestrator) fail(ctx context.Context, deploymentID int64, cause error) {
	// Always stamp the error into the log stream so the diagnostics
	// panel has something to show even when the failure happened before
	// Docker emitted any output (clone failure, kind load error, etc.).
	_ = o.store.AppendLogLine(ctx, deploymentID, "[error] "+cause.Error())
	reason := classifyReason(cause)
	_ = o.store.SetDeploymentStatus(ctx, deploymentID, storage.StatusFailed, reason)
}

// classifyReason maps an underlying error into the short reason
// strings documented in DECISIONS.md B.
//
// Order matters: ErrReadinessTimeout is checked before ErrDeployFailed
// because the applier may wrap a readiness-timeout as ErrDeployFailed
// (e.g. when currentReplicas fails on the poll deadline). errors.Is
// walks the wrap chain, so checking ErrReadinessTimeout first means
// the more specific reason wins regardless of how the caller wrapped
// the error.
func classifyReason(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, ErrReadinessTimeout):
		return "readiness_timeout"
	case errors.Is(err, ErrBuildFailed), errors.Is(err, docker.ErrBuildFailed):
		return "build_failed"
	case errors.Is(err, ErrDeployFailed):
		return "deploy_failed"
	case errors.Is(err, context.DeadlineExceeded):
		return "build_timeout"
	default:
		return err.Error()
	}
}

// storageSink streams build log lines into the deploy_log_lines
// table. Each Append is a single INSERT; we don't batch because
// builds emit lines at low enough rates (tens per second, not
// thousands) that the overhead is fine on SQLite.
type storageSink struct {
	store        *storage.Queries
	deploymentID int64
	ctx          context.Context
}

func (s *storageSink) Append(line string) error {
	if line == "" {
		return nil
	}
	return s.store.AppendLogLine(s.ctx, s.deploymentID, line)
}
