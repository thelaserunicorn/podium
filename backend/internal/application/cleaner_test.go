package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/podium/podium/internal/auth"
	"github.com/podium/podium/internal/storage"
)

// fakeCleaner records every DeleteAppResources call so we can assert
// the application service iterates over every namespace the app
// touched.
type fakeCleaner struct {
	mu    sync.Mutex
	calls []cleanerCall
	err   error
}

type cleanerCall struct {
	appID     int64
	appName   string
	namespace string
}

func (f *fakeCleaner) DeleteAppResources(_ context.Context, app *Application, namespace string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, cleanerCall{appID: app.ID, appName: app.Name, namespace: namespace})
	return f.err
}

// seedUser inserts a single APPROVED user and returns its id. Used by
// the cleaner tests to satisfy the applications.user_id FK.
func seedUser(t *testing.T, db *storage.Queries) int64 {
	t.Helper()
	ctx := context.Background()
	authSvc := auth.NewService(db.DB())
	u, err := authSvc.Signup(ctx, auth.SignupInput{
		Username: "owner", Email: "owner@e.x", Password: "longenough",
	})
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	if err := authSvc.ApproveUser(ctx, u.ID); err != nil {
		t.Fatalf("approve: %v", err)
	}
	return u.ID
}

// cleanupSvc wires up a Service that owns the queries handle + the
// fake cleaner, with one app already seeded against two namespaces.
func cleanupSvc(t *testing.T) (context.Context, *Service, *storage.Queries, *fakeCleaner, int64, int64) {
	t.Helper()
	db := storage.OpenInMemoryForTest(t)
	q := storage.NewQueries(db)
	cleaner := &fakeCleaner{}
	svc := NewService(db).WithQueries(q).WithResourceCleaner(cleaner)
	ctx := context.Background()

	userID := seedUser(t, q)

	app, err := svc.Create(ctx, CreateInput{
		Name:          "demo",
		RepositoryURL: "https://github.com/x/y",
		ContainerPort: 8080,
		UserID:        userID,
	})
	if err != nil {
		t.Fatalf("create app: %v", err)
	}

	// Seed an env-var in podium-dev and a deployment in podium-staging
	// so EnvironmentsForApp returns both.
	devEnv, err := q.GetEnvironmentByNamespace(ctx, "podium-dev")
	if err != nil {
		t.Fatalf("podium-dev env: %v", err)
	}
	stagingEnv, err := q.GetEnvironmentByNamespace(ctx, "podium-staging")
	if err != nil {
		t.Fatalf("podium-staging env: %v", err)
	}
	if _, err := q.UpsertEnvVar(ctx, app.ID, devEnv.ID, "FOO", "bar", false); err != nil {
		t.Fatalf("upsert env var: %v", err)
	}
	if _, err := q.CreateDeployment(ctx, app.ID, stagingEnv.ID, 1, 1, "demo:v1"); err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	return ctx, svc, q, cleaner, app.ID, userID
}

