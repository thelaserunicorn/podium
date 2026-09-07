package deployment

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/podium/podium/internal/application"
	"github.com/podium/podium/internal/docker"
	"github.com/podium/podium/internal/storage"
)

// fakeBuilder implements docker.Builder. It records every Build call
// and returns the canned error/success state. On success it pushes a
// couple of lines into the sink so tests can assert that build logs
// land in storage.
type fakeBuilder struct {
	mu      sync.Mutex
	calls   []buildCall
	err     error // if non-nil, Build returns this
	tagSeen string
}

type buildCall struct {
	dir string
	tag string
}

func (f *fakeBuilder) Build(_ context.Context, dir, tag string, sink docker.LogSink) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, buildCall{dir: dir, tag: tag})
	f.tagSeen = tag
	if f.err != nil {
		return f.err
	}
	_ = sink.Append("Step 1/2 : FROM scratch")
	_ = sink.Append("Successfully built")
	return nil
}

func (f *fakeBuilder) LoadIntoKind(context.Context, string, string) error { return nil }

// fakeFetcher implements docker.SourceFetcher.
type fakeFetcher struct {
	mu    sync.Mutex
	calls []fetchCall
	err   error
}

type fetchCall struct {
	url  string
	dest string
}

func (f *fakeFetcher) Fetch(_ context.Context, repoURL, destDir string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fetchCall{url: repoURL, dest: destDir})
	return f.err
}

// fakeApplier implements K8sApplier for the Scale / Restart unit
// tests. It records the call args so tests can assert on the
// (namespace, replicas) / (namespace, app) plumbing without depending
// on a real cluster.
type fakeApplier struct {
	mu       sync.Mutex
	scales   []scaleCall
	restarts []restartCall
	applies  []int64
	err      error
}

type scaleCall struct {
	app      *application.Application
	ns       string
	replicas int
}

type restartCall struct {
	app *application.Application
	ns  string
}

func (f *fakeApplier) Scale(_ context.Context, app *application.Application, ns string, replicas int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scales = append(f.scales, scaleCall{app: app, ns: ns, replicas: replicas})
	return f.err
}

func (f *fakeApplier) Restart(_ context.Context, app *application.Application, ns string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restarts = append(f.restarts, restartCall{app: app, ns: ns})
	return f.err
}

// Apply is required by K8sApplier. It records the call so Rollback
// tests (and any future Apply tests) can assert on the deployment id
// being applied.
func (f *fakeApplier) Apply(_ context.Context, deploymentID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applies = append(f.applies, deploymentID)
	return f.err
}

func newOrchFixture(t *testing.T) (*Orchestrator, *storage.Queries, int64, int64, int64) {
	t.Helper()
	db := storage.OpenInMemoryForTest(t)
	q := storage.NewQueries(db)
	ctx := context.Background()

	res, _ := db.ExecContext(ctx,
		`INSERT INTO users (username, email, password_hash, role, status) VALUES (?, ?, ?, 'USER', 'APPROVED')`,
		"alice", "alice@example.com", "x")
	userID, _ := res.LastInsertId()

	res, _ = db.ExecContext(ctx,
		`INSERT INTO applications (user_id, name, repository_url, container_port) VALUES (?, ?, ?, ?)`,
		userID, "my-api", "https://github.com/x/y", 8080)
	appID, _ := res.LastInsertId()

	var envID int64
	db.QueryRowContext(ctx, `SELECT id FROM environments WHERE namespace = 'podium-dev'`).Scan(&envID)

	depID, err := q.CreateDeployment(ctx, appID, envID, 1, 3, "my-api:v1")
	if err != nil {
		t.Fatal(err)
	}

	o := NewOrchestrator(q, &fakeBuilder{}, &fakeFetcher{}, t.TempDir(), Timeouts{Build: 5 * time.Second}, nil)
	return o, q, depID, appID, envID
}

