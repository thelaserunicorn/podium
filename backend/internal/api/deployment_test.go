package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/podium/podium/internal/application"
	"github.com/podium/podium/internal/auth"
	"github.com/podium/podium/internal/deployment"
	"github.com/podium/podium/internal/docker"
	"github.com/podium/podium/internal/storage"
)

// slowBuilder blocks until released or ctx cancels.
type slowBuilder struct {
	mu       sync.Mutex
	released bool
}

func (s *slowBuilder) Build(ctx context.Context, _, _ string, sink docker.LogSink) error {
	s.mu.Lock()
	r := s.released
	s.mu.Unlock()
	if r {
		return nil
	}
	_ = sink.Append("started")
	select {
	case <-time.After(800 * time.Millisecond):
	case <-ctx.Done():
		return ctx.Err()
	}
	_ = sink.Append("done")
	s.mu.Lock()
	s.released = true
	s.mu.Unlock()
	return nil
}
func (s *slowBuilder) LoadIntoKind(context.Context, string, string) error { return nil }

// instantBuilder returns success immediately.
type instantBuilder struct{}

func (instantBuilder) Build(_ context.Context, _, _ string, sink docker.LogSink) error {
	_ = sink.Append("Step 1/1 : FROM scratch")
	_ = sink.Append("Successfully built")
	return nil
}
func (instantBuilder) LoadIntoKind(context.Context, string, string) error { return nil }

// errBuilder fails the build.
type errBuilder struct{}

func (errBuilder) Build(_ context.Context, _, _ string, sink docker.LogSink) error {
	_ = sink.Append("Step 1/1 : FROM scratch")
	return errors.New("boom")
}
func (errBuilder) LoadIntoKind(context.Context, string, string) error { return nil }

// fixture wires up the mux + auth + a session cookie for alice. It
// returns the mux, the storage queries, the cookie, alice's user id,
// and the orchestrator-builder slot so tests can swap in a custom
// builder.
type fixture struct {
	mux     http.Handler
	store   *storage.Queries
	cookie  string
	aliceID int64
	appID   int64

	builderMu sync.Mutex
	builder   docker.Builder
	fetcher   docker.SourceFetcher
	orch      *deployment.Orchestrator
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db := storage.OpenInMemoryForTest(t)
	q := storage.NewQueries(db)
	authSvc := auth.NewService(db)
	appSvc := application.NewService(db)
	ctx := context.Background()

	// Seed alice + admin directly so we don't fight the PENDING default.
	hash, _ := auth.HashPassword("alice-secret")
	// Pre-seed a dummy user so alice lands on id=2 — same shape as
	// production where the admin row gets id=1. This catches apps.Get
	// (id, userID) argument-order swaps in the deploy handler.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO users (username, email, password_hash, role, status) VALUES ('seed', 'seed@example.com', ?, 'ADMIN', 'APPROVED')`,
		hash); err != nil {
		t.Fatalf("insert seed: %v", err)
	}
	res, err := db.ExecContext(ctx,
		`INSERT INTO users (username, email, password_hash, role, status) VALUES ('alice', 'alice@example.com', ?, 'USER', 'APPROVED')`,
		hash)
	if err != nil {
		t.Fatalf("insert alice: %v", err)
	}
	aliceID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId alice: %v", err)
	}
	res, err = db.ExecContext(ctx,
		`INSERT INTO users (username, email, password_hash, role, status) VALUES ('admin', 'admin@example.com', ?, 'ADMIN', 'APPROVED')`,
		hash)
	if err != nil {
		t.Fatalf("insert admin: %v", err)
	}
	_, _ = res.LastInsertId()

	app, err := appSvc.Create(ctx, application.CreateInput{
		Name: "my-api", RepositoryURL: "https://github.com/x/y", ContainerPort: 8080,
		UserID: aliceID,
	})
	if err != nil {
		t.Fatalf("create app: %v", err)
	}

	// DeleteDeployment needs the queries handle; WithoutQueries is the
	// historical default for older tests.
	appSvc = appSvc.WithQueries(q)

	mux := http.NewServeMux()
	MountAuth(mux, auth.NewHandler(authSvc))
	MountApplications(mux, application.NewHandler(appSvc))

	builder := instantBuilder{}
	fetcher := nopFetcher{}
	orch := deployment.NewOrchestrator(q, builder, fetcher, t.TempDir(), deployment.Timeouts{Build: 5 * time.Second}, nil)
	NewDeploymentHandler(q, appSvc, orch, nil).Mount(mux)

	wrapped := New(mux, Deps{Auth: authSvc})

	// Login alice to obtain a session cookie.
	body := bytes.NewBufferString(`{"username":"alice","password":"alice-secret"}`)
	req := httptest.NewRequest("POST", "/api/auth/login", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
	}
	cookie := rec.Header().Get("Set-Cookie")
	if cookie == "" {
		t.Fatal("no cookie")
	}
	// Strip Set-Cookie attributes so the request Cookie header is just
	// "name=value".
	if i := strings.IndexByte(cookie, ';'); i > 0 {
		cookie = cookie[:i]
	}

	f := &fixture{
		mux: wrapped, store: q, cookie: cookie,
		aliceID: aliceID, appID: app.ID,
		builder: builder, fetcher: fetcher, orch: orch,
	}
	t.Cleanup(func() { _ = appSvc.Delete(ctx, app.ID, aliceID) })
	return f
}

