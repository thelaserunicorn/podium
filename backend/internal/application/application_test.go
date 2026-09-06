package application_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/podium/podium/internal/application"
	"github.com/podium/podium/internal/auth"
	"github.com/podium/podium/internal/storage"
)

// helpers ---------------------------------------------------------------

func newCtx(t *testing.T) (context.Context, *auth.Service, *application.Service) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "podium.db")
	db, err := storage.Open(context.Background(), path, storage.Options{})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	authSvc := auth.NewService(db)
	if err := auth.SeedAdmin(context.Background(), db, "root", "root-password"); err != nil {
		t.Fatalf("SeedAdmin: %v", err)
	}

	// Provision two approved users with predictable usernames. Passwords meet
	// the 8-character minimum (auth package).
	userA := mustSignup(t, authSvc, "alice", "alice@example.com", "alice-secret")
	userB := mustSignup(t, authSvc, "bob", "bob@example.com", "bob-secret")
	_ = authSvc.ApproveUser(context.Background(), userA.ID)
	_ = authSvc.ApproveUser(context.Background(), userB.ID)

	appSvc := application.NewService(db)
	return context.Background(), authSvc, appSvc.WithUsers(userA.ID, userB.ID)
}

func mustSignup(t *testing.T, svc *auth.Service, username, email, password string) auth.User {
	t.Helper()
	u, err := svc.Signup(context.Background(), auth.SignupInput{
		Username: username,
		Email:    email,
		Password: password,
	})
	if err != nil {
		t.Fatalf("signup %s: %v", username, err)
	}
	return u
}

func uniqueName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// TestApplicationCRUDRoundtrip: create → read → update → list → delete
// through the Service, verifying each return value matches what was put in.
func TestApplicationCRUDRoundtrip(t *testing.T) {
	t.Parallel()
	ctx, _, appSvc := newCtx(t)

	created, err := appSvc.Create(ctx, application.CreateInput{
		Name:          uniqueName("api"),
		RepositoryURL: "https://github.com/example/api",
		ContainerPort: 8080,
		UserID:        testUserA(ctx, appSvc),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("expected non-zero id")
	}
	if created.Version != 0 {
		t.Fatalf("new app version: got %d, want 0", created.Version)
	}

	got, err := appSvc.Get(ctx, created.ID, testUserA(ctx, appSvc))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != created.Name {
		t.Errorf("Get Name: got %q want %q", got.Name, created.Name)
	}
	if got.RepositoryURL != created.RepositoryURL {
		t.Errorf("Get RepositoryURL: got %q want %q", got.RepositoryURL, created.RepositoryURL)
	}
	if got.ContainerPort != created.ContainerPort {
		t.Errorf("Get ContainerPort: got %d want %d", got.ContainerPort, created.ContainerPort)
	}

	updated, err := appSvc.Update(ctx, created.ID, application.UpdateInput{
		RepositoryURL: "https://github.com/example/api-v2",
		ContainerPort: 9090,
		UserID:        testUserA(ctx, appSvc),
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.RepositoryURL != "https://github.com/example/api-v2" {
		t.Errorf("Update RepositoryURL: got %q", updated.RepositoryURL)
	}
	if updated.ContainerPort != 9090 {
		t.Errorf("Update ContainerPort: got %d", updated.ContainerPort)
	}
	if updated.Version != 1 {
		t.Errorf("Update should bump version to 1, got %d", updated.Version)
	}

	list, err := appSvc.List(ctx, testUserA(ctx, appSvc))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List: expected 1 app, got %d", len(list))
	}
	if list[0].ID != created.ID {
		t.Errorf("List ID mismatch")
	}

	if err := appSvc.Delete(ctx, created.ID, testUserA(ctx, appSvc)); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := appSvc.Get(ctx, created.ID, testUserA(ctx, appSvc)); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("Get after Delete: expected ErrNotFound, got %v", err)
	}
}

// TestApplicationOwnershipIsolated: user A cannot read, update, list, or
// delete user B's application. Per AGENTS.md §14 and PLAN.md risk #5,
// cross-user access must return 404 (not 403) to avoid leaking existence.
func TestApplicationOwnershipIsolated(t *testing.T) {
	t.Parallel()
	ctx, _, appSvc := newCtx(t)
	alice := testUserA(ctx, appSvc)
	bob := testUserB(ctx, appSvc)

	created, err := appSvc.Create(ctx, application.CreateInput{
		Name:          uniqueName("alice-app"),
		RepositoryURL: "https://github.com/example/alice",
		ContainerPort: 8080,
		UserID:        alice,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Bob reading alice's app -> 404
	if _, err := appSvc.Get(ctx, created.ID, bob); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("Bob Get: expected ErrNotFound, got %v", err)
	}

	// Bob updating alice's app -> 404
	_, err = appSvc.Update(ctx, created.ID, application.UpdateInput{
		RepositoryURL: "https://github.com/malicious/replacement",
		ContainerPort: 80,
		UserID:        bob,
	})
	if !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("Bob Update: expected ErrNotFound, got %v", err)
	}

	// Bob deleting alice's app -> 404 (and the row should still exist)
	if err := appSvc.Delete(ctx, created.ID, bob); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("Bob Delete: expected ErrNotFound, got %v", err)
	}

	// Confirm alice still sees her app, unchanged.
	got, err := appSvc.Get(ctx, created.ID, alice)
	if err != nil {
		t.Fatalf("Alice Get after Bob attack: %v", err)
	}
	if got.RepositoryURL != "https://github.com/example/alice" {
		t.Fatalf("RepositoryURL was mutated: %q", got.RepositoryURL)
	}

	// Bob's list must not include alice's app.
	bobList, err := appSvc.List(ctx, bob)
	if err != nil {
		t.Fatalf("Bob List: %v", err)
	}
	for _, a := range bobList {
		if a.ID == created.ID {
			t.Fatalf("Bob's list contained Alice's app id %d", created.ID)
		}
	}
}