func TestDelete_InvokesCleanerForEachTouchedNamespace(t *testing.T) {
	ctx, svc, _, cleaner, appID, userID := cleanupSvc(t)

	if err := svc.Delete(ctx, appID, userID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(cleaner.calls) != 2 {
		t.Fatalf("cleaner calls=%d want 2 (dev + staging); %+v", len(cleaner.calls), cleaner.calls)
	}
	got := map[string]string{}
	for _, c := range cleaner.calls {
		if c.appID != appID || c.appName != "demo" {
			t.Errorf("cleaner call args: %+v", c)
		}
		got[c.namespace] = c.namespace
	}
	if _, ok := got["podium-dev"]; !ok {
		t.Errorf("cleaner not called for podium-dev; %+v", cleaner.calls)
	}
	if _, ok := got["podium-staging"]; !ok {
		t.Errorf("cleaner not called for podium-staging; %+v", cleaner.calls)
	}
}

func TestDelete_RemovesSQLiteRow(t *testing.T) {
	ctx, svc, q, _, appID, userID := cleanupSvc(t)

	if err := svc.Delete(ctx, appID, userID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := q.GetEnvironmentByNamespace(ctx, "podium-dev"); err != nil {
		t.Errorf("podium-dev env should still exist: %v", err)
	}
}

func TestDelete_CleanerErrorIsSwallowed(t *testing.T) {
	// A k8s failure must NOT fail the Delete — the SQLite row is the
	// source of truth for the dashboard, and an orphaned cluster
	// resource is much less bad than a user who can't remove a broken
	// app.
	ctx, svc, _, cleaner, appID, userID := cleanupSvc(t)
	cleaner.err = errors.New("apiserver down")

	if err := svc.Delete(ctx, appID, userID); err != nil {
		t.Errorf("Delete with cleaner err: %v", err)
	}
	// Confirm SQLite really did delete the row.
	if _, err := svc.Get(ctx, appID, userID); !errors.Is(err, ErrNotFound) {
		t.Errorf("app should be gone after Delete; got err=%v", err)
	}
}

func TestDelete_NoCleanerIsPureSQLite(t *testing.T) {
	// Without WithResourceCleaner, Delete must succeed and leave no
	// dangling goroutine / nil deref.
	db := storage.OpenInMemoryForTest(t)
	q := storage.NewQueries(db)
	svc := NewService(db).WithQueries(q) // cleaner intentionally nil
	ctx := context.Background()

	userID := seedUser(t, q)
	app, err := svc.Create(ctx, CreateInput{
		Name:          "x",
		RepositoryURL: "https://github.com/x/y",
		ContainerPort: 8080,
		UserID:        userID,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := svc.Delete(ctx, app.ID, userID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

// captureLogs swaps slog.Default() with one writing to a buffer for the
// duration of fn, then restores the previous default. Used to assert
// that a particular Warn line was emitted without polluting the test
// output with the rest of the package's log traffic.
func captureLogs(t *testing.T, fn func()) string {
	t.Helper()
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))
	fn()
	return buf.String()
}

// findWarn returns the first JSON log record whose msg field matches
// `want` and whose level is "WARN", or nil if there isn't one.
func findWarn(t *testing.T, logs, want string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec["level"] == "WARN" && rec["msg"] == want {
			return rec
		}
	}
	return nil
}

func TestDelete_NoCleanerWithDeploymentsLogsWarning(t *testing.T) {
	// Regression test for the "deleting from UI doesn't delete from
	// k8s" bug. When Podium boots before the kind cluster is reachable
	// (so WithResourceCleaner was never called) but the app being
	// deleted has prior deployments / env vars, the SQLite row is
	// removed but the cluster resources are silently orphaned. The fix
	// is a Warn-level log naming the namespaces so an operator knows
	// to either restart Podium after kind is up or manually clean up.
	//
	// cleanupSvc wires the cleaner, so this test rebuilds the fixture
	// without it — same shape, no WithResourceCleaner.
	db := storage.OpenInMemoryForTest(t)
	q := storage.NewQueries(db)
	svc := NewService(db).WithQueries(q) // cleaner intentionally nil
	ctx := context.Background()

	userID := seedUser(t, q)
	app, err := svc.Create(ctx, CreateInput{
		Name:          "demo",
		RepositoryURL: "https://github.com/x/y",
		ContainerPort: 8080,
		UserID:        userID,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	devEnv, err := q.GetEnvironmentByNamespace(ctx, "podium-dev")
	if err != nil {
		t.Fatalf("podium-dev env: %v", err)
	}
	stagingEnv, err := q.GetEnvironmentByNamespace(ctx, "podium-staging")
	if err != nil {
		t.Fatalf("podium-staging env: %v", err)
	}
	if _, err := q.UpsertEnvVar(ctx, app.ID, devEnv.ID, "FOO", "bar", false); err != nil {
		t.Fatalf("upsert env var: %v", err)
	}
	if _, err := q.CreateDeployment(ctx, app.ID, stagingEnv.ID, 1, 1, "demo:v1"); err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	logs := captureLogs(t, func() {
		if err := svc.Delete(ctx, app.ID, userID); err != nil {
			t.Fatalf("Delete: %v", err)
		}
	})

	rec := findWarn(t, logs, "app delete skipped k8s cleanup: cleaner not wired")
	if rec == nil {
		t.Fatalf("expected Warn log line; got:\n%s", logs)
	}
	if rec["app_id"] == nil {
		t.Errorf("Warn missing app_id field: %+v", rec)
	}
	if rec["app_name"] != "demo" {
		t.Errorf("Warn app_name=%v want demo", rec["app_name"])
	}
	ns, ok := rec["namespaces"].([]any)
	if !ok || len(ns) == 0 {
		t.Errorf("Warn namespaces=%v want non-empty list", rec["namespaces"])
	}
}

func TestDelete_NoCleanerWithoutDeploymentsStaysSilent(t *testing.T) {
	// Negative control: when the app has no prior deployments / env
	// vars, no cleaner being wired is fine — there's nothing to clean
	// up. Delete should succeed with no Warn line so the log doesn't
	// become noise on every fresh-app delete.
	db := storage.OpenInMemoryForTest(t)
	q := storage.NewQueries(db)
	svc := NewService(db).WithQueries(q) // cleaner intentionally nil
	ctx := context.Background()

	userID := seedUser(t, q)
	app, err := svc.Create(ctx, CreateInput{
		Name:          "fresh",
		RepositoryURL: "https://github.com/x/y",
		ContainerPort: 8080,
		UserID:        userID,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	logs := captureLogs(t, func() {
		if err := svc.Delete(ctx, app.ID, userID); err != nil {
			t.Fatalf("Delete: %v", err)
		}
	})

	if findWarn(t, logs, "app delete skipped k8s cleanup: cleaner not wired") != nil {
		t.Errorf("unexpected Warn for fresh app with no prior deployments:\n%s", logs)
	}
}

// fakeEvicter records every Evict / EvictApp call so we can assert
// the application service drops the cached port-forward route when an
// app or single deployment is deleted. Mirrors fakeCleaner above; kept
// in this file so the eviction tests live next to the cleaner tests.
type fakeEvicter struct {
	mu          sync.Mutex
	evictCalls  []evictCall
	evictAppIDs []int64
}

type evictCall struct {
	appID     int64
	namespace string
}

func (f *fakeEvicter) Evict(appID int64, namespace string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.evictCalls = append(f.evictCalls, evictCall{appID: appID, namespace: namespace})
}

func (f *fakeEvicter) EvictApp(appID int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.evictAppIDs = append(f.evictAppIDs, appID)
}

// TestDeleteDeployment_EvictsIngressRoute proves the deployment-delete
// hook wires through to the ingress router. After deleting a
// deployment, Evict must be called exactly once with the deployment's
// (appID, namespace). Without this hook the user would see a stale
// localhost URL pointing at a dead kubectl subprocess.
func TestDeleteDeployment_EvictsIngressRoute(t *testing.T) {
	ctx, svc, q, _, appID, userID := cleanupSvc(t)
	ev := &fakeEvicter{}
	svc = svc.WithIngressEvicter(ev)

	envID := mustEnvID(t, q, ctx, "podium-staging")
	list, err := q.ListDeployments(ctx, appID, envID)
	if err != nil {
		t.Fatalf("list deployments: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("seeded deployment count = %d, want 1", len(list))
	}
	depID := list[0].ID

	if err := svc.DeleteDeployment(ctx, depID, userID); err != nil {
		t.Fatalf("DeleteDeployment: %v", err)
	}
	if len(ev.evictCalls) != 1 {
		t.Fatalf("Evict calls=%d want 1; %+v", len(ev.evictCalls), ev.evictCalls)
	}
	got := ev.evictCalls[0]
	if got.appID != appID || got.namespace != "podium-staging" {
		t.Errorf("Evict args: %+v want (appID=%d, ns=podium-staging)", got, appID)
	}
	if len(ev.evictAppIDs) != 0 {
		t.Errorf("EvictApp called %d times for a single-deployment delete (want 0)", len(ev.evictAppIDs))
	}
}

// TestDelete_EvictsAllIngressRoutesForApp: when the whole app is
// deleted, EvictApp must run once with the app id (the router iterates
// its own routes). Single-deployment delete must NOT call EvictApp.
func TestDelete_EvictsAllIngressRoutesForApp(t *testing.T) {
	ctx, svc, _, _, appID, userID := cleanupSvc(t)
	ev := &fakeEvicter{}
	svc = svc.WithIngressEvicter(ev)

	if err := svc.Delete(ctx, appID, userID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(ev.evictAppIDs) != 1 || ev.evictAppIDs[0] != appID {
		t.Errorf("EvictApp calls=%v want [%d]", ev.evictAppIDs, appID)
	}
	// Whole-app delete should not also fire per-namespace Evict calls;
	// the router owns its own fan-out.
	if len(ev.evictCalls) != 0 {
		t.Errorf("Evict calls=%d during whole-app delete (want 0); %+v", len(ev.evictCalls), ev.evictCalls)
	}
}

// TestDeleteDeployment_NilEvicterIsSafe: when the service is wired
// without an evicter (Podium booted before the cluster was reachable),
// delete paths must not panic.
func TestDeleteDeployment_NilEvicterIsSafe(t *testing.T) {
	ctx, svc, q, _, appID, userID := cleanupSvc(t) // no WithIngressEvicter
	envID := mustEnvID(t, q, ctx, "podium-staging")
	list, err := q.ListDeployments(ctx, appID, envID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if err := svc.DeleteDeployment(ctx, list[0].ID, userID); err != nil {
		t.Fatalf("DeleteDeployment with nil evicter: %v", err)
	}
}