// withBuilder swaps the orchestrator's builder for the supplied one.
// Tests use this to inject a slow or failing builder.
func (f *fixture) withBuilder(t *testing.T, b docker.Builder) {
	t.Helper()
	orch := deployment.NewOrchestrator(f.store, b, f.fetcher, t.TempDir(), deployment.Timeouts{Build: 5 * time.Second}, nil)
	h := NewDeploymentHandler(f.store, application.NewService(f.store.DB()), orch, nil)
	fresh := http.NewServeMux()
	MountAuth(fresh, auth.NewHandler(auth.NewService(f.store.DB())))
	MountApplications(fresh, application.NewHandler(application.NewService(f.store.DB())))
	h.Mount(fresh)
	wrapped := New(fresh, Deps{Auth: auth.NewService(f.store.DB())})
	f.mux = wrapped
	f.orch = orch
}

func TestDeploy_CreatesDeploymentAndPersistsStatus(t *testing.T) {
	f := newFixture(t)

	body := bytes.NewBufferString(`{"namespace":"podium-dev","replicas":3}`)
	req := httptest.NewRequest("POST", "/api/applications/1/deploy", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", f.cookie)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("deploy: %d %s", rec.Code, rec.Body.String())
	}
	var resp deployResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Deployment == nil || resp.Deployment.ID == 0 {
		t.Fatal("no deployment in response")
	}
	if resp.Deployment.Status != storage.StatusQueued {
		t.Errorf("status=%q want QUEUED", resp.Deployment.Status)
	}

	// Wait for orchestrator to finish.
	waitForTerminal(t, f.store, resp.Deployment.ID, 2*time.Second)
	d, _ := f.store.GetDeployment(context.Background(), resp.Deployment.ID)
	if d.Status != storage.StatusBuilt {
		t.Errorf("status=%q want BUILT", d.Status)
	}
}

func TestDeploy_FailedBuildReturnsFailedStatus(t *testing.T) {
	f := newFixture(t)
	f.withBuilder(t, errBuilder{})

	body := bytes.NewBufferString(`{"namespace":"podium-dev"}`)
	req := httptest.NewRequest("POST", "/api/applications/1/deploy", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", f.cookie)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("deploy: %d", rec.Code)
	}
	var resp deployResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	waitForTerminal(t, f.store, resp.Deployment.ID, 2*time.Second)

	d, _ := f.store.GetDeployment(context.Background(), resp.Deployment.ID)
	if d.Status != storage.StatusFailed {
		t.Errorf("status=%q want FAILED", d.Status)
	}
	if d.Reason.String != "build_failed" {
		t.Errorf("reason=%q", d.Reason.String)
	}
}