// TestApplicationGetByIDIgnoresOwnership: GetByID is the internal-only read
// the orchestrator + kubernetes.Applier use (no userID available there
// because the deployment row doesn't carry one). It must succeed across
// users and return ErrNotFound for unknown ids — but not enforce ownership.
func TestApplicationGetByIDIgnoresOwnership(t *testing.T) {
	t.Parallel()
	ctx, _, appSvc := newCtx(t)
	alice := testUserA(ctx, appSvc)

	created, err := appSvc.Create(ctx, application.CreateInput{
		Name:          uniqueName("alice-app"),
		RepositoryURL: "https://github.com/example/alice",
		ContainerPort: 8080,
		UserID:        alice,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// GetByID succeeds even when called with the wrong user id (or any).
	got, err := appSvc.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.ID != created.ID {
		t.Fatalf("id mismatch: got %d, want %d", got.ID, created.ID)
	}

	// Unknown id still returns ErrNotFound.
	if _, err := appSvc.GetByID(ctx, 99999); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("unknown id: want ErrNotFound, got %v", err)
	}
}

// TestApplicationNameUniquePerUser: the UNIQUE(user_id, name) constraint
// should surface as a typed error. Different users with the same app name
// must both succeed (no global uniqueness).
func TestApplicationNameUniquePerUser(t *testing.T) {
	t.Parallel()
	ctx, _, appSvc := newCtx(t)
	alice := testUserA(ctx, appSvc)
	bob := testUserB(ctx, appSvc)

	name := uniqueName("shared")

	if _, err := appSvc.Create(ctx, application.CreateInput{
		Name: name, RepositoryURL: "https://github.com/example/a", ContainerPort: 1, UserID: alice,
	}); err != nil {
		t.Fatalf("alice Create: %v", err)
	}
	if _, err := appSvc.Create(ctx, application.CreateInput{
		Name: name, RepositoryURL: "https://github.com/example/b", ContainerPort: 1, UserID: bob,
	}); err != nil {
		t.Fatalf("bob Create (same name different user): %v", err)
	}

	// Alice creating a second app with the same name must fail.
	_, err := appSvc.Create(ctx, application.CreateInput{
		Name: name, RepositoryURL: "https://github.com/example/c", ContainerPort: 1, UserID: alice,
	})
	if !errors.Is(err, application.ErrDuplicateName) {
		t.Fatalf("expected ErrDuplicateName, got %v", err)
	}
}

// TestApplicationValidatesInput: name shape, repository URL shape, port
// range. AGENTS.md §41 says validate at the API boundary.
func TestApplicationValidatesInput(t *testing.T) {
	t.Parallel()
	ctx, _, appSvc := newCtx(t)
	alice := testUserA(ctx, appSvc)

	cases := []struct {
		name string
		in   application.CreateInput
		want error
	}{
		{"empty name", application.CreateInput{Name: "", RepositoryURL: "https://github.com/x/y", ContainerPort: 80, UserID: alice}, application.ErrInvalidName},
		{"name with whitespace", application.CreateInput{Name: "bad name", RepositoryURL: "https://github.com/x/y", ContainerPort: 80, UserID: alice}, application.ErrInvalidName},
		{"bad repo url", application.CreateInput{Name: uniqueName("x"), RepositoryURL: "not a url", ContainerPort: 80, UserID: alice}, application.ErrInvalidRepoURL},
		{"port too low", application.CreateInput{Name: uniqueName("x"), RepositoryURL: "https://github.com/x/y", ContainerPort: 0, UserID: alice}, application.ErrInvalidPort},
		{"port too high", application.CreateInput{Name: uniqueName("x"), RepositoryURL: "https://github.com/x/y", ContainerPort: 70000, UserID: alice}, application.ErrInvalidPort},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := appSvc.Create(ctx, tc.in)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

// TestApplicationVersionMonotonic: each successful Update bumps Version by
// exactly 1. Used as the image tag suffix in DECISIONS.md D.
func TestApplicationVersionMonotonic(t *testing.T) {
	t.Parallel()
	ctx, _, appSvc := newCtx(t)
	alice := testUserA(ctx, appSvc)

	created, err := appSvc.Create(ctx, application.CreateInput{
		Name: uniqueName("ver"), RepositoryURL: "https://github.com/x/y", ContainerPort: 80, UserID: alice,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Version != 0 {
		t.Fatalf("initial version: got %d want 0", created.Version)
	}

	for i := 1; i <= 3; i++ {
		got, err := appSvc.Update(ctx, created.ID, application.UpdateInput{
			ContainerPort: 80 + i, UserID: alice,
		})
		if err != nil {
			t.Fatalf("Update %d: %v", i, err)
		}
		if got.Version != i {
			t.Fatalf("after %d updates version: got %d want %d", i, got.Version, i)
		}
	}
}

// TestApplicationListScopesToOwner: with multiple apps owned by alice and
// bob, each user's List returns only their own.
func TestApplicationListScopesToOwner(t *testing.T) {
	t.Parallel()
	ctx, _, appSvc := newCtx(t)
	alice := testUserA(ctx, appSvc)
	bob := testUserB(ctx, appSvc)

	for i := 0; i < 3; i++ {
		if _, err := appSvc.Create(ctx, application.CreateInput{
			Name:          uniqueName(fmt.Sprintf("alice-%d", i)),
			RepositoryURL: "https://github.com/x/a", ContainerPort: 1, UserID: alice,
		}); err != nil {
			t.Fatalf("alice Create %d: %v", i, err)
		}
	}
	for i := 0; i < 2; i++ {
		if _, err := appSvc.Create(ctx, application.CreateInput{
			Name:          uniqueName(fmt.Sprintf("bob-%d", i)),
			RepositoryURL: "https://github.com/x/b", ContainerPort: 1, UserID: bob,
		}); err != nil {
			t.Fatalf("bob Create %d: %v", i, err)
		}
	}

	aliceList, err := appSvc.List(ctx, alice)
	if err != nil {
		t.Fatalf("alice List: %v", err)
	}
	if len(aliceList) != 3 {
		t.Fatalf("alice List: got %d want 3", len(aliceList))
	}
	bobList, err := appSvc.List(ctx, bob)
	if err != nil {
		t.Fatalf("bob List: %v", err)
	}
	if len(bobList) != 2 {
		t.Fatalf("bob List: got %d want 2", len(bobList))
	}
}

// DNS-1123 validator tests ----------------------------------------------

// TestDNS1123Validator exercises the namespace name validator used by the
// custom-namespace feature (DECISIONS.md C, PLAN.md risk #5).
func TestDNS1123Validator(t *testing.T) {
	t.Parallel()
	good := []string{
		"podium-dev",
		"myteam-experiment",
		"a",
		"a1",
		"abc-123",
		strings.Repeat("a", 63),
	}
	for _, s := range good {
		if err := application.ValidateNamespaceName(s); err != nil {
			t.Errorf("ValidateNamespaceName(%q): unexpected err %v", s, err)
		}
	}
	bad := []string{
		"",
		"-leading-dash",
		"trailing-dash-",
		"UPPER",
		"with spaces",
		"under_score",
		"dot.dot",
		strings.Repeat("a", 64),
	}
	for _, s := range bad {
		if err := application.ValidateNamespaceName(s); err == nil {
			t.Errorf("ValidateNamespaceName(%q): expected err, got nil", s)
		}
	}
}

// TestDNS1123ValidatorMatchesK8s: the validator should accept the same
// strings K8s accepts. Spot-check against the published DNS-1123 regex from
// the Kubernetes API server (k8s.io/apimachinery/pkg/util/validation).
func TestDNS1123ValidatorMatchesK8s(t *testing.T) {
	t.Parallel()
	k8sDNS1123 := regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	for _, s := range []string{"podium-dev", "a1b2", "x", "abc-def-ghi"} {
		want := k8sDNS1123.MatchString(s)
		got := application.ValidateNamespaceName(s) == nil
		if want != got {
			t.Errorf("ValidateNamespaceName(%q): validator=%v, k8s=%v", s, got, want)
		}
	}
}

// testUserA / testUserB: the newCtx helper stashes alice/bob's IDs inside
// the returned Service via WithUsers; these helpers retrieve them. We
// return int64 values directly to keep tests concise.
func testUserA(ctx context.Context, svc *application.Service) int64 { return svc.UserAForTest(ctx) }
func testUserB(ctx context.Context, svc *application.Service) int64 { return svc.UserBForTest(ctx) }
