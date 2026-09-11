package api_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/podium/podium/internal/api"
	"github.com/podium/podium/internal/auth"
	"github.com/podium/podium/internal/kubernetes"
	"github.com/podium/podium/internal/storage"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// nsTestEnv mirrors api_test.go's testEnv but mounts the
// NamespacesHandler in addition to the standard auth/admin wiring.
// Uses a k8s fake clientset so EnsureNamespace / DeleteNamespace
// work without a real cluster.
type nsTestEnv struct {
	authSvc  *auth.Service
	store    *storage.Queries
	server   *httptest.Server
	adminTok string
	aliceTok string
	adminID  int64
	aliceID  int64
	fakeCS   *fake.Clientset
}

func newNsTestEnv(t *testing.T) *nsTestEnv {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "podium.db")
	db, err := storage.Open(context.Background(), path, storage.Options{})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := auth.SeedAdmin(context.Background(), db, "root", "root-password"); err != nil {
		t.Fatalf("SeedAdmin: %v", err)
	}
	authSvc := auth.NewService(db)
	store := storage.NewQueries(db)

	adminSess, err := authSvc.Login(context.Background(), "root", "root-password")
	if err != nil {
		t.Fatalf("admin login: %v", err)
	}

	in := auth.SignupInput{
		Username: fmt.Sprintf("alice-%d", uniqueUnix()),
		Email:    fmt.Sprintf("alice-%d@example.com", uniqueUnix()),
		Password: "alice-secret",
	}
	u, err := authSvc.Signup(context.Background(), in)
	if err != nil {
		t.Fatalf("alice signup: %v", err)
	}
	if err := authSvc.ApproveUser(context.Background(), u.ID); err != nil {
		t.Fatalf("approve alice: %v", err)
	}
	aliceSess, err := authSvc.Login(context.Background(), in.Username, in.Password)
	if err != nil {
		t.Fatalf("alice login: %v", err)
	}

	fakeCS := fake.NewSimpleClientset()
	k8sClient := &kubernetes.Client{CS: fakeCS, Source: "fake"}
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	mux := http.NewServeMux()
	api.MountAuth(mux, auth.NewHandler(authSvc))
	api.NewAdminHandler(authSvc).Mount(mux)
	api.NewNamespacesHandler(store, k8sClient, logger).Mount(mux)

	server := httptest.NewServer(api.New(mux, api.Deps{Auth: authSvc, Logger: logger}))
	t.Cleanup(server.Close)

	return &nsTestEnv{
		authSvc:  authSvc,
		store:    store,
		server:   server,
		adminTok: adminSess.Token,
		aliceTok: aliceSess.Token,
		adminID:  adminSess.User.ID,
		aliceID:  u.ID,
		fakeCS:   fakeCS,
	}
}

// nsCookie is a small helper for tests that bypass httpJSON to send
// raw JSON via httpJSON (which is in api_test.go and handles cookies
// through the authHeader helper).
func nsAuthHeader(tok string) map[string]string {
	return authHeader(tok)
}