func TestDeploy_RejectsConcurrentWith409(t *testing.T) {
	f := newFixture(t)
	f.withBuilder(t, &slowBuilder{})

	body := bytes.NewBufferString(`{"namespace":"podium-dev"}`)
	req := httptest.NewRequest("POST", "/api/applications/1/deploy", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", f.cookie)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first deploy: %d", rec.Code)
	}
	var resp deployResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)

	// Second deploy while first is still running.
	body2 := bytes.NewBufferString(`{"namespace":"podium-dev"}`)
	req2 := httptest.NewRequest("POST", "/api/applications/1/deploy", body2)
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Cookie", f.cookie)
	rec2 := httptest.NewRecorder()
	f.mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusConflict {
		t.Errorf("second deploy: %d %s", rec2.Code, rec2.Body.String())
	}

	waitForTerminal(t, f.store, resp.Deployment.ID, 3*time.Second)
}

func TestLogs_StreamsBuildLines(t *testing.T) {
	f := newFixture(t)

	body := bytes.NewBufferString(`{"namespace":"podium-dev"}`)
	req := httptest.NewRequest("POST", "/api/applications/1/deploy", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", f.cookie)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	var resp deployResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	waitForTerminal(t, f.store, resp.Deployment.ID, 2*time.Second)

	req2 := httptest.NewRequest("GET", "/api/deployments/"+itoa(resp.Deployment.ID)+"/logs", nil)
	req2.Header.Set("Cookie", f.cookie)
	rec2 := httptest.NewRecorder()
	f.mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("logs: %d %s", rec2.Code, rec2.Body.String())
	}
	var logsResp struct {
		Lines []logLineJSON `json:"lines"`
	}
	_ = json.NewDecoder(rec2.Body).Decode(&logsResp)
	if len(logsResp.Lines) < 2 {
		t.Errorf("expected at least 2 log lines, got %d", len(logsResp.Lines))
	}
}

func TestDeploy_RequiresAuth(t *testing.T) {
	f := newFixture(t)
	req := httptest.NewRequest("POST", "/api/applications/1/deploy", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("got %d want 401", rec.Code)
	}
}

func TestDeploy_RejectsInvalidNamespace(t *testing.T) {
	f := newFixture(t)
	body := bytes.NewBufferString(`{"namespace":"Bad Name"}`)
	req := httptest.NewRequest("POST", "/api/applications/1/deploy", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", f.cookie)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d want 400", rec.Code)
	}
}

