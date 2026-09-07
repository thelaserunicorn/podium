package application

import (
	"context"
	"errors"
	"testing"

	"github.com/podium/podium/internal/auth"
	"github.com/podium/podium/internal/storage"
)

// TestDeleteDeployment_InvokesCleanerInDeploymentNamespace seeds a
// deployment in podium-dev, deletes it, and asserts the cleaner was
// called once with the app + the deployment's namespace. Sibling
// deployments in other namespaces MUST NOT trigger a cleanup — the
// Service.DeleteDeployment call is per-deployment, not per-app.
func TestDeleteDeployment_InvokesCleanerInDeploymentNamespace(t *testing.T) {
	ctx, svc, q, cleaner, appID, userID := cleanupSvc(t)

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
	if len(cleaner.calls) != 1 {
		t.Fatalf("cleaner calls=%d want 1; %+v", len(cleaner.calls), cleaner.calls)
	}
	got := cleaner.calls[0]
	if got.appID != appID || got.appName != "demo" || got.namespace != "podium-staging" {
		t.Errorf("cleaner call args: %+v", got)
	}
}

// TestDeleteDeployment_SoftDeletesRow verifies the SQLite row carries
// a non-null deleted_at after the delete, and that subsequent
// ListDeployments no longer returns it.
func TestDeleteDeployment_SoftDeletesRow(t *testing.T) {
	ctx, svc, q, _, appID, userID := cleanupSvc(t)

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
	// ListDeployments now filters out deleted rows.
	list, err = q.ListDeployments(ctx, appID, envID)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("soft-deleted deployment should be hidden; got %d", len(list))
	}
	// Direct GetDeployment still returns it but with DeletedAt set.
	d, err := q.GetDeployment(ctx, depID)
	if err != nil {
		t.Fatalf("GetDeployment: %v", err)
	}
	if !d.DeletedAt.Valid {
		t.Errorf("DeletedAt should be stamped; got %+v", d)
	}
}

// TestDeleteDeployment_UnknownIDReturnsNotFound ensures a missing id
// surfaces as ErrNotFound, not as some generic error.
func TestDeleteDeployment_UnknownIDReturnsNotFound(t *testing.T) {
	ctx, svc, _, _, _, userID := cleanupSvc(t)
	err := svc.DeleteDeployment(ctx, 99999, userID)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// TestDeleteDeployment_AlreadyDeletedReturnsNotFound asserts the
// second delete on the same id is a no-op 404 — the row is already
// soft-deleted, so the handler should treat it as gone.
func TestDeleteDeployment_AlreadyDeletedReturnsNotFound(t *testing.T) {
	ctx, svc, q, _, appID, userID := cleanupSvc(t)
	envID := mustEnvID(t, q, ctx, "podium-staging")
	list, err := q.ListDeployments(ctx, appID, envID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	depID := list[0].ID
	if err := svc.DeleteDeployment(ctx, depID, userID); err != nil {
		t.Fatalf("first DeleteDeployment: %v", err)
	}
	err = svc.DeleteDeployment(ctx, depID, userID)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("second DeleteDeployment err = %v, want ErrNotFound", err)
	}
}

// TestDeleteDeployment_WrongOwnerReturnsNotFound seeds user A's app
// and a deployment, then asks user B to delete it. The method must
// return ErrNotFound — never an unauthorized error (AGENTS.md §19).
func TestDeleteDeployment_WrongOwnerReturnsNotFound(t *testing.T) {
	ctx, svc, q, _, appID, _ := cleanupSvc(t)

	// Seed a second user (also APPROVED) via auth.Signup.
	authSvc := auth.NewService(q.DB())
	u, err := authSvc.Signup(ctx, auth.SignupInput{
		Username: "other", Email: "other@e.x", Password: "longenough",
	})
	if err != nil {
		t.Fatalf("signup other: %v", err)
	}
	if err := authSvc.ApproveUser(ctx, u.ID); err != nil {
		t.Fatalf("approve other: %v", err)
	}

	envID := mustEnvID(t, q, ctx, "podium-staging")
	list, err := q.ListDeployments(ctx, appID, envID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	depID := list[0].ID

	if err := svc.DeleteDeployment(ctx, depID, u.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteDeployment(wrong owner) err = %v, want ErrNotFound", err)
	}
}

// TestDeleteDeployment_CleanerErrorIsSwallowed mirrors the contract
// for app-delete: a k8s failure must NOT block the SQLite soft-delete,
// because the SQLite row is the source of truth for the dashboard and
// an orphaned k8s Deployment is much less bad than a stuck delete.
func TestDeleteDeployment_CleanerErrorIsSwallowed(t *testing.T) {
	ctx, svc, q, cleaner, appID, userID := cleanupSvc(t)
	cleaner.err = errors.New("apiserver down")

	envID := mustEnvID(t, q, ctx, "podium-staging")
	list, err := q.ListDeployments(ctx, appID, envID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	depID := list[0].ID

	if err := svc.DeleteDeployment(ctx, depID, userID); err != nil {
		t.Errorf("DeleteDeployment with cleaner err: %v", err)
	}
	// Confirm the SQLite row really was deleted.
	d, err := q.GetDeployment(ctx, depID)
	if err != nil {
		t.Fatalf("GetDeployment: %v", err)
	}
	if !d.DeletedAt.Valid {
		t.Errorf("DeletedAt should be stamped despite cleaner err; got %+v", d)
	}
}

// TestDeleteDeployment_NoCleanerIsPureSQLite ensures DeleteDeployment
// works without a cleaner wired (unit tests, no-cluster setups). The
// SQLite row must still be soft-deleted.
func TestDeleteDeployment_NoCleanerIsPureSQLite(t *testing.T) {
	ctx := context.Background()
	db := storage.OpenInMemoryForTest(t)
	q := storage.NewQueries(db)
	svc := NewService(db).WithQueries(q) // cleaner intentionally nil
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
	envID, err := q.EnvironmentIDByNamespace(ctx, "podium-dev")
	if err != nil {
		t.Fatalf("env: %v", err)
	}
	depID, err := q.CreateDeployment(ctx, app.ID, envID, 1, 1, "x:v1")
	if err != nil {
		t.Fatalf("deployment: %v", err)
	}

	if err := svc.DeleteDeployment(ctx, depID, userID); err != nil {
		t.Fatalf("DeleteDeployment: %v", err)
	}
	d, err := q.GetDeployment(ctx, depID)
	if err != nil {
		t.Fatalf("GetDeployment: %v", err)
	}
	if !d.DeletedAt.Valid {
		t.Errorf("DeletedAt should be stamped; got %+v", d)
	}
}

// mustEnvID is a tiny test helper that resolves a namespace to its
// environment_id or fails the test.
func mustEnvID(t *testing.T, q *storage.Queries, ctx context.Context, namespace string) int64 {
	t.Helper()
	id, err := q.EnvironmentIDByNamespace(ctx, namespace)
	if err != nil {
		t.Fatalf("env id for %s: %v", namespace, err)
	}
	return id
}
