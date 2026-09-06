package api_test

import (
	"bytes"
	"context"
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
	"github.com/podium/podium/internal/application"
	"github.com/podium/podium/internal/auth"
	"github.com/podium/podium/internal/storage"
)

// helpers ----------------------------------------------------------------

type testEnv struct {
	authSvc   *auth.Service
	appSvc    *application.Service
	server    *httptest.Server
	adminID   int64
	adminUser auth.User
	adminTok  string
	aliceID   int64
	aliceTok  string
}

func newTestEnv(t *testing.T) *testEnv {
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
	appSvc := application.NewService(db)

	// Seed admin via Login so we can grab its id and a session token.
	sess, err := authSvc.Login(context.Background(), "root", "root-password")
	if err != nil {
		t.Fatalf("admin login: %v", err)
	}

	// One more approved user — alice.
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

	authH := auth.NewHandler(authSvc)
	appH := application.NewHandler(appSvc)
	adminH := api.NewAdminHandler(authSvc)
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	mux := http.NewServeMux()
	api.MountAuth(mux, authH)
	api.MountApplications(mux, appH)
	adminH.Mount(mux)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	server := httptest.NewServer(api.New(mux, api.Deps{Auth: authSvc, Logger: logger}))
	t.Cleanup(server.Close)

	return &testEnv{
		authSvc:   authSvc,
		appSvc:    appSvc,
		server:    server,
		adminID:   sess.User.ID,
		adminUser: sess.User,
		adminTok:  sess.Token,
		aliceID:   u.ID,
		aliceTok:  aliceSess.Token,
	}
}

func uniqueUnix() int64 {
	// Tests run in parallel, so embed the test name into usernames to dodge
	// UNIQUE collisions.
	return uniqueUnixCounter.Add(1)
}

// atomic counter hidden behind a package-level var so tests can reuse the
// helper without setting up sync.Mutex themselves.
var uniqueUnixCounter = newCounter()

type counter struct{ v int64 }

func newCounter() *counter { return &counter{} }

func (c *counter) Add(n int64) int64 {
	c.v += n
	return c.v
}

// httpJSON does request + parse JSON response.
func httpJSON(t *testing.T, method, url string, body any, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	var bodyR io.Reader
	if body != nil {
		buf, _ := json.Marshal(body)
		bodyR = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, url, bodyR)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, out
}

// authHeader returns the cookie header value for a given token.
func authHeader(token string) map[string]string {
	return map[string]string{"Cookie": fmt.Sprintf("%s=%s", auth.SessionCookieName, token)}
}

// /healthz is a sanity check on the wrapper itself — no cookie needed.
func TestHealthz(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	resp, body := httpJSON(t, "GET", env.server.URL+"/healthz", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status: got %d, body=%s", resp.StatusCode, body)
	}
}

// /api/auth/signup creates a PENDING user; the response includes the DTO.
// We don't need a session to call signup.
func TestSignupEndpoint(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	body := map[string]any{
		"username": fmt.Sprintf("signup-%d", uniqueUnix()),
		"email":    fmt.Sprintf("signup-%d@example.com", uniqueUnix()),
		"password": "longenough",
	}
	resp, b := httpJSON(t, "POST", env.server.URL+"/api/auth/signup", body, nil)
	if resp.StatusCode != 201 {
		t.Fatalf("signup status: got %d, body=%s", resp.StatusCode, b)
	}
}

// /api/auth/login must succeed for the approved admin.
func TestLoginEndpointSetsCookie(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	body := map[string]any{"username": env.adminUser.Username, "password": "root-password"}
	resp, b := httpJSON(t, "POST", env.server.URL+"/api/auth/login", body, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("login status: got %d, body=%s", resp.StatusCode, b)
	}
	found := false
	for _, c := range resp.Cookies() {
		if c.Name == auth.SessionCookieName && c.Value != "" {
			found = true
			if !c.HttpOnly {
				t.Error("expected HttpOnly on session cookie")
			}
			if c.SameSite != http.SameSiteLaxMode {
				t.Errorf("SameSite: got %v, want Lax", c.SameSite)
			}
		}
	}
	if !found {
		t.Fatalf("no session cookie set; body=%s", b)
	}
}