// recordingApplier implements deployment.K8sApplier. The fixture's
// default fixture (newFixture) wires no k8s applier so Scale/Restart
// tests would 502 by default; this stub lets us assert the handler
// forwards the right args when a real applier is wired.
type recordingApplier struct {
	mu       sync.Mutex
	scales   []scaleCall
	restarts []restartCall
	applies  []int64 // deployment IDs handed to Apply (rollback path)
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

func (r *recordingApplier) Apply(_ context.Context, id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.applies = append(r.applies, id)
	return r.err
}

func (r *recordingApplier) Scale(_ context.Context, app *application.Application, ns string, replicas int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scales = append(r.scales, scaleCall{app: app, ns: ns, replicas: replicas})
	return r.err
}

func (r *recordingApplier) Restart(_ context.Context, app *application.Application, ns string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.restarts = append(r.restarts, restartCall{app: app, ns: ns})
	return r.err
}

// fixtureWithApplier rebuilds the mux with a working orchestrator so
// Scale / Restart can exercise the happy path. It mirrors the shape of
// newFixture but lets the test inject the applier.
func fixtureWithApplier(t *testing.T, applier deployment.K8sApplier) *fixture {
	t.Helper()
	f := newFixture(t)

	orch := deployment.NewOrchestrator(f.store, f.builder, f.fetcher, t.TempDir(),
		deployment.Timeouts{Build: 5 * time.Second}, applier)
	h := NewDeploymentHandler(f.store, application.NewService(f.store.DB()), orch, nil)
	fresh := http.NewServeMux()
	MountAuth(fresh, auth.NewHandler(auth.NewService(f.store.DB())))
	MountApplications(fresh, application.NewHandler(application.NewService(f.store.DB())))
	h.Mount(fresh)
	wrapped := New(fresh, Deps{Auth: auth.NewService(f.store.DB())})
	f.mux = wrapped
	f.orch = orch
	return f
}

func TestScale_HappyPathForwardsToApplier(t *testing.T) {
	app := &recordingApplier{}
	f := fixtureWithApplier(t, app)

	body := bytes.NewBufferString(`{"namespace":"podium-dev","replicas":4}`)
	req := httptest.NewRequest("POST", "/api/applications/1/scale", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", f.cookie)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("scale: %d %s", rec.Code, rec.Body.String())
	}

	app.mu.Lock()
	defer app.mu.Unlock()
	if len(app.scales) != 1 {
		t.Fatalf("scales=%d want 1", len(app.scales))
	}
	got := app.scales[0]
	if got.ns != "podium-dev" {
		t.Errorf("ns=%q want podium-dev", got.ns)
	}
	if got.replicas != 4 {
		t.Errorf("replicas=%d want 4", got.replicas)
	}
	if got.app == nil || got.app.ID != f.appID {
		t.Errorf("app mismatch: %+v", got.app)
	}
}

func TestScale_RejectsBadReplicas(t *testing.T) {
	f := fixtureWithApplier(t, &recordingApplier{})
	for _, n := range []int{0, 6, -1} {
		body := bytes.NewBufferString(fmt.Sprintf(`{"namespace":"podium-dev","replicas":%d}`, n))
		req := httptest.NewRequest("POST", "/api/applications/1/scale", body)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Cookie", f.cookie)
		rec := httptest.NewRecorder()
		f.mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("replicas=%d got %d want 400", n, rec.Code)
		}
	}
}

func TestScale_RejectsMissingReplicas(t *testing.T) {
	f := fixtureWithApplier(t, &recordingApplier{})
	body := bytes.NewBufferString(`{"namespace":"podium-dev"}`)
	req := httptest.NewRequest("POST", "/api/applications/1/scale", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", f.cookie)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d want 400", rec.Code)
	}
}

func TestScale_RequiresAuth(t *testing.T) {
	f := fixtureWithApplier(t, &recordingApplier{})
	body := bytes.NewBufferString(`{"namespace":"podium-dev","replicas":2}`)
	req := httptest.NewRequest("POST", "/api/applications/1/scale", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("got %d want 401", rec.Code)
	}
}

func TestScale_ApplierErrorBecomes502(t *testing.T) {
	f := fixtureWithApplier(t, &recordingApplier{err: errors.New("connection refused")})
	body := bytes.NewBufferString(`{"namespace":"podium-dev","replicas":3}`)
	req := httptest.NewRequest("POST", "/api/applications/1/scale", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", f.cookie)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("got %d want 502", rec.Code)
	}
}

func TestRestart_HappyPathForwardsToApplier(t *testing.T) {
	app := &recordingApplier{}
	f := fixtureWithApplier(t, app)

	body := bytes.NewBufferString(`{"namespace":"podium-staging"}`)
	req := httptest.NewRequest("POST", "/api/applications/1/restart", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", f.cookie)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("restart: %d %s", rec.Code, rec.Body.String())
	}

	app.mu.Lock()
	defer app.mu.Unlock()
	if len(app.restarts) != 1 {
		t.Fatalf("restarts=%d want 1", len(app.restarts))
	}
	if got := app.restarts[0]; got.ns != "podium-staging" || got.app == nil || got.app.ID != f.appID {
		t.Errorf("restart args mismatch: %+v", got)
	}
}

