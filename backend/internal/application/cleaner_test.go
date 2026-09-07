package application

import (
	"context"
	"errors"
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