func TestOrchestrator_HappyPathStopsAtBuilt(t *testing.T) {
	o, q, depID, appID, _ := newOrchFixture(t)
	ctx := context.Background()

	if err := o.Run(ctx, depID, appID, "my-api", "https://github.com/x/y", 0, 3); err != nil {
		t.Fatalf("Run: %v", err)
	}

	d, _ := q.GetDeployment(ctx, depID)
	if d.Status != storage.StatusBuilt {
		t.Errorf("status=%q want BUILT", d.Status)
	}
	if !d.StartedAt.Valid {
		t.Errorf("started_at should be set")
	}
	if d.FinishedAt.Valid {
		t.Errorf("finished_at should NOT be set at BUILT")
	}

	lines, _ := q.LogLinesSince(ctx, depID, time.Time{})
	if len(lines) == 0 {
		t.Errorf("expected at least one log line")
	}
}

func TestOrchestrator_FailOnBuildError(t *testing.T) {
	o, q, depID, appID, _ := newOrchFixture(t)

	// Swap in a builder that fails.
	o.builder = &fakeBuilder{err: errors.New("boom")}

	ctx := context.Background()
	err := o.Run(ctx, depID, appID, "my-api", "https://github.com/x/y", 0, 3)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrBuildFailed) {
		t.Errorf("err=%v", err)
	}

	d, _ := q.GetDeployment(ctx, depID)
	if d.Status != storage.StatusFailed {
		t.Errorf("status=%q want FAILED", d.Status)
	}
	if d.Reason.String != "build_failed" {
		t.Errorf("reason=%q", d.Reason.String)
	}
}

func TestOrchestrator_FailOnFetchError(t *testing.T) {
	o, q, depID, appID, _ := newOrchFixture(t)
	o.fetcher = &fakeFetcher{err: errors.New("clone failed")}

	ctx := context.Background()
	err := o.Run(ctx, depID, appID, "my-api", "https://github.com/x/y", 0, 3)
	if err == nil {
		t.Fatal("expected error")
	}

	d, _ := q.GetDeployment(ctx, depID)
	if d.Status != storage.StatusFailed {
		t.Errorf("status=%q", d.Status)
	}
}

func TestOrchestrator_RejectsConcurrentRun(t *testing.T) {
	o, _, depID, appID, _ := newOrchFixture(t)

	// Mark deployment active by inserting a fake cancel.
	o.mu.Lock()
	o.active[depID] = func() {}
	o.mu.Unlock()

	err := o.Run(context.Background(), depID, appID, "my-api", "https://github.com/x/y", 0, 3)
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("err=%v want ErrAlreadyRunning", err)
	}
}