func TestRestart_RejectsInvalidNamespace(t *testing.T) {
	f := fixtureWithApplier(t, &recordingApplier{})
	body := bytes.NewBufferString(`{"namespace":"Bad Name"}`)
	req := httptest.NewRequest("POST", "/api/applications/1/restart", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", f.cookie)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d want 400", rec.Code)
	}
}

func TestRestart_RequiresAuth(t *testing.T) {
	f := fixtureWithApplier(t, &recordingApplier{})
	body := bytes.NewBufferString(`{"namespace":"podium-dev"}`)
	req := httptest.NewRequest("POST", "/api/applications/1/restart", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("got %d want 401", rec.Code)
	}
}

func TestRestart_ApplierErrorBecomes502(t *testing.T) {
	f := fixtureWithApplier(t, &recordingApplier{err: errors.New("connection refused")})
	body := bytes.NewBufferString(`{"namespace":"podium-dev"}`)
	req := httptest.NewRequest("POST", "/api/applications/1/restart", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", f.cookie)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("got %d want 502", rec.Code)
	}
}

// helpers ------------------------------------------------------------

func waitForTerminal(t *testing.T, q *storage.Queries, depID int64, max time.Duration) {
	t.Helper()
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		d, _ := q.GetDeployment(context.Background(), depID)
		if d.Status == storage.StatusBuilt || d.Status == storage.StatusFailed {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("deployment %d did not reach terminal state within %v", depID, max)
}

func itoa(i int64) string {
	return jsonNumber(i)
}

func jsonNumber(i int64) string {
	b, _ := json.Marshal(i)
	return string(b)
}

// nopFetcher succeeds without cloning anything.
type nopFetcher struct{}

func (nopFetcher) Fetch(_ context.Context, _, _ string) error { return nil }

// rollbackFixture wires a fixture where a real applier is reachable AND
// four prior RUNNING deployments already exist in podium-dev. The
// returned depIDs[3] is the latest successful — passing it as the URL
// id for POST /rollback is the common "roll back to v3" happy path.
//
// The returned deploymentID is the id of the latest row in the
// history; the Rollback handler reads its (app, env) so we don't have
// to duplicate the resolution in every test.
func rollbackFixture(t *testing.T, applier *recordingApplier) (*fixture, []int64) {
	t.Helper()
	f := fixtureWithApplier(t, applier)

	ctx := context.Background()
	envID, err := f.store.EnvironmentIDByNamespace(ctx, "podium-dev")
	if err != nil {
		t.Fatal(err)
	}
	depIDs := make([]int64, 0, 4)
	for v := 1; v <= 4; v++ {
		id, err := f.store.CreateDeployment(ctx, f.appID, envID, v, 3, fmt.Sprintf("my-api:v%d", v))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.store.SetDeploymentStatus(ctx, id, storage.StatusRunning, ""); err != nil {
			t.Fatal(err)
		}
		// Mirror what ApplicationNextVersion would do so the post-rollback
		// version lands at 5 instead of starting the counter at 0.
		if _, err := f.store.DB().ExecContext(ctx,
			`UPDATE applications SET version = ? WHERE id = ?`, v, f.appID); err != nil {
			t.Fatal(err)
		}
		depIDs = append(depIDs, id)
	}
	return f, depIDs
}

// TestRollback_HappyPathForwardsToApplier and friends. We exercise the
// handler around a depID that belongs to one of alice's apps (the one
// newFixture creates).
func TestRollback_HappyPathForwardsToApplier(t *testing.T) {
	applier := &recordingApplier{}
	f, depIDs := rollbackFixture(t, applier)

	// Rollback the newest deployment row. Per-namespace scope means
	// the request body doesn't carry the namespace; the handler picks
	// env from the existing row.
	body := bytes.NewBufferString(`{}`)
	req := httptest.NewRequest("POST", "/api/deployments/"+itoa(depIDs[3])+"/rollback", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", f.cookie)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("rollback: %d %s", rec.Code, rec.Body.String())
	}
	var resp deployResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Deployment == nil {
		t.Fatal("no deployment in response")
	}
	// target_version=0 → image reuses the most recent successful row's bytes.
	if resp.Deployment.Image != "my-api:v4" {
		t.Errorf("image=%q want my-api:v4", resp.Deployment.Image)
	}
	if resp.Deployment.Version != 5 {
		t.Errorf("version=%d want 5 (counter bumped from 4)", resp.Deployment.Version)
	}
	if resp.Deployment.Status != storage.StatusDeploying && resp.Deployment.Status != storage.StatusStarting && resp.Deployment.Status != storage.StatusRunning {
		t.Errorf("status=%q want DEPLOYING/STARTING/RUNNING", resp.Deployment.Status)
	}

	applier.mu.Lock()
	defer applier.mu.Unlock()
	if len(applier.applies) != 1 {
		t.Fatalf("applies=%d want 1", len(applier.applies))
	}
	if applier.applies[0] != resp.Deployment.ID {
		t.Errorf("applies[0]=%d want %d", applier.applies[0], resp.Deployment.ID)
	}
}

func TestRollback_ToSpecificVersion(t *testing.T) {
	applier := &recordingApplier{}
	f, depIDs := rollbackFixture(t, applier)

	body := bytes.NewBufferString(`{"target_version":2}`)
	req := httptest.NewRequest("POST", "/api/deployments/"+itoa(depIDs[3])+"/rollback", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", f.cookie)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("rollback: %d %s", rec.Code, rec.Body.String())
	}
	var resp deployResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Deployment.Image != "my-api:v2" {
		t.Errorf("image=%q want my-api:v2", resp.Deployment.Image)
	}
	if resp.Deployment.Version != 5 {
		t.Errorf("version=%d want 5", resp.Deployment.Version)
	}
}

func TestRollback_UnknownVersionReturns400(t *testing.T) {
	applier := &recordingApplier{}
	f, depIDs := rollbackFixture(t, applier)

	body := bytes.NewBufferString(`{"target_version":999}`)
	req := httptest.NewRequest("POST", "/api/deployments/"+itoa(depIDs[3])+"/rollback", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", f.cookie)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("rollback: %d %s want 400", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "unknown_target_version") {
		t.Errorf("body=%q want unknown_target_version", rec.Body.String())
	}
}

func TestRollback_NoSuccessfulReturns400(t *testing.T) {
	applier := &recordingApplier{}
	f, depIDs := rollbackFixture(t, applier)
	// Mark every seeded row FAILED so LatestSuccessfulDeployment
	// returns sql.ErrNoRows → the handler should surface 400.
	for _, id := range depIDs {
		if err := f.store.SetDeploymentStatus(context.Background(), id, storage.StatusFailed, "boom"); err != nil {
			t.Fatal(err)
		}
	}

	body := bytes.NewBufferString(`{}`)
	req := httptest.NewRequest("POST", "/api/deployments/"+itoa(depIDs[3])+"/rollback", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", f.cookie)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("rollback: %d %s want 400", rec.Code, rec.Body.String())
	}
}

func TestRollback_RequiresAuth(t *testing.T) {
	applier := &recordingApplier{}
	f, depIDs := rollbackFixture(t, applier)

	body := bytes.NewBufferString(`{}`)
	req := httptest.NewRequest("POST", "/api/deployments/"+itoa(depIDs[3])+"/rollback", body)
	req.Header.Set("Content-Type", "application/json")
	// no cookie
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("got %d want 401", rec.Code)
	}
}

