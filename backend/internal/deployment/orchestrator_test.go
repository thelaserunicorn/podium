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

// Apply is required by K8sApplier but unused by the Scale / Restart
// tests. It records the call so a future test can assert on it.
func (f *fakeApplier) Apply(_ context.Context, deploymentID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scales = append(f.scales, scaleCall{}) // sentinel so tests can detect "Apply was called"
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