// TestListNamespaces_DefaultSeeds: GET /api/namespaces as any
// authenticated user returns the three seeded defaults with
// is_default=true and deployment_count=0.
func TestListNamespaces_DefaultSeeds(t *testing.T) {
	t.Parallel()
	env := newNsTestEnv(t)

	resp, body := httpJSON(t, "GET", env.server.URL+"/api/namespaces", nil, nsAuthHeader(env.aliceTok))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: %d %s", resp.StatusCode, body)
	}
	var parsed struct {
		Namespaces []struct {
			Namespace       string `json:"namespace"`
			IsDefault       bool   `json:"is_default"`
			DeploymentCount int64  `json:"deployment_count"`
		} `json:"namespaces"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := []string{"podium-dev", "podium-staging", "podium-prod"}
	if len(parsed.Namespaces) != len(want) {
		t.Fatalf("len(namespaces)=%d want %d", len(parsed.Namespaces), len(want))
	}
	for i, n := range parsed.Namespaces {
		if n.Namespace != want[i] {
			t.Errorf("namespaces[%d].namespace=%q want %q", i, n.Namespace, want[i])
		}
		if !n.IsDefault {
			t.Errorf("namespaces[%d].is_default=false want true", i)
		}
		if n.DeploymentCount != 0 {
			t.Errorf("namespaces[%d].deployment_count=%d want 0", i, n.DeploymentCount)
		}
	}
}

// TestListNamespaces_IncludesDeploymentCount: create two deployments
// in podium-dev and confirm the count surfaces in the list response.
func TestListNamespaces_IncludesDeploymentCount(t *testing.T) {
	t.Parallel()
	env := newNsTestEnv(t)
	ctx := context.Background()

	res, err := env.store.DB().ExecContext(ctx,
		`INSERT INTO users (username, email, password_hash, role, status) VALUES ('u','u@x','x','USER','APPROVED')`)
	if err != nil {
		t.Fatal(err)
	}
	uid, _ := res.LastInsertId()
	res, err = env.store.DB().ExecContext(ctx,
		`INSERT INTO applications (user_id, name, repository_url, container_port) VALUES (?, 'svc', 'https://x', 8080)`,
		uid)
	if err != nil {
		t.Fatal(err)
	}
	realAppID, _ := res.LastInsertId()
	envID, err := env.store.EnvironmentIDByNamespace(ctx, "podium-dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.CreateDeployment(ctx, realAppID, envID, 1, 1, "a:v1"); err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.CreateDeployment(ctx, realAppID, envID, 2, 1, "a:v2"); err != nil {
		t.Fatal(err)
	}

	resp, body := httpJSON(t, "GET", env.server.URL+"/api/namespaces", nil, nsAuthHeader(env.aliceTok))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `"deployment_count":2`) {
		t.Errorf("expected deployment_count:2 in body, got %s", body)
	}
}

// TestListNamespaces_RequiresAuth: anonymous GET returns 401.
func TestListNamespaces_RequiresAuth(t *testing.T) {
	t.Parallel()
	env := newNsTestEnv(t)
	resp, body := httpJSON(t, "GET", env.server.URL+"/api/namespaces", nil, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("got %d %s", resp.StatusCode, body)
	}
}

// TestCreateNamespace_HappyPath: POST /api/namespaces with a valid
// DNS-1123 name creates both the K8s object and the SQLite row, and
// returns 201 with the new DTO.
func TestCreateNamespace_HappyPath(t *testing.T) {
	t.Parallel()
	env := newNsTestEnv(t)

	resp, body := httpJSON(t, "POST", env.server.URL+"/api/namespaces",
		map[string]any{"namespace": "smoke-test"}, nsAuthHeader(env.aliceTok))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}

	// K8s namespace must exist.
	if _, err := env.fakeCS.CoreV1().Namespaces().Get(context.Background(), "smoke-test", metav1.GetOptions{}); err != nil {
		t.Errorf("k8s namespace not created: %v", err)
	}
	// SQLite row must exist.
	if _, err := env.store.GetEnvironmentByNamespace(context.Background(), "smoke-test"); err != nil {
		t.Errorf("sqlite row missing: %v", err)
	}
	// Response body includes the new DTO.
	if !strings.Contains(string(body), `"namespace":"smoke-test"`) {
		t.Errorf("response missing namespace field: %s", body)
	}
	if !strings.Contains(string(body), `"is_default":false`) {
		t.Errorf("response missing is_default:false: %s", body)
	}
}

// TestCreateNamespace_RejectsBadDNS: namespace names that don't pass
// DNS-1123 return 400 invalid_namespace. Empty string is rejected by
// the explicit missing_namespace check.
func TestCreateNamespace_RejectsBadDNS(t *testing.T) {
	t.Parallel()
	env := newNsTestEnv(t)
	cases := []struct {
		name string
		code string
	}{
		{"BadName", "invalid_namespace"},
		{"with spaces", "invalid_namespace"},
		{"-leading-dash", "invalid_namespace"},
		{"x234567890123456789012345678901234567890123456789012345678901234", "invalid_namespace"}, // > 63 chars
		{"", "missing_namespace"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := httpJSON(t, "POST", env.server.URL+"/api/namespaces",
				map[string]any{"namespace": tc.name}, nsAuthHeader(env.aliceTok))
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("name=%q: status %d want 400, body=%s", tc.name, resp.StatusCode, body)
				return
			}
			if !strings.Contains(string(body), tc.code) {
				t.Errorf("name=%q: body=%s want code=%s", tc.name, body, tc.code)
			}
		})
	}
}

// TestCreateNamespace_RejectsDuplicate: posting a name that already
// exists (a seeded default or a previously created one) returns 409.
func TestCreateNamespace_RejectsDuplicate(t *testing.T) {
	t.Parallel()
	env := newNsTestEnv(t)

	resp, body := httpJSON(t, "POST", env.server.URL+"/api/namespaces",
		map[string]any{"namespace": "podium-dev"}, nsAuthHeader(env.aliceTok))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate default: status %d want 409, body=%s", resp.StatusCode, body)
	}

	// Create one then try to re-create.
	if _, err := env.fakeCS.CoreV1().Namespaces().Create(context.Background(),
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "once"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.EnsureEnvironment(context.Background(), "once"); err != nil {
		t.Fatal(err)
	}
	resp, body = httpJSON(t, "POST", env.server.URL+"/api/namespaces",
		map[string]any{"namespace": "once"}, nsAuthHeader(env.aliceTok))
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("second create: status %d want 409, body=%s", resp.StatusCode, body)
	}
}

// TestAdminDeleteNamespace_HappyPath: an admin can delete a custom
// namespace. After the call the K8s object is gone, the SQLite row is
// gone, and any deployments referencing the env are gone too.
func TestAdminDeleteNamespace_HappyPath(t *testing.T) {
	t.Parallel()
	env := newNsTestEnv(t)
	ctx := context.Background()

	// Seed a custom namespace with two deployments so we can verify
	// the bulk-delete wipes them.
	if _, err := env.fakeCS.CoreV1().Namespaces().Create(ctx,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "doomed"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	id, err := env.store.EnsureEnvironment(ctx, "doomed")
	if err != nil {
		t.Fatal(err)
	}
	res, _ := env.store.DB().ExecContext(ctx,
		`INSERT INTO users (username, email, password_hash, role, status) VALUES ('u2','u2@x','x','USER','APPROVED')`)
	uidID, _ := res.LastInsertId()
	res, _ = env.store.DB().ExecContext(ctx,
		`INSERT INTO applications (user_id, name, repository_url, container_port) VALUES (?, 'svc2', 'https://x', 8080)`,
		uidID)
	appID, _ := res.LastInsertId()
	for v := 1; v <= 2; v++ {
		if _, err := env.store.CreateDeployment(ctx, appID, id, v, 1, fmt.Sprintf("a:v%d", v)); err != nil {
			t.Fatal(err)
		}
	}

	resp, body := httpJSON(t, "DELETE",
		fmt.Sprintf("%s/api/admin/namespaces/%d", env.server.URL, id),
		nil, nsAuthHeader(env.adminTok))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete: %d %s", resp.StatusCode, body)
	}

	// K8s namespace gone.
	if _, err := env.fakeCS.CoreV1().Namespaces().Get(ctx, "doomed", metav1.GetOptions{}); err == nil {
		t.Error("k8s namespace still present")
	} else if !apierrors.IsNotFound(err) {
		t.Errorf("unexpected k8s error: %v", err)
	}
	// SQLite row gone.
	if _, err := env.store.GetEnvironment(ctx, id); err != sql.ErrNoRows {
		t.Errorf("sqlite row still present: err=%v", err)
	}
	// Deployments for the env are gone. The env row is gone so
	// CountDeploymentsByEnv returns 0 — but verify directly.
	var direct int
	_ = env.store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM deployments WHERE environment_id = ?`, id).Scan(&direct)
	if direct != 0 {
		t.Errorf("deployments remaining: %d want 0", direct)
	}
}