func TestRollback_NotFoundWhenCrossUserDeployment(t *testing.T) {
	applier := &recordingApplier{}
	f, _ := rollbackFixture(t, applier)

	// Insert a deployment for a *different* user's app. The handler
	// must surface 404 (we never leak existence — AGENTS.md §19).
	db := f.store.DB()
	res, _ := db.ExecContext(context.Background(),
		`INSERT INTO users (username, email, password_hash, role, status) VALUES ('bob','bob@x','x','USER','APPROVED')`)
	bobID, _ := res.LastInsertId()
	res, _ = db.ExecContext(context.Background(),
		`INSERT INTO applications (user_id, name, repository_url, container_port) VALUES (?,'bob-app','https://github.com/x/y',80)`,
		bobID)
	bobAppID, _ := res.LastInsertId()
	envID, _ := f.store.EnvironmentIDByNamespace(context.Background(), "podium-dev")
	bobDepID, _ := f.store.CreateDeployment(context.Background(), bobAppID, envID, 1, 1, "bob-app:v1")
	f.store.SetDeploymentStatus(context.Background(), bobDepID, storage.StatusRunning, "")

	body := bytes.NewBufferString(`{}`)
	req := httptest.NewRequest("POST", "/api/deployments/"+itoa(bobDepID)+"/rollback", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", f.cookie)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d want 404 (don't leak existence)", rec.Code)
	}
}

