package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
func (s *slowBuilder) LoadIntoKind(context.Context, string) error { return nil }

// instantBuilder returns success immediately.
type instantBuilder struct{}

func (instantBuilder) Build(_ context.Context, _, _ string, sink docker.LogSink) error {
	_ = sink.Append("Step 1/1 : FROM scratch")
	_ = sink.Append("Successfully built")
	return nil
}
func (instantBuilder) LoadIntoKind(context.Context, string) error { return nil }

// errBuilder fails the build.
type errBuilder struct{}

func (errBuilder) Build(_ context.Context, _, _ string, sink docker.LogSink) error {
	_ = sink.Append("Step 1/1 : FROM scratch")
	return errors.New("boom")
}
func (errBuilder) LoadIntoKind(context.Context, string) error { return nil }

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