// TestAdminDeleteNamespace_RejectsDefault: trying to delete one of
// the three seeded defaults returns 400 cannot_delete_default and
// the row stays.
func TestAdminDeleteNamespace_RejectsDefault(t *testing.T) {
	t.Parallel()
	env := newNsTestEnv(t)
	ctx := context.Background()

	id, err := env.store.EnvironmentIDByNamespace(ctx, "podium-dev")
	if err != nil {
		t.Fatal(err)
	}
	resp, body := httpJSON(t, "DELETE",
		fmt.Sprintf("%s/api/admin/namespaces/%d", env.server.URL, id),
		nil, nsAuthHeader(env.adminTok))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d want 400, body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "cannot_delete_default") {
		t.Errorf("body=%s want cannot_delete_default", body)
	}

	// Row still present.
	if _, err := env.store.GetEnvironment(ctx, id); err != nil {
		t.Errorf("default env was deleted: %v", err)
	}
}

// TestAdminDeleteNamespace_RequiresAdmin: a non-admin caller gets
// 403 even for a valid id.
func TestAdminDeleteNamespace_RequiresAdmin(t *testing.T) {
	t.Parallel()
	env := newNsTestEnv(t)
	ctx := context.Background()

	if _, err := env.fakeCS.CoreV1().Namespaces().Create(ctx,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "guarded"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	id, err := env.store.EnsureEnvironment(ctx, "guarded")
	if err != nil {
		t.Fatal(err)
	}

	resp, body := httpJSON(t, "DELETE",
		fmt.Sprintf("%s/api/admin/namespaces/%d", env.server.URL, id),
		nil, nsAuthHeader(env.aliceTok))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin delete: %d want 403, body=%s", resp.StatusCode, body)
	}
	// Row still present.
	if _, err := env.store.GetEnvironment(ctx, id); err != nil {
		t.Errorf("namespace was deleted: %v", err)
	}
}