func TestRollback_NegativeTargetVersionReturns400(t *testing.T) {
	applier := &recordingApplier{}
	f, depIDs := rollbackFixture(t, applier)

	body := bytes.NewBufferString(`{"target_version":-1}`)
	req := httptest.NewRequest("POST", "/api/deployments/"+itoa(depIDs[3])+"/rollback", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", f.cookie)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d want 400", rec.Code)
	}
}

// TestDeleteDeployment_HappyPathSoftDeletesAndHidesFromList fires
// a real deploy through the orchestrator, waits for it to finish,
// then DELETE /api/deployments/{id}. Expects 204; the deployment
// must vanish from GET /api/applications/{id}/deployments.
func TestDeleteDeployment_HappyPathSoftDeletesAndHidesFromList(t *testing.T) {
	f := newFixture(t)

	depID := createAndWaitForTerminalDeployment(t, f)

	req := httptest.NewRequest("DELETE", fmt.Sprintf("/api/deployments/%d", depID), nil)
	req.Header.Set("Cookie", f.cookie)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE: got %d want 204; body=%s", rec.Code, rec.Body.String())
	}

	// Confirm hidden from the per-app deployment list.
	req = httptest.NewRequest("GET", fmt.Sprintf("/api/applications/%d/deployments?namespace=podium-dev", f.appID), nil)
	req.Header.Set("Cookie", f.cookie)
	rec = httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ListDeployments: %d %s", rec.Code, rec.Body.String())
	}
	var listed struct {
		Deployments []*storage.Deployment `json:"deployments"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&listed)
	if len(listed.Deployments) != 0 {
		t.Errorf("deleted deployment still in list: %d", len(listed.Deployments))
	}

	// Direct GetDeployment still finds the row but DeletedAt is set.
	d, err := f.store.GetDeployment(context.Background(), depID)
	if err != nil {
		t.Fatalf("GetDeployment: %v", err)
	}
	if !d.DeletedAt.Valid {
		t.Errorf("DeletedAt not stamped; got %+v", d)
	}
}

// TestDeleteDeployment_RequiresAuth hits DELETE without a session
// cookie and expects 401.
func TestDeleteDeployment_RequiresAuth(t *testing.T) {
	f := newFixture(t)
	depID := createAndWaitForTerminalDeployment(t, f)

	req := httptest.NewRequest("DELETE", fmt.Sprintf("/api/deployments/%d", depID), nil)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("DELETE without cookie: got %d want 401", rec.Code)
	}
}

// TestDeleteDeployment_NotFoundForUnknownID confirms DELETE on an id
// no one has ever seen returns 404.
func TestDeleteDeployment_NotFoundForUnknownID(t *testing.T) {
	f := newFixture(t)

	req := httptest.NewRequest("DELETE", "/api/deployments/99999", nil)
	req.Header.Set("Cookie", f.cookie)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d want 404", rec.Code)
	}
}

// TestDeleteDeployment_InvalidIDReturns400 confirms a non-integer
// path segment is rejected with 400 (the contract from every other
// handler in this package).
func TestDeleteDeployment_InvalidIDReturns400(t *testing.T) {
	f := newFixture(t)

	req := httptest.NewRequest("DELETE", "/api/deployments/notanumber", nil)
	req.Header.Set("Cookie", f.cookie)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d want 400", rec.Code)
	}
}

// TestDeleteDeployment_CrossUserReturns404 seeds a deployment
// belonging to alice, then logs bob in (also APPROVED) and has him
// try to delete it. The handler must return 404 — not 403 — to
// avoid leaking existence (AGENTS.md §19).
func TestDeleteDeployment_CrossUserReturns404(t *testing.T) {
	f := newFixture(t)
	depID := createAndWaitForTerminalDeployment(t, f)

	// Login bob — fresh user, also APPROVED so the session goes through.
	hash, _ := auth.HashPassword("bob-secret")
	if _, err := f.store.DB().ExecContext(context.Background(),
		`INSERT INTO users (username, email, password_hash, role, status) VALUES ('bob', 'bob@example.com', ?, 'USER', 'APPROVED')`,
		hash); err != nil {
		t.Fatalf("insert bob: %v", err)
	}
	body := bytes.NewBufferString(`{"username":"bob","password":"bob-secret"}`)
	req := httptest.NewRequest("POST", "/api/auth/login", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("bob login: %d %s", rec.Code, rec.Body.String())
	}
	bobCookie := rec.Header().Get("Set-Cookie")
	if i := strings.IndexByte(bobCookie, ';'); i > 0 {
		bobCookie = bobCookie[:i]
	}

	req = httptest.NewRequest("DELETE", fmt.Sprintf("/api/deployments/%d", depID), nil)
	req.Header.Set("Cookie", bobCookie)
	rec = httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("cross-user DELETE: got %d want 404", rec.Code)
	}

	// And alice can still see it — the cross-user attempt didn't
	// corrupt anything.
	d, err := f.store.GetDeployment(context.Background(), depID)
	if err != nil {
		t.Fatalf("GetDeployment: %v", err)
	}
	if d.DeletedAt.Valid {
		t.Errorf("cross-user DELETE should not have soft-deleted the row")
	}
}

// TestDeleteDeployment_IdempotentSecondDeleteReturns404 fires two
// DELETE requests in a row. The first returns 204, the second 404 —
// a deleted row is invisible to a follow-up delete.
func TestDeleteDeployment_IdempotentSecondDeleteReturns404(t *testing.T) {
	f := newFixture(t)
	depID := createAndWaitForTerminalDeployment(t, f)

	for i := 1; i <= 2; i++ {
		req := httptest.NewRequest("DELETE", fmt.Sprintf("/api/deployments/%d", depID), nil)
		req.Header.Set("Cookie", f.cookie)
		rec := httptest.NewRecorder()
		f.mux.ServeHTTP(rec, req)
		if i == 1 && rec.Code != http.StatusNoContent {
			t.Fatalf("first DELETE: got %d want 204; body=%s", rec.Code, rec.Body.String())
		}
		if i == 2 && rec.Code != http.StatusNotFound {
			t.Fatalf("second DELETE: got %d want 404", rec.Code)
		}
	}
}

// createAndWaitForTerminalDeployment is a small helper that fires
// POST /api/applications/{id}/deploy and blocks until the orchestrator
// finishes (BUILT or FAILED). Returns the deployment id.
func createAndWaitForTerminalDeployment(t *testing.T, f *fixture) int64 {
	t.Helper()
	body := bytes.NewBufferString(`{"namespace":"podium-dev","replicas":1}`)
	req := httptest.NewRequest("POST", fmt.Sprintf("/api/applications/%d/deploy", f.appID), body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", f.cookie)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /deploy: %d %s", rec.Code, rec.Body.String())
	}
	var resp deployResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Deployment == nil || resp.Deployment.ID == 0 {
		t.Fatal("no deployment id")
	}
	waitForTerminal(t, f.store, resp.Deployment.ID, 2*time.Second)
	return resp.Deployment.ID
}