// /api/auth/me returns the authenticated user.
func TestMeEndpoint(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	resp, body := httpJSON(t, "GET", env.server.URL+"/api/auth/me", nil, authHeader(env.aliceTok))
	if resp.StatusCode != 200 {
		t.Fatalf("me status: got %d, body=%s", resp.StatusCode, body)
	}
	var parsed struct {
		User struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
			Role     string `json:"role"`
		} `json:"user"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.User.ID != env.aliceID {
		t.Errorf("user id: got %d want %d", parsed.User.ID, env.aliceID)
	}
	if parsed.User.Role != auth.RoleUser {
		t.Errorf("role: got %q want %q", parsed.User.Role, auth.RoleUser)
	}
}

// /api/auth/me without a cookie returns 401 (RequireAuth honoured).
func TestMeEndpointRequiresAuth(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	resp, body := httpJSON(t, "GET", env.server.URL+"/api/auth/me", nil, nil)
	if resp.StatusCode != 401 {
		t.Fatalf("status: got %d, body=%s", resp.StatusCode, body)
	}
}

// TestApplicationCreateAndList: a logged-in user can create an application
// and see it in their list; another user cannot see it.
func TestApplicationCreateAndList(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	create := map[string]any{
		"name":           fmt.Sprintf("svc-%d", uniqueUnix()),
		"repository_url": "https://github.com/example/api",
		"container_port": 8080,
	}
	resp, body := httpJSON(t, "POST", env.server.URL+"/api/applications", create, authHeader(env.aliceTok))
	if resp.StatusCode != 201 {
		t.Fatalf("create status: got %d body=%s", resp.StatusCode, body)
	}
	var created struct {
		Application struct {
			ID            int64 `json:"id"`
			UserID        int64 `json:"user_id"`
			ContainerPort int   `json:"container_port"`
		} `json:"application"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if created.Application.UserID != env.aliceID {
		t.Fatalf("user_id: got %d want %d", created.Application.UserID, env.aliceID)
	}

	// Alice's list contains it.
	resp, body = httpJSON(t, "GET", env.server.URL+"/api/applications", nil, authHeader(env.aliceTok))
	if resp.StatusCode != 200 {
		t.Fatalf("list status: %d body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "applications") {
		t.Fatalf("list body missing applications field: %s", body)
	}

	// Admin's list does NOT contain alice's app (ownership isolation).
	resp, body = httpJSON(t, "GET", env.server.URL+"/api/applications", nil, authHeader(env.adminTok))
	if resp.StatusCode != 200 {
		t.Fatalf("admin list status: %d body=%s", resp.StatusCode, body)
	}
	if strings.Contains(string(body), fmt.Sprintf(`"id":%d`, created.Application.ID)) {
		t.Fatalf("admin saw alice's app in their list: %s", body)
	}
}

// TestAdminEndpointsRejectNonAdmin covers RequireAdmin behaviour: a non-admin
// caller hitting /api/admin/users gets 403.
func TestAdminEndpointsRejectNonAdmin(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	resp, body := httpJSON(t, "GET", env.server.URL+"/api/admin/users", nil, authHeader(env.aliceTok))
	if resp.StatusCode != 403 {
		t.Fatalf("status: got %d, body=%s", resp.StatusCode, body)
	}
}

// TestAdminListUsers: admin can list users.
func TestAdminListUsers(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	resp, body := httpJSON(t, "GET", env.server.URL+"/api/admin/users", nil, authHeader(env.adminTok))
	if resp.StatusCode != 200 {
		t.Fatalf("status: got %d, body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), env.adminUser.Username) {
		t.Fatalf("expected admin username in body, got %s", body)
	}
}

// TestAdminApproveFlow: signup -> admin sees PENDING -> admin approves ->
// user can now log in. The full §48 acceptance flow (minus Docker/K8s).
func TestAdminApproveFlow(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	// 1. Someone signs up.
	uname := fmt.Sprintf("newby-%d", uniqueUnix())
	signup := map[string]any{
		"username": uname,
		"email":    fmt.Sprintf("newby-%d@example.com", uniqueUnix()),
		"password": "newby-secret",
	}
	resp, body := httpJSON(t, "POST", env.server.URL+"/api/auth/signup", signup, nil)
	if resp.StatusCode != 201 {
		t.Fatalf("signup: %d %s", resp.StatusCode, body)
	}

	// 2. Admin lists users, finds the new pending one.
	resp, body = httpJSON(t, "GET", env.server.URL+"/api/admin/users", nil, authHeader(env.adminTok))
	if resp.StatusCode != 200 {
		t.Fatalf("admin list: %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), uname) {
		t.Fatalf("new user not in admin list: %s", body)
	}

	// 3. Admin approves by id (extracted from the previous body).
	var list struct {
		Users []struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
		} `json:"users"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("list unmarshal: %v", err)
	}
	var newID int64
	for _, u := range list.Users {
		if u.Username == uname {
			newID = u.ID
			break
		}
	}
	if newID == 0 {
		t.Fatal("new user id not found in list")
	}

	resp, body = httpJSON(t, "POST", fmt.Sprintf("%s/api/admin/users/%d/approve", env.server.URL, newID), nil, authHeader(env.adminTok))
	if resp.StatusCode != 200 {
		t.Fatalf("approve: %d %s", resp.StatusCode, body)
	}

	// 4. New user can now log in.
	resp, body = httpJSON(t, "POST", env.server.URL+"/api/auth/login",
		map[string]any{"username": uname, "password": "newby-secret"}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("login after approve: %d %s", resp.StatusCode, body)
	}
}

// Cross-user app access returns 404 (AGENTS.md §19 + PLAN.md risk #5).
func TestApplicationCrossUserAccessReturnsNotFound(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	create := map[string]any{
		"name":           fmt.Sprintf("hide-%d", uniqueUnix()),
		"repository_url": "https://github.com/x/y",
		"container_port": 80,
	}
	resp, body := httpJSON(t, "POST", env.server.URL+"/api/applications", create, authHeader(env.aliceTok))
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	var created struct {
		Application struct {
			ID int64 `json:"id"`
		} `json:"application"`
	}
	_ = json.Unmarshal(body, &created)

	// Admin hits alice's app with a different user_id — gets 404.
	resp, body = httpJSON(t, "GET", fmt.Sprintf("%s/api/applications/%d", env.server.URL, created.Application.ID), nil, authHeader(env.adminTok))
	if resp.StatusCode != 404 {
		t.Fatalf("status: got %d, body=%s", resp.StatusCode, body)
	}
}

// TestMiddlewarePanicRecover: a handler that panics must not crash the
// server. Recover catches it and returns 500.
func TestMiddlewarePanicRecover(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	// Construct a tiny mux that includes a panicking handler, then run a
	// request through api.New (which installs Recover).
	custom := http.NewServeMux()
	custom.HandleFunc("GET /boom", func(w http.ResponseWriter, r *http.Request) {
		panic("oh no")
	})

	srv := httptest.NewServer(api.New(custom, api.Deps{Auth: env.authSvc, Logger: quietLogger()}))
	t.Cleanup(srv.Close)

	resp, _ := http.Get(srv.URL + "/boom")
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("panic recovery status: got %d, want 500", resp.StatusCode)
	}
}

// quietLogger returns a slog logger that discards output. Used by tests that
// only care about behaviour, not logging output.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}