// TestAdminDeleteNamespace_UnknownID returns 404 for ids that don't
// match any environment row.
func TestAdminDeleteNamespace_UnknownID(t *testing.T) {
	t.Parallel()
	env := newNsTestEnv(t)
	resp, body := httpJSON(t, "DELETE",
		env.server.URL+"/api/admin/namespaces/99999",
		nil, nsAuthHeader(env.adminTok))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d want 404, body=%s", resp.StatusCode, body)
	}
}

// TestAdminDeleteNamespace_BadID: non-integer path segment returns 400.
func TestAdminDeleteNamespace_BadID(t *testing.T) {
	t.Parallel()
	env := newNsTestEnv(t)
	resp, body := httpJSON(t, "DELETE",
		env.server.URL+"/api/admin/namespaces/notanumber",
		nil, nsAuthHeader(env.adminTok))
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("got %d want 400, body=%s", resp.StatusCode, body)
	}
}

// TestCreateNamespace_BadJSON: a body that doesn't parse as JSON
// returns 400, not 500. Catches a handler that accidentally lets
// the error bubble as an unhandled exception.
func TestCreateNamespace_BadJSON(t *testing.T) {
	t.Parallel()
	env := newNsTestEnv(t)
	resp, body := httpJSON(t, "POST", env.server.URL+"/api/namespaces",
		bytes.NewBufferString(`{not json`), nsAuthHeader(env.aliceTok))
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("got %d want 400, body=%s", resp.StatusCode, body)
	}
}
