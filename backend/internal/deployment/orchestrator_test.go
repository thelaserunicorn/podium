package deployment

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

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