func TestClassifyReason(t *testing.T) {
	cases := []struct {
		in   error
		want string
	}{
		{nil, ""},
		{docker.ErrBuildFailed, "build_failed"},
		{fmt.Errorf("%w: bad", docker.ErrBuildFailed), "build_failed"},
		{ErrDeployFailed, "deploy_failed"},
		{fmt.Errorf("%w: bad", ErrDeployFailed), "deploy_failed"},
		{ErrReadinessTimeout, "readiness_timeout"},
		// Wrapped readiness timeout inside deploy-failed (the
		// applier's currentReplicas error path) must surface as
		// readiness_timeout, not deploy_failed. errors.Is walks
		// the wrap chain and the check order is readiness first.
		{fmt.Errorf("%w: %w", ErrDeployFailed, ErrReadinessTimeout), "readiness_timeout"},
		{context.DeadlineExceeded, "build_timeout"},
		{errors.New("something"), "something"},
	}
	for _, c := range cases {
		got := classifyReason(c.in)
		if got != c.want {
			t.Errorf("classifyReason(%v)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestStorageSink_AppendsEachLine(t *testing.T) {
	db := storage.OpenInMemoryForTest(t)
	q := storage.NewQueries(db)
	ctx := context.Background()

	res, _ := db.ExecContext(ctx,
		`INSERT INTO users (username, email, password_hash, role, status) VALUES ('a','a','a','USER','APPROVED')`)
	uid, _ := res.LastInsertId()
	res, _ = db.ExecContext(ctx,
		`INSERT INTO applications (user_id, name, repository_url, container_port) VALUES (?, 'a', 'b', 1)`, uid)
	appID, _ := res.LastInsertId()
	var envID int64
	db.QueryRowContext(ctx, `SELECT id FROM environments WHERE namespace='podium-dev'`).Scan(&envID)
	depID, _ := q.CreateDeployment(ctx, appID, envID, 1, 1, "a:v1")

	sink := &storageSink{store: q, deploymentID: depID, ctx: ctx}
	for _, l := range []string{"one", "two", "three"} {
		if err := sink.Append(l); err != nil {
			t.Fatal(err)
		}
	}
	sink.Append("") // empty should be no-op

	lines, _ := q.LogLinesSince(ctx, depID, time.Time{})
	if len(lines) != 3 {
		t.Errorf("expected 3 lines, got %d", len(lines))
	}
}

// newOrchFixtureWithK8s is the M4 variant: it wires a fakeApplier
// (replacing the nil k8s used by newOrchFixture) so Scale / Restart
// have something to delegate to.
func newOrchFixtureWithK8s(t *testing.T) (*Orchestrator, *storage.Queries, *fakeApplier, *application.Application, int64) {
	t.Helper()
	db := storage.OpenInMemoryForTest(t)
	q := storage.NewQueries(db)
	ctx := context.Background()

	res, _ := db.ExecContext(ctx,
		`INSERT INTO users (username, email, password_hash, role, status) VALUES (?, ?, ?, 'USER', 'APPROVED')`,
		"alice", "alice@example.com", "x")
	userID, _ := res.LastInsertId()
	res, _ = db.ExecContext(ctx,
		`INSERT INTO applications (user_id, name, repository_url, container_port) VALUES (?, ?, ?, ?)`,
		userID, "my-api", "https://github.com/x/y", 8080)
	appID, _ := res.LastInsertId()

	k8s := &fakeApplier{}
	appSvc := application.NewService(db)
	app, _ := appSvc.GetByID(ctx, appID)

	o := NewOrchestrator(q, &fakeBuilder{}, &fakeFetcher{}, t.TempDir(), Timeouts{Build: 5 * time.Second}, k8s)
	return o, q, k8s, &app, userID
}

// TestOrchestrator_ScaleForwardsToK8sApplier: the orchestrator must
// thread the (app, namespace, replicas) tuple through to the k8s
// applier verbatim. The API handler has already validated ownership
// and the replicas range — the orchestrator is a thin shim.
func TestOrchestrator_ScaleForwardsToK8sApplier(t *testing.T) {
	o, _, k8s, app, _ := newOrchFixtureWithK8s(t)
	ctx := context.Background()

	if err := o.Scale(ctx, app, "podium-dev", 5); err != nil {
		t.Fatalf("Scale: %v", err)
	}
	k8s.mu.Lock()
	defer k8s.mu.Unlock()
	if len(k8s.scales) != 1 {
		t.Fatalf("scales=%d want 1", len(k8s.scales))
	}
	got := k8s.scales[0]
	if got.ns != "podium-dev" {
		t.Errorf("ns=%q want podium-dev", got.ns)
	}
	if got.replicas != 5 {
		t.Errorf("replicas=%d want 5", got.replicas)
	}
	if got.app == nil || got.app.ID != app.ID {
		t.Errorf("app mismatch: %+v", got.app)
	}
}

// TestOrchestrator_RestartForwardsToK8sApplier: same shape as Scale.
func TestOrchestrator_RestartForwardsToK8sApplier(t *testing.T) {
	o, _, k8s, app, _ := newOrchFixtureWithK8s(t)
	ctx := context.Background()

	if err := o.Restart(ctx, app, "podium-staging"); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	k8s.mu.Lock()
	defer k8s.mu.Unlock()
	if len(k8s.restarts) != 1 {
		t.Fatalf("restarts=%d want 1", len(k8s.restarts))
	}
	if got := k8s.restarts[0]; got.ns != "podium-staging" || got.app == nil || got.app.ID != app.ID {
		t.Errorf("restart args mismatch: %+v", got)
	}
}

// TestOrchestrator_ScaleWithoutK8sReturnsError: when Podium is booted
// without a kubeconfig (dev / CI), Scale and Restart must fail loud
// rather than silently no-op. The API handler turns this into 502.
func TestOrchestrator_ScaleWithoutK8sReturnsError(t *testing.T) {
	o, _, _, _, _ := newOrchFixture(t)
	ctx := context.Background()
	app := application.Application{ID: 1, Name: "demo", ContainerPort: 8080}
	if err := o.Scale(ctx, &app, "podium-dev", 2); err == nil {
		t.Error("expected error when k8s applier is nil")
	}
	if err := o.Restart(ctx, &app, "podium-dev"); err == nil {
		t.Error("expected error when k8s applier is nil")
	}
}

// TestOrchestrator_ScalePropagatesApplierError: if the k8s applier
// returns an error, the orchestrator surfaces it unchanged so the
// API handler can convert it into a 502 response with the underlying
// message.
func TestOrchestrator_ScalePropagatesApplierError(t *testing.T) {
	o, _, k8s, app, _ := newOrchFixtureWithK8s(t)
	k8s.err = errors.New("connection refused")
	err := o.Scale(context.Background(), app, "podium-dev", 3)
	if err == nil || err.Error() != "connection refused" {
		t.Errorf("err=%v want connection refused", err)
	}
}

// seedRollbackHistory inserts prior RUNNING deployments so Rollback
// has something to roll back to. Returns the seeded appID/envID plus
// the list of (version, image, id) tuples in version order.
func seedRollbackHistory(t *testing.T, q *storage.Queries, userID int64) (appID, envID int64, versions [][3]any) {
	t.Helper()
	db := q.DB()
	ctx := context.Background()
	res, _ := db.ExecContext(ctx,
		`INSERT INTO applications (user_id, name, repository_url, container_port) VALUES (?, 'rollback-app', 'https://github.com/x/y', 8080)`,
		userID)
	appID, _ = res.LastInsertId()
	db.QueryRowContext(ctx, `SELECT id FROM environments WHERE namespace='podium-dev'`).Scan(&envID)
	for v := 1; v <= 4; v++ {
		img := fmt.Sprintf("rollback-app:v%d", v)
		id, _ := q.CreateDeployment(ctx, appID, envID, v, 3, img)
		// Mark each as RUNNING so LatestSuccessfulDeployment can pick them.
		if err := q.SetDeploymentStatus(ctx, id, storage.StatusRunning, ""); err != nil {
			t.Fatal(err)
		}
		// Mirror what ApplicationNextVersion would do in production:
		// bump applications.version alongside each insert. Otherwise
		// the next Rollback test would call ApplicationNextVersion
		// from a counter at 0 and get version 1 instead of 5.
		if _, err := db.ExecContext(ctx, `UPDATE applications SET version = ? WHERE id = ?`, v, appID); err != nil {
			t.Fatal(err)
		}
		versions = append(versions, [3]any{id, v, img})
	}
	return
}

// rollbackFixture spins up an orchestrator + storage seeded with one
// app ("rollback-app") and four RUNNING prior deployments. The
// returned app already points at the seeded rollback-app so the
// Rollback tests don't have to know the id.
func rollbackFixture(t *testing.T) (*Orchestrator, *storage.Queries, *fakeApplier, *application.Application) {
	t.Helper()
	o, q, k8s, _, userID := newOrchFixtureWithK8s(t)
	seededAppID, seededEnvID, _ := seedRollbackHistory(t, q, userID)
	// Read back the fresh row so the ID is right (the seed uses
	// fresh INSERTs, so the id is 2 in a clean DB).
	db := q.DB()
	var fresh application.Application
	row := db.QueryRowContext(context.Background(),
		`SELECT id, user_id, name, repository_url, container_port FROM applications WHERE id = ?`, seededAppID)
	if err := row.Scan(&fresh.ID, &fresh.UserID, &fresh.Name, &fresh.RepositoryURL, &fresh.ContainerPort); err != nil {
		t.Fatal(err)
	}
	// Stash envID on the orchestrator's note: we use it through `seededEnvID`.
	t.Setenv("ROLLBACK_ENV_ID", fmt.Sprintf("%d", seededEnvID))
	return o, q, k8s, &fresh
}

// TestOrchestrator_RollbackToLatestSuccessful: when targetVersion=0,
// Rollback picks the most recent RUNNING row's image. The new row
// gets the next version (5) and points at v4's image bytes — no rebuild.
func TestOrchestrator_RollbackToLatestSuccessful(t *testing.T) {
	o, q, k8s, app := rollbackFixture(t)
	ctx := context.Background()
	envID, _ := q.EnvironmentIDByNamespace(ctx, "podium-dev")

	id, err := o.Rollback(ctx, app, envID, 0)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	// New row should be at v5 (counter was bumped past v4) and use v4's image.
	d, err := q.GetDeployment(ctx, id)
	if err != nil {
		t.Fatalf("GetDeployment: %v", err)
	}
	if d.Version != 5 {
		t.Errorf("version=%d want 5", d.Version)
	}
	if d.Image != "rollback-app:v4" {
		t.Errorf("image=%q want rollback-app:v4 (target's bytes, no rebuild)", d.Image)
	}
	// fakeApplier.Apply is a no-op so the orchestrator's last write
	// is DEPLOYING. The real k8s.Applier drives DEPLOYING →
	// STARTING → RUNNING inside Apply; we just assert the row got
	// past QUEUED here.
	if d.Status != storage.StatusDeploying && d.Status != storage.StatusStarting && d.Status != storage.StatusRunning {
		t.Errorf("status=%q want DEPLOYING/STARTING/RUNNING", d.Status)
	}

	// The k8s applier must have been called for the new deployment id.
	k8s.mu.Lock()
	defer k8s.mu.Unlock()
	if len(k8s.applies) != 1 || k8s.applies[0] != id {
		t.Errorf("applies=%v want [%d]", k8s.applies, id)
	}
}

// TestOrchestrator_RollbackToSpecificVersion: the API lets the user
// pick a specific prior version. The new row's image is the target's
// image bytes, not a rebuild.
func TestOrchestrator_RollbackToSpecificVersion(t *testing.T) {
	o, q, _, app := rollbackFixture(t)
	ctx := context.Background()
	envID, _ := q.EnvironmentIDByNamespace(ctx, "podium-dev")

	id, err := o.Rollback(ctx, app, envID, 2)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	d, _ := q.GetDeployment(ctx, id)
	if d.Image != "rollback-app:v2" {
		t.Errorf("image=%q want rollback-app:v2", d.Image)
	}
	if d.Version != 5 {
		t.Errorf("version=%d want 5 (counter bumped to 5)", d.Version)
	}
}

// TestOrchestrator_RollbackUnknownVersionErrors: a target version that
// doesn't exist must surface as an error (caller maps to 404 / 422).
func TestOrchestrator_RollbackUnknownVersionErrors(t *testing.T) {
	o, q, _, app := rollbackFixture(t)
	ctx := context.Background()
	envID, _ := q.EnvironmentIDByNamespace(ctx, "podium-dev")

	_, err := o.Rollback(ctx, app, envID, 999)
	if err == nil {
		t.Fatal("expected error for unknown version")
	}
}

// TestOrchestrator_RollbackNoSuccessfulReturnsErrNoRows: when the
// namespace has zero RUNNING deployments (only FAILED ones), a
// version=0 rollback has nothing to roll back to.
func TestOrchestrator_RollbackNoSuccessfulReturnsErrNoRows(t *testing.T) {
	o, q, _, app := rollbackFixture(t)
	ctx := context.Background()
	envID, _ := q.EnvironmentIDByNamespace(ctx, "podium-dev")

	// Mark every seeded deployment FAILED so LatestSuccessfulDeployment
	// returns sql.ErrNoRows.
	list, _ := q.ListDeployments(ctx, app.ID, envID)
	for _, d := range list {
		if err := q.SetDeploymentStatus(ctx, d.ID, storage.StatusFailed, "boom"); err != nil {
			t.Fatal(err)
		}
	}

	_, err := o.Rollback(ctx, app, envID, 0)
	if err == nil {
		t.Fatal("expected sql.ErrNoRows when no successful deployment exists")
	}
}
